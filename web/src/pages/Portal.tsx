import { useEffect, useState } from 'react'
import { Check, ChevronDown, ChevronRight, LayoutGrid, Server } from 'lucide-react'
import { PageHeader } from '../components/PageHeader'
import { Cell, ListBar, ListCard, ListEmpty, ListHead, ListRow, ListRows, Primary } from '../components/ListCard'
import { MCPIcon } from '../components/MCPIcon'
import { StatusPill } from '../components/StatusPill'
import { Callout } from '../components/Callout'
import { CopyButton } from '../components/CopyButton'
import { KINDS, capabilitySummary, groupCapabilities, rulesPhrase } from '../components/rules/selection'
import { api, errorMessage, type PortalServerDTO } from '../api'
import { cn } from '../lib'

const COLS = 'minmax(0,1.4fr) minmax(0,1.8fr) minmax(0,1.1fr)'

// The caller's own servers: every slug they have been granted, with the URL to point an agent at
// and what it exposes.
export function PortalPage() {
  const [items, setItems] = useState<PortalServerDTO[]>([])
  const [loaded, setLoaded] = useState(false)
  const [error, setError] = useState<string>()
  const [open, setOpen] = useState<Set<string>>(new Set())

  useEffect(() => {
    api
      .portal()
      .then((x) => {
        setItems(x)
        setError(undefined)
      })
      .catch((e) => setError(errorMessage(e)))
      .finally(() => setLoaded(true))
  }, [])

  const toggle = (slug: string) =>
    setOpen((prev) => {
      const next = new Set(prev)
      if (next.has(slug)) next.delete(slug)
      else next.add(slug)
      return next
    })

  return (
    <div className="space-y-6">
      <PageHeader
        eyebrow="MCP"
        title="My servers"
        description="The MCP servers you have been granted. Point an agent at an MCP URL with one of your API keys, or at /mcp to reach all of them at once."
      />

      {error && <Callout tone="danger">{error}</Callout>}

      <GroupUrl items={items} />

      <ListCard>
        <ListBar>
          <span>
            {items.length} {items.length === 1 ? 'server' : 'servers'}
          </span>
        </ListBar>
        {items.length === 0 ? (
          <ListEmpty
            icon={LayoutGrid}
            title={loaded ? 'Nothing granted yet' : 'Loading…'}
            hint={loaded ? 'You have not been granted any servers yet. Grant yourself one from Servers, on its Access tab.' : undefined}
          />
        ) : (
          <>
            <ListHead cols={COLS}>
              <span>Server</span>
              <span>MCP URL</span>
              <span>Exposes</span>
            </ListHead>
            <ListRows>
              {items.map((x) => {
                const expanded = open.has(x.slug)
                const usable = x.enabled
                return (
                  <div key={x.slug}>
                      <ListRow cols={COLS} onClick={() => toggle(x.slug)} className={cn(!usable && 'opacity-70')}>
                        <div className="flex min-w-0 items-center gap-3">
                          <MCPIcon icons={x.icons} icon={Server} tone={usable ? 'accent' : 'muted'} />
                          <Primary
                            title={
                              <span className="flex min-w-0 items-center gap-2">
                                <span className="truncate" title={x.description || undefined}>
                                  {x.name}
                                </span>
                                {!x.enabled && <StatusPill tone="muted">Disabled</StatusPill>}
                              </span>
                            }
                            subtitle={x.slug}
                            mono
                          />
                        </div>
                        <Cell label="MCP URL">
                          <span className="flex min-w-0 items-center gap-1">
                            <span className="truncate font-mono text-xs text-muted-foreground" title={x.mcpUrl}>
                              {x.mcpUrl}
                            </span>
                            <CopyButton value={x.mcpUrl} label="Copy MCP URL" />
                          </span>
                        </Cell>
                        <Cell label="Exposes">
                          <span className="inline-flex items-center gap-1 text-xs">
                            {expanded ? <ChevronDown className="size-3.5 text-muted-foreground" /> : <ChevronRight className="size-3.5 text-muted-foreground" />}
                            {Object.keys(x.selection ?? {}).length === 0 ? 'Everything · ' : ''}
                            {capabilitySummary(x.capabilities)}
                            {rulesPhrase(x.hasRules) && <span className="ml-1.5 text-muted-foreground">{rulesPhrase(x.hasRules)}</span>}
                          </span>
                        </Cell>
                      </ListRow>
                      {expanded && (
                        <div className="px-5 pb-5">
                          <Exposes server={x} />
                        </div>
                      )}
                  </div>
                )
              })}
            </ListRows>
          </>
        )}
      </ListCard>

    </div>
  )
}

/** The gateway's public base, from any server's MCP URL: strip the trailing /<slug>/mcp. */
function publicBase(x: PortalServerDTO): string {
  const suffix = `/${x.slug}/mcp`
  return x.mcpUrl.endsWith(suffix) ? x.mcpUrl.slice(0, -suffix.length) : x.mcpUrl.replace(/\/[^/]+\/mcp\/?$/, '')
}

/** Tick several held slugs and get one /mcp?slugs=a,b URL that muxes only those. */
function GroupUrl({ items }: { items: PortalServerDTO[] }) {
  const [picked, setPicked] = useState<string[]>([])
  const usable = items.filter((x) => x.enabled)
  if (usable.length < 2) return null
  const base = publicBase(usable[0])
  const chosen = picked.filter((s) => usable.some((x) => x.slug === s))
  const url = chosen.length ? `${base}/mcp?slugs=${chosen.join(',')}` : `${base}/mcp`
  const toggle = (slug: string) => setPicked((p) => (p.includes(slug) ? p.filter((s) => s !== slug) : [...p, slug]))

  return (
    <section className="space-y-3">
      <div>
        <h2 className="text-base font-semibold tracking-tight">Group URL</h2>
        <p className="mt-0.5 text-xs text-muted-foreground">
          Tick some of your servers to reach just those on one MCP URL. With nothing ticked, /mcp reaches all of them.
        </p>
      </div>
      <div className="space-y-4 rounded-2xl border border-border bg-card p-5">
        <div className="flex flex-wrap gap-2" role="group" aria-label="Slugs in the group URL">
          {usable.map((x) => {
            const on = chosen.includes(x.slug)
            return (
              <button
                key={x.slug}
                type="button"
                aria-pressed={on}
                title={x.name}
                onClick={() => toggle(x.slug)}
                className={cn(
                  'inline-flex h-8 cursor-pointer items-center gap-1.5 rounded-lg border px-3 font-mono text-xs transition-colors',
                  on ? 'border-accent/20 bg-accent/10 text-accent' : 'bg-transparent text-muted-foreground hover:bg-elevated hover:text-foreground',
                )}
              >
                {on && <Check className="size-3.5" />}
                {x.slug}
              </button>
            )
          })}
        </div>
        <div className="flex items-center gap-1">
          <code className="min-w-0 break-all rounded-md bg-code px-2 py-1 font-mono text-xs text-code-foreground" data-testid="group-url">
            {url}
          </code>
          <CopyButton value={url} label="Copy group URL" />
        </div>
      </div>
    </section>
  )
}

function Exposes({ server }: { server: PortalServerDTO }) {
  const g = groupCapabilities(server.capabilities)
  const groups = KINDS.filter((k) => g[k.kind].length)
  if (!groups.length) return <p className="text-xs text-muted-foreground">Nothing is exposed yet.</p>
  return (
    <div className="grid gap-4 rounded-xl border border-border bg-elevated/40 p-4 sm:grid-cols-2 lg:grid-cols-4">
      {groups.map((k) => (
        <div key={k.kind} className="min-w-0">
          <p className="mb-1.5 text-[10px] font-semibold uppercase tracking-[0.16em] text-muted-foreground">
            {k.label} <span className="ml-1 font-mono normal-case tracking-normal">{g[k.kind].length}</span>
          </p>
          <ul className="space-y-0.5">
            {g[k.kind].map((n) => (
              <li key={n} className="truncate font-mono text-xs" title={n}>
                {n}
              </li>
            ))}
          </ul>
        </div>
      ))}
    </div>
  )
}
