import { useEffect, useMemo, useState } from 'react'
import type React from 'react'
import { Link, useSearchParams } from 'react-router-dom'
import { toast } from 'sonner'
import { Braces, Compass, ListTree, Play, Wrench } from 'lucide-react'
import { Button } from '../ui/components/button'
import { Input } from '../ui/components/input'
import { Switch } from '../ui/components/switch'
import { Badge } from '../ui/components/badge'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '../ui/components/select'
import { PageHeader } from '../components/PageHeader'
import { ListBar, ListCard, ListEmpty, ListRow, ListRows, Primary } from '../components/ListCard'
import { IconTile } from '../components/IconTile'
import { StatusPill } from '../components/StatusPill'
import { Callout } from '../components/Callout'
import { CodeBlock } from '../components/JsonBlock'
import { JsonTree, tryParseJson, type Json } from '../components/PayloadViewer'
import { CopyButton } from '../components/CopyButton'
import { FormField } from '../components/FormField'
import { Sheet, SheetBody, SheetContent, SheetDescription, SheetHeader, SheetTitle } from '../ui/components/sheet'
import { SchemaForm, initialDraft, toArguments, type Draft } from '../components/SchemaForm'
import { api, errorMessage, type APIKeyDTO, type CallToolResult, type E2EEventHandler, type E2EResult, type ListToolsResult, type MCPTool, type ServerDTO } from '../api'
import { cn } from '../lib'

const MUX = 'mux'
const SEARCH_STORAGE = 'mcpgw-explorer-search'
const KEY_STORAGE = 'mcpgw-explorer-key'

// The tool explorer drives the gateway through its own front door, exactly as an agent would:
// the chosen API key authenticates, the route picks the mux or one server, and every exchange
// lands in the audit trail with a request id we can link to.
export function ExplorerPage() {
  const [params, setParams] = useSearchParams()
  const [search, setSearch] = useState(() => {
    try {
      return sessionStorage.getItem(SEARCH_STORAGE) === '1'
    } catch {
      return false
    }
  })
  useEffect(() => {
    try {
      sessionStorage.setItem(SEARCH_STORAGE, search ? '1' : '0')
    } catch {
      // ignore
    }
  }, [search])
  const route = params.get('route') || MUX
  const [servers, setServers] = useState<ServerDTO[]>([])
  const [keys, setKeys] = useState<APIKeyDTO[]>([])
  const [keyId, setKeyId] = useState<string>(() => {
    try {
      return sessionStorage.getItem(KEY_STORAGE) ?? ''
    } catch {
      return ''
    }
  })
  const [loadError, setLoadError] = useState<string>()
  const [listing, setListing] = useState(false)
  const [listResult, setListResult] = useState<E2EResult<ListToolsResult>>()
  const [filter, setFilter] = useState('')
  const [selectedName, setSelectedName] = useState<string>()
  const [draft, setDraft] = useState<Draft>({})
  const [calling, setCalling] = useState(false)
  const [callResult, setCallResult] = useState<E2EResult<CallToolResult>>()
  const [formError, setFormError] = useState<string>()
  const [raw, setRaw] = useState(false)
  // Notifications streamed from the upstream during the current list/call.
  const [listEvents, setListEvents] = useState<StreamEvent[]>([])
  const [callEvents, setCallEvents] = useState<StreamEvent[]>([])

  useEffect(() => {
    Promise.all([api.servers.list(), api.apiKeys.list()])
      .then(([s, k]) => {
        setServers(s)
        setKeys(k)
        setLoadError(undefined)
      })
      .catch((e) => setLoadError(errorMessage(e)))
  }, [])

  const usableKeys = useMemo(() => keys.filter((k) => !k.revokedAt && k.scopes.includes('mcp:execute')), [keys])
  useEffect(() => {
    if (usableKeys.length && !usableKeys.some((k) => k.id === keyId)) setKeyId(usableKeys[0].id)
  }, [usableKeys, keyId])
  useEffect(() => {
    try {
      if (keyId) sessionStorage.setItem(KEY_STORAGE, keyId)
    } catch {
      // ignore
    }
  }, [keyId])

  const tools = listResult?.result?.tools ?? []
  const visibleTools = useMemo(() => {
    const q = filter.trim().toLowerCase()
    return q ? tools.filter((t) => t.name.toLowerCase().includes(q) || t.description?.toLowerCase().includes(q)) : tools
  }, [tools, filter])
  const selected: MCPTool | undefined = tools.find((t) => t.name === selectedName)

  const setRoute = (r: string) => {
    const p = new URLSearchParams(params)
    if (r === MUX) p.delete('route')
    else p.set('route', r)
    setParams(p, { replace: true })
    setListResult(undefined)
    setSelectedName(undefined)
    setCallResult(undefined)
  }

  const listTools = async () => {
    if (!keyId) return
    setListing(true)
    setListResult(undefined)
    setSelectedName(undefined)
    setCallResult(undefined)
    setListEvents([])
    try {
      const r = await api.e2e.listTools(route, { apiKeyId: keyId, search }, collect(setListEvents))
      setListResult(r)
      if (!r.ok) toast.error('tools/list failed', { description: r.error })
    } catch (e) {
      toast.error('tools/list failed', { description: errorMessage(e) })
    } finally {
      setListing(false)
    }
  }

  const pick = (t: MCPTool) => {
    setSelectedName(t.name)
    setDraft(initialDraft(t.inputSchema))
    setCallResult(undefined)
    setFormError(undefined)
  }

  const callTool = async () => {
    if (!selected || !keyId) return
    let args: Record<string, unknown>
    try {
      args = toArguments(selected.inputSchema, draft)
    } catch (e) {
      setFormError(errorMessage(e))
      return
    }
    setFormError(undefined)
    setCalling(true)
    setCallResult(undefined)
    setCallEvents([])
    try {
      setCallResult(await api.e2e.callTool(route, { apiKeyId: keyId, name: selected.name, arguments: args, search }, collect(setCallEvents)))
    } catch (e) {
      setCallResult({ ok: false, latencyMs: 0, endpoint: '', error: errorMessage(e) })
    } finally {
      setCalling(false)
    }
  }

  const enabledServers = servers.filter((s) => s.enabled)

  return (
    <div className="space-y-6">
      <PageHeader
        eyebrow="Gateway"
        title="Explorer"
        description="List and call tools through the gateway's front door with one of your API keys, exactly as an agent would. Every call is audited."
      />

      {loadError && <Callout tone="danger">{loadError}</Callout>}

      <div className="rounded-2xl border border-border bg-card p-5">
        <div className="grid gap-4 md:grid-cols-[minmax(0,1fr)_minmax(0,1fr)_auto_auto] md:items-end">
          <FormField label="Route" hint={route === MUX ? 'Tool names are prefixed slug__tool.' : 'Direct route: tool names are unprefixed.'}>
            <Select value={route} onValueChange={setRoute}>
              <SelectTrigger aria-label="Route">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value={MUX}>All servers (/mcp)</SelectItem>
                {enabledServers.map((s) => (
                  <SelectItem key={s.id} value={s.slug}>
                    <span className="font-mono text-xs">{s.slug}</span>
                    <span className="ml-2 text-muted-foreground">{s.name}</span>
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </FormField>
          <FormField
            label="API key"
            hint={
              usableKeys.length ? (
                'Only keys with mcp:execute that are not revoked.'
              ) : (
                <>
                  No usable key.{' '}
                  <Link to="/api-keys" className="underline decoration-accent/60 underline-offset-[3px] hover:text-accent">
                    Create one with mcp:execute
                  </Link>
                  .
                </>
              )
            }
          >
            <Select value={keyId} onValueChange={setKeyId} disabled={!usableKeys.length}>
              <SelectTrigger aria-label="API key">
                <SelectValue placeholder="Choose a key" />
              </SelectTrigger>
              <SelectContent>
                {usableKeys.map((k) => (
                  <SelectItem key={k.id} value={k.id}>
                    {k.name} <span className="ml-2 font-mono text-xs text-muted-foreground">{k.prefix}…</span>
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </FormField>
          <FormField label="Search mode" hint={search ? 'tool_search, list_tools, execute_tool.' : 'Flat list of every tool.'}>
            <div className="flex h-10 items-center">
              <Switch checked={search} onCheckedChange={setSearch} aria-label="Search mode" />
            </div>
          </FormField>
          <Button variant="primary" onClick={listTools} disabled={!keyId || listing} className="md:mb-[26px]">
            <ListTree /> {listing ? 'Listing…' : 'List tools'}
          </Button>
        </div>
        {(listing || listEvents.length > 0) && <StreamPanel events={listEvents} busy={listing} className="mt-4" />}
        {listResult && (
          <div className="mt-4 flex flex-wrap items-center gap-3 text-xs text-muted-foreground">
            <StatusPill tone={listResult.ok ? 'success' : 'danger'}>{listResult.ok ? 'tools/list ok' : 'tools/list failed'}</StatusPill>
            <span className="font-mono">{listResult.latencyMs} ms</span>
            {listResult.endpoint && <span className="font-mono">{listResult.endpoint}</span>}
            {listResult.requestId && <RequestLink id={listResult.requestId} />}
          </div>
        )}
        {listResult && !listResult.ok && listResult.error && (
          <Callout tone="danger" className="mt-4">
            <span className="break-words font-mono text-xs">{listResult.error}</span>
          </Callout>
        )}
      </div>

      <ListCard>
        <ListBar>
          <span>
            {tools.length.toLocaleString()} {tools.length === 1 ? 'tool' : 'tools'}
          </span>
          {tools.length > 0 && (
            <Input value={filter} onChange={(e) => setFilter(e.target.value)} placeholder="Filter" aria-label="Filter tools" className="h-8 w-48 text-xs" />
          )}
        </ListBar>
        {visibleTools.length === 0 ? (
          <ListEmpty
            icon={Compass}
            title={listResult ? (tools.length ? 'No tools match' : 'No tools returned') : 'No tools listed yet'}
            hint={listResult ? undefined : 'Pick a route and key, then list tools to see what the gateway exposes.'}
          />
        ) : (
          <ListRows>
            {visibleTools.map((t) => {
              const sep = route === MUX ? t.name.indexOf('__') : -1
              const server = sep > 0 ? t.name.slice(0, sep) : undefined
              const name = sep > 0 ? t.name.slice(sep + 2) : t.name
              return (
                <ListRow key={t.name} cols="minmax(0,1fr) auto" onClick={() => pick(t)} className="p-4">
                  <div className="flex min-w-0 items-center gap-3">
                    <IconTile icon={Wrench} tone="muted" />
                    <Primary
                      title={
                        <span className="font-mono text-xs">
                          {server && <span className="text-muted-foreground">{server}__</span>}
                          {name}
                        </span>
                      }
                      subtitle={t.description?.split('\n')[0]}
                    />
                  </div>
                  <Play className="size-4 text-muted-foreground" aria-hidden />
                </ListRow>
              )
            })}
          </ListRows>
        )}
      </ListCard>

      <Sheet
        open={!!selected}
        onOpenChange={(o) => {
          if (!o) {
            setSelectedName(undefined)
            setCallResult(undefined)
          }
        }}
      >
        <SheetContent onOpenAutoFocus={(ev) => ev.preventDefault()}>
          {selected && (
            <>
              <SheetHeader>
                <SheetTitle className="break-all font-mono text-base">{selected.name}</SheetTitle>
                <SheetDescription asChild>
                  <div className="mt-1 flex flex-wrap items-start justify-between gap-3">
                    {selected.description ? (
                      <p className="min-w-0 flex-1 whitespace-pre-wrap text-sm leading-6 text-muted-foreground">{selected.description}</p>
                    ) : (
                      <span />
                    )}
                    <Button variant="ghost" size="sm" onClick={() => setRaw((v) => !v)} aria-pressed={raw}>
                      <Braces /> {raw ? 'Form' : 'Raw JSON'}
                    </Button>
                  </div>
                </SheetDescription>
              </SheetHeader>
              <SheetBody>
                <div className="space-y-5">
                  {raw ? (
                    <RawArguments schema={selected.inputSchema} draft={draft} onChange={setDraft} />
                  ) : (
                    <SchemaForm schema={selected.inputSchema} draft={draft} onChange={setDraft} disabled={calling} />
                  )}
                  {formError && <Callout tone="danger">{formError}</Callout>}
                  <div className="flex items-center justify-end">
                    <Button variant="primary" onClick={callTool} disabled={calling || !keyId}>
                      <Play /> {calling ? 'Calling…' : 'Call tool'}
                    </Button>
                  </div>
                  {(calling || callEvents.length > 0) && <StreamPanel events={callEvents} busy={calling} />}
                  {callResult && <ResultPanel result={callResult} raw={raw} />}
                </div>
              </SheetBody>
            </>
          )}
        </SheetContent>
      </Sheet>
    </div>
  )
}

type StreamEvent = { kind: 'progress'; progress: number; total?: number; message?: string } | { kind: 'log'; level: string; text: string }

/** Turns the e2e stream callback into appended state. */
function collect(set: React.Dispatch<React.SetStateAction<StreamEvent[]>>): E2EEventHandler {
  return (e) => {
    if (e.type === 'progress') set((prev) => [...prev, { kind: 'progress', progress: e.data.progress, total: e.data.total, message: e.data.message }])
    else set((prev) => [...prev, { kind: 'log', level: e.data.level, text: typeof e.data.data === 'string' ? e.data.data : JSON.stringify(e.data.data) }])
  }
}

/** Live view of progress and log notifications the upstream sent during a call. */
function StreamPanel({ events, busy, className }: { events: StreamEvent[]; busy: boolean; className?: string }) {
  const last = [...events].reverse().find((e): e is Extract<StreamEvent, { kind: 'progress' }> => e.kind === 'progress')
  const pct = last && last.total ? Math.min(100, Math.round((last.progress / last.total) * 100)) : undefined
  return (
    <div className={cn('rounded-2xl border border-border bg-card p-4', className)}>
      <div className="flex items-center gap-3 text-xs text-muted-foreground">
        <StatusPill tone={busy ? 'info' : 'muted'}>{busy ? 'Streaming' : 'Stream'}</StatusPill>
        <span className="font-mono">
          {events.length} {events.length === 1 ? 'event' : 'events'}
        </span>
        {last && (
          <span className="font-mono">
            {last.progress}
            {last.total ? ` / ${last.total}` : ''}
          </span>
        )}
      </div>
      {pct !== undefined && (
        <div className="mt-3 h-1.5 overflow-hidden rounded-full bg-elevated">
          <div className="h-full rounded-full bg-accent transition-[width] duration-200" style={{ width: `${pct}%` }} />
        </div>
      )}
      {events.length > 0 && (
        <ol className="mt-3 max-h-48 space-y-1 overflow-y-auto font-mono text-[11px] leading-5 text-muted-foreground">
          {events.map((e, i) => (
            <li key={i} className="truncate">
              {e.kind === 'progress' ? (
                <>
                  <span className="text-faint-foreground">{e.progress}</span> {e.message}
                </>
              ) : (
                <>
                  <span className="text-warning">{e.level}</span> {e.text}
                </>
              )}
            </li>
          ))}
        </ol>
      )}
      {busy && events.length === 0 && <p className="mt-2 text-xs text-muted-foreground">Waiting for the upstream…</p>}
    </div>
  )
}

function RequestLink({ id }: { id: string }) {
  return (
    <Link to={`/audit?requestId=${encodeURIComponent(id)}`} className="font-mono underline decoration-accent/60 underline-offset-[3px] hover:text-accent">
      {id}
    </Link>
  )
}

/** Raw mode: one JSON object for all arguments, plus the schema for reference. */
function RawArguments({ schema, draft, onChange }: { schema?: unknown; draft: Draft; onChange: (d: Draft) => void }) {
  const [text, setText] = useState(() => JSON.stringify(draftToObject(draft), null, 2))
  const [err, setErr] = useState<string>()
  return (
    <div className="space-y-4">
      <FormField label="Arguments (JSON)" hint={err ?? 'Edits here replace the form values.'}>
        <textarea
          value={text}
          spellCheck={false}
          onChange={(e) => {
            setText(e.target.value)
            try {
              const obj = JSON.parse(e.target.value) as Record<string, unknown>
              const next: Draft = {}
              for (const [k, v] of Object.entries(obj)) next[k] = typeof v === 'string' ? v : JSON.stringify(v)
              onChange(next)
              setErr(undefined)
            } catch {
              setErr('Not valid JSON yet.')
            }
          }}
          className={cn(
            'min-h-32 w-full resize-y rounded-lg border border-border bg-code p-3 font-mono text-xs leading-5 text-code-foreground outline-none focus:border-accent focus:ring-[3px] focus:ring-accent/15',
            err && 'border-danger/40',
          )}
        />
      </FormField>
      <div>
        <p className="mb-1.5 text-[10px] font-semibold uppercase tracking-[0.16em] text-muted-foreground">Input schema</p>
        <div className="max-h-72 overflow-auto"><JsonTree value={(schema ?? {}) as Json} /></div>
      </div>
    </div>
  )
}

function draftToObject(d: Draft): Record<string, unknown> {
  const out: Record<string, unknown> = {}
  for (const [k, v] of Object.entries(d)) {
    try {
      out[k] = JSON.parse(v)
    } catch {
      out[k] = v
    }
  }
  return out
}

function ResultPanel({ result, raw }: { result: E2EResult<CallToolResult>; raw: boolean }) {
  const r = result.result
  const failed = !result.ok || r?.isError
  const content = r?.content ?? []
  return (
    <div className="rounded-2xl border border-border bg-card">
      <div className="flex flex-wrap items-center gap-3 border-b border-border p-5 text-xs text-muted-foreground">
        <StatusPill tone={failed ? 'danger' : 'success'}>{!result.ok ? 'Transport error' : r?.isError ? 'Tool error' : 'Success'}</StatusPill>
        <span className="font-mono">{result.latencyMs} ms</span>
        {result.endpoint && (
          <span className="truncate font-mono" title={result.endpoint}>
            {result.endpoint}
          </span>
        )}
        {result.requestId && (
          <span className="ml-auto flex items-center gap-1.5">
            request <RequestLink id={result.requestId} />
          </span>
        )}
      </div>
      <div className="space-y-4 p-5">
        {!result.ok && result.error && (
          <Callout tone="danger">
            <span className="break-words font-mono text-xs">{result.error}</span>
          </Callout>
        )}
        {r?.isError && (
          <Callout tone="danger">The tool reported an error. Its content is shown below.</Callout>
        )}
        {raw ? (
          <JsonView value={result} />
        ) : (
          <>
            {content.map((c, i) => (
              <div key={i} className="space-y-1.5">
                <div className="flex items-center gap-2">
                  <Badge mono>{c.type}</Badge>
                  {c.mimeType && <span className="font-mono text-[11px] text-muted-foreground">{c.mimeType}</span>}
                </div>
                {c.type === 'text' ? <TextOrJson text={c.text ?? ''} /> : <JsonView value={c} />}
              </div>
            ))}
            {r?.structuredContent !== undefined && (
              <div className="space-y-1.5">
                <p className="text-[10px] font-semibold uppercase tracking-[0.16em] text-muted-foreground">Structured content</p>
                <JsonView value={r.structuredContent} />
              </div>
            )}
            {result.ok && !content.length && r?.structuredContent === undefined && <p className="text-xs text-muted-foreground">The tool returned no content.</p>}
          </>
        )}
      </div>
    </div>
  )
}

/** Read-only JSON tree with a copy button; the same viewer the audit drawer uses. */
function JsonView({ value }: { value: unknown }) {
  const text = useMemo(() => JSON.stringify(value, null, 2) ?? '', [value])
  return (
    <div className="relative">
      <CopyButton value={text} label="Copy JSON" className="absolute right-2 top-2 z-10 size-7" />
      <JsonTree value={value as Json} />
    </div>
  )
}

/** Text content that parses as JSON gets the tree; anything else stays monospaced text. */
function TextOrJson({ text }: { text: string }) {
  const parsed = useMemo(() => tryParseJson(text), [text])
  if (parsed.ok && typeof parsed.value === 'object' && parsed.value !== null) return <JsonView value={parsed.value} />
  return <CodeBlock wrap>{text}</CodeBlock>
}
