import { useCallback, useEffect, useState, type ReactNode } from 'react'
import { useSearchParams } from 'react-router-dom'
import { Activity, RotateCw, X } from 'lucide-react'
import { Button } from '../../ui/components/button'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '../../ui/components/select'
import { PageHeader } from '../../components/PageHeader'
import { Cell, ListBar, ListCard, ListEmpty, ListHead, ListPager, ListRow, ListRows } from '../../components/ListCard'
import { StatusPill } from '../../components/StatusPill'
import { Callout } from '../../components/Callout'
import { tabPanelProps } from '../../components/Tabs'
import { errorMessage } from '../../api'
import { keyring, type KeyringAuditEvent } from '../../api/keyring'
import { absoluteTime, cn, relativeTime } from '../../lib'

const PAGE = 25
const ALL = '__all__'
const COLS = '130px minmax(0,1.2fr) 110px minmax(0,1.4fr) 170px minmax(0,1.3fr)'

// The event types the keyring records, as they appear on the wire.
const TYPES = ['TokenAccessed', 'TokenRefreshed', 'TokenAdded', 'TokenRemoved', 'CredentialCreated', 'AuthSucceeded', 'AuthFailed', 'AuditAccessed'] as const

// "TokenAccessed" reads as "Token accessed" in the list; the raw type stays the filter value.
const prettyType = (t: string) => t.replace(/([a-z])([A-Z])/g, (_, a: string, b: string) => `${a} ${b.toLowerCase()}`)

/** One line naming what was acted on, from whichever target fields are present. */
function target(e: KeyringAuditEvent): string {
  const parts = [e.targetKind, e.targetProvider, e.targetAccount, e.targetName].filter(Boolean)
  return parts.length ? parts.join(' · ') : '—'
}

/**
 * Where a request came from. The gateway reads the keyring in process, for an MCP server that
 * authenticates with a keyring credential: those rows carry no address, the transport "gateway",
 * and the server in `method`.
 */
function source(e: KeyringAuditEvent): string {
  return e.transport === 'gateway' ? 'Bifrost gateway' : e.sourceIp || '—'
}

function accessKey(e: KeyringAuditEvent): string {
  if (!e.accessKeyPrefix && !e.accessKeyName) return '—'
  return [e.accessKeyName, e.accessKeyPrefix ? `${e.accessKeyPrefix}…` : undefined].filter(Boolean).join(' · ')
}

/**
 * The keyring's audit trail: every credential read, token refresh and sign-in. The second tab of
 * the Audit page; `tabs` is that page's tab strip. Filters and paging live in the URL beside
 * `tab=keyring`, so a reload or a link lands on the same view.
 */
export function KeyringAudit({ tabs }: { tabs: ReactNode }) {
  const [params, setParams] = useSearchParams()
  const type = params.get('type') ?? ''
  const outcome = params.get('outcome') ?? ''
  const offset = Number(params.get('offset')) || 0
  const [items, setItems] = useState<KeyringAuditEvent[]>([])
  const [total, setTotal] = useState(0)
  const [busy, setBusy] = useState(false)
  const [loaded, setLoaded] = useState(false)
  const [error, setError] = useState<string>()

  const load = useCallback(() => {
    setBusy(true)
    setError(undefined)
    keyring.audit
      .list({ limit: PAGE, offset, type: type || undefined, outcome: outcome || undefined })
      .then((p) => {
        setItems(p.items ?? [])
        setTotal(Number(p.total) || 0)
      })
      .catch((e) => setError(errorMessage(e)))
      .finally(() => {
        setBusy(false)
        setLoaded(true)
      })
  }, [offset, type, outcome])
  useEffect(() => {
    load()
  }, [load])

  // Changing a filter goes back to the first page.
  const apply = (next: { type: string; outcome: string }, nextOffset = 0) => {
    const p = new URLSearchParams({ tab: 'keyring' })
    if (next.type) p.set('type', next.type)
    if (next.outcome) p.set('outcome', next.outcome)
    if (nextOffset > 0) p.set('offset', String(nextOffset))
    setParams(p)
  }
  const hasFilters = !!type || !!outcome
  const clear = () => apply({ type: '', outcome: '' })

  return (
    <div className="space-y-6">
      <PageHeader
        eyebrow="Observe"
        title="Audit"
        description="Every secret read, token refresh and sign-in on the keyring, newest first. Each entry names the access key or user and where the request came from. No entry ever carries a secret."
        actions={
          <Button variant="secondary" onClick={load} disabled={busy}>
            <RotateCw className={cn(busy && 'animate-spin')} /> Refresh
          </Button>
        }
      />

      {tabs}

      {error && <Callout tone="danger">{error}</Callout>}

      <ListCard {...tabPanelProps('keyring')}>
        <ListBar>
          <span>
            {total.toLocaleString()} {total === 1 ? 'event' : 'events'}
          </span>
          <div className="flex flex-wrap items-center gap-2">
            <Select value={type || ALL} onValueChange={(v) => apply({ type: v === ALL ? '' : v, outcome })}>
              <SelectTrigger className="h-8 w-44 text-xs" aria-label="Event type">
                <SelectValue placeholder="Event type" />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value={ALL}>Any event</SelectItem>
                {TYPES.map((t) => (
                  <SelectItem key={t} value={t}>
                    {prettyType(t)}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
            <Select value={outcome || ALL} onValueChange={(v) => apply({ type, outcome: v === ALL ? '' : v })}>
              <SelectTrigger className="h-8 w-36 text-xs" aria-label="Outcome">
                <SelectValue placeholder="Outcome" />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value={ALL}>Any outcome</SelectItem>
                <SelectItem value="Success">Success</SelectItem>
                <SelectItem value="Failure">Failure</SelectItem>
              </SelectContent>
            </Select>
            {hasFilters && (
              <Button type="button" size="sm" variant="ghost" onClick={clear}>
                <X /> Clear
              </Button>
            )}
          </div>
        </ListBar>
        {items.length > 0 && (
          <ListHead cols={COLS} at="lg">
            <span>Time</span>
            <span>Event</span>
            <span>Outcome</span>
            <span>Target</span>
            <span>Source</span>
            <span>Access key</span>
          </ListHead>
        )}
        {items.length === 0 ? (
          <ListEmpty
            icon={Activity}
            title={loaded ? (hasFilters ? 'Nothing matches these filters' : 'No events yet') : 'Loading…'}
            hint={loaded && !hasFilters ? 'Events appear as soon as a secret is read, a token is refreshed or someone signs in.' : undefined}
            action={
              loaded && hasFilters ? (
                <Button variant="secondary" onClick={clear}>
                  Clear filters
                </Button>
              ) : undefined
            }
          />
        ) : (
          <ListRows>
            {items.map((e) => {
              const failed = e.outcome?.toLowerCase() === 'failure'
              return (
                <ListRow key={e.id} cols={COLS} at="lg">
                  <Cell label="Time" at="lg">
                    <span className="block truncate font-mono text-xs text-muted-foreground" title={absoluteTime(e.createdAt)}>
                      {relativeTime(e.createdAt)}
                    </span>
                  </Cell>
                  <Cell label="Event" at="lg">
                    <div className="truncate text-sm font-medium">{prettyType(e.type)}</div>
                    {e.detail && (
                      <div className="mt-0.5 truncate text-xs text-muted-foreground" title={e.detail}>
                        {e.detail}
                      </div>
                    )}
                  </Cell>
                  <Cell label="Outcome" at="lg">
                    <StatusPill tone={failed ? 'danger' : 'success'}>{failed ? 'Failure' : 'Success'}</StatusPill>
                  </Cell>
                  <Cell label="Target" at="lg">
                    <span className="block truncate font-mono text-xs" title={target(e)}>
                      {target(e)}
                    </span>
                  </Cell>
                  <Cell label="Source" at="lg">
                    <span className="block truncate font-mono text-xs text-muted-foreground" title={[e.sourceIp, e.method, e.transport].filter(Boolean).join(' · ')}>
                      {source(e)}
                      {e.method && <span className="text-faint-foreground"> · {e.method}</span>}
                    </span>
                  </Cell>
                  <Cell label="Access key" at="lg">
                    <span className="block truncate font-mono text-xs text-muted-foreground" title={accessKey(e)}>
                      {accessKey(e)}
                    </span>
                  </Cell>
                </ListRow>
              )
            })}
          </ListRows>
        )}
        <ListPager
          from={total === 0 ? 0 : offset + 1}
          to={Math.min(offset + PAGE, total)}
          total={total}
          canPrev={offset > 0}
          canNext={offset + PAGE < total}
          busy={busy}
          onPrev={() => apply({ type, outcome }, Math.max(0, offset - PAGE))}
          onNext={() => apply({ type, outcome }, offset + PAGE)}
        />
      </ListCard>
    </div>
  )
}
