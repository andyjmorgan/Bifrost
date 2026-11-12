import { useCallback, useEffect, useState } from 'react'
import { Link, useNavigate } from 'react-router-dom'
import { toast } from 'sonner'
import { Pencil, Plus, Ticket } from 'lucide-react'
import { Button } from '../ui/components/button'
import { Badge } from '../ui/components/badge'
import { PageHeader } from '../components/PageHeader'
import { Cell, ListBar, ListCard, ListEmpty, ListHead, ListRow, ListRows, Primary, RowActions } from '../components/ListCard'
import { IconTile } from '../components/IconTile'
import { Callout } from '../components/Callout'
import { NewEntitlementDialog } from '../components/entitlements/NewEntitlementDialog'
import { rulesPhrase, selectionDetail, selectionSummary } from '../components/entitlements/selection'
import { api, errorMessage, type EntitlementDTO, type GrantDTO, type ServerDTO } from '../api'

const COLS = 'minmax(0,1.3fr) minmax(0,1fr) minmax(0,1.1fr) minmax(0,1.5fr) 56px'

// Every entitlement across every server: the org-wide view of who holds what. Each server's detail
// page keeps its own slice.
export function EntitlementsPage() {
  const navigate = useNavigate()
  const [items, setItems] = useState<EntitlementDTO[]>([])
  const [servers, setServers] = useState<ServerDTO[]>([])
  const [loaded, setLoaded] = useState(false)
  const [error, setError] = useState<string>()
  const [creating, setCreating] = useState(false)

  const load = useCallback(
    () =>
      Promise.all([api.entitlements.list(), api.servers.list()])
        .then(([e, s]) => {
          setItems(e)
          setServers(s)
          setError(undefined)
        })
        .catch((e) => setError(errorMessage(e)))
        .finally(() => setLoaded(true)),
    [],
  )
  useEffect(() => {
    void load()
  }, [load])

  const ungranted = items.filter((e) => !e.grants.length).length
  const open = (e: EntitlementDTO) => navigate(`/entitlements/${e.id}`)

  return (
    <div className="space-y-6">
      <PageHeader
        eyebrow="Access"
        title="Entitlements"
        description="Named, grantable views of a server, each under its own slug. A caller holds the union of the entitlements granted to them and to their role."
        actions={
          <Button variant="primary" onClick={() => setCreating(true)} disabled={!servers.length}>
            <Plus /> New entitlement
          </Button>
        }
      />

      {error && <Callout tone="danger">{error}</Callout>}

      <ListCard>
        <ListBar>
          <span>
            {items.length.toLocaleString()} {items.length === 1 ? 'entitlement' : 'entitlements'}
            {ungranted > 0 && ` · ${ungranted} not granted`}
          </span>
        </ListBar>
        {items.length === 0 ? (
          <ListEmpty
            icon={Ticket}
            title={loaded ? 'No entitlements' : 'Loading…'}
            hint={loaded ? 'Each server starts with one. Add a server, or create a narrower view of one.' : undefined}
          />
        ) : (
          <>
            <ListHead cols={COLS}>
              <span>Entitlement</span>
              <span>Server</span>
              <span>Selection</span>
              <span>Grants</span>
              <span />
            </ListHead>
            <ListRows>
              {items.map((e) => (
                <ListRow key={e.id} cols={COLS} onClick={() => open(e)}>
                  <div className="flex min-w-0 items-center gap-3">
                    <IconTile icon={Ticket} tone={e.grants.length ? 'accent' : 'muted'} />
                    <Primary title={e.name} subtitle={e.slug} mono />
                  </div>
                  <Cell label="Server">
                    <Link
                      to={`/servers/${e.serverId}`}
                      onClick={(ev) => ev.stopPropagation()}
                      className="block truncate text-sm underline decoration-accent/60 underline-offset-[3px] hover:text-accent"
                    >
                      {e.serverName}
                    </Link>
                  </Cell>
                  <Cell label="Selection">
                    <span className="flex min-w-0 items-baseline gap-2 text-xs">
                      <span className="truncate" title={selectionDetail(e.selection)}>
                        {selectionSummary(e.selection)}
                      </span>
                      {rulesPhrase(e.rules?.length) && <span className="shrink-0 text-muted-foreground">{rulesPhrase(e.rules?.length)}</span>}
                    </span>
                  </Cell>
                  <Cell label="Grants">
                    <GrantChips grants={e.grants} />
                  </Cell>
                  <RowActions>
                    <Button variant="ghost" size="iconSm" aria-label="Edit" title="Edit" onClick={() => open(e)}>
                      <Pencil />
                    </Button>
                  </RowActions>
                </ListRow>
              ))}
            </ListRows>
          </>
        )}
      </ListCard>

      <NewEntitlementDialog
        open={creating}
        servers={servers}
        onClose={() => setCreating(false)}
        onCreated={(e) => {
          setCreating(false)
          toast.success(`Created ${e.slug}`)
          navigate(`/entitlements/${e.id}`)
        }}
      />
    </div>
  )
}

const roleLabel = (id: string) => (id === 'admin' ? 'Admins' : id === 'member' ? 'Members' : id)
const grantLabel = (g: GrantDTO) => (g.subjectType === 'role' ? roleLabel(g.subjectId) : g.name || g.email || g.subjectId)

/** Role grants first as accent tags, then people by name; past `max` the rest fold into +N. */
function GrantChips({ grants, max = 3 }: { grants: GrantDTO[]; max?: number }) {
  if (!grants.length) return <span className="text-xs text-faint-foreground">Not granted</span>
  const sorted = [...grants].sort((a, b) => (a.subjectType === b.subjectType ? 0 : a.subjectType === 'role' ? -1 : 1))
  const shown = sorted.slice(0, max)
  const rest = sorted.slice(max)
  return (
    <div className="flex flex-wrap items-center gap-1">
      {shown.map((g) => (
        <Badge key={`${g.subjectType}:${g.subjectId}`} variant={g.subjectType === 'role' ? 'accent' : 'muted'} title={g.subjectType === 'user' ? g.email : undefined}>
          {grantLabel(g)}
        </Badge>
      ))}
      {rest.length > 0 && (
        <Badge variant="outline" title={rest.map(grantLabel).join(', ')}>
          +{rest.length}
        </Badge>
      )}
    </div>
  )
}
