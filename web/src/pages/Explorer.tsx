import { useEffect, useMemo, useState, type ReactNode } from 'react'
import { useSearchParams } from 'react-router-dom'
import { toast } from 'sonner'
import { Braces, Compass, FileText, ListTree, MessageSquareText, Play, Wrench } from 'lucide-react'
import { Button } from '../ui/components/button'
import { Input } from '../ui/components/input'
import { Switch } from '../ui/components/switch'
import { Badge } from '../ui/components/badge'
import { PageHeader } from '../components/PageHeader'
import { ListBar, ListCard, ListEmpty, ListRow, ListRows, Primary } from '../components/ListCard'
import { IconTile } from '../components/IconTile'
import { StatusPill } from '../components/StatusPill'
import { Callout } from '../components/Callout'
import { JsonTree, type Json } from '../components/PayloadViewer'
import { FormField } from '../components/FormField'
import { Tabs, TabPanel, type Tab } from '../components/Tabs'
import { Sheet, SheetBody, SheetContent, SheetDescription, SheetHeader, SheetTitle } from '../ui/components/sheet'
import { SchemaForm, initialDraft, toArguments, type Draft } from '../components/SchemaForm'
import { api, errorMessage, type APIKeyDTO, type CallToolResult, type E2EResult, type ListToolsResult, type MCPTool, type ServerDTO } from '../api'
import { cn } from '../lib'
import { Definition, JsonView, ListOutcome, MUX, RequestLink, StreamPanel, TargetFields, TextOrJson, UnavailableCallout, collect, type StreamEvent } from './explorer/shared'
import { PromptsTab } from './explorer/PromptsTab'
import { ResourcesTab } from './explorer/ResourcesTab'

const SEARCH_STORAGE = 'mcpgw-explorer-search'
const KEY_STORAGE = 'mcpgw-explorer-key'

type TabKey = 'tools' | 'prompts' | 'resources'
const TABS: Tab<TabKey>[] = [
  { key: 'tools', label: 'Tools', icon: <Wrench className="size-4" /> },
  { key: 'prompts', label: 'Prompts', icon: <MessageSquareText className="size-4" /> },
  { key: 'resources', label: 'Resources', icon: <FileText className="size-4" /> },
]

/** Props every tab gets: the shared route/key and a renderer for the shared route/key selects. */
export interface TabProps {
  route: string
  keyId: string
  fields: (routeHint: string) => ReactNode
}

// The explorer drives the gateway through its own front door, exactly as an agent would: the
// chosen API key authenticates, the route picks the mux or one server, and every exchange lands
// in the audit trail with a request id we can link to. Route and key are shared across the
// Tools, Prompts and Resources tabs; each tab keeps its own results while you switch.
export function ExplorerPage() {
  const [params, setParams] = useSearchParams()
  const route = params.get('route') || MUX
  const tabParam = params.get('tab')
  const tab: TabKey = tabParam === 'prompts' || tabParam === 'resources' ? tabParam : 'tools'
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

  const setParam = (name: string, value: string | undefined) => {
    const p = new URLSearchParams(params)
    if (value === undefined) p.delete(name)
    else p.set(name, value)
    setParams(p, { replace: true })
  }
  const setRoute = (r: string) => setParam('route', r === MUX ? undefined : r)
  const setTab = (t: TabKey) => setParam('tab', t === 'tools' ? undefined : t)

  const enabledServers = servers.filter((s) => s.enabled)
  const fields = (routeHint: string) => (
    <TargetFields route={route} onRouteChange={setRoute} routeHint={routeHint} keyId={keyId} onKeyChange={setKeyId} servers={enabledServers} keys={usableKeys} />
  )
  // Keying each tab by route resets its listing and selection when the route changes.
  const props: TabProps = { route, keyId, fields }

  return (
    <div className="space-y-6">
      <PageHeader
        eyebrow="Gateway"
        title="Explorer"
        description="List and call tools, prompts and resources through the gateway's front door with one of your API keys, exactly as an agent would. Every call is audited."
      />

      <Tabs tabs={TABS} value={tab} onChange={setTab} label="Explorer surface" />

      {loadError && <Callout tone="danger">{loadError}</Callout>}

      {/* All three panels stay mounted (inactive ones hidden) so switching tabs keeps each tab's results. */}
      <TabPanel tabKey="tools" className={cn(tab !== 'tools' && 'hidden')}>
        <ToolsTab key={route} {...props} />
      </TabPanel>
      <TabPanel tabKey="prompts" className={cn(tab !== 'prompts' && 'hidden')}>
        <PromptsTab key={route} {...props} />
      </TabPanel>
      <TabPanel tabKey="resources" className={cn(tab !== 'resources' && 'hidden')}>
        <ResourcesTab key={route} {...props} />
      </TabPanel>
    </div>
  )
}

function ToolsTab({ route, keyId, fields }: TabProps) {
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

  const tools = listResult?.result?.tools ?? []
  const visibleTools = useMemo(() => {
    const q = filter.trim().toLowerCase()
    return q ? tools.filter((t) => t.name.toLowerCase().includes(q) || t.description?.toLowerCase().includes(q)) : tools
  }, [tools, filter])
  const selected: MCPTool | undefined = tools.find((t) => t.name === selectedName)

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

  return (
    <div className="space-y-6">
      <div className="rounded-2xl border border-border bg-card p-5">
        <div className="grid gap-4 md:grid-cols-[minmax(0,1fr)_minmax(0,1fr)_auto_auto] md:items-end">
          {fields(route === MUX ? 'Tool names are prefixed slug__tool.' : 'Direct route: tool names are unprefixed.')}
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
        {listResult && <ListOutcome result={listResult} method="tools/list" />}
        {listResult?.ok && <UnavailableCallout result={listResult.result} what="tools" className="mt-4" />}
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
                    {(selected.title || selected.annotations?.title) && (
                      <span className="w-full text-sm font-medium text-foreground">{selected.title || selected.annotations?.title}</span>
                    )}
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
                  <Definition value={selected} />
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

