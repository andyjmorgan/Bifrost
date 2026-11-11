import { snippetCompletion, type Completion, type CompletionContext, type CompletionResult, type CompletionSource } from '@codemirror/autocomplete'
import type { EditorView } from '@codemirror/view'

// Completion source for the policy CEL inputs. Mirrors the variables and functions in
// Reference.tsx; keep the two in step when the CEL environment grows.

export type CelKind = 'bool' | 'string'

export interface CelToolName {
  name: string
  server: string
}

/** What the editor page knows that the completer cannot: live server slugs and tool names. */
export interface CelCompletionData {
  servers: { slug: string; name: string }[]
  /** Upstream tool names for the selected preview route; called lazily, the page caches it. */
  loadToolNames?: () => Promise<CelToolName[]>
}

export interface CelCompleterOptions {
  /** 'when' for a rule condition; otherwise the kind of the assigned field. */
  role: () => 'when' | CelKind
  data: () => CelCompletionData | undefined
}

const v = (label: string, detail: string, type = 'property'): Completion => ({ label, detail, type })

const TOOL_FIELDS: Completion[] = [
  v('name', 'string, the upstream tool name (no slug prefix)'),
  v('fullName', 'string, slug__name on every route'),
  v('title', 'string, "" when unset'),
  v('description', 'string, "" when unset'),
  v('annotations', 'hints with spec defaults applied'),
  v('annotationsRaw', 'hints as the upstream sent them'),
  v('inputSchema', 'map, the JSON Schema for arguments'),
  v('outputSchema', 'map, {} when absent'),
  v('icons', 'list'),
  v('meta', 'map, the _meta object'),
  v('execution', 'map, {taskSupport: …} on 2025-11-25 servers'),
  v('visible', 'bool, the decision so far'),
  v('confirm', 'bool, the decision so far'),
]

const ANNOTATION_FIELDS: Completion[] = [
  v('readOnlyHint', 'bool, default false'),
  v('destructiveHint', 'bool, default true'),
  v('idempotentHint', 'bool, default false'),
  v('openWorldHint', 'bool, default true'),
  v('title', 'string'),
]
const RAW_ANNOTATION_FIELDS: Completion[] = ANNOTATION_FIELDS.map((c) => ({ ...c, detail: c.label === 'title' ? 'string, test with has()' : 'bool, test with has()' }))

const SCHEMA_FIELDS: Completion[] = [v('properties', 'map of argument schemas'), v('required', 'list of argument names'), v('type', 'string, usually "object"')]

const SERVER_FIELDS: Completion[] = [
  v('slug', 'string, the route slug'),
  v('name', 'string'),
  v('description', 'string'),
  v('instructions', 'string'),
  v('protocolVersion', 'string, e.g. 2025-06-18'),
  v('url', 'string, the upstream URL'),
]

const m = (template: string, label: string, detail: string): Completion => snippetCompletion(template, { label, detail, type: 'method' })

const STRING_METHODS: Completion[] = [
  m('startsWith("${}")', 'startsWith', '(prefix) bool'),
  m('endsWith("${}")', 'endsWith', '(suffix) bool'),
  m('contains("${}")', 'contains', '(text) bool'),
  m('matches("${}")', 'matches', '(RE2 pattern) bool'),
  m('lowerAscii()', 'lowerAscii', '() string'),
  m('upperAscii()', 'upperAscii', '() string'),
  m('size()', 'size', '() int'),
  m('trim()', 'trim', '() string'),
  m('replace("${from}", "${to}")', 'replace', '(from, to) string'),
  m('split("${}")', 'split', '(separator) list'),
]

const COLLECTION_MACROS: Completion[] = [
  m('exists(k, ${condition})', 'exists', '(k, predicate) bool'),
  m('all(k, ${condition})', 'all', '(k, predicate) bool'),
  m('exists_one(k, ${condition})', 'exists_one', '(k, predicate) bool'),
  m('filter(k, ${condition})', 'filter', '(k, predicate) list'),
  m('map(k, ${expr})', 'map', '(k, expr) list'),
  m('size()', 'size', '() int'),
]

const STRING_PATHS = new Set([
  'tool.name',
  'tool.title',
  'tool.description',
  'tool.annotations.title',
  'server.slug',
  'server.name',
  'server.description',
  'server.instructions',
  'server.protocolVersion',
  'server.url',
  'route',
])
const COLLECTION_PATHS = new Set(['tool.inputSchema.properties', 'tool.inputSchema.required', 'tool.outputSchema.properties', 'tool.outputSchema.required', 'tool.icons', 'tool.meta'])

function membersOf(path: string): Completion[] | null {
  switch (path) {
    case 'tool':
      return TOOL_FIELDS
    case 'tool.annotations':
      return ANNOTATION_FIELDS
    case 'tool.annotationsRaw':
      return RAW_ANNOTATION_FIELDS
    case 'tool.inputSchema':
    case 'tool.outputSchema':
      return SCHEMA_FIELDS
    case 'server':
      return SERVER_FIELDS
  }
  if (STRING_PATHS.has(path)) return STRING_METHODS
  if (COLLECTION_PATHS.has(path)) return COLLECTION_MACROS
  return null
}

const TOP_LEVEL: Completion[] = [
  { label: 'tool', type: 'variable', detail: 'the tool being shaped', boost: 3 },
  { label: 'server', type: 'variable', detail: 'the upstream server', boost: 3 },
  { label: 'route', type: 'variable', detail: '"mux" or "direct"', boost: 2 },
  snippetCompletion('has(${tool.annotationsRaw.destructiveHint})', { label: 'has', type: 'function', detail: '(x.y) bool, field presence' }),
  snippetCompletion('size(${})', { label: 'size', type: 'function', detail: '(x) int' }),
  { label: 'true', type: 'keyword', detail: 'bool' },
  { label: 'false', type: 'keyword', detail: 'bool' },
]

const s = (template: string, label: string, detail: string): Completion => snippetCompletion(template, { label, detail, type: 'text', boost: -1 })

const WHEN_SNIPPETS: Completion[] = [
  s('tool.name.startsWith("${}")', 'tool.name.startsWith("…")', 'name prefix'),
  s('tool.name.matches("^${}")', 'tool.name.matches("^…")', 'RE2 match'),
  s('tool.name in ["${}"]', 'tool.name in […]', 'one of a list'),
  s('server.slug == "${}"', 'server.slug == "…"', 'one server'),
  s('tool.annotations.destructiveHint && !tool.annotations.readOnlyHint', 'destructive && !readOnly', 'destructive tools'),
  s('has(tool.annotationsRaw.${destructiveHint})', 'has(tool.annotationsRaw.…)', 'hint was sent'),
  s('tool.description.lowerAscii().contains("${}")', 'tool.description…contains("…")', 'description mentions'),
  s('tool.inputSchema.properties.exists(k, ${condition})', 'tool.inputSchema.properties.exists(k, …)', 'any argument'),
  s('has(tool.meta["${key}"])', 'has(tool.meta["…"])', '_meta key present'),
]

const BOOL_VALUES: Completion[] = [
  { label: 'true', type: 'keyword', detail: 'bool', boost: 4 },
  { label: 'false', type: 'keyword', detail: 'bool', boost: 4 },
  { label: 'tool.visible', type: 'text', detail: 'keep the decision so far', boost: 2 },
  { label: 'tool.confirm', type: 'text', detail: 'keep the decision so far', boost: 2 },
  { label: '!tool.annotations.readOnlyHint', type: 'text', detail: 'unless read-only', boost: 1 },
  { label: 'tool.annotations.destructiveHint', type: 'text', detail: 'only if destructive', boost: 1 },
  { label: '!tool.annotations.destructiveHint', type: 'text', detail: 'unless destructive', boost: 1 },
]

const STRING_VALUES: Completion[] = [
  snippetCompletion('"${}"', { label: '"…"', type: 'text', detail: 'string literal', boost: 4 }),
  snippetCompletion('tool.description + " ${}"', { label: 'tool.description + " …"', type: 'text', detail: 'append to the description', boost: 3 }),
  { label: 'tool.name', type: 'text', detail: 'the tool name', boost: 2 },
  snippetCompletion('"${}" + server.slug', { label: '"…" + server.slug', type: 'text', detail: 'prefix with the server', boost: 2 }),
]

interface StringScan {
  /** Position just after the opening quote. */
  start: number
  quote: string
}

/** Finds the string literal the cursor sits in, if any, by scanning from the start of the doc. */
function openString(text: string, base: number): StringScan | null {
  let i = 0
  let open: (StringScan & { raw: boolean }) | null = null
  while (i < text.length) {
    const c = text[i]
    if (open) {
      if (!open.raw && c === '\\') {
        i += 2
        continue
      }
      if (text.startsWith(open.quote, i)) {
        i += open.quote.length
        open = null
        continue
      }
      i++
      continue
    }
    if (c === '"' || c === "'") {
      const prev = text[i - 1] ?? ''
      const raw = /[rR]/.test(prev) && !/[\w]/.test(text[i - 2] ?? '')
      const quote = text.startsWith(c.repeat(3), i) ? c.repeat(3) : c
      i += quote.length
      open = { start: base + i, quote, raw }
      continue
    }
    i++
  }
  return open && { start: open.start, quote: open.quote }
}

const AFTER_EQ = /(server\.slug|tool\.name|tool\.fullName|route)\s*(==|!=)\s*$/
const AFTER_IN = /(server\.slug|tool\.name|tool\.fullName|route)\s+in\s*\[(?:[^\]]*,)?\s*$/

function quotedApply(value: string, quote: string) {
  return (view: EditorView, _c: Completion, from: number, to: number) => {
    const close = view.state.doc.sliceString(to, to + quote.length) === quote
    const insert = value + quote
    view.dispatch({
      changes: { from, to: close ? to + quote.length : to, insert },
      selection: { anchor: from + insert.length },
      userEvent: 'input.complete',
    })
  }
}

async function literalValues(subject: string, data: CelCompletionData | undefined): Promise<{ label: string; detail?: string }[]> {
  if (subject === 'route') return [{ label: 'mux', detail: 'the global /mcp route' }, { label: 'direct', detail: 'a per-server route' }]
  if (subject === 'server.slug') return (data?.servers ?? []).map((sv) => ({ label: sv.slug, detail: sv.name }))
  if (!data?.loadToolNames) return []
  try {
    const tools = await data.loadToolNames()
    if (subject === 'tool.fullName') return tools.map((t) => ({ label: `${t.server}__${t.name}`, detail: t.server }))
    const seen = new Map<string, string[]>()
    for (const t of tools) seen.set(t.name, [...(seen.get(t.name) ?? []), t.server])
    return [...seen].map(([label, servers]) => ({ label, detail: servers.join(', ') }))
  } catch {
    return []
  }
}

export function celCompletionSource(opts: CelCompleterOptions): CompletionSource {
  return async (ctx: CompletionContext): Promise<CompletionResult | null> => {
    const before = ctx.state.doc.sliceString(0, ctx.pos)

    // Inside a string literal: only live values, and only right after a comparison we know.
    const str = openString(before, 0)
    if (str) {
      const lead = before.slice(0, str.start - str.quote.length)
      const hit = AFTER_EQ.exec(lead) ?? AFTER_IN.exec(lead)
      if (!hit) return null
      const values = await literalValues(hit[1], opts.data())
      if (!values.length || ctx.aborted) return null
      return {
        from: str.start,
        options: values.map((x) => ({ label: x.label, detail: x.detail, type: 'constant', apply: quotedApply(x.label, str.quote) })),
        validFor: /^[\w.\-/:]*$/,
      }
    }

    // A member access: tool.an|, server.|, tool.name.st|, tool.description.lowerAscii().|
    const member = /(\)|[A-Za-z_]\w*(?:\.[A-Za-z_]\w*)*)\.(\w*)$/.exec(before)
    if (member) {
      const path = member[1]
      const options = path === ')' ? STRING_METHODS : membersOf(path)
      if (!options) return null
      return { from: ctx.pos - member[2].length, options, validFor: /^\w*$/ }
    }
    if (/[\w\]]\.\w*$/.test(before)) return null

    // A bare identifier, or Ctrl+Space on nothing.
    const word = ctx.matchBefore(/!?[A-Za-z_]\w*$/)
    if (!word && !ctx.explicit) return null
    const role = opts.role()
    const plain = TOP_LEVEL.map(({ boost: _b, ...c }) => c)
    const options =
      role === 'when'
        ? [...TOP_LEVEL, ...WHEN_SNIPPETS]
        : role === 'bool'
          ? [...BOOL_VALUES, ...plain.filter((c) => c.label !== 'true' && c.label !== 'false')]
          : [...STRING_VALUES, ...plain.filter((c) => c.type !== 'keyword')]
    return { from: word ? word.from : ctx.pos, options, validFor: /^!?\w*$/ }
  }
}
