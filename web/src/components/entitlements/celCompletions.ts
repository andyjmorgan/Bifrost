import { snippetCompletion, type Completion, type CompletionContext, type CompletionResult, type CompletionSource } from '@codemirror/autocomplete'
import type { EditorView } from '@codemirror/view'
import type { CapabilityDTO, HookSchema, HookVariable } from '../../api'

// Completion source for a rule's CEL: its `when` and each `then` value. What a rule may read comes
// from GET /entitlements/rules-schema for the rule's hook, so the suggestions cannot drift from the
// engine. Only the CEL language itself (string methods, macros) and the live values from the
// server's catalog are added here.

/** What the editor page knows that the completer cannot. */
export interface CelCompletionData {
  /** The schema of the rule's hook; undefined while the schema loads or when the hook is unknown. */
  hook?: HookSchema
  /** Flag name -> what it means, from the schema. */
  flags?: Record<string, string>
  /** The entitlement's slug. */
  slug: string
  /** The server's catalog, for names and values the rule can compare against. */
  caps: CapabilityDTO[]
  /** In a `then` value: the type of the field being set. */
  setType?: string
}

export interface CelCompleterOptions {
  data: () => CelCompletionData | undefined
}

/* ---------- the CEL language ---------- */

const m = (template: string, label: string, detail: string): Completion => snippetCompletion(template, { label, detail, type: 'method' })

const STRING_METHODS: Completion[] = [
  m("startsWith('${}')", 'startsWith', '(prefix) bool'),
  m("endsWith('${}')", 'endsWith', '(suffix) bool'),
  m("contains('${}')", 'contains', '(text) bool'),
  m("matches('${}')", 'matches', '(RE2 pattern) bool'),
  m('lowerAscii()', 'lowerAscii', '() string'),
  m('upperAscii()', 'upperAscii', '() string'),
  m('size()', 'size', '() int'),
  m('trim()', 'trim', '() string'),
  m("replace('${from}', '${to}')", 'replace', '(from, to) string'),
  m("split('${}')", 'split', '(separator) list'),
]

const COLLECTION_MACROS: Completion[] = [
  m('exists(k, ${condition})', 'exists', '(k, predicate) bool'),
  m('all(k, ${condition})', 'all', '(k, predicate) bool'),
  m('exists_one(k, ${condition})', 'exists_one', '(k, predicate) bool'),
  m('filter(k, ${condition})', 'filter', '(k, predicate) list'),
  m('map(k, ${expr})', 'map', '(k, expr) list'),
  m('size()', 'size', '() int'),
]

const GLOBALS: Completion[] = [
  snippetCompletion('has(${})', { label: 'has', type: 'function', detail: '(x.y) bool, field presence' }),
  snippetCompletion('size(${})', { label: 'size', type: 'function', detail: '(x) int' }),
  { label: 'true', type: 'keyword', detail: 'bool' },
  { label: 'false', type: 'keyword', detail: 'bool' },
]

function methodsFor(type: string): Completion[] {
  if (type === 'string') return STRING_METHODS
  if (type === 'list' || type === 'map') return COLLECTION_MACROS
  return []
}

/* ---------- the catalog ---------- */

const isObject = (x: unknown): x is Record<string, unknown> => typeof x === 'object' && x !== null && !Array.isArray(x)
const definition = (c: CapabilityDTO) => (isObject(c.definition) ? c.definition : {})

/**
 * The names seen in the server's catalog under one map of a hook's item: the parameters of its
 * tools for `args` and `params`, the argument names of its prompts, the keys of `_meta` for `meta`.
 */
export function catalogKeys(caps: CapabilityDTO[], item: string | undefined, head: string): string[] {
  const out = new Set<string>()
  for (const c of caps) {
    if (c.kind !== item) continue
    const d = definition(c)
    let source: unknown
    if (head === 'args' || head === 'params') source = isObject(d.inputSchema) ? d.inputSchema.properties : d.arguments
    else if (head === 'meta') source = d._meta
    else source = d[head]
    if (Array.isArray(source)) {
      for (const x of source) if (isObject(x) && typeof x.name === 'string') out.add(x.name)
    } else if (isObject(source)) {
      for (const k of Object.keys(source)) out.add(k)
    }
  }
  return [...out].sort()
}

type Literal = { label: string; detail?: string }

/** The values a string comparison against `path` could use: the schema's enumeration, or what the catalog holds. */
function literalValues(path: string, data: CelCompletionData): Literal[] {
  const segs = path.split('.')
  if (path === 'entitlement.slug') return data.slug ? [{ label: data.slug, detail: 'this entitlement' }] : []
  // `upstream` is the item as the server sent it, so its fields compare against the same values.
  if (segs.length === 2 && segs[0] === 'upstream') segs.shift()
  if (segs.length !== 1) return []
  const v = data.hook?.variables.find((x) => x.name === segs[0])
  if (!v) return []
  if (v.values) return v.values.map((label) => ({ label }))
  if (v.type !== 'string') return []
  const seen = new Map<string, Literal>()
  for (const c of data.caps) {
    if (c.kind !== data.hook?.item) continue
    const d = definition(c)
    const value = d[v.name]
    if (typeof value !== 'string' || !value || seen.has(value)) continue
    seen.set(value, { label: value, detail: v.name === 'name' && typeof d.title === 'string' ? d.title : undefined })
  }
  return [...seen.values()]
}

/* ---------- the schema ---------- */

const IDENT = /^[A-Za-z_]\w*$/

function variableCompletion(v: HookVariable): Completion {
  return { label: v.name, type: 'variable', detail: v.values ? v.values.map((x) => `'${x}'`).join(' | ') : v.type, info: v.doc, boost: 2 }
}

/** What can follow `path.`: a struct's fields, the catalog's keys for a map, the methods of the type. */
function membersOf(path: string, data: CelCompletionData): Completion[] {
  const [head, ...rest] = path.split('.')
  const v = data.hook?.variables.find((x) => x.name === head)
  if (!v) return []
  if (rest.length === 0) {
    if (v.fields) {
      return Object.entries(v.fields).map(([name, type]) => ({ label: name, type: 'property', detail: type, info: head === 'flags' ? data.flags?.[name] : undefined }))
    }
    const keys = v.type === 'map' || v.type === 'any' ? catalogKeys(data.caps, data.hook?.item, head).filter((k) => IDENT.test(k)) : []
    return [...keys.map((k): Completion => ({ label: k, type: 'property', detail: 'seen on the server', boost: 1 })), ...methodsFor(v.type)]
  }
  if (rest.length === 1 && v.fields?.[rest[0]]) return methodsFor(v.fields[rest[0]])
  return []
}

function topLevel(data: CelCompletionData): Completion[] {
  const hook = data.hook
  if (!hook) return GLOBALS
  const flagType = hook.variables.find((v) => v.name === 'flags')?.fields ?? {}
  const out: Completion[] = []
  if (data.setType === 'decision') out.push({ label: "'deny'", type: 'constant', detail: 'the only decision', boost: 5 })
  out.push(...hook.variables.map(variableCompletion))
  for (const name of Object.keys(flagType).sort()) {
    out.push({ label: `flags.${name}`, type: 'variable', detail: flagType[name], info: data.flags?.[name], boost: 1 })
  }
  for (const h of hook.helpers ?? []) out.push(snippetCompletion(`${h}(\${})`, { label: h, type: 'function', detail: `${hook.hook} helper`, boost: 1 }))
  return [...out, ...GLOBALS]
}

/* ---------- the source ---------- */

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

const PATH = String.raw`([A-Za-z_]\w*(?:\.[A-Za-z_]\w*)*)`
const AFTER_EQ = new RegExp(PATH + String.raw`\s*(?:==|!=)\s*$`)
const AFTER_IN = new RegExp(PATH + String.raw`\s+in\s*\[(?:[^\]]*,)?\s*$`)
const AFTER_CALL = new RegExp(PATH + String.raw`\.(?:startsWith|endsWith|contains)\(\s*$`)

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

export function celCompletionSource(opts: CelCompleterOptions): CompletionSource {
  return (ctx: CompletionContext): CompletionResult | null => {
    const data = opts.data()
    if (!data) return null
    const before = ctx.state.doc.sliceString(0, ctx.pos)

    // Inside a string literal: only live values, and only right after a comparison.
    const str = openString(before, 0)
    if (str) {
      const lead = before.slice(0, str.start - str.quote.length)
      const hit = AFTER_EQ.exec(lead) ?? AFTER_IN.exec(lead) ?? AFTER_CALL.exec(lead)
      if (!hit) return null
      const values = literalValues(hit[1], data)
      if (!values.length) return null
      return {
        from: str.start,
        options: values.map((x) => ({ label: x.label, detail: x.detail, type: 'constant', apply: quotedApply(x.label, str.quote) })),
        validFor: /^[^"']*$/,
      }
    }

    // A member access: flags.se|, args.|, name.st|, description.lowerAscii().|
    const member = /(\)|[A-Za-z_]\w*(?:\.[A-Za-z_]\w*)*)\.(\w*)$/.exec(before)
    if (member) {
      const options = member[1] === ')' ? STRING_METHODS : membersOf(member[1], data)
      if (!options.length) return null
      return { from: ctx.pos - member[2].length, options, validFor: /^\w*$/ }
    }
    if (/[\w\]]\.\w*$/.test(before)) return null

    // A bare identifier, or Ctrl+Space on nothing.
    const word = ctx.matchBefore(/[A-Za-z_]\w*$/)
    if (!word && !ctx.explicit) return null
    return { from: word ? word.from : ctx.pos, options: topLevel(data), validFor: /^\w*$/ }
  }
}
