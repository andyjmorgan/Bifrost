import type { CapabilityKind, Selection } from '../../api'

export const SLUG_MAX = 24
const SLUG_RE = /^[a-z0-9]([a-z0-9-]{0,22}[a-z0-9])?$/

/** Derive a slug exactly as the Go side does: lowercase, other characters become one hyphen, 24 max. */
export function slugBase(name: string): string {
  let s = ''
  let dash = false
  for (const ch of name.toLowerCase()) {
    if (ch.charCodeAt(0) > 127) continue
    if ((ch >= 'a' && ch <= 'z') || (ch >= '0' && ch <= '9')) {
      s += ch
      dash = false
    } else if (s.length > 0 && !dash) {
      s += '-'
      dash = true
    }
  }
  s = s.replace(/^-+|-+$/g, '')
  if (!s) s = 'server'
  if (s.length > SLUG_MAX) s = s.slice(0, SLUG_MAX).replace(/-+$/, '')
  return s
}

/** The problem with a slug the user typed, or undefined when the server will accept it. */
export function slugProblem(slug: string): string | undefined {
  if (!slug) return 'Slug is required.'
  if (!SLUG_RE.test(slug)) return 'Use 1–24 lowercase letters, digits or hyphens, not starting or ending with a hyphen.'
  if (slug === 'mcp') return '"mcp" is reserved.'
  return undefined
}

export const KINDS: { kind: CapabilityKind; key: keyof Selection; one: string; many: string; label: string }[] = [
  { kind: 'tool', key: 'tools', one: 'tool', many: 'tools', label: 'Tools' },
  { kind: 'prompt', key: 'prompts', one: 'prompt', many: 'prompts', label: 'Prompts' },
  { kind: 'resource', key: 'resources', one: 'resource', many: 'resources', label: 'Resources' },
  { kind: 'template', key: 'templates', one: 'template', many: 'templates', label: 'Templates' },
]

function countPhrase(counts: number[]): string {
  const parts = KINDS.map((k, i) => (counts[i] ? `${counts[i]} ${counts[i] === 1 ? k.one : k.many}` : '')).filter(Boolean)
  return parts.length ? parts.join(', ') : 'Nothing'
}

/** One kind's part of a selection: "Everything" when its key is absent, "No tools", or "3 tools". */
export function kindSummary(sel: Selection | undefined, k: (typeof KINDS)[number]): string {
  const list = sel?.[k.key]
  if (!list) return `All ${k.many}`
  if (!list.length) return `No ${k.many}`
  return `${list.length} ${list.length === 1 ? k.one : k.many}`
}

/**
 * "Everything" (no key present), or the narrowed kinds: "2 tools, 1 prompt", "No tools". When only
 * some kinds are narrowed the rest stay open, so it ends ", everything else".
 */
export function selectionSummary(sel?: Selection): string {
  const narrowed = KINDS.filter((k) => sel?.[k.key])
  if (!narrowed.length) return 'Everything'
  const parts = narrowed.map((k) => {
    const n = sel![k.key]!.length
    return n ? `${n} ${n === 1 ? k.one : k.many}` : `No ${k.many}`
  })
  if (narrowed.length < KINDS.length) parts.push('everything else')
  return parts.join(', ')
}

/** The full per-kind breakdown, for a tooltip: "1 tool · All prompts · All resources · All templates". */
export function selectionDetail(sel?: Selection): string {
  return KINDS.map((k) => kindSummary(sel, k)).join(' · ')
}

/** Groups "kind:name" strings by kind. */
export function groupCapabilities(caps: string[]): Record<CapabilityKind, string[]> {
  const out: Record<CapabilityKind, string[]> = { tool: [], prompt: [], resource: [], template: [] }
  for (const c of caps) {
    const i = c.indexOf(':')
    const kind = c.slice(0, i) as CapabilityKind
    if (out[kind]) out[kind].push(c.slice(i + 1))
  }
  return out
}

/** "2 tools, 1 resource" for a list of "kind:name". */
export function capabilitySummary(caps: string[]): string {
  const g = groupCapabilities(caps)
  return countPhrase(KINDS.map((k) => g[k.kind].length))
}

/** "user" → "Direct grant", "role:member" → "Members role". */
export function viaLabel(via: string): string {
  if (via === 'user') return 'Direct'
  if (via === 'role:admin') return 'Admins'
  if (via === 'role:member') return 'Members'
  return via
}
