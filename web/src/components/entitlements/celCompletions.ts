import { snippetCompletion, startCompletion, type Completion, type CompletionContext, type CompletionResult } from '@codemirror/autocomplete'
import type { EditorView } from '@codemirror/view'
import type { CapabilityDTO, HookInfo, HookSet, HookVariable, Shape } from '../../api'

// Completion for a rule's CEL: its condition and each value it sets. What a rule may read comes
// from GET /entitlements/hooks-schema for the rule's hook, so the suggestions cannot drift from
// the engine. Only the CEL language itself (string methods, macros) and the live values from the
// server's catalog are added here.
//
// Two rules keep the lists short. A name is offered whole and its members only after its dot:
// `flags`, then `search` after `flags.`. And the first list is names with a one-word type; the
// descriptions live in the Help panel.

/** What the editor page knows that the completer cannot. */
export interface CelCompletionData {
  /** The rule's hook; undefined while the schema loads. */
  hook?: HookInfo
  /** Key of `caller` -> what it holds, from the schema. */
  caller?: Record<string, string>
  /** The entitlement's slug. */
  slug: string
  /** The server's catalog, for names and values the rule can compare against. */
  caps: CapabilityDTO[]
  /** In a value: the type of the field being set. */
  setType?: string
  /** The item the rule's condition names (name == 'flip'), to narrow what the catalog offers. */
  itemName?: string
}

/* ---------- the CEL language ---------- */

const m = (template: string, label: string, detail: string): Completion => snippetCompletion(template, { label, detail, type: 'method' })
const fn = (template: string, label: string, detail: string, boost = 0): Completion => snippetCompletion(template, { label, detail, type: 'function', boost })

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

const MACROS: Completion[] = [
  m('exists(k, ${condition})', 'exists', '(k, predicate) bool'),
  m('all(k, ${condition})', 'all', '(k, predicate) bool'),
  m('exists_one(k, ${condition})', 'exists_one', '(k, predicate) bool'),
  m('filter(k, ${condition})', 'filter', '(k, predicate) list'),
  m('map(k, ${expr})', 'map', '(k, expr) list'),
  m('size()', 'size', '() int'),
]

const LIST_METHODS: Completion[] = [...MACROS, m("join('${, }')", 'join', '(separator) string'), m('slice(${0}, ${n})', 'slice', '(from, to) list')]

/** Functions that live under a name, offered after its dot. */
const NAMESPACES: Record<string, Completion[]> = {
  regex: [
    m("replace(${text}, '${pattern}', '${with}')", 'replace', '(text, RE2 pattern, with) string'),
    m("extract(${text}, '${pattern}')", 'extract', '(text, RE2 pattern) optional string'),
    m("extractAll(${text}, '${pattern}')", 'extractAll', '(text, RE2 pattern) list'),
  ],
}

const FUNCTIONS: Completion[] = [
  fn('has(${})', 'has', '(x.y) bool'),
  fn('size(${})', 'size', '(x) int'),
  fn('string(${})', 'string', '(x) string'),
  fn('int(${})', 'int', '(x) int'),
  fn("duration('${30s}')", 'duration', "('30s') duration"),
  { label: 'regex', type: 'namespace', detail: 'functions' },
  { label: 'true', type: 'keyword', detail: 'bool' },
  { label: 'false', type: 'keyword', detail: 'bool' },
]

function methodsFor(type: string): Completion[] {
  if (type === 'string') return STRING_METHODS
  if (type === 'list') return LIST_METHODS
  if (type === 'map') return MACROS
  return []
}

/* ---------- the catalog ---------- */

type Obj = Record<string, unknown>
const isObject = (x: unknown): x is Obj => typeof x === 'object' && x !== null && !Array.isArray(x)
const definition = (c: CapabilityDTO) => (isObject(c.definition) ? c.definition : {})
const IDENT = /^[A-Za-z_]\w*$/

/** The catalog entries a rule is about: the one its condition names when there is one, or all of the hook's kind. */
function itemsOf(data: CelCompletionData): Obj[] {
  const all = data.caps.filter((c) => c.kind === data.hook?.item)
  const named = data.itemName ? all.filter((c) => c.name === data.itemName) : []
  return (named.length ? named : all).map(definition)
}

/** The type of a JSON Schema node, in the completer's words. */
function schemaType(node: unknown): string {
  if (!isObject(node)) return 'any'
  const types = (Array.isArray(node.type) ? node.type : [node.type]).filter((x) => x !== 'null')
  switch (types[0]) {
    case 'string':
      return 'string'
    case 'array':
      return 'list'
    case 'object':
      return 'map'
    case 'boolean':
      return 'bool'
    case 'integer':
    case 'number':
      return 'number'
  }
  return isObject(node.properties) ? 'map' : 'any'
}

/** The schema nodes reached from each item's `which` schema by following property names. */
function schemaNodes(items: Obj[], which: 'inputSchema' | 'outputSchema', path: string[]): Obj[] {
  let nodes = items.map((d) => d[which]).filter(isObject)
  for (const seg of path) {
    nodes = nodes.map((n) => (isObject(n.properties) ? n.properties[seg] : undefined)).filter(isObject)
  }
  return nodes
}

/** The properties of some schema nodes, as completions, then the methods of what the nodes are. */
function schemaMembers(nodes: Obj[]): Completion[] {
  const props = new Map<string, string>()
  const kinds = new Set<string>()
  for (const n of nodes) {
    kinds.add(schemaType(n))
    if (isObject(n.properties)) for (const [k, v] of Object.entries(n.properties)) if (IDENT.test(k) && !props.has(k)) props.set(k, schemaType(v))
  }
  const out: Completion[] = [...props].sort(([a], [b]) => a.localeCompare(b)).map(([label, detail]) => ({ label, detail, type: 'property', boost: 1 }))
  // Methods only when every item agrees on the type, and never the map macros beside real keys.
  if (kinds.size === 1 && !out.length) out.push(...methodsFor([...kinds][0]))
  return out
}

/** The names of what a request passes: a tool's parameters, a prompt's arguments. */
export function parameterNames(data: CelCompletionData): Map<string, string> {
  const out = new Map<string, string>()
  for (const d of itemsOf(data)) {
    if (isObject(d.inputSchema) && isObject(d.inputSchema.properties)) {
      for (const [k, v] of Object.entries(d.inputSchema.properties)) if (!out.has(k)) out.set(k, schemaType(v))
    }
    if (Array.isArray(d.arguments)) for (const a of d.arguments) if (isObject(a) && typeof a.name === 'string' && !out.has(a.name)) out.set(a.name, 'string')
  }
  return out
}

/** The keys seen in the catalog under one map of the item: annotations, _meta. */
export function catalogKeys(data: CelCompletionData, head: string): string[] {
  const out = new Set<string>()
  for (const d of itemsOf(data)) {
    const source = head === 'meta' ? d._meta : d[head]
    if (isObject(source)) for (const k of Object.keys(source)) out.add(k)
  }
  return [...out].sort()
}

type Literal = { label: string; detail?: string }

/** The values a string comparison against `path` could use: the schema's enumeration, or what the catalog holds. */
function literalValues(path: string, data: CelCompletionData): Literal[] {
  const segs = path.split('.')
  if (path === 'entitlement.slug') return data.slug ? [{ label: data.slug, detail: 'this entitlement' }] : []
  if (segs[0] === 'args' && segs.length > 1) {
    const out = new Set<string>()
    for (const n of schemaNodes(itemsOf(data), 'inputSchema', segs.slice(1))) if (Array.isArray(n.enum)) for (const x of n.enum) if (typeof x === 'string') out.add(x)
    return [...out].map((label) => ({ label }))
  }
  // `upstream` is the item as the server sent it, so its fields compare against the same values.
  if (segs.length === 2 && segs[0] === 'upstream') segs.shift()
  if (segs.length !== 1) return []
  const v = data.hook?.reads.find((x) => x.name === segs[0])
  if (!v) return []
  if (v.values) return v.values.map((label) => ({ label }))
  if (v.type !== 'string') return []
  const seen = new Map<string, Literal>()
  // Every item of the kind, not only the one the rule names: this is where the name is chosen.
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

const prop = (label: string, detail: string): Completion => ({ label, detail, type: 'property', boost: 1 })

/**
 * Where a path has got to. A shape is the engine's description of the value. A schema is the
 * JSON Schema nodes the catalog holds for it: arguments follow a tool's input schema, structured
 * content its output schema. A struct has fixed fields. Past all of those, only a type is known.
 */
type Node = { shape: Shape } | { schema: Obj[] } | { struct: Record<string, string> } | { type: string }

const MACRO_NAMES = 'exists|all|exists_one|filter|map'
const SEGMENT = /\.([A-Za-z_]\w*)|\[([^\]]*)\]/g

/** Splits `result.content[0].text` into its head and the steps after it; an index step is null. */
function splitPath(path: string): { head: string; steps: (string | null)[] } {
  const head = /^[A-Za-z_]\w*/.exec(path)?.[0] ?? ''
  const steps: (string | null)[] = []
  for (const m of path.slice(head.length).matchAll(SEGMENT)) {
    const key = m[2] === undefined ? undefined : /^\s*(['"])(.*)\1\s*$/.exec(m[2])?.[2]
    steps.push(m[1] ?? key ?? null)
  }
  return { head, steps }
}

/** The node a JSON Schema reference lands on: the item's schemas, then the properties named. */
const followed = (which: string, data: CelCompletionData): Node => ({ schema: schemaNodes(itemsOf(data), which as 'inputSchema' | 'outputSchema', []) })

function start(head: string, before: string, data: CelCompletionData): Node | null {
  const v = data.hook?.reads.find((x) => x.name === head)
  if (v) {
    if (v.fields) return { struct: v.fields }
    if (head === 'caller' && data.caller) return { shape: { type: 'map', keys: Object.fromEntries(Object.keys(data.caller).map((k) => [k, { type: 'string' }])) } }
    if (v.shape?.follows) return followed(v.shape.follows, data)
    return v.shape ? { shape: v.shape } : { type: v.type }
  }
  // A macro's own variable: in result.content.exists(c, c.|) c is one element of result.content.
  const bound = new RegExp(String.raw`([A-Za-z_]\w*(?:\.[A-Za-z_]\w*|\[[^\]]*\])*)\.(?:${MACRO_NAMES})\(\s*${head}\s*,`, 'g')
  const last = [...before.matchAll(bound)].pop()
  if (!last) return null
  const over = resolve(last[1], before.slice(0, last.index), data)
  return over ? step(over, null, data) : null
}

/** One step from a node: a key by name, or an element when the step is null. */
function step(node: Node, key: string | null, data: CelCompletionData): Node {
  if ('struct' in node) return { type: key ? (node.struct[key] ?? 'any') : 'any' }
  if ('schema' in node) {
    const next = node.schema.map((n) => (key === null ? n.items : isObject(n.properties) ? n.properties[key] : undefined)).filter(isObject)
    return next.length ? { schema: next } : { type: 'any' }
  }
  if ('shape' in node) {
    const sh = node.shape
    const next = key === null ? (sh.type === 'list' ? sh.items : sh.type === 'map' ? { type: 'string' } : undefined) : (sh.keys?.[key] ?? (sh.names ? (sh.values ?? { type: 'any' }) : undefined))
    if (!next) return { type: 'any' }
    return next.follows ? followed(next.follows, data) : { shape: next }
  }
  return { type: 'any' }
}

function resolve(path: string, before: string, data: CelCompletionData): Node | null {
  const { head, steps } = splitPath(path)
  let node = start(head, before, data)
  for (const s of steps) {
    if (!node) return null
    node = step(node, s, data)
  }
  return node
}

/** The names the catalog holds for a shape whose keys come from it. */
function catalogNames(names: string, data: CelCompletionData): [string, string][] {
  if (names === 'parameters') return [...parameterNames(data)]
  if (names === 'meta') return catalogKeys(data, 'meta').map((k) => [k, 'seen on the server'])
  if (names === 'output') {
    const out = new Map<string, string>()
    for (const n of schemaNodes(itemsOf(data), 'outputSchema', [])) if (isObject(n.properties)) for (const [k, v] of Object.entries(n.properties)) if (!out.has(k)) out.set(k, schemaType(v))
    return [...out]
  }
  return []
}

/** What can follow `path.`: the keys of what the path reaches, or the methods of its type. */
function membersOf(path: string, before: string, data: CelCompletionData): Completion[] {
  if (NAMESPACES[path]) return NAMESPACES[path]
  const node = resolve(path, before, data)
  if (!node) return []
  if ('struct' in node) return Object.entries(node.struct).map(([name, type]) => prop(name, type))
  if ('schema' in node) return schemaMembers(node.schema)
  if ('type' in node) return methodsFor(node.type)
  const sh = node.shape
  const keys = new Map<string, string>(Object.entries(sh.keys ?? {}).map(([k, v]) => [k, v.type]))
  if (sh.names) for (const [k, type] of catalogNames(sh.names, data)) if (!keys.has(k)) keys.set(k, sh.values?.type && sh.values.type !== 'any' ? sh.values.type : type)
  const out = [...keys].filter(([k]) => IDENT.test(k)).map(([k, type]) => prop(k, type))
  return out.length ? out : methodsFor(sh.type)
}

/** A request's own values first, then the item, then the context. */
const ORDER = ['args', 'result', 'request', 'progress', 'input', 'name', 'uri', 'uriTemplate', 'upstream']

function topLevel(data: CelCompletionData): Completion[] {
  const hook = data.hook
  if (!hook) return FUNCTIONS
  const out: Completion[] = []
  if (data.setType === 'bool') out.push({ label: 'true', type: 'keyword', detail: 'bool', boost: 6 }, { label: 'false', type: 'keyword', detail: 'bool', boost: 6 })
  if (data.setType === 'duration') out.push(fn("duration('${30s}')", 'duration', "('30s') duration", 6))
  out.push(...hook.reads.map((v: HookVariable): Completion => ({ label: v.name, type: 'variable', detail: v.type, boost: ORDER.includes(v.name) ? 3 : 2 })))
  for (const h of hook.helpers ?? []) out.push(fn(`${h}(\${})`, h, 'helper', 1))
  const seen = new Set(out.map((c) => c.label))
  return [...out, ...FUNCTIONS.filter((c) => !seen.has(c.label))]
}

/* ---------- the source ---------- */

interface StringScan {
  /** Position just after the opening quote. */
  start: number
  quote: string
}

/** Finds the string literal the text ends inside, if any, by scanning it from its start. */
export function openString(text: string): StringScan | null {
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
      open = { start: i, quote, raw }
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

/**
 * Completes the CEL that runs from `base` to the cursor. `open` shows the first list on an empty
 * prefix, as Ctrl+Space does.
 */
export function completeCel(ctx: CompletionContext, data: CelCompletionData, base: number, open = false): CompletionResult | null {
  const before = ctx.state.doc.sliceString(base, ctx.pos)

  // Inside a string literal: only live values, and only right after a comparison.
  const str = openString(before)
  if (str) {
    const lead = before.slice(0, str.start - str.quote.length)
    const hit = AFTER_EQ.exec(lead) ?? AFTER_IN.exec(lead) ?? AFTER_CALL.exec(lead)
    if (!hit) return null
    const values = literalValues(hit[1], data)
    if (!values.length) return null
    return {
      from: base + str.start,
      options: values.map((x) => ({ label: x.label, detail: x.detail, type: 'constant', apply: quotedApply(x.label, str.quote) })),
      validFor: /^[^"']*$/,
    }
  }

  // A member access: flags.se|, args.|, result.content[0].|, description.lowerAscii().|
  const member = /(\)|[A-Za-z_]\w*(?:\.[A-Za-z_]\w*|\[[^\]]*\])*)\.(\w*)$/.exec(before)
  if (member) {
    const options = member[1] === ')' ? STRING_METHODS : membersOf(member[1], before.slice(0, member.index), data)
    if (!options.length) return null
    return { from: ctx.pos - member[2].length, options, validFor: /^\w*$/ }
  }
  if (/[\w\]]\.\w*$/.test(before)) return null

  // A bare identifier, or the first list on nothing.
  const word = ctx.matchBefore(/[A-Za-z_]\w*$/)
  if (!word && !ctx.explicit && !open) return null
  return { from: word ? word.from : ctx.pos, options: topLevel(data), validFor: /^\w*$/ }
}

/* ---------- set paths ---------- */

/** The hints a tool or resource may carry, offered under annotations.* beside what the catalog holds. */
const ANNOTATIONS: Record<string, string[]> = {
  tool: ['readOnlyHint', 'destructiveHint', 'idempotentHint', 'openWorldHint', 'title'],
  resource: ['audience', 'priority', 'lastModified'],
  template: ['audience', 'priority', 'lastModified'],
}

/** The names a `*` in a set path can stand for, given the path before it. */
function wildcardNames(before: string[], data: CelCompletionData): string[] {
  const head = before[before.length - 1]
  if (head === 'args' || head === 'params' || head === 'arguments') return [...parameterNames(data).keys()]
  if (head === 'annotations') return [...new Set([...(ANNOTATIONS[data.hook?.item ?? ''] ?? []), ...catalogKeys(data, 'annotations')])]
  if (head === 'meta') return catalogKeys(data, 'meta')
  return []
}

/** Inserts a completion and opens the next list straight away. */
export function thenComplete(text: string) {
  return (view: EditorView, _c: Completion, from: number, to: number) => {
    view.dispatch({ changes: { from, to, insert: text }, selection: { anchor: from + text.length }, userEvent: 'input.complete' })
    startCompletion(view)
  }
}

/**
 * Completes the path after `set`, one part at a time: `params.`, then the parameter, then what
 * to set on it. A part that ends a path inserts ` = ` and opens the values.
 */
export function completeSetPath(ctx: CompletionContext, typed: string, sets: HookSet[], data: CelCompletionData): CompletionResult | null {
  const done = typed.split('.')
  const partial = done.pop() ?? ''
  const options = new Map<string, Completion>()
  for (const s of sets) {
    const parts = s.field.split('.')
    if (parts.length <= done.length || !done.every((seg, i) => parts[i] === '*' || parts[i] === seg)) continue
    const last = parts.length === done.length + 1
    const names = parts[done.length] === '*' ? wildcardNames(done, data) : [parts[done.length]]
    for (const name of names) {
      if (options.has(name) && !last) continue
      options.set(name, { label: name, type: 'property', detail: last ? s.type : '…', apply: thenComplete(last ? name + ' = ' : name + '.') })
    }
  }
  if (!options.size) return null
  return { from: ctx.pos - partial.length, options: [...options.values()], validFor: /^[\w*/-]*$/ }
}

/** The type of the field a set path names, or undefined when no field matches it. */
export function setTypeOf(path: string, sets: HookSet[]): string | undefined {
  const segs = path.split('.')
  return sets.find((s) => {
    const parts = s.field.split('.')
    return parts.length === segs.length && parts.every((p, i) => p === '*' || p === segs[i])
  })?.type
}
