import { useId, useState } from 'react'
import { Plus, X } from 'lucide-react'
import { Button } from '../../ui/components/button'
import { Input } from '../../ui/components/input'
import { Checkbox } from '../../ui/components/checkbox'
import { Callout } from '../Callout'
import { ListBar, ListCard, ListRows } from '../ListCard'
import { KINDS } from '../rules/selection'
import type { CapabilityDTO } from '../../api'
import { cn } from '../../lib'
import { CapabilityInfo, CapabilitySummary, matchesCapability } from './CapabilitySummary'

// One kind of a server's selection: everything of that kind, or only what is ticked. Resources
// also take URI patterns.

export function KindSection({
  kind: k,
  list,
  caps,
  onChange,
}: {
  kind: (typeof KINDS)[number]
  list: string[] | null
  caps: CapabilityDTO[]
  onChange: (list: string[] | null) => void
}) {
  const [filter, setFilter] = useState('')
  const only = list !== null
  const seen = caps.map((c) => c.name)
  const resources = k.kind === 'resource'
  // Resource entries not among the seen URIs are free-text patterns; for other kinds they are names
  // the server no longer offers, shown so they can be cleared.
  const others = (list ?? []).filter((n) => !seen.includes(n))
  const count = (list ?? []).filter((n) => n.trim()).length
  const toggle = (name: string, on: boolean) => onChange(on ? [...(list ?? []), name] : (list ?? []).filter((n) => n !== name))
  const setPatterns = (next: string[]) => onChange([...(list ?? []).filter((r) => seen.includes(r)), ...next])

  return (
    <ListCard>
      <ListBar>
        <span className="text-[10px] font-semibold uppercase tracking-[0.16em]">
          {k.label}
          {only && <span className="ml-1 font-mono normal-case tracking-normal">{count}</span>}
        </span>
        <div className="flex overflow-hidden rounded-lg border border-border" role="group" aria-label={`${k.label} selection`}>
          {(
            [
              [false, 'Everything'],
              [true, 'Only selected'],
            ] as const
          ).map(([v, label]) => (
            <button
              key={label}
              type="button"
              onClick={() => v !== only && onChange(v ? [] : null)}
              aria-pressed={only === v}
              className={cn(
                'h-8 cursor-pointer px-3 text-xs font-medium transition-colors',
                only === v ? 'bg-elevated text-foreground' : 'text-muted-foreground hover:bg-elevated/60 hover:text-foreground',
              )}
            >
              {label}
            </button>
          ))}
        </div>
      </ListBar>
      <div className="border-b border-border px-5 py-3">
        <Input value={filter} onChange={(e) => setFilter(e.target.value)} placeholder={`Search ${k.many}`} aria-label={`Search ${k.many} selection`} className="h-8 text-xs" />
      </div>
      {!only ? (
        <>
          <p className="px-5 py-4 text-xs text-muted-foreground">
            Every {k.one} the server offers is exposed{seen.length ? ` (${seen.length} seen)` : ''}, including ones it adds later.
          </p>
          <CapabilityChecks items={caps} selected={seen} extra={[]} onToggle={toggle} filter={filter} readOnly empty={`No ${k.many} seen on this server.`} />
        </>
      ) : (
        <>
          {count === 0 && (
            <div className="border-b border-border px-5 py-3">
              <Callout tone="warning">No {k.many} will be exposed.</Callout>
            </div>
          )}
          <CapabilityChecks
            filter={filter}
            items={caps}
            selected={list}
            extra={resources ? [] : others}
            onToggle={toggle}
            empty={resources ? (others.length ? undefined : 'No resources seen. Add a URI pattern to allow resources by address.') : `No ${k.many} seen on this server.`}
          />
          {resources && (
            <div className="space-y-2 border-t border-border p-4">
              <div className="flex items-center justify-between gap-3">
                <p className="text-xs text-faint-foreground">Patterns: an exact URI, * within one path segment, ** for any suffix.</p>
                <Button variant="secondary" size="sm" onClick={() => setPatterns([...others, ''])}>
                  <Plus /> Add pattern
                </Button>
              </div>
              {others.map((p, i) => (
                <div key={i} className="flex items-center gap-2">
                  <Input
                    value={p}
                    onChange={(e) => setPatterns(others.map((x, j) => (j === i ? e.target.value : x)))}
                    placeholder="file:///docs/**"
                    className="h-9 font-mono text-xs"
                    aria-label={`Resource pattern ${i + 1}`}
                  />
                  <Button variant="ghost" size="iconSm" className="hover:text-danger" aria-label="Remove pattern" onClick={() => setPatterns(others.filter((_, j) => j !== i))}>
                    <X />
                  </Button>
                </div>
              ))}
            </div>
          )}
        </>
      )}
    </ListCard>
  )
}

function CapabilityChecks({
  items,
  selected,
  extra,
  onToggle,
  empty,
  filter,
  readOnly = false,
}: {
  items: CapabilityDTO[]
  selected: string[]
  /** Selected names the server no longer offers: shown so they can be cleared. */
  extra: string[]
  onToggle: (name: string, on: boolean) => void
  empty?: string
  filter: string
  readOnly?: boolean
}) {
  const prefix = useId()
  const visible = items.filter((c) => matchesCapability(c, filter))
  const visibleExtra = extra.filter((n) => n.toLowerCase().includes(filter.trim().toLowerCase()))
  if (filter.trim() && !visible.length && !visibleExtra.length) return <p className="px-5 py-6 text-center text-xs text-muted-foreground">No capabilities match.</p>
  if (!items.length && !extra.length) return empty ? <p className="px-5 py-6 text-center text-xs text-muted-foreground">{empty}</p> : null
  return (
    <ListRows>
      {visible.map((c) => {
        const id = `${prefix}-${c.kind}-${c.name}`
        return (
          <div key={c.name} className="flex items-center gap-3 px-5 py-2.5 hover:bg-elevated/45">
            <label htmlFor={id} className={cn('flex min-w-0 flex-1 items-center gap-3', !readOnly && 'cursor-pointer')}>
              <Checkbox id={id} checked={selected.includes(c.name)} disabled={readOnly} onCheckedChange={(v) => onToggle(c.name, v === true)} aria-label={c.name} />
              <CapabilitySummary cap={c} />
            </label>
            <CapabilityInfo cap={c} />
          </div>
        )
      })}
      {visibleExtra.map((n) => (
        <label key={n} className="flex cursor-pointer items-center gap-3 px-5 py-2.5 hover:bg-elevated/45">
          <Checkbox checked onCheckedChange={(v) => onToggle(n, v === true)} aria-label={n} />
          <span className="min-w-0 flex-1 truncate font-mono text-xs">{n}</span>
          <span className="shrink-0 text-[11px] text-warning">not seen on the server</span>
        </label>
      ))}
    </ListRows>
  )
}
