import type { ReactNode } from 'react'
import { Sheet, SheetBody, SheetContent, SheetDescription, SheetHeader, SheetTitle } from '../../ui/components/sheet'
import { Badge } from '../../ui/components/badge'
import { Callout } from '../Callout'
import { JsonTree, type Json } from '../PayloadViewer'
import { Definition } from '../../pages/explorer/shared'
import type { CapabilityDTO } from '../../api'
import { absoluteTime, cn, relativeTime } from '../../lib'
import { KINDS } from './selection'

type Obj = Record<string, unknown>

const asObj = (v: unknown): Obj | undefined => (v && typeof v === 'object' && !Array.isArray(v) ? (v as Obj) : undefined)
const asStr = (v: unknown): string | undefined => (typeof v === 'string' && v ? v : undefined)

/** The MCP spec defaults a client must assume when a tool omits a hint. */
const HINTS: { key: string; label: string; fallback: boolean }[] = [
  { key: 'readOnlyHint', label: 'Read only', fallback: false },
  { key: 'destructiveHint', label: 'Destructive', fallback: true },
  { key: 'idempotentHint', label: 'Idempotent', fallback: false },
  { key: 'openWorldHint', label: 'Open world', fallback: true },
]

/**
 * A read-only view of one catalog entry: the definition exactly as the server last sent it. Tools
 * show their hints and schemas, prompts their arguments, resources and templates their address.
 */
export function CapabilitySheet({ cap, onClose }: { cap?: CapabilityDTO; onClose: () => void }) {
  return (
    <Sheet open={!!cap} onOpenChange={(o) => !o && onClose()}>
      <SheetContent onOpenAutoFocus={(ev) => ev.preventDefault()}>{cap && <Body cap={cap} />}</SheetContent>
    </Sheet>
  )
}

function Body({ cap }: { cap: CapabilityDTO }) {
  const def = asObj(cap.definition)
  const kind = KINDS.find((k) => k.kind === cap.kind)
  const title = asStr(def?.title) ?? asStr(asObj(def?.annotations)?.title)
  return (
    <>
      <SheetHeader>
        <SheetTitle className="break-all font-mono text-base">{cap.name}</SheetTitle>
        <SheetDescription asChild>
          <div className="mt-1 flex flex-wrap items-center gap-x-3 gap-y-1">
            <Badge variant="muted">{kind ? kind.one.charAt(0).toUpperCase() + kind.one.slice(1) : cap.kind}</Badge>
            <span className="font-mono" title={absoluteTime(cap.firstSeenAt)}>
              first seen {relativeTime(cap.firstSeenAt)}
            </span>
            <span className="font-mono" title={absoluteTime(cap.lastSeenAt)}>
              last seen {relativeTime(cap.lastSeenAt)}
            </span>
          </div>
        </SheetDescription>
      </SheetHeader>
      <SheetBody>
        {!def ? (
          <Callout tone="info">No definition recorded yet. Ping or list the server to refresh.</Callout>
        ) : (
          <>
            <Fields>
              {cap.kind === 'resource' && <Field label="URI" mono value={asStr(def.uri)} />}
              {cap.kind === 'template' && <Field label="URI template" mono value={asStr(def.uriTemplate)} />}
              <Field label="Name" mono value={asStr(def.name)} />
              <Field label="Title" value={title} />
              <Field label="Description" value={asStr(def.description)} pre />
              {(cap.kind === 'resource' || cap.kind === 'template') && <Field label="MIME type" mono value={asStr(def.mimeType)} />}
            </Fields>
            {cap.kind === 'tool' && <ToolDetail def={def} />}
            {cap.kind === 'prompt' && <PromptArguments args={def.arguments} />}
            <Definition value={def} />
          </>
        )}
      </SheetBody>
    </>
  )
}

function SectionLabel({ children }: { children: ReactNode }) {
  return <p className="mb-1.5 text-[10px] font-semibold uppercase tracking-[0.16em] text-muted-foreground">{children}</p>
}

function Fields({ children }: { children: ReactNode }) {
  return <dl className="divide-y divide-border rounded-lg border border-border">{children}</dl>
}

function Field({ label, value, mono = false, pre = false }: { label: string; value?: ReactNode; mono?: boolean; pre?: boolean }) {
  return (
    <div className="grid gap-1 px-4 py-2.5 sm:grid-cols-[120px_minmax(0,1fr)] sm:gap-4">
      <dt className="text-[10px] font-semibold uppercase tracking-[0.16em] text-muted-foreground sm:pt-0.5">{label}</dt>
      <dd className={cn('min-w-0 break-words text-sm', mono && 'font-mono text-xs', pre && 'whitespace-pre-wrap leading-6', value === undefined && 'text-faint-foreground')}>
        {value ?? '—'}
      </dd>
    </div>
  )
}

function ToolDetail({ def }: { def: Obj }) {
  const ann = asObj(def.annotations) ?? {}
  return (
    <>
      <div>
        <SectionLabel>Annotations</SectionLabel>
        <Fields>
          {HINTS.map((h) => {
            const set = typeof ann[h.key] === 'boolean'
            const v = set ? (ann[h.key] as boolean) : h.fallback
            return (
              <Field
                key={h.key}
                label={h.label}
                value={
                  <span className="flex flex-wrap items-baseline gap-2">
                    <span className="font-mono text-xs">
                      {h.key}: <span className={cn(v ? 'text-foreground' : 'text-muted-foreground')}>{String(v)}</span>
                    </span>
                    {!set && <span className="text-[11px] text-faint-foreground">not set, spec default</span>}
                  </span>
                }
              />
            )
          })}
        </Fields>
      </div>
      <div>
        <SectionLabel>Input schema</SectionLabel>
        <SchemaTree value={def.inputSchema} />
      </div>
      {def.outputSchema !== undefined && (
        <div>
          <SectionLabel>Output schema</SectionLabel>
          <SchemaTree value={def.outputSchema} />
        </div>
      )}
    </>
  )
}

function SchemaTree({ value }: { value: unknown }) {
  if (value === undefined) return <p className="text-xs text-muted-foreground">None.</p>
  return (
    <div className="max-h-96 overflow-auto rounded-lg border border-border bg-code p-3">
      <JsonTree value={value as Json} />
    </div>
  )
}

function PromptArguments({ args }: { args: unknown }) {
  const list = Array.isArray(args) ? args.map(asObj).filter((a): a is Obj => !!a) : []
  return (
    <div>
      <SectionLabel>Arguments</SectionLabel>
      {list.length === 0 ? (
        <p className="text-xs text-muted-foreground">This prompt takes no arguments.</p>
      ) : (
        <div className="divide-y divide-border rounded-lg border border-border">
          <div className="hidden grid-cols-[minmax(0,1fr)_88px_minmax(0,1.6fr)] gap-4 px-4 py-2 text-[10px] font-semibold uppercase tracking-[0.16em] text-muted-foreground sm:grid">
            <span>Name</span>
            <span>Required</span>
            <span>Description</span>
          </div>
          {list.map((a, i) => (
            <div key={asStr(a.name) ?? i} className="grid gap-1 px-4 py-2.5 sm:grid-cols-[minmax(0,1fr)_88px_minmax(0,1.6fr)] sm:gap-4">
              <span className="break-all font-mono text-xs">{asStr(a.name) ?? '—'}</span>
              <span className="text-xs text-muted-foreground">{a.required === true ? 'Required' : 'Optional'}</span>
              <span className="text-xs text-muted-foreground">{asStr(a.description) ?? asStr(a.title) ?? ''}</span>
            </div>
          ))}
        </div>
      )}
    </div>
  )
}
