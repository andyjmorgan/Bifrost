import { useCallback, useEffect, useState } from 'react'
import { useNavigate, useSearchParams } from 'react-router-dom'
import { toast } from 'sonner'
import { Activity, Pencil, Plus, Server, Trash2 } from 'lucide-react'
import { Button } from '../ui/components/button'
import { Badge } from '../ui/components/badge'
import { Switch } from '../ui/components/switch'
import { PageHeader } from '../components/PageHeader'
import { Cell, ListBar, ListCard, ListEmpty, ListHead, ListRow, ListRows, Primary, RowActions } from '../components/ListCard'
import { IconTile } from '../components/IconTile'
import { StatusPill } from '../components/StatusPill'
import { Callout } from '../components/Callout'
import { ConfirmDialog } from '../components/ConfirmDialog'
import { ServerDialog } from '../components/servers/ServerDialog'
import { authKindBadge, credentialStatus } from '../components/servers/credential'
import { api, errorMessage, type CreateServerResult, type ServerDTO } from '../api'
import { cn } from '../lib'
import { SlugChips } from '../components/entitlements/SlugChips'

const COLS = 'minmax(0,1.5fr) minmax(0,1.5fr) 96px 150px 80px 120px'

export function ServersPage() {
  const navigate = useNavigate()
  const [params, setParams] = useSearchParams()
  const [servers, setServers] = useState<ServerDTO[]>([])
  const [loaded, setLoaded] = useState(false)
  const [error, setError] = useState<string>()
  const [creating, setCreating] = useState(false)
  const [editing, setEditing] = useState<ServerDTO>()
  const [deleting, setDeleting] = useState<ServerDTO>()
  const [deleteBusy, setDeleteBusy] = useState(false)
  const [deleteErr, setDeleteErr] = useState<string>()
  const [pinging, setPinging] = useState<string>()

  const load = useCallback(
    () =>
      api.servers
        .list()
        .then((items) => {
          setServers(items)
          setError(undefined)
        })
        .catch((e) => setError(errorMessage(e)))
        .finally(() => setLoaded(true)),
    [],
  )
  useEffect(() => {
    void load()
  }, [load])

  // The OAuth callback lands on /servers?oauth=… only when the state was unknown or expired.
  useEffect(() => {
    const status = params.get('oauth')
    if (!status) return
    const reason = params.get('reason')
    if (status === 'ok') toast.success('Authorization complete')
    else toast.error('Authorization failed', { description: reason ?? undefined })
    const next = new URLSearchParams(params)
    next.delete('oauth')
    next.delete('reason')
    setParams(next, { replace: true })
  }, [params, setParams])

  const toggleEnabled = async (s: ServerDTO) => {
    try {
      const updated = await api.servers.update(s.id, { enabled: !s.enabled })
      setServers((list) => list.map((x) => (x.id === s.id ? updated : x)))
      toast.success(`${updated.enabled ? 'Enabled' : 'Disabled'} ${s.name}`)
    } catch (e) {
      toast.error('Could not update server', { description: errorMessage(e) })
    }
  }

  const ping = async (s: ServerDTO) => {
    setPinging(s.id)
    try {
      const r = await api.servers.ping(s.id)
      if (r.ok) {
        toast.success(`${s.name} responded in ${r.latencyMs} ms`, {
          description: [r.serverName && `${r.serverName}${r.serverVersion ? ` ${r.serverVersion}` : ''}`, `${r.toolCount} tools`].filter(Boolean).join(' · '),
        })
      } else {
        toast.error(`${s.name} did not respond`, { description: r.error })
      }
    } catch (e) {
      toast.error('Ping failed', { description: errorMessage(e) })
    } finally {
      setPinging(undefined)
    }
  }

  const onCreated = (result: CreateServerResult) => {
    setCreating(false)
    if (result.authorizeUrl) {
      toast('Continuing to the provider for consent…')
      window.location.assign(result.authorizeUrl)
      return
    }
    toast.success(`Added ${result.server.name}`)
    navigate(`/servers/${result.server.id}`)
  }

  const remove = async () => {
    if (!deleting) return
    setDeleteBusy(true)
    setDeleteErr(undefined)
    try {
      await api.servers.remove(deleting.id)
      toast.success(`Deleted ${deleting.name}`)
      setDeleting(undefined)
      void load()
    } catch (e) {
      setDeleteErr(errorMessage(e))
    } finally {
      setDeleteBusy(false)
    }
  }

  const enabledCount = servers.filter((s) => s.enabled).length

  return (
    <div className="space-y-6">
      <PageHeader
        eyebrow="Gateway"
        title="Servers"
        description="Upstream MCP servers behind the gateway, each with a stored credential. Agents reach a server through its entitlements: named, grantable views under their own slug."
        actions={
          <Button variant="primary" onClick={() => setCreating(true)}>
            <Plus /> New server
          </Button>
        }
      />

      {error && <Callout tone="danger">{error}</Callout>}

      <ListCard>
        <ListBar>
          <span>
            {servers.length.toLocaleString()} {servers.length === 1 ? 'server' : 'servers'}
            {servers.length > 0 && enabledCount !== servers.length ? ` · ${servers.length - enabledCount} disabled` : ''}
          </span>
        </ListBar>
        {servers.length > 0 && (
          <ListHead cols={COLS}>
            <span>Server</span>
            <span>Entitlements</span>
            <span>Auth</span>
            <span>Credential</span>
            <span>Enabled</span>
            <span className="text-right">Actions</span>
          </ListHead>
        )}
        {servers.length === 0 ? (
          <ListEmpty
            icon={Server}
            title={loaded ? 'No servers yet' : 'Loading…'}
            hint={loaded ? 'Register an upstream MCP server and the gateway will expose it to your agents.' : undefined}
            action={
              loaded ? (
                <Button variant="secondary" onClick={() => setCreating(true)}>
                  <Plus /> New server
                </Button>
              ) : undefined
            }
          />
        ) : (
          <ListRows>
            {servers.map((s) => {
              const status = credentialStatus(s)
              return (
                <ListRow key={s.id} cols={COLS} onClick={() => navigate(`/servers/${s.id}`)} className={cn(!s.enabled && 'opacity-70')}>
                  <div className="flex min-w-0 items-center gap-3">
                    <IconTile icon={Server} tone={s.enabled ? 'accent' : 'muted'} />
                    <Primary
                      title={
                        <span className="flex min-w-0 items-center gap-2">
                          <span className="truncate">{s.name}</span>
                          {s.pending > 0 && (
                            <StatusPill tone="warning" className="py-0.5">
                              {s.pending} pending
                            </StatusPill>
                          )}
                        </span>
                      }
                      subtitle={<span title={s.url}>{s.url}</span>}
                      mono
                    />
                  </div>
                  <Cell label="Entitlements">
                    <SlugChips entitlements={s.entitlements ?? []} />
                  </Cell>
                  <Cell label="Auth">
                    <Badge mono variant={authKindBadge[s.authKind]}>
                      {s.authKind}
                    </Badge>
                  </Cell>
                  <Cell label="Credential">
                    <StatusPill tone={status.tone}>{status.label}</StatusPill>
                  </Cell>
                  <Cell label="Enabled">
                    <span className="inline-flex" onClick={(e) => e.stopPropagation()} onKeyDown={(e) => e.stopPropagation()}>
                      <Switch checked={s.enabled} onCheckedChange={() => toggleEnabled(s)} aria-label={`${s.name} enabled`} />
                    </span>
                  </Cell>
                  <RowActions>
                    <Button variant="ghost" size="iconSm" aria-label="Ping" title="Ping" disabled={pinging === s.id} onClick={() => ping(s)}>
                      <Activity className={cn(pinging === s.id && 'animate-pulse')} />
                    </Button>
                    <Button variant="ghost" size="iconSm" aria-label="Edit" title="Edit" onClick={() => setEditing(s)}>
                      <Pencil />
                    </Button>
                    <Button variant="ghost" size="iconSm" className="hover:text-danger" aria-label="Delete" title="Delete" onClick={() => setDeleting(s)}>
                      <Trash2 />
                    </Button>
                  </RowActions>
                </ListRow>
              )
            })}
          </ListRows>
        )}
      </ListCard>

      <ServerDialog open={creating} onClose={() => setCreating(false)} onCreated={onCreated} />
      <ServerDialog
        open={!!editing}
        server={editing}
        onClose={() => setEditing(undefined)}
        onUpdated={(updated) => {
          setEditing(undefined)
          setServers((list) => list.map((x) => (x.id === updated.id ? updated : x)))
          toast.success(`Saved ${updated.name}`)
        }}
      />
      <ConfirmDialog
        open={!!deleting}
        title={`Delete ${deleting?.name ?? 'server'}?`}
        description="The server, its credential and its entitlements are removed. Agents using those slugs will start failing."
        busy={deleteBusy}
        error={deleteErr}
        onCancel={() => {
          setDeleting(undefined)
          setDeleteErr(undefined)
        }}
        onConfirm={remove}
      />
    </div>
  )
}
