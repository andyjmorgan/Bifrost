import { useState, type ReactNode } from 'react'
import { Telescope } from 'lucide-react'
import { Cell, ListBar, ListCard, ListEmpty, ListHead, ListRow, ListRows } from '../ListCard'
import type { CapabilityDTO } from '../../api'
import { absoluteTime, relativeTime } from '../../lib'
import { KINDS } from './selection'
import { CapabilitySheet } from './CapabilitySheet'

const COLS = 'minmax(0,1.6fr) 150px 150px'

function Time({ value }: { value: string }) {
  return (
    <span className="font-mono text-xs text-muted-foreground" title={absoluteTime(value)}>
      {relativeTime(value)}
    </span>
  )
}

/**
 * The server's catalog: every capability the gateway has seen on it, grouped by kind. Read-only;
 * entitlements choose from it. A row opens its definition in a sheet.
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
  const [openKey, setOpenKey] = useState<string>()
  const keyOf = (c: CapabilityDTO) => `${c.kind}:${c.name}`
  const open = caps.find((c) => keyOf(c) === openKey)
  return (
    <ListCard>
      <ListBar>
        <span>
          {caps.length} {caps.length === 1 ? 'capability' : 'capabilities'} seen
        </span>
      </ListBar>
      {caps.length === 0 ? (
        <ListEmpty
          icon={Telescope}
          title={loaded ? 'Nothing seen yet' : 'Loading…'}
          hint={loaded ? 'Nothing seen yet. Ping or list the server to populate the catalog.' : undefined}
          action={loaded ? seedActions : undefined}
        />
      ) : (
        <>
          <ListHead cols={COLS}>
            <span>Capability</span>
            <span>First seen</span>
            <span>Last seen</span>
          </ListHead>
          {KINDS.map((k) => {
            const group = caps.filter((c) => c.kind === k.kind)
            if (!group.length) return null
            return (
              <div key={k.kind}>
                <div className="border-b border-border bg-elevated/40 px-5 py-2 text-[10px] font-semibold uppercase tracking-[0.16em] text-muted-foreground">
                  {k.label} <span className="ml-1 font-mono normal-case tracking-normal">{group.length}</span>
                </div>
                <ListRows className="border-b border-border last:border-b-0">
                  {group.map((c) => (
                    <ListRow key={keyOf(c)} cols={COLS} className="py-3" onClick={() => setOpenKey(keyOf(c))}>
                      <span className="block truncate font-mono text-xs" title={c.name}>
                        {c.name}
                      </span>
                      <Cell label="First seen">
                        <Time value={c.firstSeenAt} />
                      </Cell>
                      <Cell label="Last seen">
                        <Time value={c.lastSeenAt} />
                      </Cell>
                    </ListRow>
                  ))}
                </ListRows>
              </div>
            )
          })}
        </>
      )}
      <CapabilitySheet cap={open} onClose={() => setOpenKey(undefined)} />
    </ListCard>
  )
}
