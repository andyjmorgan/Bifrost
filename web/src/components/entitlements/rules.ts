import type { HookSchema, HookSet, Rule, RuleMode, RuleProblem, RulesSchema } from '../../api'

// The rule list as the editor holds it. A stored rule's `then` is a map, which cannot keep a row
// steady while its key is being typed, so the draft holds rows; and a stored rule may have no id,
// so every draft carries a client-only key for React and for pinning problems across a reorder.

export interface ThenRow {
  /** Client-only. */
  key: string
  /** The concrete `then` key, e.g. "decision" or "args.limit". */
  field: string
  /** CEL expression. */
  expr: string
}

export interface RuleDraft {
  /** Client-only. */
  key: string
  id?: string
  name: string
  enabled: boolean
  mode: RuleMode
  hook: string
  when: string
  then: ThenRow[]
}

const uid = () => crypto.randomUUID()

export function toDrafts(rules: Rule[]): RuleDraft[] {
  return rules.map((r) => ({
    key: uid(),
    id: r.id,
    name: r.name ?? '',
    enabled: r.enabled !== false,
    mode: r.mode === 'audit' ? 'audit' : 'enforce',
    hook: r.hook,
    when: r.when,
    then: Object.entries(r.then ?? {}).map(([field, expr]) => ({ key: uid(), field, expr })),
  }))
}

/** Rules as sent: defaults left out, `then` keys sorted as the server stores them. */
export function fromDrafts(drafts: RuleDraft[]): Rule[] {
  return drafts.map((d) => {
    const out: Rule = { hook: d.hook, when: d.when }
    if (d.id) out.id = d.id
    if (d.name.trim()) out.name = d.name.trim()
    if (!d.enabled) out.enabled = false
    if (d.mode === 'audit') out.mode = 'audit'
    if (d.then.length) out.then = Object.fromEntries(d.then.map((t) => [t.field, t.expr] as const).sort(([a], [b]) => (a < b ? -1 : a > b ? 1 : 0)))
    return out
  })
}

/** `then` keys a rule sets more than once. Only one would be sent, so the editor refuses to save. */
export function duplicateFields(rule: RuleDraft): Set<string> {
  const seen = new Set<string>()
  const dup = new Set<string>()
  for (const t of rule.then) (seen.has(t.field) ? dup : seen).add(t.field)
  return dup
}

/* ---------- problems ---------- */

/** Problems keyed by draft key, so they survive a reorder. "" holds those about the list as a whole. */
export type RuleProblems = Record<string, RuleProblem[]>

export function keyProblems(drafts: RuleDraft[], problems: RuleProblem[]): RuleProblems {
  const out: RuleProblems = {}
  for (const p of problems) (out[drafts[p.rule]?.key ?? ''] ??= []).push(p)
  return out
}

/* ---------- schema ---------- */

export const hookOf = (schema: RulesSchema | undefined, hook: string): HookSchema | undefined => schema?.hooks.find((h) => h.hook === hook)

// Presentation order only: the common hooks first, and each family in the order a request meets
// them. A family or phase the schema adds later sorts after these.
const FAMILY_ORDER = ['tools', 'prompts', 'resources', 'templates', 'skills', 'server']
const PHASE_ORDER = ['filter', 'modify', 'call', 'input', 'event', 'result']
const rank = (order: string[], x: string) => (order.includes(x) ? order.indexOf(x) : order.length)

/** Hooks grouped by the part before the slash. */
export function groupHooks(schema: RulesSchema | undefined): { family: string; hooks: HookSchema[] }[] {
  const groups = new Map<string, HookSchema[]>()
  for (const h of schema?.hooks ?? []) {
    const family = h.hook.split('/')[0]
    groups.set(family, [...(groups.get(family) ?? []), h])
  }
  return [...groups.entries()]
    .sort(([a], [b]) => rank(FAMILY_ORDER, a) - rank(FAMILY_ORDER, b))
    .map(([family, hooks]) => ({ family, hooks: [...hooks].sort((a, b) => rank(PHASE_ORDER, a.phase) - rank(PHASE_ORDER, b.phase)) }))
}

/** A filter hook only hides items: its one useful `then` is the decision. */
export const isFilter = (hook: HookSchema | undefined, name: string) => (hook ? hook.phase === 'filter' : name.endsWith('/filter'))

/** The value a newly set field starts with: a decision can only be 'deny'. */
export const defaultExpr = (set: HookSet) => (set.type === 'decision' ? "'deny'" : '')

export function denyRow(hook: HookSchema | undefined): ThenRow | undefined {
  const set = hook?.sets.find((s) => s.type === 'decision')
  return set && { key: uid(), field: set.field, expr: defaultExpr(set) }
}

export function newRow(set: HookSet): ThenRow {
  return { key: uid(), field: buildField(set.field, []), expr: defaultExpr(set) }
}

/** A new rule starts as the commonest one: hide matching tools. */
export function newRule(schema: RulesSchema | undefined): RuleDraft {
  const hooks = schema?.hooks ?? []
  const hook = hooks.find((h) => h.hook === 'tools/filter') ?? hooks.find((h) => h.phase === 'filter') ?? hooks[0]
  const deny = isFilter(hook, '') ? denyRow(hook) : undefined
  return { key: uid(), id: uid(), name: '', enabled: true, mode: 'enforce', hook: hook?.hook ?? '', when: '', then: deny ? [deny] : [] }
}

/* ---------- then keys ---------- */

// A then key is a dotted path. A backslash escapes the next character, so a property literally
// named "a.b" is written `a\.b` (pkg/hooks/view.go, splitPath).

export function splitPath(p: string): string[] {
  const out: string[] = []
  let cur = ''
  let esc = false
  for (const ch of p) {
    if (esc) {
      cur += ch
      esc = false
    } else if (ch === '\\') esc = true
    else if (ch === '.') {
      out.push(cur)
      cur = ''
    } else cur += ch
  }
  out.push(cur)
  return out
}

const escapeSegment = (s: string) => s.replace(/[\\.]/g, '\\$&')

/** The concrete key for a set pattern, with each "*" replaced by the next name. */
export function buildField(pattern: string, names: string[]): string {
  let i = 0
  return pattern
    .split('.')
    .map((seg) => (seg === '*' ? escapeSegment(names[i++] ?? '') : seg))
    .join('.')
}

/**
 * The set pattern a then key belongs to, and the names standing in for its wildcards. Unlike the
 * engine this accepts an empty name, so a row keeps its field while the name is being typed.
 */
export function matchSet(sets: HookSet[], field: string): { set: HookSet; names: string[] } | undefined {
  const parts = splitPath(field)
  for (const set of sets) {
    const pattern = set.field.split('.')
    if (pattern.length !== parts.length || !pattern.every((seg, i) => seg === '*' || seg === parts[i])) continue
    return { set, names: parts.filter((_, i) => pattern[i] === '*') }
  }
  return undefined
}
