import { useCallback, useEffect, useMemo, useState, type ReactNode } from 'react'
import { Link, useSearchParams } from 'react-router-dom'
import { Activity, RotateCw, X } from 'lucide-react'
import { Button } from '../ui/components/button'
import { Input } from '../ui/components/input'
import { Badge } from '../ui/components/badge'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '../ui/components/select'
import { Dialog, DialogBody, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '../ui/components/dialog'
import { PageHeader } from '../components/PageHeader'
import { Cell, ListBar, ListCard, ListEmpty, ListHead, ListPager, ListRow, ListRows } from '../components/ListCard'
import { StatusPill, type Tone } from '../components/StatusPill'
import { Callout } from '../components/Callout'
import { CodeBlock, prettyJson } from '../components/JsonBlock'
import { api, errorMessage, type AuditDetailDTO, type AuditEventDTO, type Hop, type Outcome } from '../api'
import { absoluteTime, cn, relativeTime } from '../lib'

const PAGE = 25
const ALL = '__all__'
const COLS = '130px 90px minmax(0,1.2fr) 110px minmax(0,1.4fr) 64px 110px 80px'

const outcomeTone: Record<Outcome, Tone> = { success: 'success', error: 'danger', denied: 'warning' }
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

export function AuditPage() {
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

      <ListCard>
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

      <AuditDetailDialog event={selected} onClose={() => setSelected(undefined)} />
    </div>
  )
}

function Row({ label, children, mono = false }: { label: string; children: ReactNode; mono?: boolean }) {
  return (
    <div className="grid gap-1 py-2 sm:grid-cols-[150px_minmax(0,1fr)] sm:gap-4">
      <dt className="text-[10px] font-semibold uppercase tracking-[0.16em] text-muted-foreground sm:pt-0.5">{label}</dt>
      <dd className={cn('min-w-0 break-words text-sm', mono && 'font-mono text-xs')}>{children}</dd>
    </div>
  )
}

function Headers({ headers }: { headers?: Record<string, string> }) {
  const entries = Object.entries(headers ?? {}).sort(([a], [b]) => a.localeCompare(b))
  if (!entries.length) return <p className="text-xs text-muted-foreground">None recorded.</p>
  return (
    <div className="divide-y divide-border rounded-lg border border-border font-mono text-xs">
      {entries.map(([k, v]) => (
        <div key={k} className="grid gap-1 px-3 py-1.5 sm:grid-cols-[minmax(0,180px)_minmax(0,1fr)] sm:gap-3">
          <span className="truncate text-muted-foreground" title={k}>
            {k}
          </span>
          <span className="break-all">{v}</span>
        </div>
      ))}
    </div>
  )
}

/** SSE bodies stay raw; anything that parses as JSON is pretty-printed. */
function Body({ body }: { body?: string }) {
  if (!body) return <p className="text-xs text-muted-foreground">Empty.</p>
  const sse = /^(event|data|id|retry):/m.test(body) && !body.trimStart().startsWith('{') && !body.trimStart().startsWith('[')
  return <CodeBlock wrap>{sse ? body : prettyJson(body)}</CodeBlock>
}

function AuditDetailDialog({ event, onClose }: { event?: AuditEventDTO; onClose: () => void }) {
  const [detail, setDetail] = useState<AuditDetailDTO>()
  const [error, setError] = useState<string>()
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
  return (
    <Dialog open={!!event} onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="max-w-3xl" onOpenAutoFocus={(ev) => ev.preventDefault()}>
        <DialogHeader>
          <DialogTitle>
            {e?.mcpMethod || e?.httpMethod} {e?.toolName && <span className="text-muted-foreground">· {e.toolName}</span>}
          </DialogTitle>
          <DialogDescription>
            {e && (
              <>
                {absoluteTime(e.createdAt)} · {e.hop} · request <code className="font-mono">{e.requestId}</code>
              </>
            )}
          </DialogDescription>
        </DialogHeader>
        <DialogBody>
          {error && <Callout tone="danger">{error}</Callout>}
          {e && (
            <dl className="divide-y divide-border">
              <Row label="Outcome">
                <div className="flex flex-wrap items-center gap-2">
                  <StatusPill tone={outcomeTone[e.outcome] ?? 'muted'}>{e.outcome}</StatusPill>
                  <span className="font-mono text-xs text-muted-foreground">HTTP {e.statusCode || '—'}</span>
                  {e.errorType && <Badge mono variant="danger">{e.errorType}</Badge>}
                </div>
              </Row>
              <Row label="Actor">
                <span className="text-muted-foreground">{e.actorType}</span> <span className="font-mono text-xs">{e.actorSubject}</span>
                {e.apiKeyPrefix && <span className="ml-2 font-mono text-xs text-muted-foreground">key {e.apiKeyPrefix}…</span>}
              </Row>
              <Row label="Route" mono>
                {e.route}
                {e.slug && ` · ${e.slug}`}
              </Row>
              <Row label="Request" mono>
                {e.httpMethod} {e.path}
              </Row>
              {(e.jsonrpcId || e.protocolVersion) && (
                <Row label="JSON-RPC" mono>
                  {e.jsonrpcId && `id ${e.jsonrpcId}`}
                  {e.jsonrpcId && e.protocolVersion && ' · '}
                  {e.protocolVersion && `protocol ${e.protocolVersion}`}
                </Row>
              )}
              <Row label="Size" mono>
                {e.requestBytes.toLocaleString()} B in · {e.responseBytes.toLocaleString()} B out · {e.durationMs} ms
              </Row>
              {e.serverId && (
                <Row label="Server" mono>
                  <Link to={`/servers/${e.serverId}`} className="underline decoration-accent/60 underline-offset-[3px] hover:text-accent">
                    {e.serverId}
                  </Link>
                </Row>
              )}
              <Row label="Request ID" mono>
                <Link to={`/audit?requestId=${encodeURIComponent(e.requestId)}`} onClick={onClose} className="underline decoration-accent/60 underline-offset-[3px] hover:text-accent">
                  {e.requestId}
                </Link>
                {e.parentId && <span className="ml-2 text-muted-foreground">parent {e.parentId}</span>}
              </Row>
            </dl>
          )}
          {e?.hasPayload && !payload && !error && <p className="text-xs text-muted-foreground">Loading exchange…</p>}
          {e && !e.hasPayload && <p className="text-xs text-muted-foreground">No payload was recorded for this event.</p>}
          {payload && (
            <div className="space-y-5">
              <section className="space-y-2">
                <h3 className="text-[10px] font-semibold uppercase tracking-[0.16em] text-muted-foreground">Request headers</h3>
                <Headers headers={payload.requestHeaders} />
              </section>
              <section className="space-y-2">
                <h3 className="text-[10px] font-semibold uppercase tracking-[0.16em] text-muted-foreground">Request body</h3>
                <Body body={payload.requestBody} />
              </section>
              <section className="space-y-2">
                <h3 className="text-[10px] font-semibold uppercase tracking-[0.16em] text-muted-foreground">Response headers</h3>
                <Headers headers={payload.responseHeaders} />
              </section>
              <section className="space-y-2">
                <h3 className="text-[10px] font-semibold uppercase tracking-[0.16em] text-muted-foreground">Response body</h3>
                <Body body={payload.responseBody} />
              </section>
            </div>
          )}
        </DialogBody>
        <DialogFooter>
          <Button variant="outline" onClick={onClose}>
            Close
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
