import { useMemo, useState, type ReactNode } from 'react'
import { toast } from 'sonner'
import { Braces, BookOpen, FileText, ListTree, Variable } from 'lucide-react'
import { Button } from '../../ui/components/button'
import { Input } from '../../ui/components/input'
import { Badge } from '../../ui/components/badge'
import { ListBar, ListCard, ListEmpty, ListRow, ListRows, Primary } from '../../components/ListCard'
import { IconTile } from '../../components/IconTile'
import { Callout } from '../../components/Callout'
import { FormField } from '../../components/FormField'
import { Sheet, SheetBody, SheetContent, SheetDescription, SheetHeader, SheetTitle } from '../../ui/components/sheet'
import {
  api,
  errorMessage,
  type E2EResult,
  type ListResourceTemplatesResult,
  type ListResourcesResult,
  type MCPResource,
  type MCPResourceTemplate,
  type ReadResourceResult,
} from '../../api'
import type { TabProps } from '../Explorer'
import { BinaryNote, Definition, JsonView, ListOutcome, MUX, ResourceText, ResultHead, StreamPanel, UnavailableCallout, base64Bytes, collect, formatBytes, type StreamEvent } from './shared'

const SERVER_META_KEY = 'ai.andrewmorgan.bifrost/server'

function ownerOf(meta?: Record<string, unknown>): string | undefined {
  const v = meta?.[SERVER_META_KEY]
  return typeof v === 'string' ? v : undefined
}

/** What the sheet is open on: a listed resource, a template to fill in, or a URI typed at the top of the tab. */
type Selection = { kind: 'resource'; resource: MCPResource } | { kind: 'template'; template: MCPResourceTemplate } | { kind: 'uri' }

export function ResourcesTab({ route, keyId, fields }: TabProps) {
  const [listing, setListing] = useState(false)
  const [resResult, setResResult] = useState<E2EResult<ListResourcesResult>>()
  const [tplResult, setTplResult] = useState<E2EResult<ListResourceTemplatesResult>>()
  const [resEvents, setResEvents] = useState<StreamEvent[]>([])
  const [tplEvents, setTplEvents] = useState<StreamEvent[]>([])
  const [filter, setFilter] = useState('')
  const [directUri, setDirectUri] = useState('')
  const [selection, setSelection] = useState<Selection>()
  const [uri, setUri] = useState('')
  const [reading, setReading] = useState(false)
  const [readResult, setReadResult] = useState<E2EResult<ReadResourceResult>>()
  const [readEvents, setReadEvents] = useState<StreamEvent[]>([])

  const resources = resResult?.result?.resources ?? []
  const templates = tplResult?.result?.resourceTemplates ?? []
  const listed = !!(resResult || tplResult)
  const q = filter.trim().toLowerCase()
  const visibleResources = useMemo(
    () =>
      q
        ? resources.filter((r) => [r.uri, r.name, r.title, r.description, r.mimeType].some((v) => v?.toLowerCase().includes(q)))
        : resources,
    [resources, q],
  )
  const visibleTemplates = useMemo(
    () => (q ? templates.filter((t) => [t.uriTemplate, t.name, t.title, t.description].some((v) => v?.toLowerCase().includes(q))) : templates),
    [templates, q],
  )
  const total = resources.length + templates.length

  const list = async () => {
    if (!keyId) return
    setListing(true)
    setResResult(undefined)
    setTplResult(undefined)
    setSelection(undefined)
    setReadResult(undefined)
    setResEvents([])
    setTplEvents([])
    const run = async <T,>(method: string, call: () => Promise<E2EResult<T>>, set: (r: E2EResult<T>) => void) => {
      try {
        const r = await call()
        set(r)
        if (!r.ok) toast.error(`${method} failed`, { description: r.error })
      } catch (e) {
        // Keep a result so the outcome line and Callout show; the list call itself never reached the gateway.
        set({ ok: false, latencyMs: 0, endpoint: '', error: errorMessage(e) })
        toast.error(`${method} failed`, { description: errorMessage(e) })
      }
    }
    try {
      await Promise.all([
        run('resources/list', () => api.e2e.listResources(route, { apiKeyId: keyId }, collect(setResEvents)), setResResult),
        run('resources/templates', () => api.e2e.listResourceTemplates(route, { apiKeyId: keyId }, collect(setTplEvents)), setTplResult),
      ])
    } finally {
      setListing(false)
    }
  }

  const open = (s: Selection, initialUri: string, autoRead = false) => {
    setSelection(s)
    setUri(initialUri)
    setReadResult(undefined)
    setReadEvents([])
    if (autoRead) void read(initialUri)
  }

  const read = async (target = uri) => {
    const u = target.trim()
    if (!u || !keyId) return
    setReading(true)
    setReadResult(undefined)
    setReadEvents([])
    try {
      setReadResult(await api.e2e.readResource(route, { apiKeyId: keyId, uri: u }, collect(setReadEvents)))
    } catch (e) {
      setReadResult({ ok: false, latencyMs: 0, endpoint: '', error: errorMessage(e) })
    } finally {
      setReading(false)
    }
  }

  const sel = selection
  const title = sel?.kind === 'resource' ? sel.resource.uri : sel?.kind === 'template' ? sel.template.uriTemplate : 'Read a URI'
  const subtitle = sel?.kind === 'resource' ? sel.resource.title || sel.resource.name : sel?.kind === 'template' ? sel.template.title || sel.template.name : undefined
  const description = sel?.kind === 'resource' ? sel.resource.description : sel?.kind === 'template' ? sel.template.description : undefined
  const definition = sel?.kind === 'resource' ? sel.resource : sel?.kind === 'template' ? sel.template : undefined

  return (
    <div className="space-y-6">
      <div className="rounded-2xl border border-border bg-card p-5">
        <div className="grid gap-4 md:grid-cols-[minmax(0,1fr)_minmax(0,1fr)_auto] md:items-end">
          {fields(route === MUX ? 'Resources from every server; each shows its owning server.' : 'Direct route: one server’s resources.')}
          <Button variant="primary" onClick={list} disabled={!keyId || listing} className="md:mb-[26px]">
            <ListTree /> {listing ? 'Listing…' : 'List resources'}
          </Button>
        </div>
        <form
          className="mt-4 flex flex-col gap-2 border-t border-border pt-4 sm:flex-row sm:items-end"
          onSubmit={(e) => {
            e.preventDefault()
            if (directUri.trim()) open({ kind: 'uri' }, directUri.trim(), true)
          }}
        >
          <FormField label="Read a URI" hint="Any resource URI the route can resolve, listed or not." className="min-w-0 flex-1">
            <Input value={directUri} onChange={(e) => setDirectUri(e.target.value)} placeholder="file:///path/to/thing" className="font-mono" aria-label="Resource URI" />
          </FormField>
          <Button type="submit" disabled={!keyId || !directUri.trim()} className="sm:mb-[26px]">
            <BookOpen /> Read
          </Button>
        </form>
        {/* Two calls, two streams: each panel is followed by its own outcome line. */}
        {(listing || resEvents.length > 0) && <StreamPanel events={resEvents} busy={listing && !resResult} label="resources/list" className="mt-4" />}
        {resResult && <ListOutcome result={resResult} method="resources/list" />}
        {(listing || tplEvents.length > 0) && <StreamPanel events={tplEvents} busy={listing && !tplResult} label="resources/templates" className="mt-4" />}
        {tplResult && <ListOutcome result={tplResult} method="resources/templates" />}
        <UnavailableCallout result={[resResult?.ok && resResult.result, tplResult?.ok && tplResult.result]} what="resources" className="mt-4" />
      </div>

      <ListCard>
        <ListBar>
          <span>
            {resources.length.toLocaleString()} {resources.length === 1 ? 'resource' : 'resources'} · {templates.length.toLocaleString()}{' '}
            {templates.length === 1 ? 'template' : 'templates'}
          </span>
          {total > 0 && <Input value={filter} onChange={(e) => setFilter(e.target.value)} placeholder="Filter" aria-label="Filter resources" className="h-8 w-48 text-xs" />}
        </ListBar>
        {visibleResources.length + visibleTemplates.length === 0 ? (
          <ListEmpty
            icon={FileText}
            title={listed ? (total ? 'No resources match' : 'No resources returned') : 'No resources listed yet'}
            hint={listed ? undefined : 'Pick a route and key, then list resources to see what the gateway exposes.'}
          />
        ) : (
          <>
            <Section title="Resources" count={visibleResources.length} empty={q ? 'No resources match.' : 'No resources returned.'}>
              {visibleResources.map((r) => (
                <ListRow key={r.uri} cols="minmax(0,1fr) auto" onClick={() => open({ kind: 'resource', resource: r }, r.uri)} className="p-4">
                  <div className="flex min-w-0 items-center gap-3">
                    <IconTile icon={FileText} tone="muted" />
                    <Primary title={<span className="break-all font-mono text-xs">{r.uri}</span>} subtitle={r.title || r.name} />
                  </div>
                  <Meta mimeType={r.mimeType} size={r.size} server={route === MUX ? ownerOf(r._meta) : undefined} />
                </ListRow>
              ))}
            </Section>
            <Section title="Templates" count={visibleTemplates.length} empty={q ? 'No templates match.' : 'No templates returned.'}>
              {visibleTemplates.map((t) => (
                <ListRow key={t.uriTemplate} cols="minmax(0,1fr) auto" onClick={() => open({ kind: 'template', template: t }, t.uriTemplate)} className="p-4">
                  <div className="flex min-w-0 items-center gap-3">
                    <IconTile icon={Variable} tone="muted" />
                    <Primary
                      title={<span className="break-all font-mono text-xs">{t.uriTemplate}</span>}
                      subtitle={[t.title || t.name, t.description?.split('\n')[0]].filter(Boolean).join(' · ') || undefined}
                    />
                  </div>
                  <Meta mimeType={t.mimeType} server={route === MUX ? ownerOf(t._meta) : undefined} />
                </ListRow>
              ))}
            </Section>
          </>
        )}
      </ListCard>

      <Sheet
        open={!!sel}
        onOpenChange={(o) => {
          if (!o) {
            setSelection(undefined)
            setReadResult(undefined)
          }
        }}
      >
        <SheetContent onOpenAutoFocus={(ev) => ev.preventDefault()}>
          {sel && (
            <>
              <SheetHeader>
                <SheetTitle className={sel.kind === 'uri' ? 'text-base' : 'break-all font-mono text-base'}>{title}</SheetTitle>
                <SheetDescription asChild>
                  <div className="mt-1 space-y-1">
                    {subtitle && <span className="block text-sm font-medium text-foreground">{subtitle}</span>}
                    {description && <p className="whitespace-pre-wrap text-sm leading-6 text-muted-foreground">{description}</p>}
                  </div>
                </SheetDescription>
              </SheetHeader>
              <SheetBody>
                <form
                  className="space-y-5"
                  onSubmit={(e) => {
                    e.preventDefault()
                    void read()
                  }}
                >
                  {definition && <Definition value={definition} defaultOpen />}
                  {sel.kind !== 'resource' && (
                    <FormField label="URI" required hint={sel.kind === 'template' ? 'Replace each {placeholder} with a value, then read.' : undefined}>
                      <Input value={uri} onChange={(e) => setUri(e.target.value)} disabled={reading} className="font-mono" aria-label="URI" />
                    </FormField>
                  )}
                  <div className="flex items-center justify-end">
                    <Button type="submit" variant="primary" disabled={reading || !keyId || !uri.trim()}>
                      <BookOpen /> {reading ? 'Reading…' : 'Read'}
                    </Button>
                  </div>
                </form>
                <div className="mt-5 space-y-5">
                  {(reading || readEvents.length > 0) && <StreamPanel events={readEvents} busy={reading} />}
                  {readResult && <ReadResult result={readResult} />}
                </div>
              </SheetBody>
            </>
          )}
        </SheetContent>
      </Sheet>
    </div>
  )
}

function Section({ title, count, empty, children }: { title: string; count: number; empty: string; children: ReactNode }) {
  return (
    <section className="border-t border-border first:border-t-0">
      <h3 className="flex items-center gap-2 bg-elevated/40 px-4 py-2 text-[10px] font-semibold uppercase tracking-[0.16em] text-muted-foreground">
        {title} <span className="font-mono tracking-normal">{count.toLocaleString()}</span>
      </h3>
      {count ? <ListRows className="border-t border-border">{children}</ListRows> : <p className="border-t border-border px-4 py-3 text-xs text-muted-foreground">{empty}</p>}
    </section>
  )
}

function Meta({ mimeType, size, server }: { mimeType?: string; size?: number; server?: string }) {
  if (!mimeType && size === undefined && !server) return null
  return (
    <div className="flex flex-wrap items-center gap-2 md:justify-end">
      {server && (
        <Badge mono variant="outline" title="Owning server">
          {server}
        </Badge>
      )}
      {mimeType && <Badge mono>{mimeType}</Badge>}
      {size !== undefined && <span className="font-mono text-xs text-muted-foreground">{formatBytes(size)}</span>}
    </div>
  )
}

function ReadResult({ result }: { result: E2EResult<ReadResourceResult> }) {
  const [raw, setRaw] = useState(false)
  const contents = result.result?.contents ?? []
  return (
    <div className="rounded-2xl border border-border bg-card">
      <ResultHead result={result} label={{ ok: 'Success', failed: 'Error' }} />
      <div className="space-y-4 p-5">
        {!result.ok && result.error && (
          <Callout tone="danger">
            <span className="break-words font-mono text-xs">{result.error}</span>
          </Callout>
        )}
        {result.ok && (
          <div className="flex items-center justify-between gap-3">
            <span className="font-mono text-xs text-muted-foreground">
              {contents.length} {contents.length === 1 ? 'content' : 'contents'}
            </span>
            <Button variant="ghost" size="sm" onClick={() => setRaw((v) => !v)} aria-pressed={raw}>
              <Braces /> {raw ? 'Contents' : 'Raw'}
            </Button>
          </div>
        )}
        {raw ? (
          <JsonView value={result} />
        ) : (
          <>
            {contents.map((c, i) => (
              <div key={i} className="space-y-1.5">
                <div className="flex flex-wrap items-center gap-2">
                  <span className="break-all font-mono text-xs">{c.uri}</span>
                  {c.mimeType && <Badge mono>{c.mimeType}</Badge>}
                </div>
                {c.text !== undefined ? (
                  <ResourceText text={c.text} mimeType={c.mimeType} />
                ) : c.blob !== undefined ? (
                  <BinaryNote bytes={base64Bytes(c.blob)} mimeType={c.mimeType} />
                ) : (
                  <p className="text-xs text-muted-foreground">No text or blob.</p>
                )}
              </div>
            ))}
            {result.ok && !contents.length && <p className="text-xs text-muted-foreground">The resource returned no contents.</p>}
          </>
        )}
      </div>
    </div>
  )
}
