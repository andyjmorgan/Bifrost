import { useMemo, useState } from 'react'
import { toast } from 'sonner'
import { Braces, ListTree, MessageSquareText, Play } from 'lucide-react'
import { Button } from '../../ui/components/button'
import { Input } from '../../ui/components/input'
import { Badge } from '../../ui/components/badge'
import { ListBar, ListCard, ListEmpty, ListRow, ListRows, Primary } from '../../components/ListCard'
import { IconTile } from '../../components/IconTile'
import { Callout } from '../../components/Callout'
import { FormField, FormSection } from '../../components/FormField'
import { Sheet, SheetBody, SheetContent, SheetDescription, SheetHeader, SheetTitle } from '../../ui/components/sheet'
import { api, errorMessage, type E2EResult, type GetPromptResult, type ListPromptsResult, type MCPPrompt, type MCPPromptContent } from '../../api'
import type { TabProps } from '../Explorer'
import { Definition, JsonView, ListOutcome, MUX, BinaryNote, ResourceText, ResultHead, StreamPanel, UnavailableCallout, base64Bytes, collect, type StreamEvent } from './shared'

/** Splits a mux name `slug__name` into its owning server and the bare name. */
function splitMux(route: string, name: string): { server?: string; name: string } {
  const sep = route === MUX ? name.indexOf('__') : -1
  return sep > 0 ? { server: name.slice(0, sep), name: name.slice(sep + 2) } : { name }
}

export function PromptsTab({ route, keyId, fields }: TabProps) {
  const [listing, setListing] = useState(false)
  const [listResult, setListResult] = useState<E2EResult<ListPromptsResult>>()
  const [listEvents, setListEvents] = useState<StreamEvent[]>([])
  const [filter, setFilter] = useState('')
  const [selectedName, setSelectedName] = useState<string>()
  const [args, setArgs] = useState<Record<string, string>>({})
  const [formError, setFormError] = useState<string>()
  const [getting, setGetting] = useState(false)
  const [getResult, setGetResult] = useState<E2EResult<GetPromptResult>>()
  const [getEvents, setGetEvents] = useState<StreamEvent[]>([])

  const prompts = listResult?.result?.prompts ?? []
  const visible = useMemo(() => {
    const q = filter.trim().toLowerCase()
    return q
      ? prompts.filter((p) => p.name.toLowerCase().includes(q) || p.title?.toLowerCase().includes(q) || p.description?.toLowerCase().includes(q))
      : prompts
  }, [prompts, filter])
  const selected: MCPPrompt | undefined = prompts.find((p) => p.name === selectedName)

  const listPrompts = async () => {
    if (!keyId) return
    setListing(true)
    setListResult(undefined)
    setSelectedName(undefined)
    setGetResult(undefined)
    setListEvents([])
    try {
      const r = await api.e2e.listPrompts(route, { apiKeyId: keyId }, collect(setListEvents))
      setListResult(r)
      if (!r.ok) toast.error('prompts/list failed', { description: r.error })
    } catch (e) {
      toast.error('prompts/list failed', { description: errorMessage(e) })
    } finally {
      setListing(false)
    }
  }

  const pick = (p: MCPPrompt) => {
    setSelectedName(p.name)
    setArgs(Object.fromEntries((p.arguments ?? []).map((a) => [a.name, ''])))
    setGetResult(undefined)
    setGetEvents([])
    setFormError(undefined)
  }

  const getPrompt = async () => {
    if (!selected || !keyId) return
    const missing = (selected.arguments ?? []).filter((a) => a.required && !args[a.name]?.trim()).map((a) => a.name)
    if (missing.length) {
      setFormError(`Required: ${missing.join(', ')}.`)
      return
    }
    setFormError(undefined)
    // Optional arguments left blank are omitted rather than sent as empty strings.
    const sent = Object.fromEntries(Object.entries(args).filter(([, v]) => v !== ''))
    setGetting(true)
    setGetResult(undefined)
    setGetEvents([])
    try {
      setGetResult(await api.e2e.getPrompt(route, { apiKeyId: keyId, name: selected.name, arguments: sent }, collect(setGetEvents)))
    } catch (e) {
      setGetResult({ ok: false, latencyMs: 0, endpoint: '', error: errorMessage(e) })
    } finally {
      setGetting(false)
    }
  }

  return (
    <div className="space-y-6">
      <div className="rounded-2xl border border-border bg-card p-5">
        <div className="grid gap-4 md:grid-cols-[minmax(0,1fr)_minmax(0,1fr)_auto] md:items-end">
          {fields(route === MUX ? 'Prompt names are prefixed slug__prompt.' : 'Direct route: prompt names are unprefixed.')}
          <Button variant="primary" onClick={listPrompts} disabled={!keyId || listing} className="md:mb-[26px]">
            <ListTree /> {listing ? 'Listing…' : 'List prompts'}
          </Button>
        </div>
        {(listing || listEvents.length > 0) && <StreamPanel events={listEvents} busy={listing} className="mt-4" />}
        {listResult && <ListOutcome result={listResult} method="prompts/list" />}
        {listResult?.ok && <UnavailableCallout result={listResult.result} what="prompts" className="mt-4" />}
      </div>

      <ListCard>
        <ListBar>
          <span>
            {prompts.length.toLocaleString()} {prompts.length === 1 ? 'prompt' : 'prompts'}
          </span>
          {prompts.length > 0 && (
            <Input value={filter} onChange={(e) => setFilter(e.target.value)} placeholder="Filter" aria-label="Filter prompts" className="h-8 w-48 text-xs" />
          )}
        </ListBar>
        {visible.length === 0 ? (
          <ListEmpty
            icon={MessageSquareText}
            title={listResult ? (prompts.length ? 'No prompts match' : 'No prompts returned') : 'No prompts listed yet'}
            hint={listResult ? undefined : 'Pick a route and key, then list prompts to see what the gateway exposes.'}
          />
        ) : (
          <ListRows>
            {visible.map((p) => {
              const n = splitMux(route, p.name)
              const count = p.arguments?.length ?? 0
              const required = p.arguments?.filter((a) => a.required).length ?? 0
              return (
                <ListRow key={p.name} cols="minmax(0,1fr) auto" onClick={() => pick(p)} className="p-4">
                  <div className="flex min-w-0 items-center gap-3">
                    <IconTile icon={MessageSquareText} tone="muted" />
                    <Primary
                      title={
                        <span className="font-mono text-xs">
                          {n.server && <span className="text-muted-foreground">{n.server}__</span>}
                          {n.name}
                        </span>
                      }
                      subtitle={[p.title, p.description?.split('\n')[0]].filter(Boolean).join(' · ') || undefined}
                    />
                  </div>
                  <span className="font-mono text-xs text-muted-foreground" title={required ? `${required} required` : undefined}>
                    {count} {count === 1 ? 'arg' : 'args'}
                  </span>
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
            setGetResult(undefined)
          }
        }}
      >
        <SheetContent onOpenAutoFocus={(ev) => ev.preventDefault()}>
          {selected && (
            <>
              <SheetHeader>
                <SheetTitle className="break-all font-mono text-base">{selected.name}</SheetTitle>
                <SheetDescription asChild>
                  <div className="mt-1 space-y-1">
                    {selected.title && <span className="block text-sm font-medium text-foreground">{selected.title}</span>}
                    {selected.description && <p className="whitespace-pre-wrap text-sm leading-6 text-muted-foreground">{selected.description}</p>}
                  </div>
                </SheetDescription>
              </SheetHeader>
              <SheetBody>
                <form
                  className="space-y-5"
                  onSubmit={(e) => {
                    e.preventDefault()
                    void getPrompt()
                  }}
                >
                  <Definition value={selected} />
                  {(selected.arguments?.length ?? 0) > 0 ? (
                    <FormSection title="Arguments">
                      {selected.arguments!.map((a) => (
                        <FormField key={a.name} label={a.name} required={a.required} hint={a.description}>
                          <Input
                            value={args[a.name] ?? ''}
                            onChange={(e) => setArgs((prev) => ({ ...prev, [a.name]: e.target.value }))}
                            disabled={getting}
                            aria-label={a.name}
                          />
                        </FormField>
                      ))}
                    </FormSection>
                  ) : (
                    <p className="text-xs text-muted-foreground">This prompt takes no arguments.</p>
                  )}
                  {formError && <Callout tone="danger">{formError}</Callout>}
                  <div className="flex items-center justify-end">
                    <Button type="submit" variant="primary" disabled={getting || !keyId}>
                      <Play /> {getting ? 'Getting…' : 'Get prompt'}
                    </Button>
                  </div>
                </form>
                <div className="mt-5 space-y-5">
                  {(getting || getEvents.length > 0) && <StreamPanel events={getEvents} busy={getting} />}
                  {getResult && <PromptResult result={getResult} />}
                </div>
              </SheetBody>
            </>
          )}
        </SheetContent>
      </Sheet>
    </div>
  )
}

function PromptResult({ result }: { result: E2EResult<GetPromptResult> }) {
  const [raw, setRaw] = useState(false)
  const r = result.result
  const messages = r?.messages ?? []
  return (
    <div className="rounded-2xl border border-border bg-card">
      <ResultHead result={result} label={{ ok: 'Success', failed: 'Transport error' }} />
      <div className="space-y-4 p-5">
        {!result.ok && result.error && (
          <Callout tone="danger">
            <span className="break-words font-mono text-xs">{result.error}</span>
          </Callout>
        )}
        {result.ok && (
          <div className="flex items-start justify-between gap-3">
            {r?.description ? <p className="min-w-0 flex-1 whitespace-pre-wrap text-sm leading-6 text-muted-foreground">{r.description}</p> : <span />}
            <Button variant="ghost" size="sm" onClick={() => setRaw((v) => !v)} aria-pressed={raw}>
              <Braces /> {raw ? 'Messages' : 'Raw'}
            </Button>
          </div>
        )}
        {raw ? (
          <JsonView value={result} />
        ) : (
          <>
            {messages.map((m, i) => (
              <div key={i} className="space-y-2 rounded-lg border border-border p-4">
                <div className="flex items-center gap-2">
                  <Badge mono variant={m.role === 'assistant' ? 'plum' : 'info'}>
                    {m.role}
                  </Badge>
                  <span className="font-mono text-[11px] text-muted-foreground">{m.content?.type}</span>
                </div>
                <PromptContent content={m.content} />
              </div>
            ))}
            {result.ok && !messages.length && <p className="text-xs text-muted-foreground">The prompt returned no messages.</p>}
          </>
        )}
      </div>
    </div>
  )
}

function PromptContent({ content }: { content: MCPPromptContent | undefined }) {
  if (!content) return <p className="text-xs text-muted-foreground">No content.</p>
  switch (content.type) {
    case 'text':
      return <p className="whitespace-pre-wrap break-words text-sm leading-6">{content.text}</p>
    case 'image':
      return (
        <div className="space-y-1.5">
          {content.data && content.mimeType?.startsWith('image/') && (
            <img src={`data:${content.mimeType};base64,${content.data}`} alt="Prompt image" className="max-h-48 rounded-lg border border-border" />
          )}
          <BinaryNote bytes={base64Bytes(content.data ?? '')} mimeType={content.mimeType} />
        </div>
      )
    case 'audio':
      return <BinaryNote bytes={base64Bytes(content.data ?? '')} mimeType={content.mimeType} />
    case 'resource': {
      const res = content.resource
      return (
        <div className="space-y-1.5">
          <div className="flex flex-wrap items-center gap-2">
            <span className="break-all font-mono text-xs">{res?.uri}</span>
            {res?.mimeType && <Badge mono>{res.mimeType}</Badge>}
          </div>
          {res?.text !== undefined ? (
            <ResourceText text={res.text} mimeType={res.mimeType} />
          ) : res?.blob !== undefined ? (
            <BinaryNote bytes={base64Bytes(res.blob)} mimeType={res.mimeType} />
          ) : null}
        </div>
      )
    }
    case 'resource_link':
      return (
        <div className="flex flex-wrap items-center gap-2">
          <span className="break-all font-mono text-xs">{content.uri}</span>
          {content.name && <span className="text-xs text-muted-foreground">{content.name}</span>}
          {content.mimeType && <Badge mono>{content.mimeType}</Badge>}
        </div>
      )
    default:
      return <JsonView value={content} />
  }
}
