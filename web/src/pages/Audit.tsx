import { useCallback, useEffect, useMemo, useState, type ReactNode } from 'react'
import { Link, useSearchParams } from 'react-router-dom'
import { Activity, ChevronRight, RotateCw, Waypoints, X } from 'lucide-react'
import { TraceView } from '../components/TraceView'
import { Button } from '../ui/components/button'
import { Input } from '../ui/components/input'
import { Badge } from '../ui/components/badge'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '../ui/components/select'
import { Sheet, SheetBody, SheetContent, SheetDescription, SheetHeader, SheetTitle } from '../ui/components/sheet'
import { PageHeader } from '../components/PageHeader'
import { Cell, ListBar, ListCard, ListEmpty, ListHead, ListPager, ListRow, ListRows } from '../components/ListCard'
import { StatusPill, type Tone } from '../components/StatusPill'
import { Callout } from '../components/Callout'
import { BodyViewer, findUnavailable } from '../components/PayloadViewer'
import { Tabs, tabPanelProps } from '../components/Tabs'
import { KeyringAudit, prettyType, target as keyringTarget } from './keyring/Audit'
import { keyringEnabled } from '../keyring'
import { useMe } from '../me'
import {
  api,
  errorMessage,
  type AuditDetailDTO,
  type AuditEventDTO,
  type AuditTraceDTO,
  type KeyringAuditDTO,
  type KeyringAuditRowDTO,
  type RuleHitsDTO,
  type UserDTO,
  type Hop,
  type Outcome,
} from '../api'
import { absoluteTime, cn, relativeTime } from '../lib'

const PAGE = 25
const ALL = '__all__'
const COLS = '130px 90px minmax(0,1.2fr) 110px minmax(0,1.4fr) 64px 110px 80px'

const outcomeTone: Record<Outcome, Tone> = { success: 'success', partial: 'warning', error: 'danger', denied: 'warning' }
const hopVariant: Record<Hop, 'info' | 'plum'> = { inbound: 'info', upstream: 'plum' }

interface Filters {
  slug: string
  hop: string
  method: string
  tool: string
  outcome: string
  requestId: string
}

const FILTER_KEYS: (keyof Filters)[] = ['slug', 'hop', 'method', 'tool', 'outcome', 'requestId']

function readFilters(p: URLSearchParams): Filters {
  return {
    slug: p.get('slug') ?? '',
    hop: p.get('hop') ?? '',
    method: p.get('method') ?? '',
    tool: p.get('tool') ?? '',
    outcome: p.get('outcome') ?? '',
    requestId: p.get('requestId') ?? '',
  }
}

type TabKey = 'gateway' | 'keyring'

/**
 * Two trails once the gateway has a keyring: the gateway's own (MCP requests and upstream hops)
 * and the keyring's (credential reads, token refreshes, sign-ins). They record different things in
 * different shapes, so they are two tabs. Without a keyring there is one trail and no tab strip.
 */
export function AuditPage() {
  const [params, setParams] = useSearchParams()
  const withKeyring = keyringEnabled()
  const tab: TabKey = withKeyring && params.get('tab') === 'keyring' ? 'keyring' : 'gateway'
  const tabs = withKeyring ? (
    <Tabs<TabKey>
      label="Audit trail"
      value={tab}
      // Each trail has its own filters, so switching starts the other one clean.
      onChange={(next) => setParams(next === 'keyring' ? { tab: 'keyring' } : {})}
      tabs={[
        { key: 'gateway', label: 'MCP' },
        { key: 'keyring', label: 'Keyring' },
      ]}
    />
  ) : null
  return tab === 'keyring' ? <KeyringAudit tabs={tabs} /> : <GatewayAudit tabs={tabs} />
}

function GatewayAudit({ tabs }: { tabs: ReactNode }) {
  const [params, setParams] = useSearchParams()
  const filters = useMemo(() => readFilters(params), [params])
  const offset = Number(params.get('offset')) || 0
  const [draft, setDraft] = useState<Filters>(filters)
  const [items, setItems] = useState<AuditEventDTO[]>([])
  const [total, setTotal] = useState(0)
  const [busy, setBusy] = useState(false)
  const [loaded, setLoaded] = useState(false)
  const [error, setError] = useState<string>()
  const [selected, setSelected] = useState<AuditEventDTO>()

  useEffect(() => setDraft(filters), [filters])

  const load = useCallback(() => {
    setBusy(true)
    setError(undefined)
    api.audit
      .list({
        offset,
        limit: PAGE,
        slug: filters.slug || undefined,
        hop: (filters.hop as Hop) || undefined,
        method: filters.method || undefined,
        tool: filters.tool || undefined,
        outcome: (filters.outcome as Outcome) || undefined,
        requestId: filters.requestId || undefined,
      })
      .then((p) => {
        setItems(p.items ?? [])
        setTotal(p.total || 0)
      })
      .catch((e) => setError(errorMessage(e)))
      .finally(() => {
        setBusy(false)
        setLoaded(true)
      })
  }, [filters, offset])
  useEffect(() => {
    load()
  }, [load])

  const apply = (next: Filters, nextOffset = 0) => {
    const p = new URLSearchParams()
    for (const k of FILTER_KEYS) if (next[k]) p.set(k, next[k])
    if (nextOffset > 0) p.set('offset', String(nextOffset))
    setParams(p)
  }
  const clear = () => apply({ slug: '', hop: '', method: '', tool: '', outcome: '', requestId: '' })
  const hasFilters = FILTER_KEYS.some((k) => filters[k])

  return (
    <div className="space-y-6">
      <PageHeader
        eyebrow="Observe"
        title="Audit"
        description="Every request through the front door and every upstream hop it caused, with the exchange bodies kept for inspection."
        actions={
          <Button variant="secondary" onClick={load} disabled={busy}>
            <RotateCw className={cn(busy && 'animate-spin')} /> Refresh
          </Button>
        }
      />

      {tabs}

      {error && <Callout tone="danger">{error}</Callout>}

      {filters.requestId && (
        <Callout tone="info">
          Showing the inbound request and its upstream hops for request{' '}
          <code className="font-mono text-xs">{filters.requestId}</code>.{' '}
          <button type="button" className="underline decoration-accent/60 underline-offset-[3px] hover:text-accent" onClick={clear}>
            Show everything
          </button>
        </Callout>
      )}

      <ListCard {...tabPanelProps('gateway', !!tabs)}>
        <ListBar className="items-end">
          <span className="pb-2.5">
            {total.toLocaleString()} {total === 1 ? 'event' : 'events'}
          </span>
          <form
            className="flex flex-wrap items-end gap-2"
            onSubmit={(e) => {
              e.preventDefault()
              apply(draft)
            }}
          >
            <Input
              value={draft.slug}
              onChange={(e) => setDraft({ ...draft, slug: e.target.value })}
              placeholder="slug"
              aria-label="Slug"
              className="h-8 w-28 font-mono text-xs"
            />
            <Select value={draft.hop || ALL} onValueChange={(v) => setDraft({ ...draft, hop: v === ALL ? '' : v })}>
              <SelectTrigger className="h-8 w-32 text-xs" aria-label="Hop">
                <SelectValue placeholder="Hop" />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value={ALL}>Any hop</SelectItem>
                <SelectItem value="inbound">Inbound</SelectItem>
                <SelectItem value="upstream">Upstream</SelectItem>
              </SelectContent>
            </Select>
            <Input
              value={draft.method}
              onChange={(e) => setDraft({ ...draft, method: e.target.value })}
              placeholder="tools/call"
              aria-label="MCP method"
              className="h-8 w-32 font-mono text-xs"
            />
            <Input
              value={draft.tool}
              onChange={(e) => setDraft({ ...draft, tool: e.target.value })}
              placeholder="tool"
              aria-label="Tool"
              className="h-8 w-32 font-mono text-xs"
            />
            <Select value={draft.outcome || ALL} onValueChange={(v) => setDraft({ ...draft, outcome: v === ALL ? '' : v })}>
              <SelectTrigger className="h-8 w-32 text-xs" aria-label="Outcome">
                <SelectValue placeholder="Outcome" />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value={ALL}>Any outcome</SelectItem>
                <SelectItem value="success">Success</SelectItem>
                <SelectItem value="partial">Partial</SelectItem>
                <SelectItem value="error">Error</SelectItem>
                <SelectItem value="denied">Denied</SelectItem>
              </SelectContent>
            </Select>
            <Input
              value={draft.requestId}
              onChange={(e) => setDraft({ ...draft, requestId: e.target.value })}
              placeholder="request id"
              aria-label="Request ID"
              className="h-8 w-40 font-mono text-xs"
            />
            <Button type="submit" size="sm" variant="secondary">
              Filter
            </Button>
            {hasFilters && (
              <Button type="button" size="sm" variant="ghost" onClick={clear}>
                <X /> Clear
              </Button>
            )}
          </form>
        </ListBar>
        {items.length > 0 && (
          <ListHead cols={COLS} at="lg">
            <span>Time</span>
            <span>Hop</span>
            <span>Actor</span>
            <span>Slug</span>
            <span>Method</span>
            <span>Status</span>
            <span>Outcome</span>
            <span className="text-right">Duration</span>
          </ListHead>
        )}
        {items.length === 0 ? (
          <ListEmpty
            icon={Activity}
            title={loaded ? (hasFilters ? 'Nothing matches these filters' : 'No events yet') : 'Loading…'}
            hint={loaded && !hasFilters ? 'Events appear as soon as an agent calls the gateway or the gateway calls upstream.' : undefined}
            action={loaded && hasFilters ? <Button variant="secondary" onClick={clear}>Clear filters</Button> : undefined}
          />
        ) : (
          <ListRows>
            {items.map((e) => (
              <ListRow key={e.id} cols={COLS} at="lg" onClick={() => setSelected(e)}>
                <Cell label="Time" at="lg">
                  <span className="block truncate font-mono text-xs text-muted-foreground" title={absoluteTime(e.createdAt)}>
                    {relativeTime(e.createdAt)}
                  </span>
                </Cell>
                <Cell label="Hop" at="lg">
                  <Badge mono variant={hopVariant[e.hop] ?? 'muted'}>
                    {e.hop}
                  </Badge>
                </Cell>
                <Cell label="Actor" at="lg">
                  <span className="block truncate text-xs">
                    <span className="text-muted-foreground">{e.actorType}</span>
                    {e.actorSubject && <span className="ml-1.5 font-mono">{e.actorSubject}</span>}
                    {e.apiKeyPrefix && <span className="ml-1.5 font-mono text-muted-foreground">{e.apiKeyPrefix}…</span>}
                  </span>
                </Cell>
                <Cell label="Slug" at="lg">
                  <span className="block truncate font-mono text-xs">{e.slug || <span className="text-muted-foreground">{e.route}</span>}</span>
                </Cell>
                <Cell label="Method" at="lg">
                  <span className="block truncate font-mono text-xs">
                    {e.mcpMethod || <span className="text-muted-foreground">{e.httpMethod}</span>}
                    {e.toolName && <span className="text-muted-foreground"> · {e.toolName}</span>}
                    {!!e.ruleHitCount && (
                      <span className="ml-2 rounded bg-accent/10 px-1.5 text-[11px] leading-5 text-accent" title="Rules that applied to this request">
                        {e.ruleHitCount} {e.ruleHitCount === 1 ? 'rule' : 'rules'}
                      </span>
                    )}
                  </span>
                </Cell>
                <Cell label="Status" at="lg">
                  <span className="font-mono text-xs">{e.statusCode || '—'}</span>
                </Cell>
                <Cell label="Outcome" at="lg">
                  <StatusPill tone={outcomeTone[e.outcome] ?? 'muted'}>{e.outcome}</StatusPill>
                </Cell>
                <Cell label="Duration" at="lg" className="lg:text-right">
                  <span className="font-mono text-xs text-muted-foreground">{e.durationMs} ms</span>
                </Cell>
              </ListRow>
            ))}
          </ListRows>
        )}
        <ListPager
          from={total === 0 ? 0 : offset + 1}
          to={Math.min(offset + PAGE, total)}
          total={total}
          canPrev={offset > 0}
          canNext={offset + PAGE < total}
          busy={busy}
          onPrev={() => apply(filters, Math.max(0, offset - PAGE))}
          onNext={() => apply(filters, offset + PAGE)}
        />
      </ListCard>

      <AuditDetailSheet event={selected} onPick={setSelected} onClose={() => setSelected(undefined)} />
    </div>
  )
}

/** Header table behind a disclosure, collapsed by default so bodies lead. */
function Headers({ label, headers }: { label: string; headers?: Record<string, string> }) {
  const [open, setOpen] = useState(false)
  const entries = Object.entries(headers ?? {}).sort(([a], [b]) => a.localeCompare(b))
  return (
    <section>
      <button
        type="button"
        onClick={() => setOpen(!open)}
        aria-expanded={open}
        className="flex h-8 w-full items-center gap-1.5 text-left text-[10px] font-semibold uppercase tracking-[0.16em] text-muted-foreground hover:text-foreground"
      >
        <ChevronRight className={cn('size-3.5 transition-transform duration-200', open && 'rotate-90')} />
        {label}
        <span className="font-mono text-[11px] font-normal normal-case tracking-normal">· {entries.length}</span>
      </button>
      {open &&
        (entries.length === 0 ? (
          <p className="mt-1 text-xs text-muted-foreground">None recorded.</p>
        ) : (
          <div className="mt-1 divide-y divide-border rounded-lg border border-border font-mono text-xs">
            {entries.map(([k, v]) => (
              <div key={k} className="grid gap-1 px-3 py-1.5 sm:grid-cols-[minmax(0,180px)_minmax(0,1fr)] sm:gap-3">
                <span className="truncate text-muted-foreground" title={k}>
                  {k}
                </span>
                <span className="break-all">{v}</span>
              </div>
            ))}
          </div>
        ))}
    </section>
  )
}

const STAGE_LINE = /^(.+):(\d+)$/

/** What the server's rules did on a request: one row per rule that applied. */
function RuleHits({ rules }: { rules: RuleHitsDTO }) {
  return (
    <div className="space-y-2">
      <h3 className="text-[10px] font-semibold uppercase tracking-[0.16em] text-muted-foreground">Rules that applied</h3>
      <div className="rounded-xl border border-border bg-background p-2">
        {rules.hits.map((h, i) => {
          const line = STAGE_LINE.exec(h.rule)
          const quiet = h.mode === 'audit'
          const bad = /deny|error/.test(h.effect)
          return (
            <div key={i} className="grid grid-cols-[minmax(0,11rem)_minmax(0,1fr)] gap-x-3 px-2 py-1 font-mono text-xs leading-5">
              <span className="truncate text-muted-foreground" title={h.rule}>
                {h.hook}
                {line && line[1] === h.hook ? ` · line ${line[2]}` : ` · ${h.rule}`}
              </span>
              <span className="min-w-0 break-words">
                <span className={quiet ? 'text-warning' : bad ? 'text-danger' : 'text-accent'}>{h.effect}</span>
                {h.item && <span className="text-foreground"> {h.item}</span>}
                {h.server && <span className="text-muted-foreground"> on {h.server}</span>}
                {h.fields && h.fields.length > 0 && <span className="text-muted-foreground"> · {h.fields.join(', ')}</span>}
                {h.reason && <span className="text-muted-foreground"> · {h.reason}</span>}
                {h.error && <span className="text-danger"> · {h.error}</span>}
              </span>
            </div>
          )
        })}
        {!!rules.dropped && <p className="px-2 py-1 text-xs text-muted-foreground">{rules.dropped} more applied and were not kept.</p>}
      </div>
      <p className="text-xs leading-5 text-muted-foreground">An audit rule records what it would have done and changes nothing.</p>
    </div>
  )
}

/**
 * The keyring credentials the gateway fetched to forward a request: the keyring's own audit rows
 * for it, found by the request id both trails carry. Loaded when the detail opens. Nothing is
 * shown for a request that used none, and a failed lookup is one muted line under the rest.
 */
function CredentialsUsed({ eventId }: { eventId: string }) {
  const { userId } = useMe()
  const [used, setUsed] = useState<KeyringAuditDTO>()
  const [error, setError] = useState<string>()
  const [users, setUsers] = useState<UserDTO[]>([])
  useEffect(() => {
    setUsed(undefined)
    setError(undefined)
    let cancelled = false
    api.audit
      .keyring(eventId)
      .then((k) => !cancelled && setUsed(k))
      .catch((e) => !cancelled && setError(errorMessage(e)))
    return () => {
      cancelled = true
    }
  }, [eventId])
  const items = used?.items ?? []
  // Names are only needed for a credential lent from somebody else's keyring.
  const needNames = items.some((r) => !r.heldByGateway && r.owner !== userId)
  useEffect(() => {
    if (!needNames) return
    let cancelled = false
    api.users
      .list()
      .then((u) => !cancelled && setUsers(u ?? []))
      .catch(() => undefined)
    return () => {
      cancelled = true
    }
  }, [needNames])

  if (error) return <p className="text-xs text-muted-foreground">The credentials this request used could not be loaded: {error}</p>
  if (!used || items.length === 0) return null

  const whose = (r: KeyringAuditRowDTO) => {
    if (r.heldByGateway) return 'held for the server'
    if (r.owner === userId) return 'your keyring'
    const u = users.find((x) => x.id === r.owner)
    const name = u?.name || u?.email
    return name ? `${name}'s keyring` : "another user's keyring"
  }
  // My own rows are in the keyring's trail; the ones held for a server are in nobody's.
  const mine = items.some((r) => !r.heldByGateway && r.owner === userId)
  return (
    <div className="space-y-2">
      <div className="flex flex-wrap items-baseline justify-between gap-x-3 gap-y-1">
        <h3 className="text-[10px] font-semibold uppercase tracking-[0.16em] text-muted-foreground">Credentials used</h3>
        {mine && (
          <Link
            to={`/audit?tab=keyring&requestId=${encodeURIComponent(used.requestId)}`}
            className="text-xs underline decoration-accent/60 underline-offset-[3px] hover:text-accent"
          >
            Open in the keyring trail
          </Link>
        )}
      </div>
      <div className="rounded-xl border border-border bg-background p-2">
        {items.map((r) => {
          const failed = r.outcome.toLowerCase() === 'failure'
          return (
            <div key={r.id} className="grid grid-cols-[minmax(0,7.5rem)_minmax(0,1fr)] gap-x-3 px-2 py-1 font-mono text-xs leading-5 sm:grid-cols-[minmax(0,11rem)_minmax(0,1fr)]">
              <span className="text-muted-foreground">{prettyType(r.type)}</span>
              <span className="min-w-0 break-words">
                <span className="text-foreground">{keyringTarget(r)}</span>
                <span className="text-muted-foreground">
                  {' · '}
                  <span className="whitespace-nowrap">{whose(r)}</span>
                  {' · '}
                </span>
                <span className={failed ? 'text-danger' : 'text-success'}>{failed ? 'Failure' : 'Success'}</span>
                <span className="text-muted-foreground">
                  {r.detail && ` · ${r.detail}`}
                  {' · '}
                  <span className="whitespace-nowrap" title={absoluteTime(r.createdAt)}>
                    {relativeTime(r.createdAt)}
                  </span>
                </span>
              </span>
            </div>
          )
        })}
      </div>
    </div>
  )
}

function AuditDetailSheet({ event, onPick, onClose }: { event?: AuditEventDTO; onPick: (e: AuditEventDTO) => void; onClose: () => void }) {
  const [detail, setDetail] = useState<AuditDetailDTO>()
  const [error, setError] = useState<string>()
  const [mode, setMode] = useState<'detail' | 'trace'>('detail')
  const [trace, setTrace] = useState<AuditTraceDTO>()
  const [traceError, setTraceError] = useState<string>()
  // Reset to the exchange view when the sheet opens on a new request.
  useEffect(() => {
    if (!event) return
    setMode('detail')
  }, [event?.requestId]) // eslint-disable-line react-hooks/exhaustive-deps
  useEffect(() => {
    if (!event || mode !== 'trace') return
    if (trace && (trace.root?.requestId === event.requestId || trace.spans[0]?.requestId === event.requestId)) return
    let cancelled = false
    setTraceError(undefined)
    api.audit
      .trace(event.id)
      .then((t) => !cancelled && setTrace(t))
      .catch((e) => !cancelled && setTraceError(errorMessage(e)))
    return () => {
      cancelled = true
    }
  }, [event, mode, trace])
  useEffect(() => {
    setDetail(undefined)
    setError(undefined)
    if (!event) return
    let cancelled = false
    api.audit
      .get(event.id)
      .then((d) => !cancelled && setDetail(d))
      .catch((e) => !cancelled && setError(errorMessage(e)))
    return () => {
      cancelled = true
    }
  }, [event])

  const e = detail?.event ?? event
  const payload = detail?.payload
  const unavailable = useMemo(() => findUnavailable(payload?.responseBody, payload?.responseHeaders), [payload])

  return (
    <Sheet open={!!event} onOpenChange={(o) => !o && onClose()}>
      <SheetContent onOpenAutoFocus={(ev) => ev.preventDefault()}>
        <SheetHeader>
          <SheetTitle>
            {e?.mcpMethod || e?.httpMethod} {e?.toolName && <span className="text-muted-foreground">· {e.toolName}</span>}
          </SheetTitle>
          <SheetDescription asChild>
            <div className="mt-2 flex flex-wrap items-center gap-x-3 gap-y-1.5 text-xs text-muted-foreground">
              {e && (
                <>
                  <StatusPill tone={outcomeTone[e.outcome] ?? 'muted'}>{e.outcome}</StatusPill>
                  <Badge mono variant={hopVariant[e.hop] ?? 'muted'}>
                    {e.hop}
                  </Badge>
                  {e.errorType && (
                    <Badge mono variant="danger">
                      {e.errorType}
                    </Badge>
                  )}
                  <span className="font-mono">
                    HTTP {e.statusCode || '—'} · {e.durationMs} ms
                  </span>
                  <span className="font-mono" title={absoluteTime(e.createdAt)}>
                    {relativeTime(e.createdAt)}
                  </span>
                  <span className="font-mono">
                    {e.slug ? (
                      e.serverId ? (
                        <Link to={`/servers/${e.serverId}`} className="underline decoration-accent/60 underline-offset-[3px] hover:text-accent">
                          {e.slug}
                        </Link>
                      ) : (
                        e.slug
                      )
                    ) : (
                      e.route
                    )}
                  </span>
                  <Link
                    to={`/audit?requestId=${encodeURIComponent(e.requestId)}`}
                    onClick={onClose}
                    className="font-mono underline decoration-accent/60 underline-offset-[3px] hover:text-accent"
                    title="Filter the list to this request"
                  >
                    {e.requestId}
                  </Link>
                  <Button
                    variant="ghost"
                    size="sm"
                    className="ml-auto h-7 px-2 text-xs"
                    onClick={() => setMode(mode === 'trace' ? 'detail' : 'trace')}
                    aria-pressed={mode === 'trace'}
                  >
                    <Waypoints /> {mode === 'trace' ? 'Exchange' : 'Trace'}
                  </Button>
                </>
              )}
            </div>
          </SheetDescription>
        </SheetHeader>
        <SheetBody>
          {mode === 'trace' && (
            <>
              {traceError && <Callout tone="danger">{traceError}</Callout>}
              {!trace && !traceError && <p className="text-xs text-muted-foreground">Loading trace…</p>}
              {trace && (
                <TraceView
                  trace={trace}
                  currentId={e?.id}
                  onPick={(span) => {
                    onPick(span)
                    setMode('detail')
                  }}
                />
              )}
            </>
          )}
          {mode === 'detail' && error && <Callout tone="danger">{error}</Callout>}
          {mode === 'detail' && e?.rules && <RuleHits rules={e.rules} />}
          {mode === 'detail' && event && keyringEnabled() && <CredentialsUsed eventId={event.id} />}
          {mode === 'detail' && e?.hasPayload && !payload && !error && <p className="text-xs text-muted-foreground">Loading exchange…</p>}
          {mode === 'detail' && e && !e.hasPayload && <p className="text-xs text-muted-foreground">No payload was recorded for this event.</p>}
          {mode === 'detail' && payload && (
            <>
              <Headers label="Request headers" headers={payload.requestHeaders} />
              <BodyViewer heading="Request" body={payload.requestBody} headers={payload.requestHeaders} />
              <Headers label="Response headers" headers={payload.responseHeaders} />
              <BodyViewer
                heading="Response"
                body={payload.responseBody}
                headers={payload.responseHeaders}
                above={
                  unavailable.length > 0 && (
                    <Callout tone="warning" className="text-xs leading-5">
                      <p className="font-medium">
                        {unavailable.length === 1 ? 'One server was skipped' : `${unavailable.length} servers were skipped`} while listing tools.
                      </p>
                      <ul className="mt-1 space-y-0.5">
                        {unavailable.map((u) => (
                          <li key={u.slug} className="flex flex-wrap items-baseline gap-x-2">
                            <span className="font-mono">{u.slug}</span>
                            {u.name && u.name !== u.slug && <span className="text-muted-foreground">{u.name}</span>}
                            {u.error && <span className="text-muted-foreground">— {u.error}</span>}
                          </li>
                        ))}
                      </ul>
                    </Callout>
                  )
                }
              />
            </>
          )}
        </SheetBody>
      </SheetContent>
    </Sheet>
  )
}
