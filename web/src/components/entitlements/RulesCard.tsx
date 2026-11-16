import { useId, useState } from 'react'
import { ArrowDown, ArrowUp, Ban, BookOpen, GripVertical, Plus, RotateCw, ScrollText, X } from 'lucide-react'
import { Button } from '../../ui/components/button'
import { Input } from '../../ui/components/input'
import { Switch } from '../../ui/components/switch'
import { Select, SelectContent, SelectGroup, SelectItem, SelectLabel, SelectTrigger, SelectValue } from '../../ui/components/select'
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuTrigger } from '../../ui/components/dropdown-menu'
import { ListBar, ListCard, ListEmpty, ListRows } from '../ListCard'
import { Callout } from '../Callout'
import type { CapabilityDTO, HookSchema, HookSet, RuleMode, RuleProblem, RulesSchema } from '../../api'
import { CelEditor, type CelCompletionData } from './CelEditor'
import { catalogKeys } from './celCompletions'
import { buildField, defaultExpr, denyRow, duplicateFields, groupHooks, hookOf, isFilter, matchSet, newRow, newRule, type RuleDraft, type RuleProblems, type ThenRow } from './rules'
import { cn } from '../../lib'

/**
 * The hook rules: each runs at its hook, top to bottom. The editor page owns the list, the
 * problems and the schema; this card edits the list and asks for validation as fields settle.
 */
export function RulesCard({
  rules,
  onChange,
  problems,
  onValidate,
  validating,
  schema,
  schemaError,
  onReloadSchema,
  rulesError,
  slug,
  caps,
}: {
  rules: RuleDraft[]
  onChange: (rules: RuleDraft[]) => void
  problems: RuleProblems
  /** Re-run validation against the given list (defaults to the current one). */
  onValidate: (rules?: RuleDraft[]) => void
  validating: boolean
  /** What each hook's rules may read and set; undefined while it loads. */
  schema?: RulesSchema
  schemaError?: string
  onReloadSchema: () => void
  /** Why the stored rules cannot be used, from the entitlement. */
  rulesError?: string
  /** For completions: the entitlement's slug and the server's catalog. */
  slug: string
  caps: CapabilityDTO[]
}) {
  const [dragFrom, setDragFrom] = useState<number>()
  const [dragOver, setDragOver] = useState<number>()
  const [armed, setArmed] = useState<number>()
  const groups = groupHooks(schema)

  /** Applies a change to one rule. A change no blur will follow is validated at once, unless the rule has no condition yet. */
  const update = (i: number, patch: Partial<RuleDraft>, settle = false) => {
    const next = rules.map((r, j) => (j === i ? { ...r, ...patch } : r))
    onChange(next)
    if (settle && next[i].when.trim()) onValidate(next)
  }
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
  const general = problems[''] ?? []
  const problemCount = Object.values(problems).reduce((n, p) => n + p.length, 0)

  return (
    <section className="space-y-3">
      {rulesError && (
        <Callout tone="danger">
          <p className="font-medium">This entitlement exposes nothing until its rules are rewritten and saved.</p>
          <p className="break-words font-mono text-xs leading-5 text-muted-foreground">{rulesError}</p>
        </Callout>
      )}
      {schemaError && (
        <Callout tone="danger">
          <div className="flex flex-wrap items-center justify-between gap-3">
            <span className="min-w-0 break-words">Could not load the hooks: {schemaError}</span>
            <Button variant="secondary" size="sm" onClick={onReloadSchema}>
              <RotateCw /> Retry
            </Button>
          </div>
        </Callout>
      )}
      <div className="flex flex-wrap items-end justify-between gap-3">
        <p className="max-w-2xl text-xs text-muted-foreground">
          Each rule runs at its hook, top to bottom. A later rule sees what earlier ones set, and the first deny is final.
        </p>
        <Button variant="secondary" size="sm" onClick={() => onChange([...rules, newRule(schema)])} disabled={!schema}>
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
        {general.length > 0 && (
          <div className="border-b border-border px-5 py-3">
            <Problems items={general} />
          </div>
        )}
        {rules.length === 0 ? (
          <ListEmpty icon={ScrollText} title="No rules" hint="The allow lists decide on their own." />
        ) : (
          <ListRows>
            {rules.map((r, i) => (
              <div
                key={r.key}
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
                className={cn('space-y-3 px-5 py-4 transition-colors', dragFrom === i && 'opacity-40', dragOver === i && dragFrom !== i && 'bg-elevated/60')}
              >
                <RuleEditor
                  rule={r}
                  index={i}
                  count={rules.length}
                  hook={hookOf(schema, r.hook)}
                  groups={groups}
                  problems={problems[r.key] ?? []}
                  completions={{ hook: hookOf(schema, r.hook), flags: schema?.flags, slug, caps }}
                  onChange={(patch, settle) => update(i, patch, settle)}
                  onBlur={() => onValidate()}
                  onMove={(to) => move(i, to)}
                  onRemove={() => remove(i)}
                  onArm={(on) => setArmed(on ? i : undefined)}
                />
              </div>
            ))}
          </ListRows>
        )}
      </ListCard>
    </section>
  )
}

const FIELD_LABEL = 'pt-2.5 font-mono text-xs text-muted-foreground'

function RuleEditor({
  rule: r,
  index: i,
  count,
  hook,
  groups,
  problems,
  completions,
  onChange,
  onBlur,
  onMove,
  onRemove,
  onArm,
}: {
  rule: RuleDraft
  index: number
  count: number
  /** The schema of the rule's hook; undefined while the schema loads or when the hook is unknown. */
  hook?: HookSchema
  groups: ReturnType<typeof groupHooks>
  problems: RuleProblem[]
  completions: CelCompletionData
  /** `settle` marks a change no blur will follow, so it is validated at once. */
  onChange: (patch: Partial<RuleDraft>, settle?: boolean) => void
  onBlur: () => void
  onMove: (to: number) => void
  onRemove: () => void
  onArm: (on: boolean) => void
}) {
  const [reference, setReference] = useState(false)
  const n = i + 1
  const sets = hook?.sets ?? []
  const dups = duplicateFields(r)
  const at = (field: string) => problems.filter((p) => p.field === field)
  const placed = new Set(['hook', 'mode', 'when', 'then', ...r.then.map((t) => t.field)])
  const loose = problems.filter((p) => !p.field || !placed.has(p.field))
  const taken = (s: HookSet) => !s.field.includes('*') && r.then.some((t) => t.field === s.field)
  const canDeny = isFilter(hook, r.hook) && !r.then.some((t) => matchSet(sets, t.field)?.set.type === 'decision')

  const setHook = (name: string) => {
    // Fields the new hook cannot set would never compile, so they go with the old hook.
    const next = groups.flatMap((g) => g.hooks).find((h) => h.hook === name)
    onChange({ hook: name, then: next ? r.then.filter((t) => matchSet(next.sets, t.field)) : r.then }, true)
  }
  const setRow = (key: string, patch: Partial<ThenRow>, settle = false) => onChange({ then: r.then.map((t) => (t.key === key ? { ...t, ...patch } : t)) }, settle)
  const addRow = (row: ThenRow | undefined) => row && onChange({ then: [...r.then, row] }, row.expr !== '')

  return (
    <>
      <div className={cn('flex flex-wrap items-center gap-2', !r.enabled && 'opacity-60')}>
        <span
          className="-ml-2 flex cursor-grab items-center text-faint-foreground hover:text-muted-foreground active:cursor-grabbing"
          onMouseDown={() => onArm(true)}
          onMouseUp={() => onArm(false)}
          aria-hidden
          title="Drag to reorder"
        >
          <GripVertical className="size-4" />
        </span>
        <span className="w-5 font-mono text-xs text-muted-foreground">{n}</span>
        <Switch checked={r.enabled} onCheckedChange={(v) => onChange({ enabled: v }, true)} aria-label={`Rule ${n} enabled`} />
        <Select value={r.hook} onValueChange={setHook}>
          <SelectTrigger aria-label={`Rule ${n} hook`} className={cn('h-8 w-[196px] font-mono text-xs', at('hook').length > 0 && 'ring-[3px] ring-danger/20')}>
            <SelectValue placeholder="Hook" />
          </SelectTrigger>
          <SelectContent>
            {r.hook && !hook && (
              <SelectItem value={r.hook} className="font-mono text-xs">
                {r.hook}
              </SelectItem>
            )}
            {groups.map((g) => (
              <SelectGroup key={g.family}>
                <SelectLabel>{g.family}</SelectLabel>
                {g.hooks.map((h) => (
                  <SelectItem key={h.hook} value={h.hook} className="font-mono text-xs">
                    {h.hook}
                  </SelectItem>
                ))}
              </SelectGroup>
            ))}
          </SelectContent>
        </Select>
        <Select value={r.mode} onValueChange={(v) => onChange({ mode: v as RuleMode }, true)}>
          <SelectTrigger
            aria-label={`Rule ${n} mode`}
            title={r.mode === 'audit' ? 'Records what the rule would do and changes nothing' : undefined}
            className={cn('h-8 w-[104px] text-xs', r.mode === 'audit' && 'text-warning')}
          >
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="enforce">Enforce</SelectItem>
            <SelectItem value="audit">Audit</SelectItem>
          </SelectContent>
        </Select>
        <Input
          value={r.name}
          onChange={(e) => onChange({ name: e.target.value })}
          placeholder="Name this rule"
          aria-label={`Rule ${n} name`}
          className="h-8 min-w-40 flex-1 text-xs"
        />
        <span className="flex items-center">
          <Button variant="ghost" size="iconSm" aria-label="Move up" title="Move up" disabled={i === 0} onClick={() => onMove(i - 1)}>
            <ArrowUp />
          </Button>
          <Button variant="ghost" size="iconSm" aria-label="Move down" title="Move down" disabled={i === count - 1} onClick={() => onMove(i + 1)}>
            <ArrowDown />
          </Button>
          <Button variant="ghost" size="iconSm" className="hover:text-danger" aria-label="Remove rule" title="Remove rule" onClick={onRemove}>
            <X />
          </Button>
        </span>
      </div>
      <Problems items={[...at('hook'), ...at('mode')]} />

      <div className={cn('grid gap-x-3 gap-y-1.5 sm:grid-cols-[36px_minmax(0,1fr)] sm:gap-y-3', !r.enabled && 'opacity-60')}>
        <span className={FIELD_LABEL}>when</span>
        <div className="min-w-0 space-y-1.5">
          <CelEditor
            value={r.when}
            onChange={(when) => onChange({ when })}
            onBlur={onBlur}
            placeholder="A condition that is true or false"
            completions={completions}
            invalid={at('when').length > 0}
            ariaLabel={`Rule ${n} when`}
          />
          <Problems items={at('when')} />
        </div>

        <span className={FIELD_LABEL}>then</span>
        <div className="min-w-0 space-y-2">
          {r.then.map((row) => (
            <ThenRowEditor
              key={row.key}
              row={row}
              label={`Rule ${n}`}
              hook={hook}
              caps={completions.caps}
              completions={completions}
              taken={taken}
              problems={[...at(row.field), ...(dups.has(row.field) ? [{ rule: i, field: row.field, message: `${row.field} is set more than once` }] : [])]}
              onChange={(patch, settle) => setRow(row.key, patch, settle)}
              onBlur={onBlur}
              onRemove={() => onChange({ then: r.then.filter((t) => t.key !== row.key) }, true)}
            />
          ))}
          <div className="flex flex-wrap items-center gap-2">
            <DropdownMenu>
              <DropdownMenuTrigger asChild>
                <Button variant="secondary" size="sm" disabled={!sets.some((s) => !taken(s))}>
                  <Plus /> Set field
                </Button>
              </DropdownMenuTrigger>
              <DropdownMenuContent align="start" className="max-h-80 overflow-y-auto">
                {sets.map((s) => (
                  <DropdownMenuItem key={s.field} disabled={taken(s)} onSelect={() => addRow(newRow(s))} className="font-mono text-xs">
                    {s.field}
                    <span className="ml-auto pl-6 text-[11px] text-muted-foreground">{s.type}</span>
                  </DropdownMenuItem>
                ))}
              </DropdownMenuContent>
            </DropdownMenu>
            {canDeny && (
              <Button variant="secondary" size="sm" title="Set decision to 'deny'" onClick={() => addRow(denyRow(hook))} disabled={!hook}>
                <Ban /> Deny
              </Button>
            )}
            {hook && (
              <Button variant="ghost" size="sm" className="ml-auto" aria-expanded={reference} onClick={() => setReference(!reference)}>
                <BookOpen /> Variables
              </Button>
            )}
          </div>
          <Problems items={at('then')} />
        </div>
      </div>
      <Problems items={loose} />
      {reference && hook && <VariableReference hook={hook} flags={completions.flags} />}
    </>
  )
}

function ThenRowEditor({
  row,
  label,
  hook,
  caps,
  completions,
  taken,
  problems,
  onChange,
  onBlur,
  onRemove,
}: {
  row: ThenRow
  /** "Rule 2", for the controls' accessible names. */
  label: string
  hook?: HookSchema
  caps: CapabilityDTO[]
  completions: CelCompletionData
  /** Whether another row already sets a field that can only be set once. */
  taken: (s: HookSet) => boolean
  problems: RuleProblem[]
  onChange: (patch: Partial<ThenRow>, settle?: boolean) => void
  onBlur: () => void
  onRemove: () => void
}) {
  const listId = useId()
  const sets = hook?.sets ?? []
  const match = matchSet(sets, row.field)
  const pattern = match?.set.field
  const invalid = problems.length > 0

  const setPattern = (next: string) => {
    const set = sets.find((s) => s.field === next)
    if (!set) return
    // An untouched starting value follows the field; anything typed stays.
    const untouched = row.expr === '' || (match !== undefined && row.expr === defaultExpr(match.set))
    onChange({ field: buildField(set.field, match?.names ?? []), expr: untouched ? defaultExpr(set) : row.expr }, true)
  }
  const setName = (at: number, value: string) => match && onChange({ field: buildField(match.set.field, match.names.map((x, j) => (j === at ? value : x))) })
  const suggestions = pattern?.includes('*') ? catalogKeys(caps, hook?.item, pattern.split('.')[0]) : []

  return (
    <div className="space-y-1.5">
      <div className="flex flex-wrap items-start gap-2">
        <Select value={pattern ?? row.field} onValueChange={setPattern}>
          <SelectTrigger aria-label={`${label} field ${row.field}`} className={cn('order-1 min-w-0 flex-1 font-mono text-xs sm:w-[212px] sm:flex-none', !match && hook && 'text-danger')}>
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            {!match && row.field && (
              <SelectItem value={row.field} className="font-mono text-xs">
                {row.field}
              </SelectItem>
            )}
            {sets.map((s) => (
              <SelectItem key={s.field} value={s.field} disabled={s.field !== pattern && taken(s)} className="font-mono text-xs">
                {s.field}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
        {match?.names.map((name, j) => (
          <Input
            key={j}
            value={name}
            onChange={(e) => setName(j, e.target.value)}
            onBlur={onBlur}
            placeholder="name"
            list={suggestions.length ? listId : undefined}
            title={`The name that stands for * in ${pattern}`}
            aria-label={`${label} name for * in ${pattern}`}
            spellCheck={false}
            autoCapitalize="off"
            className="order-3 w-full font-mono text-xs sm:w-36"
          />
        ))}
        {suggestions.length > 0 && (
          <datalist id={listId}>
            {suggestions.map((k) => (
              <option key={k} value={k} />
            ))}
          </datalist>
        )}
        <CelEditor
          value={row.expr}
          onChange={(expr) => onChange({ expr })}
          onBlur={onBlur}
          placeholder={match ? `CEL (${match.set.type})` : 'CEL'}
          completions={{ ...completions, setType: match?.set.type }}
          invalid={invalid}
          ariaLabel={`${label} value of ${row.field}`}
          className="order-4 min-h-10 flex-1 basis-full sm:min-w-48 sm:basis-0"
        />
        <Button variant="ghost" size="iconSm" className="order-2 mt-1 hover:text-danger sm:order-5" aria-label={`Remove ${row.field}`} title="Remove field" onClick={onRemove}>
          <X />
        </Button>
      </div>
      <Problems items={problems} />
    </div>
  )
}

function Problems({ items }: { items: RuleProblem[] }) {
  if (!items.length) return null
  return (
    <div className="space-y-1">
      {items.map((p, k) => (
        <p key={k} className="break-words font-mono text-xs text-danger">
          {p.message}
        </p>
      ))}
    </div>
  )
}

/** What the rule's hook can read, straight from the schema: variables, struct fields, flags and helpers. */
function VariableReference({ hook, flags }: { hook: HookSchema; flags?: Record<string, string> }) {
  const rows = hook.variables.flatMap((v) => [
    { name: v.name, type: v.type, doc: v.values ? `${v.doc} One of ${v.values.map((x) => `'${x}'`).join(', ')}.` : v.doc },
    ...Object.entries(v.fields ?? {})
      .sort(([a], [b]) => a.localeCompare(b))
      .map(([f, type]) => ({ name: `${v.name}.${f}`, type, doc: (v.name === 'flags' && flags?.[f]) || '' })),
  ])
  return (
    <dl className="divide-y divide-border rounded-lg border border-border">
      {rows.map((v) => (
        <div key={v.name} className="grid gap-x-4 gap-y-0.5 px-4 py-2 sm:grid-cols-[150px_76px_minmax(0,1fr)]">
          <dt className="break-all font-mono text-xs leading-5 text-foreground">{v.name}</dt>
          <dd className="font-mono text-xs leading-5 text-muted-foreground">{v.type}</dd>
          <dd className="text-xs leading-5 text-muted-foreground">{v.doc}</dd>
        </div>
      ))}
      {hook.helpers?.map((h) => (
        <div key={h} className="grid gap-x-4 gap-y-0.5 px-4 py-2 sm:grid-cols-[150px_76px_minmax(0,1fr)]">
          <dt className="break-all font-mono text-xs leading-5 text-foreground">{h}()</dt>
          <dd className="font-mono text-xs leading-5 text-muted-foreground">helper</dd>
        </div>
      ))}
    </dl>
  )
}
