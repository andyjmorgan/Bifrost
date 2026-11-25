import { useEffect, useRef, useState } from 'react'
import { LoaderCircle } from 'lucide-react'
import { api, errorMessage, type DiscoverResult, type PingResult, type ServerDTO } from '../../api'
import { Sheet, SheetBody, SheetContent, SheetDescription, SheetHeader, SheetTitle } from '../../ui/components/sheet'
import { Callout } from '../Callout'
import { StatusPill } from '../StatusPill'
import { JsonTree, type Json } from '../PayloadViewer'

export interface ServerProbe {
  server: Pick<ServerDTO, 'id' | 'name'>
  kind: 'ping' | 'discover'
}

export function ServerProbeSheet({ probe, onClose, onComplete }: {
  probe?: ServerProbe
  onClose: () => void
  onComplete: () => void
}) {
  const completed = useRef(onComplete)
  completed.current = onComplete
  const mounted = useRef(true)
  useEffect(() => {
    mounted.current = true
    return () => { mounted.current = false }
  }, [])
  return (
    <Sheet open={!!probe} onOpenChange={(open) => !open && onClose()}>
      <SheetContent>
        {probe && <Probe key={`${probe.server.id}:${probe.kind}`} probe={probe} onComplete={() => { if (mounted.current) completed.current() }} />}
      </SheetContent>
    </Sheet>
  )
}

function Probe({ probe: { server, kind }, onComplete }: { probe: ServerProbe; onComplete: () => void }) {
  const [result, setResult] = useState<DiscoverResult | PingResult>()
  const completed = useRef(onComplete)
  completed.current = onComplete
  useEffect(() => {
    let active = true
    // Delay until the effect survives StrictMode's setup/cleanup cycle to avoid duplicate probes.
    void Promise.resolve().then(async () => {
      if (!active) return
      let response: DiscoverResult | PingResult
      try {
        response = await api.servers[kind](server.id)
      } catch (e) {
        response = { ok: false, latencyMs: 0, error: errorMessage(e) }
      }
      if (active) setResult(response)
      // The probe still updates the catalog after dismissal; refresh its owning page too.
      completed.current()
    })
    return () => { active = false }
  }, [server.id, kind])
  return (
    <>
      <SheetHeader>
        <SheetTitle>{kind === 'ping' ? 'Ping' : 'Discovery'} · {server.name}</SheetTitle>
        <SheetDescription>{kind === 'ping' ? 'Initialize the server and list its tools.' : 'Inspect the server’s identity, capabilities and instructions.'}</SheetDescription>
      </SheetHeader>
      <SheetBody>
        {result && <p role="status" className="sr-only">{kind === 'ping' ? 'Ping' : 'Discovery'} {result.ok ? 'completed' : 'failed'}</p>}
        {!result ? <div role="status" className="flex items-center gap-2 text-sm text-muted-foreground"><LoaderCircle className="size-4 animate-spin" />{kind === 'ping' ? 'Pinging…' : 'Discovering…'}</div>
          : kind === 'ping' ? <PingPanel ping={result as PingResult} /> : <DiscoverPanel d={result} />}
      </SheetBody>
    </>
  )
}

function DiscoverPanel({ d }: { d: DiscoverResult }) {
  if (!d.ok) {
    return (
      <Callout tone="danger">
        <span className="font-medium">Discover failed</span>
        {d.error && <span className="ml-2 break-words font-mono text-xs">{d.error}</span>}
      </Callout>
    )
  }
  const info = d.serverInfo
  const items: [string, string][] = [
    ['Latency', `${d.latencyMs} ms`],
    ['Protocol', d.protocolVersion || '—'],
    ['Server', info ? [info.title || info.name, info.version].filter(Boolean).join(' ') : '—'],
    ['Website', info?.websiteUrl || '—'],
  ]
  const caps = Object.keys(d.capabilities ?? {})
  const { ok: _ok, latencyMs: _ms, error: _err, ...raw } = d
  return (
    <div className="rounded-2xl border border-border bg-card p-5">
      <div className="mb-3 flex items-center gap-2">
        <StatusPill tone="success">Initialized</StatusPill>
        <span className="text-xs text-muted-foreground">
          {caps.length ? `Capabilities: ${caps.join(', ')}.` : 'No capabilities declared.'} The init cache was refreshed.
        </span>
      </div>
      <dl className="grid gap-4 sm:grid-cols-2">
        {items.map(([k, v]) => (
          <div key={k} className="min-w-0">
            <dt className="text-[10px] font-semibold uppercase tracking-[0.16em] text-muted-foreground">{k}</dt>
            <dd className="mt-1 break-words font-mono text-sm" title={v}>{v}</dd>
          </div>
        ))}
      </dl>
      <div className="mt-4">
        <p className="mb-1.5 text-[10px] font-semibold uppercase tracking-[0.16em] text-muted-foreground">Instructions</p>
        {d.instructions ? (
          <p className="whitespace-pre-wrap break-words rounded-lg border border-border bg-elevated p-3 text-sm leading-6">{d.instructions}</p>
        ) : (
          <p className="text-sm text-muted-foreground">None. The server sends no instructions at initialize.</p>
        )}
      </div>
      <details className="group mt-4 text-sm">
        <summary className="cursor-pointer select-none text-[10px] font-semibold uppercase tracking-[0.16em] text-muted-foreground hover:text-foreground">
          Initialize result
        </summary>
        <div className="mt-2 max-h-96 overflow-auto rounded-lg border border-border bg-code p-3">
          <JsonTree value={raw as unknown as Json} />
        </div>
      </details>
    </div>
  )
}

function PingPanel({ ping }: { ping: PingResult }) {
  if (!ping.ok) {
    return (
      <Callout tone="danger">
        <span className="font-medium">Ping failed</span>
        {ping.error && <span className="ml-2 break-words font-mono text-xs">{ping.error}</span>}
      </Callout>
    )
  }
  const items: [string, string][] = [
    ['Latency', `${ping.latencyMs} ms`],
    ['Protocol', ping.protocolVersion || '—'],
    ['Server', [ping.serverName, ping.serverVersion].filter(Boolean).join(' ') || '—'],
    ['Tools', String(ping.toolCount)],
  ]
  return (
    <div className="rounded-2xl border border-border bg-card p-5">
      <div className="mb-3 flex items-center gap-2">
        <StatusPill tone="success">Reachable</StatusPill>
        <span className="text-xs text-muted-foreground">Initialized and listed tools through the stored credential.</span>
      </div>
      <dl className="grid gap-4 sm:grid-cols-2">
        {items.map(([k, v]) => (
          <div key={k}>
            <dt className="text-[10px] font-semibold uppercase tracking-[0.16em] text-muted-foreground">{k}</dt>
            <dd className="mt-1 break-words font-mono text-sm">{v}</dd>
          </div>
        ))}
      </dl>
    </div>
  )
}
