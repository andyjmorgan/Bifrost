import { useState, type ReactNode } from 'react'
import { toast } from 'sonner'
import { Check, CheckCheck, ShieldQuestion, Undo2 } from 'lucide-react'
import { Button } from '../../ui/components/button'
import { Cell, ListBar, ListCard, ListEmpty, ListHead, ListRow, ListRows, RowActions } from '../ListCard'
import { StatusPill } from '../StatusPill'
import { api, errorMessage, type CapabilityDTO, type ServerDTO } from '../../api'
import { absoluteTime, cn, relativeTime } from '../../lib'
import { KINDS } from './selection'

const COLS = 'minmax(0,1.6fr) 130px 150px 120px'

const key = (c: CapabilityDTO) => `${c.kind}:${c.name}`

/**
 * The server's ceiling: every capability the gateway has seen on it, approved or pending, grouped
 * by kind. Entitlements can only expose what is approved here.
 */
export function CeilingCard({
  server,
  caps,
  loaded,
  onChange,
  seedActions,
}: {
  server: ServerDTO
  caps: CapabilityDTO[]
  loaded: boolean
  onChange: (caps: CapabilityDTO[]) => void
  /** Ping / list actions offered in the empty state. */
  seedActions?: ReactNode
}) {
  const [busy, setBusy] = useState<string>()
  const pending = caps.filter((c) => !c.approvedAt)

  const set = async (approve: boolean, names?: string[], label = 'all') => {
    setBusy(names?.[0] ?? label)
    try {
      onChange(await api.capabilities.set(server.id, { approve, names }))
      toast.success(approve ? (names ? `Approved ${names[0].split(':').slice(1).join(':')}` : 'Approved all pending') : `Revoked ${names?.[0].split(':').slice(1).join(':')}`)
    } catch (e) {
      toast.error(approve ? 'Could not approve' : 'Could not revoke', { description: errorMessage(e) })
    } finally {
      setBusy(undefined)
    }
  }

  return (
    <ListCard>
      <ListBar>
        <span>
          {caps.length} {caps.length === 1 ? 'capability' : 'capabilities'} seen
          {pending.length > 0 && ` · ${pending.length} pending`}
          <span className="ml-2 text-faint-foreground">
            {server.ceilingMode === 'open' ? 'New capabilities are approved as seen.' : 'New capabilities wait for approval.'}
          </span>
        </span>
        {pending.length > 0 && (
          <Button variant="secondary" size="sm" disabled={!!busy} onClick={() => set(true, undefined, 'all')}>
            <CheckCheck /> {busy === 'all' ? 'Approving…' : 'Approve all pending'}
          </Button>
        )}
      </ListBar>
      {caps.length === 0 ? (
        <ListEmpty icon={ShieldQuestion} title={loaded ? 'Nothing seen yet' : 'Loading…'} hint={loaded ? 'Nothing seen yet. Ping or list the server to seed the ceiling.' : undefined} action={loaded ? seedActions : undefined} />
      ) : (
        <>
          <ListHead cols={COLS}>
            <span>Capability</span>
            <span>Status</span>
            <span>First seen</span>
            <span className="text-right">Actions</span>
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
                  {group.map((c) => {
                    const approved = !!c.approvedAt
                    const id = key(c)
                    return (
                      <ListRow key={id} cols={COLS} className="py-3">
                        <span className="block truncate font-mono text-xs" title={c.name}>
                          {c.name}
                        </span>
                        <Cell label="Status">
                          <StatusPill tone={approved ? 'success' : 'warning'}>{approved ? 'Approved' : 'Pending'}</StatusPill>
                        </Cell>
                        <Cell label="First seen">
                          <span className="font-mono text-xs text-muted-foreground" title={absoluteTime(c.firstSeenAt)}>
                            {relativeTime(c.firstSeenAt)}
                          </span>
                        </Cell>
                        <RowActions>
                          {approved ? (
                            <Button variant="ghost" size="sm" className="hover:text-danger" disabled={!!busy} onClick={() => set(false, [id])}>
                              <Undo2 /> Revoke
                            </Button>
                          ) : (
                            <Button variant="secondary" size="sm" disabled={!!busy} onClick={() => set(true, [id])}>
                              <Check className={cn(busy === id && 'animate-pulse')} /> Approve
                            </Button>
                          )}
                        </RowActions>
                      </ListRow>
                    )
                  })}
                </ListRows>
              </div>
            )
          })}
        </>
      )}
    </ListCard>
  )
}
