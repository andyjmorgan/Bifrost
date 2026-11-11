import { useCallback, useEffect, useState } from 'react'
import { Link, useNavigate, useParams, useSearchParams } from 'react-router-dom'
import { toast } from 'sonner'
import { Activity, ArrowLeft, KeyRound, Pencil, RefreshCw, Telescope, Trash2 } from 'lucide-react'
import { Button } from '../ui/components/button'
import { Badge } from '../ui/components/badge'
import { Switch } from '../ui/components/switch'
import { PageHeader } from '../components/PageHeader'
import { Definition, DefinitionList } from '../components/DefinitionList'
import { StatusPill } from '../components/StatusPill'
import { Callout } from '../components/Callout'
import { CopyButton } from '../components/CopyButton'
import { ConfirmDialog } from '../components/ConfirmDialog'
import { BindingsCard } from '../components/policies/Bindings'
import { ServerDialog } from '../components/servers/ServerDialog'
import { CredentialDialog } from '../components/servers/CredentialDialog'
import { authKindBadge, authKindLabel, credentialStatus } from '../components/servers/credential'
import { api, errorMessage, type CreateServerResult, type DiscoverResult, type PingResult, type ServerDTO } from '../api'
import { JsonTree, type Json } from '../components/PayloadViewer'
import { absoluteTime, relativeTime } from '../lib'

function Time({ value }: { value?: string | null }) {
  if (!value) return <span className="text-muted-foreground">—</span>
  return (
    <span className="font-mono text-xs text-muted-foreground" title={absoluteTime(value)}>
      {relativeTime(value)}
    </span>
  )
}

export function ServerDetailPage() {
  const { id = '' } = useParams()
  const navigate = useNavigate()
  const [params, setParams] = useSearchParams()
  const [server, setServer] = useState<ServerDTO>()
  const [error, setError] = useState<string>()
  const [loaded, setLoaded] = useState(false)
  const [editing, setEditing] = useState(false)
  const [replacing, setReplacing] = useState(false)
  const [deleting, setDeleting] = useState(false)
  const [deleteBusy, setDeleteBusy] = useState(false)
  const [deleteErr, setDeleteErr] = useState<string>()
  const [pinging, setPinging] = useState(false)
  const [ping, setPing] = useState<PingResult>()
  const [discovering, setDiscovering] = useState(false)
  const [discovery, setDiscovery] = useState<DiscoverResult>()
  const [reauthBusy, setReauthBusy] = useState(false)

  const load = useCallback(
    () =>
      api.servers
        .get(id)
        .then((s) => {
          setServer(s)
          setError(undefined)
        })
        .catch((e) => setError(errorMessage(e)))
        .finally(() => setLoaded(true)),
    [id],
  )
  useEffect(() => {
    void load()
  }, [load])

  // Arriving back from the provider: ?oauth=ok|error&reason=… → toast, then strip the query.
  useEffect(() => {
    const status = params.get('oauth')
    if (!status) return
    const reason = params.get('reason')
    if (status === 'ok') toast.success('Authorization complete', { description: 'The credential is connected.' })
    else toast.error('Authorization failed', { description: reason ?? 'The provider returned an error.' })
    const next = new URLSearchParams(params)
    next.delete('oauth')
    next.delete('reason')
    setParams(next, { replace: true })
  }, [params, setParams])

  const toggleEnabled = async () => {
    if (!server) return
    try {
      const updated = await api.servers.update(server.id, { enabled: !server.enabled })
      setServer(updated)
      toast.success(`${updated.enabled ? 'Enabled' : 'Disabled'} ${updated.name}`)
    } catch (e) {
      toast.error('Could not update server', { description: errorMessage(e) })
    }
  }

  const doDiscover = async () => {
    if (!server) return
    setDiscovering(true)
    setDiscovery(undefined)
    try {
      setDiscovery(await api.servers.discover(server.id))
    } catch (e) {
      setDiscovery({ ok: false, latencyMs: 0, error: errorMessage(e) })
    } finally {
      setDiscovering(false)
    }
  }

  const doPing = async () => {
    if (!server) return
    setPinging(true)
    setPing(undefined)
    try {
      setPing(await api.servers.ping(server.id))
    } catch (e) {
      setPing({ ok: false, latencyMs: 0, toolCount: 0, error: errorMessage(e) })
    } finally {
      setPinging(false)
    }
  }

  const followAuthorize = (result: CreateServerResult) => {
    setReplacing(false)
    setServer(result.server)
    if (result.authorizeUrl) {
      toast('Continuing to the provider for consent…')
      window.location.assign(result.authorizeUrl)
    } else {
      toast.success('Credential replaced')
    }
  }

  const reauthorize = async () => {
    if (!server) return
    setReauthBusy(true)
    try {
      const { authorizeUrl } = await api.servers.oauthStart(server.id, { returnTo: window.location.href })
      window.location.assign(authorizeUrl)
    } catch (e) {
      toast.error('Could not start authorization', { description: errorMessage(e) })
      setReauthBusy(false)
    }
  }

  const remove = async () => {
    if (!server) return
    setDeleteBusy(true)
    setDeleteErr(undefined)
    try {
      await api.servers.remove(server.id)
      toast.success(`Deleted ${server.name}`)
      navigate('/servers', { replace: true })
    } catch (e) {
      setDeleteErr(errorMessage(e))
      setDeleteBusy(false)
    }
  }

  if (!server) {
    return (
      <div className="space-y-6">
        <Button variant="link" asChild>
          <Link to="/servers">
            <ArrowLeft /> Servers
          </Link>
        </Button>
        {error ? <Callout tone="danger">{error}</Callout> : <p className="text-sm text-muted-foreground">{loaded ? 'Server not found.' : 'Loading…'}</p>}
      </div>
    )
  }

  const cred = server.credential
  const status = credentialStatus(server)
  const oauth = server.authKind === 'oauth'

  return (
    <div className="space-y-6">
      <Button variant="link" asChild>
        <Link to="/servers">
          <ArrowLeft /> Servers
        </Link>
      </Button>
      <PageHeader
        eyebrow={server.slug}
        title={server.name}
        description={server.description || 'No description.'}
        actions={
          <>
            <Button variant="secondary" onClick={doPing} disabled={pinging}>
              <Activity /> {pinging ? 'Pinging…' : 'Ping'}
            </Button>
            <Button variant="secondary" onClick={doDiscover} disabled={discovering}>
              <Telescope /> {discovering ? 'Discovering…' : 'Discover'}
            </Button>
            <Button variant="secondary" onClick={() => setEditing(true)}>
              <Pencil /> Edit
            </Button>
            <Button variant="danger" onClick={() => setDeleting(true)}>
              <Trash2 /> Delete
            </Button>
          </>
        }
      />

      {error && <Callout tone="danger">{error}</Callout>}
      {ping && <PingPanel ping={ping} />}
      {discovery && <DiscoverPanel d={discovery} />}
      {cred?.lastError && (
        <Callout tone="danger">
          <span className="font-medium">Credential error</span>
          <span className="ml-2 break-words font-mono text-xs">{cred.lastError}</span>
        </Callout>
      )}

      <DefinitionList>
        <Definition label="Enabled">
          <div className="flex items-center gap-3">
            <Switch checked={server.enabled} onCheckedChange={toggleEnabled} aria-label="Enabled" />
            <span className="text-xs text-muted-foreground">{server.enabled ? 'Reachable through the gateway' : 'Hidden from the mux and direct routes'}</span>
          </div>
        </Definition>
        <Definition label="Upstream" mono>
          <span className="break-all">{server.url}</span>
        </Definition>
        <Definition label="Gateway">
          <div className="flex items-center gap-2">
            <code className="min-w-0 break-all rounded-md bg-code px-2 py-1 font-mono text-xs text-code-foreground">{server.mcpUrl}</code>
            <CopyButton value={server.mcpUrl} label="Copy gateway URL" />
          </div>
        </Definition>
        <Definition label="Credential">
          <div className="flex flex-wrap items-center gap-2">
            <Badge mono variant={authKindBadge[server.authKind]}>
              {authKindLabel(server.authKind)}
            </Badge>
            <StatusPill tone={status.tone}>{status.label}</StatusPill>
            <CredentialSummary cred={cred} />
            {server.authKind !== 'none' && (
              <Button variant="link" size="sm" onClick={() => setReplacing(true)}>
                <KeyRound /> Replace
              </Button>
            )}
            {oauth && (
              <Button variant="link" size="sm" onClick={reauthorize} disabled={reauthBusy}>
                <RefreshCw /> {reauthBusy ? 'Starting…' : 'Re-authorize'}
              </Button>
            )}
          </div>
        </Definition>
      </DefinitionList>

      <BindingsCard route={server.slug} title="Policies" hint="No policies bound. Global policies still apply on /mcp." />

      <details className="group text-sm">
        <summary className="cursor-pointer select-none text-xs font-semibold uppercase tracking-[0.16em] text-muted-foreground hover:text-foreground">
          Details
        </summary>
        <DefinitionList className="mt-3">
          {cred?.kind === 'oauth' && (
            <>
              <Definition label="Registration" mono>
                {cred.oauthRegistrationKind ? cred.oauthRegistrationKind.toUpperCase() : '—'}
              </Definition>
              <Definition label="Client ID" mono>
                <span className="break-all">{cred.oauthClientId || '—'}</span>
              </Definition>
              <Definition label="Issuer" mono>
                <span className="break-all">{cred.oauthIssuer || '—'}</span>
              </Definition>
              {cred.oauthResource && (
                <Definition label="Resource" mono>
                  <span className="break-all">{cred.oauthResource}</span>
                </Definition>
              )}
              <Definition label="Last refreshed">
                <Time value={cred.lastRefreshedAt} />
              </Definition>
            </>
          )}
          <Definition label="Created">
            <Time value={server.createdAt} />
          </Definition>
          <Definition label="Updated">
            <Time value={server.updatedAt} />
          </Definition>
          <Definition label="ID" mono>
            {server.id}
          </Definition>
        </DefinitionList>
      </details>

      <ServerDialog
        open={editing}
        server={server}
        onClose={() => setEditing(false)}
        onUpdated={(updated) => {
          setEditing(false)
          setServer(updated)
          toast.success(`Saved ${updated.name}`)
        }}
      />
      <CredentialDialog open={replacing} server={server} onClose={() => setReplacing(false)} onSaved={followAuthorize} />
      <ConfirmDialog
        open={deleting}
        title={`Delete ${server.name}?`}
        description="The server, its credential and its gateway URL are removed. Agents using this slug will start failing."
        busy={deleteBusy}
        error={deleteErr}
        onCancel={() => {
          setDeleting(false)
          setDeleteErr(undefined)
        }}
        onConfirm={remove}
      />
    </div>
  )
}

/** One muted phrase describing the stored credential: who/what header, or scopes + expiry for OAuth. */
function CredentialSummary({ cred }: { cred?: ServerDTO['credential'] }) {
  if (!cred) return null
  const parts: string[] = []
  if (cred.kind === 'basic' && cred.basicUsername) parts.push(`user ${cred.basicUsername}`)
  if (cred.kind === 'header' && cred.headerName) parts.push(cred.headerName)
  if (cred.kind === 'oauth') {
    if (cred.oauthScopes.length) parts.push(cred.oauthScopes.join(' '))
    if (cred.oauthExpiresAt) parts.push(`expires ${relativeTime(cred.oauthExpiresAt)}`)
    if (!cred.oauthHasRefreshToken && cred.state === 'ok') parts.push('no refresh token')
  }
  if (cred.stateReason) parts.push(cred.stateReason)
  if (!parts.length) return null
  return <span className="font-mono text-xs text-muted-foreground">{parts.join(' · ')}</span>
}

/** What the server said at initialize: identity, instructions, capabilities, verbatim. */
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
      <dl className="grid gap-4 sm:grid-cols-4">
        {items.map(([k, v]) => (
          <div key={k} className="min-w-0">
            <dt className="text-[10px] font-semibold uppercase tracking-[0.16em] text-muted-foreground">{k}</dt>
            <dd className="mt-1 truncate font-mono text-sm" title={v}>{v}</dd>
          </div>
        ))}
      </dl>
      <div className="mt-4">
        <p className="mb-1.5 text-[10px] font-semibold uppercase tracking-[0.16em] text-muted-foreground">Instructions</p>
        {d.instructions ? (
          <p className="whitespace-pre-wrap rounded-lg border border-border bg-elevated p-3 text-sm leading-6">{d.instructions}</p>
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
      <dl className="grid gap-4 sm:grid-cols-4">
        {items.map(([k, v]) => (
          <div key={k}>
            <dt className="text-[10px] font-semibold uppercase tracking-[0.16em] text-muted-foreground">{k}</dt>
            <dd className="mt-1 font-mono text-sm">{v}</dd>
          </div>
        ))}
      </dl>
    </div>
  )
}
