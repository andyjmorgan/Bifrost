import { useState } from 'react'
import { ArrowDown, ArrowUp, BookOpen, Check, GripVertical, Plus, ScrollText, X } from 'lucide-react'
import { Button } from '../../ui/components/button'
import { Input } from '../../ui/components/input'
import { Switch } from '../../ui/components/switch'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '../../ui/components/select'
import { ListBar, ListCard, ListEmpty, ListRows } from '../ListCard'
import type { CapabilityDTO, CapabilityKind, Rule } from '../../api'
import { CelEditor } from './CelEditor'
import { cn } from '../../lib'
import { KINDS } from './selection'

const PLACEHOLDER: Record<CapabilityKind | 'any', string> = {
  tool: 'tool.name.startsWith("get_")',
  prompt: 'prompt.name in ["summarise", "triage"]',
  resource: 'resource.uri.startsWith("confluence://spaces/ENG/")',
  template: 'template.uriTemplate.startsWith("confluence://")',
  any: 'caller.role != "admin"',
}

/** Problems from POST /entitlements/validate, keyed by rule id so they survive a reorder. */
export type RuleProblems = Record<string, string[]>

export function newRule(partial: Partial<Rule> = {}): Rule {
  return { id: crypto.randomUUID(), name: '', action: 'allow', when: '', ...partial }
}

/**
 * The advanced rules: evaluated top to bottom after the allow lists, last match wins. The editor
 * owns the list and the problems; this card edits them and asks for validation on blur.
 */
export function RulesCard({
  rules,
  onChange,
  problems,
  onValidate,
  validating,
  slug,
  caps,
}: {
  rules: Rule[]
  onChange: (rules: Rule[]) => void
  problems: RuleProblems
  /** Re-run validation against the given list (defaults to the current one). */
  onValidate: (rules?: Rule[]) => void
  validating: boolean
  /** For completions: the entitlement's slug and the server's catalog. */
  slug: string
  caps: CapabilityDTO[]
}) {
  const [dragFrom, setDragFrom] = useState<number>()
  const [dragOver, setDragOver] = useState<number>()
  const [armed, setArmed] = useState<number>()

  const update = (i: number, patch: Partial<Rule>) => onChange(rules.map((r, j) => (j === i ? { ...r, ...patch } : r)))
  const move = (from: number, to: number) => {
    if (to < 0 || to >= rules.length || from === to) return
    const next = [...rules]
    const [r] = next.splice(from, 1)
    next.splice(to, 0, r)
    onChange(next)
  }
  const remove = (i: number) => {
    const next = rules.filter((_, j) => j !== i)
    onChange(next)
    if (Object.keys(problems).length) onValidate(next)
  }
  const add = (r: Rule = newRule()) => onChange([...rules, r])
  const problemCount = Object.values(problems).reduce((n, p) => n + p.length, 0)

  return (
    <section className="space-y-3">
      <div className="flex flex-wrap items-end justify-between gap-3">
        <div>
          <h2 className="text-base font-semibold tracking-tight">Rules</h2>
          <p className="mt-0.5 max-w-2xl text-xs text-muted-foreground">
            Allow lists first, then these rules top to bottom: each enabled rule that matches sets the outcome, and the last match wins.
          </p>
        </div>
        <Button variant="secondary" size="sm" onClick={() => add()}>
          <Plus /> Add rule
        </Button>
      </div>
      <ListCard>
        <ListBar>
          <span>
            {rules.length} {rules.length === 1 ? 'rule' : 'rules'}
            {problemCount > 0 && <span className="ml-2 text-danger">{problemCount === 1 ? '1 problem' : `${problemCount} problems`}</span>}
          </span>
          {validating && <span className="font-mono">validating…</span>}
        </ListBar>
        {rules.length === 0 ? (
          <ListEmpty icon={ScrollText} title="No rules" hint="No rules. The allow lists decide on their own." />
        ) : (
          <ListRows>
            {rules.map((r, i) => {
              const errs = problems[r.id] ?? []
              const enabled = r.enabled !== false
              return (
                <div
                  key={r.id}
                  draggable={armed === i}
                  onDragStart={(e) => {
                    setDragFrom(i)
                    e.dataTransfer.effectAllowed = 'move'
                  }}
                  onDragOver={(e) => {
                    if (dragFrom === undefined) return
                    e.preventDefault()
                    setDragOver(i)
                  }}
                  onDrop={(e) => {
                    e.preventDefault()
                    if (dragFrom !== undefined) move(dragFrom, i)
                    setDragFrom(undefined)
                    setDragOver(undefined)
                  }}
                  onDragEnd={() => {
                    setDragFrom(undefined)
                    setDragOver(undefined)
                    setArmed(undefined)
                  }}
                  className={cn(
                    'space-y-3 px-5 py-4 transition-colors',
                    !enabled && 'opacity-60',
                    dragFrom === i && 'opacity-40',
                    dragOver === i && dragFrom !== i && 'bg-elevated/60',
                  )}
                >
                  <div className="flex flex-wrap items-center gap-2">
                    <span
                      className="-ml-2 flex cursor-grab items-center text-faint-foreground hover:text-muted-foreground active:cursor-grabbing"
                      onMouseDown={() => setArmed(i)}
                      onMouseUp={() => setArmed(undefined)}
                      aria-hidden
                      title="Drag to reorder"
                    >
                      <GripVertical className="size-4" />
                    </span>
                    <span className="w-5 font-mono text-xs text-muted-foreground">{i + 1}</span>
                    <Switch checked={enabled} onCheckedChange={(v) => update(i, { enabled: v ? undefined : false })} aria-label={`Rule ${i + 1} enabled`} />
                    <Select value={r.action} onValueChange={(v) => update(i, { action: v as Rule['action'] })}>
                      <SelectTrigger aria-label={`Rule ${i + 1} action`} className={cn('h-8 w-[92px] text-xs', r.action === 'deny' ? 'text-danger' : 'text-foreground')}>
                        <SelectValue />
                      </SelectTrigger>
                      <SelectContent>
                        <SelectItem value="allow">Allow</SelectItem>
                        <SelectItem value="deny">Deny</SelectItem>
                      </SelectContent>
                    </Select>
                    <KindChips kinds={r.kinds} onChange={(kinds) => update(i, { kinds: kinds.length ? kinds : undefined })} />
                    <Input
                      value={r.name}
                      onChange={(e) => update(i, { name: e.target.value })}
                      placeholder="Name this rule"
                      aria-label={`Rule ${i + 1} name`}
                      className="h-8 min-w-40 flex-1 text-xs"
                    />
                    <span className="flex items-center">
                      <Button variant="ghost" size="iconSm" aria-label="Move up" title="Move up" disabled={i === 0} onClick={() => move(i, i - 1)}>
                        <ArrowUp />
                      </Button>
                      <Button variant="ghost" size="iconSm" aria-label="Move down" title="Move down" disabled={i === rules.length - 1} onClick={() => move(i, i + 1)}>
                        <ArrowDown />
                      </Button>
                      <Button variant="ghost" size="iconSm" className="hover:text-danger" aria-label="Remove rule" title="Remove rule" onClick={() => remove(i)}>
                        <X />
                      </Button>
                    </span>
                  </div>
                  <CelEditor
                    value={r.when}
                    onChange={(when) => update(i, { when })}
                    onBlur={() => onValidate()}
                    placeholder={PLACEHOLDER[r.kinds?.length === 1 ? r.kinds[0] : 'any']}
                    completions={{ slug, caps, kinds: r.kinds }}
                    invalid={errs.length > 0}
                    ariaLabel={`Rule ${i + 1} when`}
                  />
                  {errs.map((m, k) => (
                    <p key={k} className="break-words font-mono text-xs text-danger">
                      {m}
                    </p>
                  ))}
                </div>
              )
            })}
          </ListRows>
        )}
      </ListCard>
      <Reference onUse={(r) => add(newRule(r))} />
    </section>
  )
}

function KindChips({ kinds, onChange }: { kinds?: CapabilityKind[]; onChange: (k: CapabilityKind[]) => void }) {
  const set = kinds ?? []
  return (
    <span className="flex flex-wrap items-center gap-1" role="group" aria-label="Kinds">
      {KINDS.map((k) => {
        const on = set.includes(k.kind)
        return (
          <button
            key={k.kind}
            type="button"
            aria-pressed={on}
            onClick={() => onChange(on ? set.filter((x) => x !== k.kind) : [...set, k.kind])}
            className={cn(
              'inline-flex h-8 cursor-pointer items-center gap-1 rounded-lg border px-2.5 text-xs font-medium transition-colors',
              on ? 'border-accent/20 bg-accent/10 text-accent' : 'bg-transparent text-muted-foreground hover:bg-elevated hover:text-foreground',
            )}
          >
            {on && <Check className="size-3.5" />}
            {k.label}
          </button>
        )
      })}
      {set.length === 0 && <span className="px-1 text-[11px] text-faint-foreground">any kind</span>}
    </span>
  )
}

/* ---------- reference ---------- */

const VARIABLES: { name: string; fields: string }[] = [
  { name: 'capability', fields: 'kind, name, plus every field of the definition below' },
  {
    name: 'tool',
    fields:
      'name, title, description, annotations {readOnlyHint=false, destructiveHint=true, idempotentHint=false, openWorldHint=true by default, title}, annotationsRaw, inputSchema, outputSchema, meta',
  },
  { name: 'prompt', fields: 'name, title, description, arguments, meta' },
  { name: 'resource', fields: 'uri, name, title, description, mimeType, meta' },
  { name: 'template', fields: 'uriTemplate, name, title, description, mimeType, meta' },
  { name: 'server', fields: 'slug, name, description, url, instructions, protocolVersion' },
  { name: 'caller', fields: 'userId, email, name, role ("admin" | "member"), type ("oidc" | "apikey" | "dev"), keyName' },
  { name: 'client', fields: 'name, version, protocolVersion, capabilities {elicitation, sampling, roots} (booleans)' },
  { name: 'route', fields: '"mux" | "direct"' },
]

const EXAMPLES: { name: string; action: Rule['action']; kinds?: CapabilityKind[]; when: string }[] = [
  { name: 'Read-style tools', action: 'allow', kinds: ['tool'], when: 'tool.name.matches("^(get|list|read)_")' },
  { name: 'No destructive tools', action: 'deny', kinds: ['tool'], when: 'tool.annotations.destructiveHint && !tool.annotations.readOnlyHint' },
  { name: 'Admins only', action: 'deny', when: 'caller.role != "admin"' },
  { name: 'Sending needs elicitation', action: 'deny', kinds: ['tool'], when: '!client.capabilities.elicitation && tool.name.startsWith("send_")' },
  { name: 'ENG space only', action: 'allow', kinds: ['resource'], when: 'resource.uri.startsWith("confluence://spaces/ENG/")' },
]

function Reference({ onUse }: { onUse: (r: Partial<Rule>) => void }) {
  return (
    <details className="group rounded-2xl border border-border bg-card text-sm">
      <summary className="flex cursor-pointer select-none items-center gap-2 px-5 py-3.5 text-[10px] font-semibold uppercase tracking-[0.16em] text-muted-foreground">
        <BookOpen className="size-3.5" /> Reference
      </summary>
      <div className="space-y-5 border-t border-border px-5 py-4">
        <div className="space-y-2 text-xs leading-5 text-muted-foreground">
          <p>
            Each enabled rule whose kinds include the capability and whose <code className="font-mono text-foreground">when</code> is true sets the outcome to
            its action; the last match wins. An allow rule can re-enable something a list left out, and a later deny can cut again.
          </p>
          <p>
            A rule that errors while evaluating (for example reading <code className="font-mono text-foreground">tool.annotations</code> on a prompt) counts as a
            deny, so scope rules to kinds. A rule list that does not compile fails the whole entitlement closed, and listings report "rules do not compile".
          </p>
        </div>

        <div>
          <p className="mb-1.5 text-[10px] font-semibold uppercase tracking-[0.16em] text-muted-foreground">Variables</p>
          <dl className="divide-y divide-border rounded-lg border border-border">
            {VARIABLES.map((v) => (
              <div key={v.name} className="grid gap-1 px-4 py-2 sm:grid-cols-[110px_minmax(0,1fr)] sm:gap-4">
                <dt className="font-mono text-xs text-foreground">{v.name}</dt>
                <dd className="text-xs leading-5 text-muted-foreground">{v.fields}</dd>
              </div>
            ))}
          </dl>
          <p className="mt-2 text-xs leading-5 text-muted-foreground">
            <code className="font-mono text-foreground">tool</code>, <code className="font-mono text-foreground">prompt</code>,{' '}
            <code className="font-mono text-foreground">resource</code> and <code className="font-mono text-foreground">template</code> are the same object as{' '}
            <code className="font-mono text-foreground">capability</code>, populated only for the matching kind and an empty map otherwise.
          </p>
        </div>

        <div>
          <p className="mb-1.5 text-[10px] font-semibold uppercase tracking-[0.16em] text-muted-foreground">Functions</p>
          <p className="font-mono text-xs leading-6 text-muted-foreground">
            startsWith · endsWith · contains · matches (RE2) · lowerAscii() · in · has(x.y) · &amp;&amp; · || · ! · ? : · s.regexReplace(pattern, replacement)
          </p>
        </div>

        <div>
          <p className="mb-1.5 text-[10px] font-semibold uppercase tracking-[0.16em] text-muted-foreground">Examples</p>
          <div className="divide-y divide-border rounded-lg border border-border">
            {EXAMPLES.map((ex) => (
              <div key={ex.name} className="flex items-start gap-3 px-4 py-2.5">
                <span className={cn('w-10 shrink-0 pt-0.5 text-xs font-medium', ex.action === 'deny' ? 'text-danger' : 'text-foreground')}>
                  {ex.action === 'deny' ? 'Deny' : 'Allow'}
                </span>
                <span className="w-20 shrink-0 pt-0.5 text-xs text-muted-foreground">
                  {ex.kinds ? ex.kinds.map((k) => KINDS.find((x) => x.kind === k)?.label).join(', ') : 'Any kind'}
                </span>
                <code className="min-w-0 flex-1 break-all pt-0.5 font-mono text-xs text-foreground">{ex.when}</code>
                <Button variant="ghost" size="sm" className="h-7 shrink-0 px-2 text-xs" onClick={() => onUse({ name: ex.name, action: ex.action, kinds: ex.kinds, when: ex.when })}>
                  <Plus /> Use
                </Button>
              </div>
            ))}
          </div>
        </div>
      </div>
    </details>
  )
}
