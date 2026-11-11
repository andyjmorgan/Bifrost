import { useMemo, useState } from 'react'
import type React from 'react'
import { Link } from 'react-router-dom'
import { Button } from '../../ui/components/button'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '../../ui/components/select'
import { StatusPill } from '../../components/StatusPill'
import { Callout } from '../../components/Callout'
import { CodeBlock } from '../../components/JsonBlock'
import { JsonTree, SseList, parseSse, tryParseJson, unavailableFrom, type Json } from '../../components/PayloadViewer'
import { CopyButton } from '../../components/CopyButton'
import { FormField } from '../../components/FormField'
import type { APIKeyDTO, E2EEventHandler, E2EResult, ServerDTO } from '../../api'
import { cn } from '../../lib'

export const MUX = 'mux'

/** The route and key every tab drives the gateway with; owned by the page, shared across tabs. */
export interface Target {
  route: string
  keyId: string
}

export type StreamEvent = ({ kind: 'progress'; progress: number; total?: number; message?: string } | { kind: 'log'; level: string; text: string } | { kind: 'result' }) & { raw: string }

/** Turns the e2e stream callback into appended state. */
export function collect(set: React.Dispatch<React.SetStateAction<StreamEvent[]>>): E2EEventHandler {
  return (e) => {
    if (e.type === 'progress') set((prev) => [...prev, { kind: 'progress', progress: e.data.progress, total: e.data.total, message: e.data.message, raw: e.raw }])
    else if (e.type === 'log') set((prev) => [...prev, { kind: 'log', level: e.data.level, text: typeof e.data.data === 'string' ? e.data.data : JSON.stringify(e.data.data), raw: e.raw }])
    else set((prev) => [...prev, { kind: 'result', raw: e.raw }])
  }
}

/** Route and API key selects, shared by every tab's control card. */
export function TargetFields({
  route,
  onRouteChange,
  routeHint,
  keyId,
  onKeyChange,
  servers,
  keys,
}: {
  route: string
  onRouteChange: (r: string) => void
  routeHint: string
  keyId: string
  onKeyChange: (k: string) => void
  servers: ServerDTO[]
  keys: APIKeyDTO[]
}) {
  return (
    <>
      <FormField label="Route" hint={routeHint}>
        <Select value={route} onValueChange={onRouteChange}>
          <SelectTrigger aria-label="Route">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value={MUX}>All servers (/mcp)</SelectItem>
            {servers.map((s) => (
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
          keys.length ? (
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
        <Select value={keyId} onValueChange={onKeyChange} disabled={!keys.length}>
          <SelectTrigger aria-label="API key">
            <SelectValue placeholder="Choose a key" />
          </SelectTrigger>
          <SelectContent>
            {keys.map((k) => (
              <SelectItem key={k.id} value={k.id}>
                {k.name} <span className="ml-2 font-mono text-xs text-muted-foreground">{k.prefix}…</span>
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      </FormField>
    </>
  )
}

/** The e2e stream as it arrived: progress and log notifications, then the result, each event rendered like the audit SSE viewer. */
export function StreamPanel({ events, busy, label, className }: { events: StreamEvent[]; busy: boolean; label?: string; className?: string }) {
  const [rawView, setRawView] = useState(false)
  const last = [...events].reverse().find((e): e is Extract<StreamEvent, { kind: 'progress' }> => e.kind === 'progress')
  const pct = last && last.total ? Math.min(100, Math.round((last.progress / last.total) * 100)) : undefined
  const rawText = events.map((e) => e.raw).join('\n\n')
  const parsed = useMemo(() => parseSse(rawText), [rawText])
  return (
    <div className={cn('rounded-2xl border border-border bg-card p-4', className)}>
      <div className="flex items-center gap-3 text-xs text-muted-foreground">
        <StatusPill tone={busy ? 'info' : 'muted'}>{busy ? 'Streaming' : 'Stream'}</StatusPill>
        {label && <span className="font-mono text-foreground">{label}</span>}
        <span className="font-mono">
          {events.length} {events.length === 1 ? 'event' : 'events'}
        </span>
        {last && (
          <span className="font-mono">
            {last.progress}
            {last.total ? ` / ${last.total}` : ''}
          </span>
        )}
        <div className="ml-auto flex items-center gap-1">
          <Button variant="ghost" size="sm" className="h-7 px-2 font-mono text-[11px]" onClick={() => setRawView((v) => !v)} aria-pressed={rawView}>
            {rawView ? 'Tree' : 'Raw'}
          </Button>
          <CopyButton value={rawText} label="Copy raw stream" className="size-7" />
        </div>
      </div>
      {pct !== undefined && (
        <div className="mt-3 h-1.5 overflow-hidden rounded-full bg-elevated">
          <div className="h-full rounded-full bg-accent transition-[width] duration-200" style={{ width: `${pct}%` }} />
        </div>
      )}
      {busy && events.length === 0 && <p className="mt-2 text-xs text-muted-foreground">Waiting for the upstream…</p>}
      {events.length > 0 && (
        <div className="mt-3 max-h-[32rem] overflow-auto">
          {rawView ? <CodeBlock wrap>{rawText}</CodeBlock> : <SseList events={parsed} />}
        </div>
      )}
    </div>
  )
}

export function RequestLink({ id }: { id: string }) {
  return (
    <Link to={`/audit?requestId=${encodeURIComponent(id)}`} className="font-mono underline decoration-accent/60 underline-offset-[3px] hover:text-accent">
      {id}
    </Link>
  )
}

/** The outcome line under a list call (status pill, latency, endpoint, request link) plus its transport error. */
export function ListOutcome({ result, method }: { result: E2EResult; method: string }) {
  return (
    <>
      <div className="mt-4 flex flex-wrap items-center gap-3 text-xs text-muted-foreground">
        <StatusPill tone={result.ok ? 'success' : 'danger'}>{result.ok ? `${method} ok` : `${method} failed`}</StatusPill>
        <span className="font-mono">{result.latencyMs} ms</span>
        {result.endpoint && <span className="font-mono">{result.endpoint}</span>}
        {result.requestId && <RequestLink id={result.requestId} />}
      </div>
      {!result.ok && result.error && (
        <Callout tone="danger" className="mt-4">
          <span className="break-words font-mono text-xs">{result.error}</span>
        </Callout>
      )}
    </>
  )
}

/** Servers the mux skipped while listing, read from the result's `_meta`. */
export function UnavailableCallout({ result, what, className }: { result: unknown | unknown[]; what: string; className?: string }) {
  const list = useMemo(() => {
    const all = (Array.isArray(result) ? result : [result]).flatMap(unavailableFrom)
    return all.filter((u, i) => all.findIndex((v) => v.slug === u.slug) === i)
  }, [result])
  if (!list.length) return null
  return (
    <Callout tone="warning" className={cn('text-xs leading-5', className)}>
      <p className="font-medium">
        {list.length === 1 ? 'One server was skipped' : `${list.length} servers were skipped`} while listing {what}.
      </p>
      <ul className="mt-1 space-y-0.5">
        {list.map((u) => (
          <li key={u.slug} className="flex flex-wrap items-baseline gap-x-2">
            <span className="font-mono">{u.slug}</span>
            {u.name && u.name !== u.slug && <span className="text-muted-foreground">{u.name}</span>}
            {u.error && <span className="break-all text-muted-foreground">— {u.error}</span>}
          </li>
        ))}
      </ul>
    </Callout>
  )
}

/** The collapsed "Definition" disclosure at the top of a sheet. */
export function Definition({ value }: { value: unknown }) {
  return (
    <details className="group text-sm">
      <summary className="cursor-pointer select-none text-[10px] font-semibold uppercase tracking-[0.16em] text-muted-foreground">Definition</summary>
      <div className="mt-2 max-h-96 overflow-auto rounded-lg border border-border bg-code p-3">
        <JsonTree value={value as Json} />
      </div>
    </details>
  )
}

/** The header band of a call result: status, latency, endpoint, request link. */
export function ResultHead({ result, label }: { result: E2EResult; label: { ok: string; failed: string } }) {
  return (
    <div className="flex flex-wrap items-center gap-3 border-b border-border p-5 text-xs text-muted-foreground">
      <StatusPill tone={result.ok ? 'success' : 'danger'}>{result.ok ? label.ok : label.failed}</StatusPill>
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
  )
}

/** Read-only JSON tree with a copy button; the same viewer the audit drawer uses. */
export function JsonView({ value }: { value: unknown }) {
  const text = useMemo(() => JSON.stringify(value, null, 2) ?? '', [value])
  return (
    <div className="relative">
      <CopyButton value={text} label="Copy JSON" className="absolute right-2 top-2 z-10 size-7" />
      <JsonTree value={value as Json} />
    </div>
  )
}

/** Text content that parses as JSON gets the tree; anything else stays monospaced text. */
export function TextOrJson({ text }: { text: string }) {
  const parsed = useMemo(() => tryParseJson(text), [text])
  if (parsed.ok && typeof parsed.value === 'object' && parsed.value !== null) return <JsonView value={parsed.value} />
  return <CodeBlock wrap>{text}</CodeBlock>
}

/** Decoded size of a base64 payload, without decoding it. */
export function base64Bytes(b64: string): number {
  const s = b64.replace(/\s+/g, '')
  const pad = s.endsWith('==') ? 2 : s.endsWith('=') ? 1 : 0
  return Math.max(0, Math.floor((s.length * 3) / 4) - pad)
}

export function formatBytes(n: number): string {
  if (n < 1024) return `${n.toLocaleString()} bytes`
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KB`
  return `${(n / 1024 / 1024).toFixed(1)} MB`
}

/** Text from a resource: JSON pretty-printed, everything else (markdown included) pre-wrapped. */
export function ResourceText({ text, mimeType }: { text: string; mimeType?: string }) {
  const isJson = !!mimeType && /(^|[/+])json\b/i.test(mimeType)
  const pretty = useMemo(() => {
    if (!isJson) return text
    try {
      return JSON.stringify(JSON.parse(text), null, 2)
    } catch {
      return text
    }
  }, [text, isJson])
  return (
    <div className="relative">
      <CopyButton value={text} label="Copy text" className="absolute right-2 top-2 z-10 size-7" />
      <CodeBlock wrap>{pretty}</CodeBlock>
    </div>
  )
}

export function BinaryNote({ bytes, mimeType }: { bytes: number; mimeType?: string }) {
  return (
    <p className="font-mono text-xs text-muted-foreground">
      binary, {formatBytes(bytes)}
      {mimeType && ` · ${mimeType}`}
    </p>
  )
}
