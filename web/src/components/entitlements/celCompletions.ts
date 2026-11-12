import { snippetCompletion, type Completion, type CompletionContext, type CompletionResult, type CompletionSource } from '@codemirror/autocomplete'
import type { EditorView } from '@codemirror/view'
import type { CapabilityDTO, CapabilityKind } from '../../api'

// Completion source for a rule's `when`. Mirrors the variables the Go side binds in
// internal/rules (rules.go: env and ToMap; gateway/entitle.go: serverMap, callerMap, clientMap)
// and the Reference panel in RulesCard; keep the three in step when the environment grows.

/** What the editor page knows that the completer cannot: the catalog and the slug. */
export interface CelCompletionData {
  slug: string
  caps: CapabilityDTO[]
  /** The rule's kinds; empty = any. Narrows the top-level suggestions and the literal values. */
  kinds?: CapabilityKind[]
}

export interface CelCompleterOptions {
  data: () => CelCompletionData | undefined
}

const v = (label: string, detail: string, type = 'property'): Completion => ({ label, detail, type })

const COMMON_FIELDS: Completion[] = [v('name', 'string'), v('title', 'string, "" when unset'), v('description', 'string, "" when unset'), v('meta', 'map, the _meta object')]

const TOOL_FIELDS: Completion[] = [
  ...COMMON_FIELDS,
  v('annotations', 'hints with spec defaults applied'),
  v('annotationsRaw', 'hints as the server sent them'),
  v('inputSchema', 'map, the JSON Schema for arguments'),
  v('outputSchema', 'map, {} when absent'),
  v('icons', 'list'),
]
const PROMPT_FIELDS: Completion[] = [...COMMON_FIELDS, v('arguments', 'list of {name, description, required}'), v('icons', 'list')]
const RESOURCE_FIELDS: Completion[] = [v('uri', 'string'), ...COMMON_FIELDS, v('mimeType', 'string'), v('size', 'int'), v('annotations', 'audience, priority, lastModified')]
const TEMPLATE_FIELDS: Completion[] = [v('uriTemplate', 'string'), ...COMMON_FIELDS, v('mimeType', 'string'), v('annotations', 'audience, priority, lastModified')]
const CAPABILITY_FIELDS: Completion[] = [v('kind', '"tool" | "prompt" | "resource" | "template"'), ...COMMON_FIELDS, v('uri', 'string, resources'), v('uriTemplate', 'string, templates'), v('annotations', 'map'), v('inputSchema', 'map, tools')]

const ANNOTATION_FIELDS: Completion[] = [
  v('readOnlyHint', 'bool, default false'),
  v('destructiveHint', 'bool, default true'),
  v('idempotentHint', 'bool, default false'),
  v('openWorldHint', 'bool, default true'),
  v('title', 'string'),
]
const RAW_ANNOTATION_FIELDS: Completion[] = ANNOTATION_FIELDS.map((c) => ({ ...c, detail: c.label === 'title' ? 'string, test with has()' : 'bool, test with has()' }))
const RESOURCE_ANNOTATION_FIELDS: Completion[] = [v('audience', 'list of "user" | "assistant"'), v('priority', 'double, 0 to 1'), v('lastModified', 'string, RFC 3339')]

const SCHEMA_FIELDS: Completion[] = [v('properties', 'map of argument schemas'), v('required', 'list of argument names'), v('type', 'string, usually "object"')]

const SERVER_FIELDS: Completion[] = [
  v('slug', 'string, this entitlement’s slug'),
  v('name', 'string'),
  v('description', 'string'),
  v('url', 'string, the upstream URL'),
  v('instructions', 'string, from initialize'),
  v('protocolVersion', 'string, e.g. 2025-06-18'),
]
const CALLER_FIELDS: Completion[] = [
  v('userId', 'string'),
  v('email', 'string'),
  v('name', 'string'),
  v('role', '"admin" | "member"'),
  v('type', '"oidc" | "apikey" | "dev"'),
  v('keyName', 'string, "" unless an API key'),
]
const CLIENT_FIELDS: Completion[] = [v('name', 'string, from initialize'), v('version', 'string'), v('protocolVersion', 'string'), v('capabilities', 'elicitation, sampling, roots')]
const CLIENT_CAPABILITY_FIELDS: Completion[] = [v('elicitation', 'bool'), v('sampling', 'bool'), v('roots', 'bool')]

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
  m('regexReplace("${pattern}", "${replacement}")', 'regexReplace', '(RE2 pattern, replacement) string'),
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

const STRING_TAILS = new Set(['name', 'title', 'description', 'uri', 'uriTemplate', 'mimeType', 'slug', 'url', 'instructions', 'protocolVersion', 'userId', 'email', 'role', 'type', 'keyName', 'version', 'kind'])
const COLLECTION_TAILS = new Set(['properties', 'required', 'icons', 'meta', 'arguments', 'audience'])

function membersOf(path: string): Completion[] | null {
  switch (path) {
    case 'capability':
      return CAPABILITY_FIELDS
    case 'tool':
      return TOOL_FIELDS
    case 'prompt':
      return PROMPT_FIELDS
    case 'resource':
      return RESOURCE_FIELDS
    case 'template':
      return TEMPLATE_FIELDS
    case 'server':
      return SERVER_FIELDS
    case 'caller':
      return CALLER_FIELDS
    case 'client':
      return CLIENT_FIELDS
    case 'client.capabilities':
      return CLIENT_CAPABILITY_FIELDS
    case 'tool.annotations':
    case 'capability.annotations':
      return ANNOTATION_FIELDS
    case 'tool.annotationsRaw':
      return RAW_ANNOTATION_FIELDS
    case 'resource.annotations':
    case 'template.annotations':
      return RESOURCE_ANNOTATION_FIELDS
    case 'tool.inputSchema':
    case 'tool.outputSchema':
    case 'capability.inputSchema':
      return SCHEMA_FIELDS
  }
  const tail = path.slice(path.lastIndexOf('.') + 1)
  if (path === 'route' || STRING_TAILS.has(tail)) return STRING_METHODS
  if (COLLECTION_TAILS.has(tail)) return COLLECTION_MACROS
  return null
}

const KIND_VARS: Record<CapabilityKind, Completion> = {
  tool: { label: 'tool', type: 'variable', detail: 'the tool, {} for other kinds', boost: 3 },
  prompt: { label: 'prompt', type: 'variable', detail: 'the prompt, {} for other kinds', boost: 3 },
  resource: { label: 'resource', type: 'variable', detail: 'the resource, {} for other kinds', boost: 3 },
  template: { label: 'template', type: 'variable', detail: 'the resource template, {} for other kinds', boost: 3 },
}

const CONTEXT_VARS: Completion[] = [
  { label: 'capability', type: 'variable', detail: 'any kind: kind, name, …', boost: 2 },
  { label: 'server', type: 'variable', detail: 'the upstream server', boost: 2 },
  { label: 'caller', type: 'variable', detail: 'who is asking', boost: 2 },
  { label: 'client', type: 'variable', detail: 'the MCP client', boost: 2 },
  { label: 'route', type: 'variable', detail: '"mux" or "direct"', boost: 1 },
  snippetCompletion('has(${tool.annotationsRaw.destructiveHint})', { label: 'has', type: 'function', detail: '(x.y) bool, field presence' }),
  snippetCompletion('size(${})', { label: 'size', type: 'function', detail: '(x) int' }),
  { label: 'true', type: 'keyword', detail: 'bool' },
  { label: 'false', type: 'keyword', detail: 'bool' },
]

const s = (template: string, label: string, detail: string): Completion => snippetCompletion(template, { label, detail, type: 'text', boost: -1 })

const SNIPPETS: Record<CapabilityKind | 'any', Completion[]> = {
  tool: [
    s('tool.name.startsWith("${}")', 'tool.name.startsWith("…")', 'name prefix'),
    s('tool.name.matches("^${}")', 'tool.name.matches("^…")', 'RE2 on the name'),
    s('tool.name in ["${}"]', 'tool.name in […]', 'one of a list'),
    s('tool.annotations.destructiveHint && !tool.annotations.readOnlyHint', 'destructive && !readOnly', 'destructive tools'),
    s('tool.annotations.readOnlyHint', 'tool.annotations.readOnlyHint', 'read-only tools'),
    s('has(tool.annotationsRaw.${destructiveHint})', 'has(tool.annotationsRaw.…)', 'hint was sent'),
    s('tool.description.lowerAscii().contains("${}")', 'tool.description…contains("…")', 'description mentions'),
    s('tool.inputSchema.properties.exists(k, ${condition})', 'tool.inputSchema.properties.exists(k, …)', 'any argument'),
    s('has(tool.meta["${key}"])', 'has(tool.meta["…"])', '_meta key present'),
  ],
  prompt: [
    s('prompt.name.startsWith("${}")', 'prompt.name.startsWith("…")', 'name prefix'),
    s('prompt.name in ["${}"]', 'prompt.name in […]', 'one of a list'),
    s('prompt.arguments.exists(a, a.name == "${}")', 'prompt.arguments.exists(a, …)', 'takes an argument'),
  ],
  resource: [
    s('resource.uri.startsWith("${}")', 'resource.uri.startsWith("…")', 'URI prefix'),
    s('resource.uri.matches("^${}")', 'resource.uri.matches("^…")', 'RE2 on the URI'),
    s('resource.mimeType == "${}"', 'resource.mimeType == "…"', 'one media type'),
    s('resource.annotations.audience.exists(a, a == "${user}")', 'resource.annotations.audience.exists(…)', 'meant for an audience'),
  ],
  template: [
    s('template.uriTemplate.startsWith("${}")', 'template.uriTemplate.startsWith("…")', 'template prefix'),
    s('template.mimeType == "${}"', 'template.mimeType == "…"', 'one media type'),
  ],
  any: [
    s('capability.kind == "${}"', 'capability.kind == "…"', 'one kind'),
    s('capability.name.startsWith("${}")', 'capability.name.startsWith("…")', 'name prefix, any kind'),
    s('caller.role != "admin"', 'caller.role != "admin"', 'not an admin'),
    s('caller.type == "apikey"', 'caller.type == "apikey"', 'called with an API key'),
    s('caller.email.endsWith("@${}")', 'caller.email.endsWith("@…")', 'one email domain'),
    s('!client.capabilities.elicitation', '!client.capabilities.elicitation', 'client cannot elicit'),
    s('client.name.startsWith("${}")', 'client.name.startsWith("…")', 'one client'),
    s('route == "direct"', 'route == "direct"', 'on /{slug}/mcp'),
  ],
}

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

const SUBJECT = String.raw`((?:tool|prompt|resource|template|capability)\.(?:name|uri|uriTemplate|mimeType|kind)|server\.slug|caller\.(?:role|type)|client\.name|route)`
const AFTER_EQ = new RegExp(SUBJECT + String.raw`\s*(==|!=)\s*$`)
const AFTER_IN = new RegExp(SUBJECT + String.raw`\s+in\s*\[(?:[^\]]*,)?\s*$`)
const AFTER_CALL = new RegExp(SUBJECT + String.raw`\.(startsWith|endsWith|contains)\(\s*$`)

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

type Literal = { label: string; detail?: string }

const uniq = (xs: Literal[]) => [...new Map(xs.map((x) => [x.label, x])).values()]

function literalValues(subject: string, data: CelCompletionData | undefined): Literal[] {
  const [head, field] = subject.split('.')
  const caps = data?.caps ?? []
  const ofKind = (k: CapabilityKind) => caps.filter((c) => c.kind === k)
  const def = (c: CapabilityDTO) => (c.definition ?? {}) as Record<string, unknown>
  switch (head) {
    case 'route':
      return [{ label: 'mux', detail: '/mcp' }, { label: 'direct', detail: '/{slug}/mcp' }]
    case 'server':
      return data?.slug ? [{ label: data.slug, detail: 'this entitlement' }] : []
    case 'caller':
      return field === 'role' ? [{ label: 'admin' }, { label: 'member' }] : [{ label: 'oidc' }, { label: 'apikey' }, { label: 'dev' }]
    case 'client':
      return []
    case 'tool':
      return ofKind('tool').map((c) => ({ label: c.name, detail: String(def(c).title ?? '') || undefined }))
    case 'prompt':
      return ofKind('prompt').map((c) => ({ label: c.name, detail: String(def(c).title ?? '') || undefined }))
    case 'resource':
      if (field === 'uri') return ofKind('resource').map((c) => ({ label: c.name, detail: String(def(c).title ?? def(c).name ?? '') || undefined }))
      if (field === 'mimeType') return uniq(ofKind('resource').map((c) => ({ label: String(def(c).mimeType ?? '') })).filter((x) => x.label))
      return ofKind('resource').map((c) => ({ label: String(def(c).name ?? c.name) }))
    case 'template':
      if (field === 'uriTemplate') return ofKind('template').map((c) => ({ label: c.name, detail: String(def(c).title ?? def(c).name ?? '') || undefined }))
      if (field === 'mimeType') return uniq(ofKind('template').map((c) => ({ label: String(def(c).mimeType ?? '') })).filter((x) => x.label))
      return ofKind('template').map((c) => ({ label: String(def(c).name ?? c.name) }))
    case 'capability':
      if (field === 'kind') return [{ label: 'tool' }, { label: 'prompt' }, { label: 'resource' }, { label: 'template' }]
      if (field === 'uri') return ofKind('resource').map((c) => ({ label: c.name }))
      if (field === 'uriTemplate') return ofKind('template').map((c) => ({ label: c.name }))
      if (field === 'mimeType') return uniq(caps.map((c) => ({ label: String(def(c).mimeType ?? '') })).filter((x) => x.label))
      return caps.filter((c) => (data?.kinds?.length ? data.kinds.includes(c.kind) : true)).map((c) => ({ label: c.kind === 'resource' ? String(def(c).name ?? c.name) : c.name, detail: c.kind }))
  }
  return []
}

export function celCompletionSource(opts: CelCompleterOptions): CompletionSource {
  return (ctx: CompletionContext): CompletionResult | null => {
    const before = ctx.state.doc.sliceString(0, ctx.pos)

    // Inside a string literal: only live values, and only right after a comparison we know.
    const str = openString(before, 0)
    if (str) {
      const lead = before.slice(0, str.start - str.quote.length)
      const hit = AFTER_EQ.exec(lead) ?? AFTER_IN.exec(lead) ?? AFTER_CALL.exec(lead)
      if (!hit) return null
      const values = literalValues(hit[1], opts.data())
      if (!values.length) return null
      return {
        from: str.start,
        options: values.map((x) => ({ label: x.label, detail: x.detail, type: 'constant', apply: quotedApply(x.label, str.quote) })),
        validFor: /^[^"']*$/,
      }
    }

    // A member access: tool.an|, caller.|, resource.uri.st|, tool.description.lowerAscii().|
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
    const kinds = opts.data()?.kinds ?? []
    const scoped: CapabilityKind[] = kinds.length ? kinds : ['tool', 'prompt', 'resource', 'template']
    const options = [...scoped.map((k) => KIND_VARS[k]), ...CONTEXT_VARS, ...scoped.flatMap((k) => SNIPPETS[k]), ...SNIPPETS.any]
    return { from: word ? word.from : ctx.pos, options, validFor: /^!?\w*$/ }
  }
}
