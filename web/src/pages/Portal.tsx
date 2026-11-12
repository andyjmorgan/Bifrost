import { useEffect, useState } from 'react'
import { ChevronDown, ChevronRight, LayoutGrid, Ticket } from 'lucide-react'
import { Badge } from '../ui/components/badge'
import { PageHeader } from '../components/PageHeader'
import { Cell, ListBar, ListCard, ListEmpty, ListHead, ListRow, ListRows, Primary } from '../components/ListCard'
import { IconTile } from '../components/IconTile'
import { StatusPill } from '../components/StatusPill'
import { Callout } from '../components/Callout'
import { CopyButton } from '../components/CopyButton'
import { KINDS, capabilitySummary, groupCapabilities, viaLabel } from '../components/entitlements/selection'
import { api, errorMessage, type ExposureDTO } from '../api'
import { cn } from '../lib'

const COLS = 'minmax(0,1.3fr) minmax(0,0.9fr) minmax(0,1.7fr) minmax(0,1fr) 140px'

// The member portal: every slug the caller holds, with the URL to point an agent at and what it
// exposes. Admins see their own holdings here too.
export function PortalPage() {
  const [items, setItems] = useState<ExposureDTO[]>([])
  const [loaded, setLoaded] = useState(false)
  const [error, setError] = useState<string>()
  const [open, setOpen] = useState<Set<string>>(new Set())

  useEffect(() => {
    api
      .exposures()
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

  const conflicts = items.filter((x) => x.conflict).length

  return (
    <div className="space-y-6">
      <PageHeader
        eyebrow="Gateway"
        title="My servers"
        description="The MCP servers you have been granted. Point an agent at an MCP URL with one of your API keys, or at /mcp to reach all of them at once."
      />

      {error && <Callout tone="danger">{error}</Callout>}

      <ListCard>
        <ListBar>
          <span>
            {items.length} {items.length === 1 ? 'server' : 'servers'}
            {conflicts > 0 && ` · ${conflicts} in conflict`}
          </span>
        </ListBar>
        {items.length === 0 ? (
          <ListEmpty
            icon={LayoutGrid}
            title={loaded ? 'Nothing granted yet' : 'Loading…'}
            hint={loaded ? 'You have not been granted any servers yet. Ask an admin for an entitlement.' : undefined}
          />
        ) : (
          <>
            <ListHead cols={COLS}>
              <span>Name</span>
              <span>Server</span>
              <span>MCP URL</span>
              <span>Exposes</span>
              <span>Via</span>
            </ListHead>
            <ListRows>
              {items.map((x) => {
                const expanded = open.has(x.slug)
                const usable = x.enabled && !x.conflict
                return (
                  <div key={x.slug}>
                      <ListRow cols={COLS} onClick={() => toggle(x.slug)} className={cn(!usable && 'opacity-70')}>
                        <div className="flex min-w-0 items-center gap-3">
                          <IconTile icon={Ticket} tone={usable ? 'accent' : 'muted'} />
                          <Primary title={x.name} subtitle={x.slug} mono />
                        </div>
                        <Cell label="Server">
                          <span className="block truncate text-sm" title={x.serverUrl}>
                            {x.serverName}
                          </span>
                          {!x.enabled && (
                            <StatusPill tone="muted" className="mt-1">
                              Disabled
                            </StatusPill>
                          )}
                        </Cell>
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
                            {x.selection.all ? 'Everything · ' : ''}
                            {capabilitySummary(x.capabilities)}
                          </span>
                        </Cell>
                        <Cell label="Via">
                          <div className="flex flex-wrap gap-1">
                            {x.via.map((v) => (
                              <Badge key={v} variant={v === 'user' ? 'info' : 'muted'}>
                                {viaLabel(v)}
                              </Badge>
                            ))}
                          </div>
                        </Cell>
                      </ListRow>
                      {(x.conflict || expanded) && (
                        <div className="space-y-4 px-5 pb-5">
                          {x.conflict && (
                            <Callout tone="warning">
                              <span className="font-medium">Not available.</span> {x.conflict}. Ask an admin to remove one of the grants.
                            </Callout>
                          )}
                          {expanded && <Exposes exposure={x} />}
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

function Exposes({ exposure }: { exposure: ExposureDTO }) {
  const g = groupCapabilities(exposure.capabilities)
  const groups = KINDS.filter((k) => g[k.kind].length)
  if (!groups.length) return <p className="text-xs text-muted-foreground">Nothing approved is exposed yet.</p>
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
