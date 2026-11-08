import { useMemo, useState, type ReactNode } from 'react'
import { ChevronDown, ChevronRight } from 'lucide-react'
import { Button } from '../ui/components/button'
import { Badge } from '../ui/components/badge'
import { CopyButton } from './CopyButton'
import { CodeBlock } from './JsonBlock'
import { cn } from '../lib'

// Renders a recorded exchange body: a collapsible tree for JSON, a list of events for an SSE
// stream (each event's data through the same tree), and plain preformatted text otherwise. Value
// types take status hues from the tokens so both themes track; keys stay in the code foreground.

type Json = null | boolean | number | string | Json[] | { [k: string]: Json }

export function tryParseJson(text: string): { ok: true; value: Json } | { ok: false } {
  const t = text.trim()
  if (!t || !/^[[{"\-\d]|^(true|false|null)$/.test(t)) return { ok: false }
  try {
    return { ok: true, value: JSON.parse(t) as Json }
  } catch {
    return { ok: false }
  }
}

/* ---------- JSON tree ---------- */

function Punct({ children }: { children: ReactNode }) {
  return <span className="text-muted-foreground">{children}</span>
}

function Leaf({ value }: { value: Json }) {
  if (value === null) return <span className="italic text-faint-foreground">null</span>
  switch (typeof value) {
    case 'string':
      return <span className="break-all text-warning">"{value}"</span>
    case 'number':
      return <span className="text-info">{String(value)}</span>
    case 'boolean':
      return <span className="text-plum">{String(value)}</span>
    default:
      return <span>{String(value)}</span>
  }
}

function isContainer(v: Json): v is Json[] | { [k: string]: Json } {
  return typeof v === 'object' && v !== null
}

function JsonNode({ name, value, depth, trailing }: { name?: string; value: Json; depth: number; trailing: boolean }) {
  const container = isContainer(value)
  const entries = container ? (Array.isArray(value) ? value.map((v, i) => [String(i), v] as const) : Object.entries(value)) : []
  const [open, setOpen] = useState(depth < 4 && entries.length <= 60)
  const comma = trailing ? <Punct>,</Punct> : null
  const label = name !== undefined && (
    <>
      <span className="text-code-foreground">"{name}"</span>
      <Punct>: </Punct>
    </>
  )

  if (!container) {
    return (
      <div className="pl-4">
        {label}
        <Leaf value={value} />
        {comma}
      </div>
    )
  }

  const [o, c] = Array.isArray(value) ? ['[', ']'] : ['{', '}']
  const count = `${entries.length} ${Array.isArray(value) ? (entries.length === 1 ? 'item' : 'items') : entries.length === 1 ? 'key' : 'keys'}`
  const Chevron = open ? ChevronDown : ChevronRight
  return (
    <div className={cn(depth > 0 && 'pl-4')}>
      <div className="-ml-4 flex items-start">
        <button
          type="button"
          aria-expanded={open}
          aria-label={open ? 'Collapse' : 'Expand'}
          onClick={() => setOpen(!open)}
          className="mt-0.5 flex size-4 shrink-0 items-center justify-center rounded text-muted-foreground hover:text-foreground"
          disabled={entries.length === 0}
        >
          {entries.length > 0 && <Chevron className="size-3.5" />}
        </button>
        <span className="min-w-0">
          {label}
          <Punct>{o}</Punct>
          {!open && entries.length > 0 && (
            <button type="button" onClick={() => setOpen(true)} className="mx-1 rounded px-1 text-muted-foreground hover:bg-elevated hover:text-foreground">
              … {count}
            </button>
          )}
          {(!open || entries.length === 0) && <Punct>{c}</Punct>}
          {(!open || entries.length === 0) && comma}
        </span>
      </div>
      {open && entries.length > 0 && (
        <>
          <div className="ml-[-9px] border-l border-border/70 pl-[9px]">
            {entries.map(([k, v], i) => (
              <JsonNode key={k} name={Array.isArray(value) ? undefined : k} value={v} depth={depth + 1} trailing={i < entries.length - 1} />
            ))}
          </div>
          <div>
            <Punct>{c}</Punct>
            {comma}
          </div>
        </>
      )}
    </div>
  )
}

export function JsonTree({ value, className }: { value: Json; className?: string }) {
  return (
    <div className={cn('overflow-auto rounded-lg bg-code py-3 pl-7 pr-3 font-mono text-xs leading-5 text-code-foreground', className)}>
      <JsonNode value={value} depth={0} trailing={false} />
    </div>
  )
}

/* ---------- SSE ---------- */

export interface SseEvent {
  event?: string
  id?: string
  retry?: string
  data: string
}

export function looksLikeSse(body: string, headers?: Record<string, string>): boolean {
  const ct = Object.entries(headers ?? {}).find(([k]) => k.toLowerCase() === 'content-type')?.[1] ?? ''
  if (/text\/event-stream/i.test(ct)) return true
  const t = body.trimStart()
  return /^(event|data|id|retry):/m.test(body) && !t.startsWith('{') && !t.startsWith('[')
}

export function parseSse(body: string): SseEvent[] {
  const events: SseEvent[] = []
  let cur: SseEvent | undefined
  const data: string[] = []
  const flush = () => {
    if (cur && (data.length || cur.event || cur.id)) events.push({ ...cur, data: data.join('\n') })
    cur = undefined
    data.length = 0
  }
  for (const raw of body.split(/\r?\n/)) {
    if (raw === '') {
      flush()
      continue
    }
    if (raw.startsWith(':')) continue
    cur ??= { data: '' }
    const i = raw.indexOf(':')
    const field = i < 0 ? raw : raw.slice(0, i)
    const val = i < 0 ? '' : raw.slice(i + 1).replace(/^ /, '')
    if (field === 'data') data.push(val)
    else if (field === 'event') cur.event = val
    else if (field === 'id') cur.id = val
    else if (field === 'retry') cur.retry = val
  }
  flush()
  return events
}

function SseList({ events }: { events: SseEvent[] }) {
  if (!events.length) return <p className="text-xs text-muted-foreground">No events in the stream.</p>
  return (
    <ol className="divide-y divide-border rounded-lg border border-border">
      {events.map((ev, i) => {
        const parsed = ev.data ? tryParseJson(ev.data) : ({ ok: false } as const)
        return (
          <li key={i} className="space-y-2 p-3">
            <div className="flex flex-wrap items-center gap-2 font-mono text-[11px] text-muted-foreground">
              <span className="tabular-nums">#{i + 1}</span>
              <Badge mono variant={ev.event && ev.event !== 'message' ? 'plum' : 'muted'}>
                {ev.event || 'message'}
              </Badge>
              {ev.id && <span>id {ev.id}</span>}
              {ev.retry && <span>retry {ev.retry}</span>}
            </div>
            {ev.data ? (
              parsed.ok ? (
                <JsonTree value={parsed.value} />
              ) : (
                <CodeBlock wrap>{ev.data}</CodeBlock>
              )
            ) : (
              <p className="text-xs text-muted-foreground">No data.</p>
            )}
          </li>
        )
      })}
    </ol>
  )
}

/* ---------- body ---------- */

export type BodyKind = 'json' | 'sse' | 'text' | 'empty'

export function classifyBody(body: string | undefined, headers?: Record<string, string>): { kind: BodyKind; json?: Json; events?: SseEvent[] } {
  if (!body) return { kind: 'empty' }
  if (looksLikeSse(body, headers)) return { kind: 'sse', events: parseSse(body) }
  const p = tryParseJson(body)
  if (p.ok && isContainer(p.value)) return { kind: 'json', json: p.value }
  return { kind: 'text' }
}

/**
 * A body section: uppercase heading with a Tree/Raw toggle and a copy button on the right, an
 * optional notice (`above`), then the tree, event list or preformatted text.
 */
export function BodyViewer({
  body,
  headers,
  heading,
  above,
}: {
  body?: string
  headers?: Record<string, string>
  heading: ReactNode
  above?: ReactNode
}) {
  const [raw, setRaw] = useState(false)
  const info = useMemo(() => classifyBody(body, headers), [body, headers])
  const canToggle = info.kind === 'json' || info.kind === 'sse'
  return (
    <section className="space-y-2">
      <div className="flex h-8 items-center justify-between gap-3">
        <h3 className="text-[10px] font-semibold uppercase tracking-[0.16em] text-muted-foreground">
          {heading}
          {info.kind === 'sse' && <span className="ml-2 normal-case tracking-normal">SSE · {info.events?.length ?? 0} events</span>}
        </h3>
        {body && (
          <div className="flex items-center gap-1">
            {canToggle && (
              <Button variant="ghost" size="sm" className="h-7 px-2 font-mono text-[11px] font-medium" onClick={() => setRaw(!raw)} aria-pressed={raw}>
                {raw ? 'Tree' : 'Raw'}
              </Button>
            )}
            <CopyButton value={body} label="Copy body" className="size-7" />
          </div>
        )}
      </div>
      {above}
      {info.kind === 'empty' && <p className="text-xs text-muted-foreground">Empty.</p>}
      {info.kind === 'text' && <CodeBlock wrap>{body!}</CodeBlock>}
      {info.kind === 'json' && (raw ? <CodeBlock wrap>{JSON.stringify(info.json, null, 2)}</CodeBlock> : <JsonTree value={info.json!} />)}
      {info.kind === 'sse' && (raw ? <CodeBlock wrap>{body!}</CodeBlock> : <SseList events={info.events!} />)}
    </section>
  )
}

/* ---------- gateway _meta ---------- */

export const UNAVAILABLE_META_KEY = 'ai.andrewmorgan.mcp-gateway/unavailable'

export interface UnavailableServer {
  slug: string
  name?: string
  error?: string
}

/** Finds the mux's unavailable-servers marker in a JSON or SSE body (checks `_meta` and `result._meta`). */
export function findUnavailable(body?: string, headers?: Record<string, string>): UnavailableServer[] {
  const info = classifyBody(body, headers)
  const docs: Json[] = []
  if (info.kind === 'json' && info.json !== undefined) docs.push(info.json)
  if (info.kind === 'sse') for (const ev of info.events ?? []) {
    const p = ev.data ? tryParseJson(ev.data) : ({ ok: false } as const)
    if (p.ok) docs.push(p.value)
  }
  const out: UnavailableServer[] = []
  for (const d of docs) {
    if (!isContainer(d) || Array.isArray(d)) continue
    const result = d.result
    const candidates = [d._meta, isContainer(result) && !Array.isArray(result) ? result._meta : undefined]
    for (const meta of candidates) {
      if (meta === undefined || !isContainer(meta) || Array.isArray(meta)) continue
      const list = meta[UNAVAILABLE_META_KEY]
      if (!Array.isArray(list)) continue
      for (const item of list) {
        if (isContainer(item) && !Array.isArray(item) && typeof item.slug === 'string') {
          out.push({ slug: item.slug, name: typeof item.name === 'string' ? item.name : undefined, error: typeof item.error === 'string' ? item.error : undefined })
        }
      }
    }
  }
  return out
}
