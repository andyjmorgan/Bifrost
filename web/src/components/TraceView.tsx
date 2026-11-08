import { useMemo } from 'react'
import { Badge } from '../ui/components/badge'
import { cn } from '../lib'
import type { AuditEventDTO, AuditTraceDTO, Outcome } from '../api'

const barTone: Record<Outcome, string> = {
  success: 'bg-success',
  partial: 'bg-warning',
  denied: 'bg-warning',
  error: 'bg-danger',
}

function fmtMs(ms: number): string {
  return ms >= 1000 ? `${(ms / 1000).toFixed(2)} s` : `${ms} ms`
}

/**
 * Waterfall of one exchange: the inbound root span on top, every upstream hop
 * under it positioned by start offset and sized by duration. Concurrent hops
 * (a mux fan-out) overlap in time, sequential ones stack.
 */
export function TraceView({ trace, currentId, onPick }: { trace: AuditTraceDTO; currentId?: string; onPick: (e: AuditEventDTO) => void }) {
  const rows = useMemo(() => {
    const all: { e: AuditEventDTO; depth: number }[] = []
    if (trace.root) all.push({ e: trace.root, depth: 0 })
    for (const s of trace.spans) all.push({ e: s, depth: trace.root ? 1 : 0 })
    const starts = all.map((r) => Date.parse(r.e.createdAt))
    const t0 = Math.min(...starts)
    const end = Math.max(...all.map((r, i) => starts[i] + Math.max(r.e.durationMs, 0)))
    const total = Math.max(end - t0, 1)
    return {
      total,
      items: all.map((r, i) => ({
        ...r,
        left: ((starts[i] - t0) / total) * 100,
        width: Math.max((Math.max(r.e.durationMs, 0) / total) * 100, 0.6),
        offset: starts[i] - t0,
      })),
    }
  }, [trace])

  const failed = trace.spans.filter((s) => s.outcome !== 'success').length

  return (
    <section className="space-y-3">
      <div className="flex flex-wrap items-center gap-x-3 gap-y-1 text-xs text-muted-foreground">
        <span className="font-mono">{trace.spans.length} upstream {trace.spans.length === 1 ? 'hop' : 'hops'}</span>
        <span className="font-mono">{fmtMs(rows.total)} wall</span>
        {failed > 0 && <span className="font-mono text-danger">{failed} failed</span>}
        {!trace.root && <span>No inbound parent: hops grouped by request id.</span>}
      </div>
      <div className="overflow-hidden rounded-2xl border border-border bg-card">
        {rows.items.map(({ e, depth, left, width, offset }) => {
          const current = e.id === currentId
          return (
            <button
              key={e.id}
              type="button"
              onClick={() => onPick(e)}
              aria-current={current || undefined}
              className={cn(
                'grid w-full grid-cols-[minmax(0,1fr)] items-center gap-x-4 border-b border-border px-4 py-2.5 text-left last:border-b-0 hover:bg-elevated/60 sm:grid-cols-[minmax(0,240px)_minmax(0,1fr)_64px]',
                current && 'bg-elevated/60',
              )}
            >
              <div className={cn('flex min-w-0 items-center gap-2', depth > 0 && 'pl-4')}>
                <Badge mono variant={e.hop === 'inbound' ? 'info' : 'plum'} className="shrink-0">
                  {e.hop === 'inbound' ? 'in' : e.slug || e.route}
                </Badge>
                <span className="min-w-0">
                  <span className="block truncate font-mono text-xs">
                    {e.mcpMethod || e.httpMethod}
                    {e.toolName && <span className="text-muted-foreground"> · {e.toolName}</span>}
                  </span>
                  {e.outcome !== 'success' && (
                    <span className={cn('block truncate font-mono text-[11px]', e.outcome === 'error' ? 'text-danger' : 'text-warning')}>
                      {e.errorType || e.outcome}
                    </span>
                  )}
                </span>
              </div>
              <div className="relative mt-2 h-4 sm:mt-0" title={`+${fmtMs(offset)} · ${fmtMs(e.durationMs)} · HTTP ${e.statusCode || '—'}`}>
                <div className="absolute inset-y-1/2 left-0 right-0 h-px -translate-y-1/2 bg-border" />
                <div
                  className={cn('absolute inset-y-0 rounded-sm', barTone[e.outcome] ?? 'bg-muted-foreground', depth === 0 ? 'opacity-40' : 'opacity-90')}
                  style={{ left: `${left}%`, width: `${width}%` }}
                />
              </div>
              <span className="hidden text-right font-mono text-[11px] text-muted-foreground sm:block">{fmtMs(e.durationMs)}</span>
            </button>
          )
        })}
        <div className="flex justify-between px-4 py-1.5 font-mono text-[10px] text-faint-foreground sm:pl-[calc(240px+2rem)] sm:pr-[calc(64px+2rem)]">
          <span>0</span>
          <span>{fmtMs(rows.total)}</span>
        </div>
      </div>
    </section>
  )
}
