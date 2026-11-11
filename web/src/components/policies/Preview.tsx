import { useMemo, useState } from 'react'
import { toast } from 'sonner'
import { AlertTriangle, Eye, Play, Wrench } from 'lucide-react'
import { Button } from '../../ui/components/button'
import { Badge } from '../../ui/components/badge'
import { Input } from '../../ui/components/input'
import { Checkbox } from '../../ui/components/checkbox'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '../../ui/components/select'
import { Sheet, SheetBody, SheetContent, SheetDescription, SheetHeader, SheetTitle } from '../../ui/components/sheet'
import { FormField, FormSection } from '../FormField'
import { Callout } from '../Callout'
import { StatusPill } from '../StatusPill'
import { IconTile } from '../IconTile'
import { CopyButton } from '../CopyButton'
import { JsonTree, type Json } from '../PayloadViewer'
import { Cell, ListBar, ListCard, ListEmpty, ListHead, ListRow, ListRows, Primary } from '../ListCard'
import { api, errorMessage, problemsOf, type PolicyInput, type PolicyPreviewResult, type PolicyPreviewTool, type PolicyProblem, type ServerDTO } from '../../api'
import { cn } from '../../lib'

export const MUX = 'mux'
const COLS = 'minmax(0,1.6fr) 110px 100px 100px minmax(0,1fr) minmax(0,1fr)'

/** Runs the unsaved policy against a route's live tools and shows what each tool becomes. */
export function PolicyPreview({
  input,
  onProblems,
  servers,
  route,
  onRouteChange: setRoute,
}: {
  input: () => PolicyInput
  onProblems: (p: PolicyProblem[]) => void
  servers: ServerDTO[]
  route: string
  onRouteChange: (route: string) => void
}) {
  const [extra, setExtra] = useState(false)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string>()
  const [result, setResult] = useState<PolicyPreviewResult>()
  const [filter, setFilter] = useState('')
  const [selected, setSelected] = useState<PolicyPreviewTool>()

  const run = async () => {
    setBusy(true)
    setError(undefined)
    try {
      setResult(await api.policies.preview({ policy: input(), route, extra }))
    } catch (e) {
      const problems = problemsOf(e)
      if (problems) {
        onProblems(problems)
        toast.error('Fix the highlighted rules first')
      } else setError(errorMessage(e))
    } finally {
      setBusy(false)
    }
  }

  const tools = result?.tools ?? []
  const shown = useMemo(() => {
    const q = filter.trim().toLowerCase()
    return q ? tools.filter((t) => t.name.toLowerCase().includes(q) || t.upstream.toLowerCase().includes(q) || t.server.toLowerCase().includes(q)) : tools
  }, [tools, filter])
  const hidden = tools.filter((t) => !t.visible).length

  return (
    <section className="space-y-4">
      <div className="rounded-2xl border border-border bg-card p-5">
        <div className="grid gap-4 md:grid-cols-[minmax(0,1fr)_auto_auto] md:items-end">
          <FormField label="Preview route">
            <Select value={route} onValueChange={setRoute}>
              <SelectTrigger aria-label="Preview route">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value={MUX}>Global (/mcp)</SelectItem>
                {servers.map((s) => (
                  <SelectItem key={s.id} value={s.slug}>
                    <span className="font-mono text-xs">{s.slug}</span>
                    <span className="ml-2 text-muted-foreground">{s.name}</span>
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </FormField>
          <label className="flex h-10 cursor-pointer items-center gap-2 text-sm">
            <Checkbox checked={extra} onCheckedChange={(v) => setExtra(v === true)} />
            Layer on bound policies
          </label>
          <Button variant="secondary" onClick={run} disabled={busy}>
            <Play /> {busy ? 'Running…' : 'Run preview'}
          </Button>
        </div>
        {error && (
          <Callout tone="danger" className="mt-4">
            <span className="break-words">{error}</span>
          </Callout>
        )}
        {result?.unavailable && result.unavailable.length > 0 && (
          <Callout tone="warning" className="mt-4">
            <span className="font-medium">Some servers did not answer</span>
            <ul className="mt-1 space-y-0.5">
              {result.unavailable.map((u) => (
                <li key={u.server} className="break-words font-mono text-xs">
                  <span className="text-foreground">{u.server}</span> <span className="text-muted-foreground">{u.error}</span>
                </li>
              ))}
            </ul>
          </Callout>
        )}
      </div>

      {result && (
        <ListCard>
          <ListBar>
            <span>
              {tools.length.toLocaleString()} {tools.length === 1 ? 'tool' : 'tools'}
              {hidden ? ` · ${hidden} hidden` : ''}
            </span>
            {tools.length > 0 && (
              <Input value={filter} onChange={(e) => setFilter(e.target.value)} placeholder="Filter" aria-label="Filter tools" className="h-8 w-48 text-xs" />
            )}
          </ListBar>
          {shown.length > 0 && (
            <ListHead cols={COLS} at="lg">
              <span>Tool</span>
              <span>Server</span>
              <span>Visible</span>
              <span>Confirm</span>
              <span>Changed</span>
              <span>Rules hit</span>
            </ListHead>
          )}
          {shown.length === 0 ? (
            <ListEmpty icon={Eye} title={tools.length ? 'No tools match' : 'The route has no tools'} />
          ) : (
            <ListRows>
              {shown.map((t) => {
                const errored = t.hits.some((h) => h.error)
                return (
                  <ListRow key={`${t.server}/${t.upstream}`} cols={COLS} at="lg" onClick={() => setSelected(t)} className={cn('p-4', !t.visible && 'opacity-50')}>
                    <div className="flex min-w-0 items-center gap-3">
                      <IconTile icon={Wrench} tone="muted" />
                      <Primary
                        title={<span className={cn('font-mono text-xs', !t.visible && 'line-through decoration-faint-foreground')}>{t.name}</span>}
                        subtitle={t.tool.description?.split('\n')[0]}
                      />
                    </div>
                    <Cell label="Server" at="lg">
                      <span className="block truncate font-mono text-xs text-muted-foreground">{t.server}</span>
                    </Cell>
                    <Cell label="Visible" at="lg">
                      {t.visible ? <StatusPill tone="success">Visible</StatusPill> : <StatusPill tone="muted">Hidden</StatusPill>}
                    </Cell>
                    <Cell label="Confirm" at="lg">
                      {t.confirm ? <StatusPill tone="warning">Confirm</StatusPill> : <span className="text-xs text-faint-foreground">No</span>}
                    </Cell>
                    <Cell label="Changed" at="lg">
                      <Tags items={t.changed} />
                    </Cell>
                    <Cell label="Rules hit" at="lg">
                      <span className="flex min-w-0 items-center gap-1.5 text-xs text-muted-foreground">
                        {errored && <AlertTriangle className="size-3.5 shrink-0 text-warning" aria-label="A rule errored" />}
                        <span className="truncate">{t.hits.length ? t.hits.map((h) => h.rule).join(', ') : <span className="text-faint-foreground">None</span>}</span>
                      </span>
                    </Cell>
                  </ListRow>
                )
              })}
            </ListRows>
          )}
        </ListCard>
      )}

      <Sheet open={!!selected} onOpenChange={(o) => !o && setSelected(undefined)}>
        <SheetContent onOpenAutoFocus={(ev) => ev.preventDefault()}>{selected && <ToolSheet tool={selected} />}</SheetContent>
      </Sheet>
    </section>
  )
}

function Tags({ items }: { items: string[] }) {
  if (!items.length) return <span className="text-xs text-faint-foreground">None</span>
  const shown = items.slice(0, 2)
  const rest = items.slice(2)
  return (
    <div className="flex flex-wrap gap-1">
      {shown.map((f) => (
        <Badge key={f} mono>
          {f}
        </Badge>
      ))}
      {rest.length > 0 && (
        <Badge mono title={rest.join(', ')}>
          +{rest.length}
        </Badge>
      )}
    </div>
  )
}

function ToolSheet({ tool: t }: { tool: PolicyPreviewTool }) {
  const json = useMemo(() => JSON.stringify(t.tool, null, 2) ?? '', [t.tool])
  return (
    <>
      <SheetHeader>
        <SheetTitle className="break-all font-mono text-base">{t.name}</SheetTitle>
        <SheetDescription>
          <span className="font-mono">{t.server}</span>
          {t.upstream !== t.name && (
            <>
              {' · upstream '}
              <span className="font-mono">{t.upstream}</span>
            </>
          )}
        </SheetDescription>
      </SheetHeader>
      <SheetBody>
        <div className="flex flex-wrap items-center gap-2">
          {t.visible ? <StatusPill tone="success">Visible</StatusPill> : <StatusPill tone="muted">Hidden</StatusPill>}
          {t.confirm && <StatusPill tone="warning">Confirm</StatusPill>}
          {t.changed.map((f) => (
            <Badge key={f} mono>
              {f}
            </Badge>
          ))}
        </div>
        {t.confirm && t.confirmMessage && <Callout tone="info">{t.confirmMessage}</Callout>}
        <FormSection title="Rules hit">
          {t.hits.length === 0 ? (
            <p className="text-xs text-muted-foreground">No rule matched this tool.</p>
          ) : (
            <ol className="divide-y divide-border rounded-lg border border-border">
              {t.hits.map((h, i) => (
                <li key={i} className="space-y-2 px-3 py-2.5">
                  <div className="flex flex-wrap items-center gap-2 text-sm">
                    <span className="text-muted-foreground">{h.policy}</span>
                    <span className="text-faint-foreground">/</span>
                    <span className="font-medium">{h.rule}</span>
                    {h.set?.map((f) => (
                      <Badge key={f} mono>
                        {f}
                      </Badge>
                    ))}
                  </div>
                  {h.error && (
                    <Callout tone="warning">
                      <span className="break-words font-mono text-xs">{h.error}</span>
                    </Callout>
                  )}
                </li>
              ))}
            </ol>
          )}
        </FormSection>
        <FormSection title="Tool definition">
          <div className="relative">
            <CopyButton value={json} label="Copy JSON" className="absolute right-2 top-2 z-10 size-7" />
            <JsonTree value={t.tool as unknown as Json} />
          </div>
        </FormSection>
      </SheetBody>
    </>
  )
}
