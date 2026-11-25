import { useState, type ReactNode } from 'react'
import { Telescope } from 'lucide-react'
import { Cell, ListBar, ListCard, ListEmpty, ListHead, ListRow, ListRows } from '../ListCard'
import type { CapabilityDTO } from '../../api'
import { absoluteTime, relativeTime } from '../../lib'
import { KINDS } from '../rules/selection'
import { CapabilityInfo, CapabilitySummary, matchesCapability } from './CapabilitySummary'
import { Input } from '../../ui/components/input'

const COLS = 'minmax(0,1.6fr) 150px 150px 32px'

function Time({ value }: { value: string }) {
  return (
    <span className="font-mono text-xs text-muted-foreground" title={absoluteTime(value)}>
      {relativeTime(value)}
    </span>
  )
}

/**
 * The server's catalog: every capability the gateway has seen on it, grouped by kind. Read-only;
 * the server's selection chooses from it. A row opens its definition in a sheet.
 */
export function CatalogCard({
  caps,
  loaded,
  seedActions,
}: {
  caps: CapabilityDTO[]
  loaded: boolean
  /** Ping / list actions offered in the empty state. */
  seedActions?: ReactNode
}) {
  const [filter, setFilter] = useState('')
  const visible = caps.filter((c) => matchesCapability(c, filter))
  const keyOf = (c: CapabilityDTO) => `${c.kind}:${c.name}`
  return (
    <ListCard>
      <ListBar>
        <span>
          {filter.trim() ? `${visible.length} of ${caps.length}` : caps.length} {caps.length === 1 ? 'capability' : 'capabilities'} seen
        </span>
        <Input value={filter} onChange={(e) => setFilter(e.target.value)} placeholder="Search catalog" aria-label="Search catalog" className="h-8 w-full text-xs sm:w-64" />
      </ListBar>
      {caps.length === 0 ? (
        <ListEmpty
          icon={Telescope}
          title={loaded ? 'Nothing seen yet' : 'Loading…'}
          hint={loaded ? 'Nothing seen yet. Ping or list the server to populate the catalog.' : undefined}
          action={loaded ? seedActions : undefined}
        />
      ) : visible.length === 0 ? <ListEmpty icon={Telescope} title="No capabilities match" hint="Try a different name, description or URI." /> : (
        <>
          <ListHead cols={COLS}>
            <span>Capability</span>
            <span>First seen</span>
            <span>Last seen</span>
            <span className="sr-only">Info</span>
          </ListHead>
          {KINDS.map((k) => {
            const group = visible.filter((c) => c.kind === k.kind)
            if (!group.length) return null
            return (
              <div key={k.kind}>
                <div className="border-b border-border bg-elevated/40 px-5 py-2 text-[10px] font-semibold uppercase tracking-[0.16em] text-muted-foreground">
                  {k.label} <span className="ml-1 font-mono normal-case tracking-normal">{group.length}</span>
                </div>
                <ListRows className="border-b border-border last:border-b-0">
                  {group.map((c) => (
                    <ListRow key={keyOf(c)} cols={COLS} className="py-3">
                      <CapabilitySummary cap={c} />
                      <Cell label="First seen">
                        <Time value={c.firstSeenAt} />
                      </Cell>
                      <Cell label="Last seen">
                        <Time value={c.lastSeenAt} />
                      </Cell>
                      <CapabilityInfo cap={c} />
                    </ListRow>
                  ))}
                </ListRows>
              </div>
            )
          })}
        </>
      )}
    </ListCard>
  )
}
