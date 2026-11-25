import { useCallback, useEffect, useMemo, useState, type FormEvent } from 'react'
import { Link, Navigate, useNavigate, useSearchParams } from 'react-router-dom'
import { toast } from 'sonner'
import { ArrowUpRight, Database, KeyRound, Lock, Pencil, Plug, Plus, RefreshCw, RotateCw, Server, ShieldCheck, Terminal, Trash2, UserRound, type LucideIcon } from 'lucide-react'
import { Button } from '../ui/components/button'
import { Input } from '../ui/components/input'
import { Textarea } from '../ui/components/textarea'
import { Badge } from '../ui/components/badge'
import { Dialog, DialogBody, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '../ui/components/dialog'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '../ui/components/select'
import { PageHeader } from '../components/PageHeader'
import { Cell, ListBar, ListCard, ListEmpty, ListHead, ListPager, ListRow, ListRows, Primary, RowActions } from '../components/ListCard'
import { IconTile } from '../components/IconTile'
import { StatusPill } from '../components/StatusPill'
import { ScopeBadges } from '../components/ScopeBadges'
import { ConfirmDialog } from '../components/ConfirmDialog'
import { Definition, DefinitionList } from '../components/DefinitionList'
import { FormField } from '../components/FormField'
import { Callout } from '../components/Callout'
import { SearchBox } from '../components/SearchBox'
import { ProviderIcon } from '../components/keyring/ProviderIcon'
import { RevealButton } from '../components/keyring/RevealButton'
import { CONNECT_ROUTE, OAUTH_ACCOUNTS_ROUTE, shortScope, useOAuthOutcome } from '../components/keyring/oauth'
import { NOT_MOVED, notMoved } from '../components/servers/credential'
import { api, errorMessage, type CredentialDTO, type ServerDTO } from '../api'
import { useMe } from '../me'
import { keyring, type OAuthAccount, type OAuthProvider, type SaveSecretInput, type Secret, type SecretKind } from '../api/keyring'
import { absoluteTime, cn, relativeTime } from '../lib'

const PAGE_SIZE = 10

// Flatten only values, not property names, so every text field returned for a row is searchable
// without implementation details such as "baseUrl" producing false matches.
const textValues = (value: unknown): string[] => {
  if (typeof value === 'string') return [value]
  if (Array.isArray(value)) return value.flatMap(textValues)
  if (value && typeof value === 'object') return Object.values(value).flatMap(textValues)
  return []
}

const matchesSearch = (value: unknown, query: string, extra: string[] = []) => {
  const terms = query.trim().toLocaleLowerCase().split(/\s+/).filter(Boolean)
  if (terms.length === 0) return true
  const searchable = [...textValues(value), ...extra].join(' ').toLocaleLowerCase()
  return terms.every((term) => searchable.includes(term))
}

// The wire's kind discriminator as a label and an icon.
const kinds: Record<SecretKind, { label: string; icon: LucideIcon }> = {
  header_api_key: { label: 'Header API key', icon: KeyRound },
  opaque: { label: 'Opaque secret', icon: Lock },
  http_basic: { label: 'HTTP Basic', icon: ShieldCheck },
  username_password: { label: 'Username + password', icon: UserRound },
  ssh: { label: 'SSH', icon: Terminal },
  connection_string: { label: 'Connection string', icon: Database },
}
const kindLabel = (k: SecretKind) => kinds[k]?.label ?? k
const kindIcon = (k: SecretKind) => kinds[k]?.icon ?? Lock

// Each kind needs a different field set; this drives the add/edit form and the read-only view.
type Shape = {
  /** Show (and require) a username. */
  username?: boolean
  secretLabel: string
  /** Show the Header and Prefix inputs (header_api_key only). */
  headerPrefix?: boolean
  baseUrlLabel: string
  note?: string
}
const shapes: Record<SecretKind, Shape> = {
  opaque: { secretLabel: 'Secret', baseUrlLabel: 'Base URL / host' },
  header_api_key: { secretLabel: 'Secret', headerPrefix: true, baseUrlLabel: 'Base URL' },
  http_basic: {
    username: true,
    secretLabel: 'Password',
    baseUrlLabel: 'Base URL',
    note: 'Sent as Authorization: Basic base64(username:password). The header and prefix are handled for you.',
  },
  username_password: { username: true, secretLabel: 'Password', baseUrlLabel: 'Base URL / host' },
  ssh: { username: true, secretLabel: 'Password / private key', baseUrlLabel: 'Host' },
  connection_string: { secretLabel: 'Connection string', baseUrlLabel: 'Base URL' },
}

// One-line "how it authenticates" for the list, so username-bearing kinds (ssh, username +
// password) read sensibly instead of being mislabelled as Basic.
const authSummary = (s: Secret): string => {
  if (s.username) return s.username
  if (s.header) return `${s.header}${s.prefix ? `: ${s.prefix.trim()} …` : ''}`
  return '—'
}

const SECRET_COLS = 'minmax(0,1.7fr) 170px minmax(0,1.1fr) minmax(0,1.1fr) 108px'
// "Held for" took the room of the old Refreshed column, whose time now sits under the expiry.
const ACCOUNT_COLS = 'minmax(0,1.4fr) 112px minmax(0,1.3fr) 132px 190px 72px'

/**
 * A row of the OAuth accounts page. The keyring holds two kinds of OAuth tokens: an account a person
 * connected through a provider, in that person's keyring, and the connection of an MCP server the
 * gateway signed in to, held for the server in nobody's keyring. They are listed together and each
 * row says which it is. A server's connection comes from the servers API and is never revealable.
 */
type OAuthRow = { kind: 'account'; account: OAuthAccount } | { kind: 'server'; server: ServerDTO; cred: CredentialDTO }

/** The state of the OAuth connection held for an MCP server. */
function serverTokenStatus(cred: CredentialDTO, expired: boolean): { tone: 'success' | 'warning' | 'danger'; label: string; title?: string } {
  if (notMoved(cred)) return { tone: 'danger', label: 'Not moved', title: cred.stateReason || NOT_MOVED }
  if (cred.state === 'pending') return { tone: 'warning', label: 'Awaiting consent', title: 'Consent was started and has not been completed.' }
  if (cred.state === 'broken') return { tone: 'danger', label: 'Broken', title: cred.stateReason || cred.lastError || 'The server needs to be re-authorized.' }
  if (expired && cred.oauthHasRefreshToken) return { tone: 'warning', label: 'Refresh due', title: 'The access token has expired. The keyring refreshes it the next time the server is called.' }
  if (expired) return { tone: 'danger', label: 'Expired', title: 'The access token has expired and there is no refresh token. Re-authorize the server.' }
  return { tone: 'success', label: 'Connected' }
}

const hostOf = (url?: string) => {
  try {
    return url ? new URL(url).host : ''
  } catch {
    return url ?? ''
  }
}

type FormState = { open: boolean; item?: Secret; view?: boolean }
const closedForm: FormState = { open: false }

type View = 'secrets' | 'oauth'
type Deleting = { kind: 'secret'; item: Secret } | { kind: 'account'; item: OAuthAccount }

/** What deleting a keyring credential does to the MCP servers that authenticate with it. */
const stopsServers = (list: ServerDTO[]) =>
  list.length ? ` ${list.map((s) => s.name).join(', ')} ${list.length === 1 ? 'authenticates' : 'authenticate'} with it and will stop connecting.` : ''

/** Stored secrets, on /secrets. */
export function SecretsPage() {
  const [params] = useSearchParams()
  // /secrets?tab=oauth is how the Vault console addressed its OAuth tab; here that is its own page.
  if (params.get('tab') === 'oauth') return <Navigate to={OAUTH_ACCOUNTS_ROUTE} replace />
  return <CredentialList view="secrets" />
}

/** Connected OAuth accounts, on /oauth-accounts. */
export function OAuthAccountsPage() {
  return <CredentialList view="oauth" />
}

/**
 * The keyring's stored credentials, one kind per page: secrets or connected OAuth accounts. They
 * share a list shape, the search, the pager and the delete dialog, so they share this component.
 */
function CredentialList({ view }: { view: View }) {
  const tab = view
  useOAuthOutcome()
  const navigate = useNavigate()
  const { userId } = useMe()

  const [secrets, setSecrets] = useState<Secret[]>([])
  const [accounts, setAccounts] = useState<OAuthAccount[]>([])
  const [providers, setProviders] = useState<OAuthProvider[]>([])
  const [servers, setServers] = useState<ServerDTO[]>([])
  const [loaded, setLoaded] = useState(false)
  const [error, setError] = useState<string>()
  const [search, setSearch] = useState('')
  const [secretPage, setSecretPage] = useState(0)
  const [accountPage, setAccountPage] = useState(0)
  const [form, setForm] = useState<FormState>(closedForm)
  const [deleting, setDeleting] = useState<Deleting>()
  const [deleteBusy, setDeleteBusy] = useState(false)
  const [deleteErr, setDeleteErr] = useState<string>()
  const [refreshing, setRefreshing] = useState(false)
  // The row whose access token is being refreshed, keyed as the row's React key.
  const [refreshingRow, setRefreshingRow] = useState<string>()

  const load = useCallback(
    () =>
      // Each page loads its own kind. The provider list only names and illustrates accounts;
      // without it rows show the slug.
      Promise.all([
        view === 'secrets' ? keyring.secrets.list() : Promise.resolve([] as Secret[]),
        view === 'oauth' ? keyring.oauth.accounts() : Promise.resolve([] as OAuthAccount[]),
        view === 'oauth' ? keyring.providers.list().catch(() => [] as OAuthProvider[]) : Promise.resolve([] as OAuthProvider[]),
        // The gateway's servers: the OAuth connections it holds for them are listed beside the
        // accounts, and a credential a server uses says so. A keyring user who may not read the
        // gateway's API still gets their keyring.
        api.servers.list().catch(() => [] as ServerDTO[]),
      ])
        .then(([s, a, p, srv]) => {
          setSecrets(s ?? [])
          setAccounts(a ?? [])
          setProviders(p ?? [])
          setServers(srv ?? [])
          setError(undefined)
        })
        .catch((e) => setError(errorMessage(e)))
        .finally(() => setLoaded(true)),
    [view],
  )
  useEffect(() => {
    void load()
  }, [load])
  const refresh = () => {
    setRefreshing(true)
    void load().finally(() => setRefreshing(false))
  }
  const refreshToken = async (key: string, label: string, run: () => Promise<unknown>) => {
    setRefreshingRow(key)
    try {
      await run()
      toast.success(`Refreshed ${label}`)
    } catch (e) {
      toast.error(`Could not refresh ${label}`, { description: errorMessage(e) })
    } finally {
      setRefreshingRow(undefined)
      // A refused refresh can mark the connection broken: reload either way to show it.
      void load()
    }
  }

  const providerOf = (key: string) => providers.find((p) => p.key === key)
  const providerName = (key: string) => providerOf(key)?.name || key

  const filteredSecrets = useMemo(() => secrets.filter((s) => matchesSearch(s, search, [kindLabel(s.kind)])), [secrets, search])
  const filteredAccounts = useMemo(
    () => accounts.filter((a) => matchesSearch(a, search, [providers.find((p) => p.key === a.provider)?.name ?? ''])),
    [accounts, providers, search],
  )
  // The OAuth connections the keyring holds for the gateway's MCP servers. Only what the servers
  // API returns: issuer, scopes, expiry. The token is in nobody's keyring and is never returned.
  const serverTokens = useMemo(
    () => servers.flatMap((s): OAuthRow[] => (s.credential?.kind === 'oauth' ? [{ kind: 'server', server: s, cred: s.credential }] : [])),
    [servers],
  )
  const filteredServerTokens = useMemo(
    () =>
      serverTokens.filter(
        (r) => r.kind === 'server' && matchesSearch([r.server.name, r.server.slug, r.server.url, r.cred.oauthIssuer, r.cred.oauthScopes, r.cred.state], search, ['MCP server']),
      ),
    [serverTokens, search],
  )
  const oauthRows = useMemo<OAuthRow[]>(
    () => [...filteredAccounts.map((account): OAuthRow => ({ kind: 'account', account })), ...filteredServerTokens],
    [filteredAccounts, filteredServerTokens],
  )
  // The caller's keyring credentials that MCP servers authenticate with: removing one breaks them.
  const serversUsingAccount = (a: OAuthAccount) =>
    servers.filter((s) => {
      const c = s.credential
      return c?.kind === 'keyring_oauth' && c.keyringOwner === userId && c.keyringProvider === a.provider && c.keyringAccount === a.account
    })
  const serversUsingSecret = (x: Secret) =>
    servers.filter((s) => s.credential?.kind === 'keyring_secret' && s.credential.keyringOwner === userId && s.credential.keyringSecretName === x.name)
  const usedBy = (list: ServerDTO[]) => (list.length ? `used by ${list.length === 1 ? 'MCP server' : 'MCP servers'} ${list.map((s) => s.name).join(', ')}` : '')

  const secretPages = Math.max(1, Math.ceil(filteredSecrets.length / PAGE_SIZE))
  const accountPages = Math.max(1, Math.ceil(oauthRows.length / PAGE_SIZE))
  // Clamp rather than store, so deleting the last row of a page or narrowing the search never
  // leaves the list on a page that no longer exists.
  const sp = Math.min(secretPage, secretPages - 1)
  const ap = Math.min(accountPage, accountPages - 1)
  const visibleSecrets = filteredSecrets.slice(sp * PAGE_SIZE, (sp + 1) * PAGE_SIZE)
  const visibleRows = oauthRows.slice(ap * PAGE_SIZE, (ap + 1) * PAGE_SIZE)

  const updateSearch = (value: string) => {
    setSearch(value)
    setSecretPage(0)
    setAccountPage(0)
  }

  const requestDelete = (target: Deleting) => {
    setForm(closedForm)
    setDeleteErr(undefined)
    setDeleting(target)
  }
  const remove = async () => {
    if (!deleting) return
    setDeleteBusy(true)
    setDeleteErr(undefined)
    try {
      if (deleting.kind === 'secret') {
        await keyring.secrets.remove(deleting.item.id)
        toast.success(`Deleted ${deleting.item.name}`)
      } else {
        await keyring.oauth.disconnect(deleting.item.id)
        toast.success(`Disconnected ${deleting.item.account || providerName(deleting.item.provider)}`)
      }
      setDeleting(undefined)
      void load()
    } catch (e) {
      setDeleteErr(errorMessage(e))
    } finally {
      setDeleteBusy(false)
    }
  }

  const newSecret = () => setForm({ open: true })

  return (
    <div className="space-y-6">
      <PageHeader
        eyebrow="Keyring"
        title={tab === 'secrets' ? 'Secrets' : 'OAuth accounts'}
        description={
          tab === 'secrets'
            ? 'Stored API keys, passwords, SSH keys and connection strings that agents fetch by name. Each one describes itself, so an agent knows where and how to send it.'
            : "Accounts connected through an OAuth provider: the keyring holds the refresh token and hands agents a fresh access token on request. The OAuth connections of the gateway's MCP servers are kept here too, held for the server: they are in nobody's own keyring and cannot be revealed."
        }
        actions={
          tab === 'secrets' ? (
            <Button variant="primary" onClick={newSecret}>
              <Plus /> New secret
            </Button>
          ) : (
            <>
              <Button variant="secondary" onClick={refresh} disabled={refreshing}>
                <RotateCw className={cn(refreshing && 'animate-spin')} /> Refresh
              </Button>
              <Button variant="primary" asChild>
                <Link to={CONNECT_ROUTE}>
                  <Plug /> Connect account
                </Link>
              </Button>
            </>
          )
        }
      />

      {error && <Callout tone="danger">{error}</Callout>}

      {tab === 'secrets' && (
        <>
          <ListCard>
            <ListBar>
              <span>
                {filteredSecrets.length.toLocaleString()} {filteredSecrets.length === 1 ? 'secret' : 'secrets'}
                {search && secrets.length !== filteredSecrets.length ? ` of ${secrets.length}` : ''}
              </span>
              <SearchBox value={search} onChange={updateSearch} placeholder="Search secrets" />
            </ListBar>
            {visibleSecrets.length > 0 && (
              <ListHead cols={SECRET_COLS}>
                <span>Secret</span>
                <span>Kind</span>
                <span>Auth</span>
                <span>Base URL</span>
                <span />
              </ListHead>
            )}
            {filteredSecrets.length === 0 ? (
              <ListEmpty
                icon={Lock}
                title={secrets.length === 0 ? (loaded ? 'No secrets stored yet' : 'Loading…') : 'No secrets match your search'}
                hint={secrets.length === 0 && loaded ? 'Store an API key, password, SSH key or connection string and agents can fetch it by name.' : undefined}
                action={
                  secrets.length === 0 && loaded ? (
                    <Button variant="secondary" onClick={newSecret}>
                      <Plus /> New secret
                    </Button>
                  ) : undefined
                }
              />
            ) : (
              <>
                <ListRows>
                  {visibleSecrets.map((s) => (
                    <ListRow key={s.id} cols={SECRET_COLS} onClick={() => setForm({ open: true, item: s, view: true })}>
                      <div className="flex min-w-0 items-center gap-3">
                        <IconTile icon={kindIcon(s.kind)} />
                        <Primary title={s.name} subtitle={[s.description, usedBy(serversUsingSecret(s))].filter(Boolean).join(' · ') || undefined} />
                      </div>
                      <Cell label="Kind">
                        <Badge>{kindLabel(s.kind)}</Badge>
                      </Cell>
                      <Cell label="Auth">
                        <span className="block truncate font-mono text-xs text-muted-foreground" title={authSummary(s)}>
                          {authSummary(s)}
                        </span>
                      </Cell>
                      <Cell label="Base URL">
                        {s.baseUrl || s.docsUrl ? (
                          <a
                            className="block truncate font-mono text-xs underline decoration-accent/60 underline-offset-[3px] hover:text-accent"
                            href={s.docsUrl || s.baseUrl || undefined}
                            target="_blank"
                            rel="noreferrer"
                            title={s.docsUrl ? `Docs: ${s.docsUrl}` : (s.baseUrl ?? undefined)}
                            onClick={(e) => e.stopPropagation()}
                          >
                            {s.baseUrl || 'docs'}
                          </a>
                        ) : (
                          <span className="text-xs text-muted-foreground">—</span>
                        )}
                      </Cell>
                      <RowActions>
                        <RevealButton title={s.name} load={() => keyring.secrets.reveal(s.name).then((r) => r.secret)} />
                        <Button variant="ghost" size="iconSm" aria-label={`Edit ${s.name}`} title="Edit" onClick={() => setForm({ open: true, item: s })}>
                          <Pencil />
                        </Button>
                        <Button
                          variant="ghost"
                          size="iconSm"
                          className="hover:text-danger"
                          aria-label={`Delete ${s.name}`}
                          title="Delete"
                          onClick={() => requestDelete({ kind: 'secret', item: s })}
                        >
                          <Trash2 />
                        </Button>
                      </RowActions>
                    </ListRow>
                  ))}
                </ListRows>
                {filteredSecrets.length > PAGE_SIZE && (
                  <ListPager
                    from={sp * PAGE_SIZE + 1}
                    to={Math.min((sp + 1) * PAGE_SIZE, filteredSecrets.length)}
                    total={filteredSecrets.length}
                    canPrev={sp > 0}
                    canNext={(sp + 1) * PAGE_SIZE < filteredSecrets.length}
                    onPrev={() => setSecretPage(sp - 1)}
                    onNext={() => setSecretPage(sp + 1)}
                  />
                )}
              </>
            )}
          </ListCard>
        </>
      )}

      {tab === 'oauth' && (
        <>
          <ListCard>
            <ListBar>
              <span>
                {filteredAccounts.length.toLocaleString()} connected {filteredAccounts.length === 1 ? 'account' : 'accounts'}
                {search && accounts.length !== filteredAccounts.length ? ` of ${accounts.length}` : ''}
                {serverTokens.length > 0 && (
                  <>
                    {' · '}
                    {filteredServerTokens.length.toLocaleString()} MCP server {filteredServerTokens.length === 1 ? 'connection' : 'connections'}
                    {search && serverTokens.length !== filteredServerTokens.length ? ` of ${serverTokens.length}` : ''}
                  </>
                )}
              </span>
              <SearchBox value={search} onChange={updateSearch} placeholder="Search accounts and servers" />
            </ListBar>
            {visibleRows.length > 0 && (
              <ListHead cols={ACCOUNT_COLS}>
                <span>Account</span>
                <span>Held for</span>
                <span>Scopes</span>
                <span>Status</span>
                <span>Access token</span>
                <span />
              </ListHead>
            )}
            {oauthRows.length === 0 ? (
              <ListEmpty
                icon={Plug}
                title={accounts.length + serverTokens.length === 0 ? (loaded ? 'No OAuth accounts connected' : 'Loading…') : 'Nothing matches your search'}
                hint={accounts.length + serverTokens.length === 0 && loaded ? 'Connect a provider and the keyring keeps its access token fresh for you.' : undefined}
                action={
                  accounts.length + serverTokens.length === 0 && loaded ? (
                    <Button variant="secondary" asChild>
                      <Link to={CONNECT_ROUTE}>
                        <Plug /> Connect account
                      </Link>
                    </Button>
                  ) : undefined
                }
              />
            ) : (
              <>
                <ListRows>
                  {visibleRows.map((row) => {
                    if (row.kind === 'server') {
                      const { server: srv, cred } = row
                      const expired = !!cred.oauthExpiresAt && new Date(cred.oauthExpiresAt).getTime() < Date.now()
                      const state = serverTokenStatus(cred, expired)
                      const to = `/servers/${srv.id}`
                      return (
                        <ListRow key={`server:${srv.id}`} cols={ACCOUNT_COLS} onClick={() => navigate(to)}>
                          <div className="flex min-w-0 items-center gap-3">
                            <IconTile icon={Server} tone="muted" />
                            <Primary title={srv.name} subtitle={<span title={cred.oauthIssuer || srv.url}>{hostOf(cred.oauthIssuer) || hostOf(srv.url)}</span>} />
                          </div>
                          <Cell label="Held for">
                            <Badge variant="plum" title="Held in the keyring for this MCP server, in nobody's own keyring. It is used only to call the server and cannot be revealed.">
                              MCP server
                            </Badge>
                          </Cell>
                          <Cell label="Scopes">
                            <ScopeBadges scopes={cred.oauthScopes ?? []} variant="muted" label={shortScope} />
                          </Cell>
                          <Cell label="Status">
                            <StatusPill tone={state.tone}>
                              <span title={state.title}>{state.label}</span>
                            </StatusPill>
                          </Cell>
                          <Cell label="Access token">
                            <span
                              className={cn('block truncate font-mono text-xs', expired ? 'text-warning' : 'text-muted-foreground')}
                              title={absoluteTime(cred.oauthExpiresAt)}
                            >
                              {notMoved(cred)
                                ? 'not in the keyring yet'
                                : cred.state === 'pending'
                                  ? 'not issued yet'
                                  : cred.oauthExpiresAt
                                    ? `${expired ? 'expired' : 'expires'} ${relativeTime(cred.oauthExpiresAt)}`
                                    : 'no expiry'}
                            </span>
                            {!notMoved(cred) && (
                              <span className="mt-0.5 block truncate font-mono text-[11px] text-faint-foreground" title={absoluteTime(cred.lastRefreshedAt)}>
                                {cred.lastRefreshedAt ? `refreshed ${relativeTime(cred.lastRefreshedAt)}` : cred.oauthHasRefreshToken ? 'never refreshed' : 'no refresh token'}
                              </span>
                            )}
                          </Cell>
                          <RowActions>
                            {cred.oauthHasRefreshToken && !notMoved(cred) && cred.state !== 'pending' && (
                              <TokenRefreshButton
                                label={srv.name}
                                busy={refreshingRow === `server:${srv.id}`}
                                onClick={() => refreshToken(`server:${srv.id}`, srv.name, () => api.servers.oauthRefresh(srv.id))}
                              />
                            )}
                            <Button variant="ghost" size="iconSm" asChild>
                              <Link to={to} aria-label={`Open ${srv.name}`} title="Open the server">
                                <ArrowUpRight />
                              </Link>
                            </Button>
                          </RowActions>
                        </ListRow>
                      )
                    }
                    const a = row.account
                    const name = providerName(a.provider)
                    const title = `${name}${a.account ? ` · ${a.account}` : ''}`
                    const expired = !!a.expiresAt && new Date(a.expiresAt).getTime() < Date.now()
                    const used = usedBy(serversUsingAccount(a))
                    return (
                      <ListRow key={`account:${a.id}`} cols={ACCOUNT_COLS}>
                        <div className="flex min-w-0 items-center gap-3">
                          <ProviderIcon src={providerOf(a.provider)?.iconUrl} name={name} />
                          <Primary title={a.account || name} subtitle={used ? <span title={used}>{used}</span> : a.account ? name : a.provider} />
                        </div>
                        <Cell label="Held for">
                          <Badge title={`An account at ${name}, held in your keyring.`} className="max-w-full">
                            <span className="truncate">{name}</span>
                          </Badge>
                        </Cell>
                        <Cell label="Scopes">
                          <ScopeBadges scopes={a.scopes ?? []} variant="muted" label={shortScope} />
                        </Cell>
                        <Cell label="Status">
                          {expired ? (
                            <StatusPill tone="warning">
                              <span title="The access token has expired. The keyring refreshes it the next time it is asked for.">Refresh due</span>
                            </StatusPill>
                          ) : (
                            <StatusPill tone="success">Connected</StatusPill>
                          )}
                        </Cell>
                        <Cell label="Access token">
                          <span className={cn('block truncate font-mono text-xs', expired ? 'text-warning' : 'text-muted-foreground')} title={absoluteTime(a.expiresAt)}>
                            {a.expiresAt ? `${expired ? 'expired' : 'expires'} ${relativeTime(a.expiresAt)}` : 'no expiry'}
                          </span>
                          <span className="mt-0.5 block truncate font-mono text-[11px] text-faint-foreground" title={absoluteTime(a.lastRefreshedAt)}>
                            {a.lastRefreshedAt ? `refreshed ${relativeTime(a.lastRefreshedAt)}` : 'never refreshed'}
                          </span>
                        </Cell>
                        <RowActions>
                          <TokenRefreshButton
                            label={title}
                            busy={refreshingRow === `account:${a.id}`}
                            onClick={() => refreshToken(`account:${a.id}`, title, () => keyring.oauth.refresh(a.id))}
                          />
                          <RevealButton title={title} load={() => keyring.oauth.accessToken(a.provider, a.account || undefined).then((r) => r.accessToken)} />
                          <Button
                            variant="ghost"
                            size="iconSm"
                            className="hover:text-danger"
                            aria-label={`Disconnect ${title}`}
                            title="Disconnect"
                            onClick={() => requestDelete({ kind: 'account', item: a })}
                          >
                            <Trash2 />
                          </Button>
                        </RowActions>
                      </ListRow>
                    )
                  })}
                </ListRows>
                {oauthRows.length > PAGE_SIZE && (
                  <ListPager
                    from={ap * PAGE_SIZE + 1}
                    to={Math.min((ap + 1) * PAGE_SIZE, oauthRows.length)}
                    total={oauthRows.length}
                    canPrev={ap > 0}
                    canNext={(ap + 1) * PAGE_SIZE < oauthRows.length}
                    onPrev={() => setAccountPage(ap - 1)}
                    onNext={() => setAccountPage(ap + 1)}
                  />
                )}
              </>
            )}
          </ListCard>
        </>
      )}

      <Dialog open={form.open} onOpenChange={(o) => !o && setForm(closedForm)}>
        <DialogContent className="max-w-xl" onOpenAutoFocus={form.view ? (e) => e.preventDefault() : undefined}>
          {form.view && form.item ? (
            <SecretDetail
              item={form.item}
              onClose={() => setForm(closedForm)}
              onEdit={() => setForm({ open: true, item: form.item })}
              onDelete={() => requestDelete({ kind: 'secret', item: form.item! })}
            />
          ) : (
            <SecretForm
              key={form.item?.id ?? 'new'}
              initial={form.item}
              onClose={() => setForm(closedForm)}
              onStored={(name) => {
                toast.success(form.item ? `Saved ${name}` : `Stored ${name}`)
                setForm(closedForm)
                void load()
              }}
            />
          )}
        </DialogContent>
      </Dialog>

      <ConfirmDialog
        open={!!deleting}
        title={deleting?.kind === 'account' ? 'Disconnect account?' : `Delete ${deleting?.item.name ?? 'secret'}?`}
        description={
          deleting?.kind === 'secret'
            ? `This permanently deletes the secret. Agents that fetch it by name will start failing.${stopsServers(serversUsingSecret(deleting.item))}`
            : deleting
              ? `This removes the stored ${providerName(deleting.item.provider)} token${deleting.item.account ? ` for ${deleting.item.account}` : ''}. You can connect it again later.${stopsServers(serversUsingAccount(deleting.item))}`
              : undefined
        }
        confirmLabel={deleting?.kind === 'account' ? 'Disconnect' : 'Delete'}
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

/** The read-only view of a secret: label/value rows, with the value itself behind a reveal. */
function SecretDetail({ item, onClose, onEdit, onDelete }: { item: Secret; onClose: () => void; onEdit: () => void; onDelete: () => void }) {
  const shape = shapes[item.kind] ?? shapes.opaque
  const link = (url?: string | null) =>
    url ? (
      <a className="underline decoration-accent/60 underline-offset-[3px] hover:text-accent" href={url} target="_blank" rel="noreferrer">
        {url}
      </a>
    ) : (
      '—'
    )
  return (
    <>
      <DialogHeader>
        <div className="flex items-center gap-3">
          <IconTile icon={kindIcon(item.kind)} className="size-10" />
          <div className="min-w-0">
            <DialogTitle className="truncate">{item.name}</DialogTitle>
            <DialogDescription className="mt-0.5">{kindLabel(item.kind)}</DialogDescription>
          </div>
        </div>
      </DialogHeader>
      <DialogBody>
        <DefinitionList className="rounded-lg">
          {item.description && <Definition label="Description">{item.description}</Definition>}
          {shape.username && (
            <Definition label="Username" mono>
              {item.username || '—'}
            </Definition>
          )}
          <Definition label={shape.secretLabel} className="sm:items-center">
            <div className="flex items-center justify-between gap-2 rounded-lg border border-dashed border-border-strong bg-code py-0.5 pl-3 pr-1">
              <code className="font-mono text-xs text-muted-foreground" aria-label="Hidden value">
                ••••••••••••
              </code>
              <RevealButton title={item.name} load={() => keyring.secrets.reveal(item.name).then((r) => r.secret)} />
            </div>
          </Definition>
          {shape.headerPrefix && (
            <Definition label="Header" mono>
              {item.header ? `${item.header}${item.prefix ? `: ${item.prefix.trim()} …` : ''}` : 'Default (Authorization: Bearer)'}
            </Definition>
          )}
          <Definition label={shape.baseUrlLabel} mono>
            {link(item.baseUrl)}
          </Definition>
          <Definition label="API docs" mono>
            {link(item.docsUrl)}
          </Definition>
          <Definition label="Created" mono>
            <span title={absoluteTime(item.createdAt)}>{relativeTime(item.createdAt)}</span>
          </Definition>
          <Definition label="Last used" mono>
            <span title={absoluteTime(item.lastUsedAt)}>{item.lastUsedAt ? relativeTime(item.lastUsedAt) : 'never'}</span>
          </Definition>
        </DefinitionList>
      </DialogBody>
      <DialogFooter>
        <Button variant="ghost" className="mr-auto text-danger hover:text-danger" onClick={onDelete}>
          <Trash2 /> Delete
        </Button>
        <Button variant="outline" onClick={onClose}>
          Close
        </Button>
        <Button variant="primary" onClick={onEdit}>
          <Pencil /> Edit
        </Button>
      </DialogFooter>
    </>
  )
}

function SecretForm({ initial, onClose, onStored }: { initial?: Secret; onClose: () => void; onStored: (name: string) => void }) {
  const editing = !!initial
  // The kind is chosen while adding (the selector swaps the form body live); once stored it is fixed.
  const [kind, setKind] = useState<SecretKind>(initial?.kind ?? 'header_api_key')
  const shape = shapes[kind] ?? shapes.opaque
  const [f, setF] = useState({
    name: initial?.name ?? '',
    secret: '',
    description: initial?.description ?? '',
    baseUrl: initial?.baseUrl ?? '',
    docsUrl: initial?.docsUrl ?? '',
    header: initial?.header ?? '',
    prefix: initial?.prefix ?? '',
    username: initial?.username ?? '',
  })
  const [error, setError] = useState<string>()
  const [busy, setBusy] = useState(false)
  const set = (patch: Partial<typeof f>) => setF((cur) => ({ ...cur, ...patch }))

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    if (!f.name.trim()) return setError('Name is required.')
    if (!editing && !f.secret) return setError(`${shape.secretLabel} is required.`)
    if (shape.username && !f.username.trim()) return setError('Username is required.')
    setBusy(true)
    setError(undefined)
    // Send only the fields this kind uses, so another kind's values cannot leak through: clear the
    // username for kinds without one, and the header and prefix for kinds that do not expose them.
    const payload: SaveSecretInput = {
      ...f,
      name: f.name.trim(),
      kind,
      username: shape.username ? f.username.trim() : '',
      header: shape.headerPrefix ? f.header : '',
      prefix: shape.headerPrefix ? f.prefix : '',
    }
    try {
      await keyring.secrets.save(payload)
      onStored(payload.name)
    } catch (err) {
      setError(errorMessage(err))
    } finally {
      setBusy(false)
    }
  }

  return (
    <form className="contents" onSubmit={submit}>
      <DialogHeader>
        <DialogTitle className="break-words">{editing ? `Edit ${initial.name}` : 'New secret'}</DialogTitle>
        <DialogDescription>
          {editing ? 'Leave the value blank to keep what is stored.' : 'The description, base URL and docs link tell an agent what the secret is for and how to send it.'}
        </DialogDescription>
      </DialogHeader>
      <DialogBody>
        <div className="grid gap-4 sm:grid-cols-2">
          <FormField label="Kind" className="sm:col-span-2" hint={editing ? 'Fixed once the secret is stored.' : undefined}>
            {editing ? (
              <Input value={kindLabel(kind)} readOnly />
            ) : (
              <Select value={kind} onValueChange={(v) => setKind(v as SecretKind)}>
                <SelectTrigger aria-label="Kind">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {(Object.keys(kinds) as SecretKind[]).map((k) => (
                    <SelectItem key={k} value={k}>
                      {kinds[k].label}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            )}
          </FormField>

          <FormField label="Name" required className={shape.username ? undefined : 'sm:col-span-2'} hint={editing ? 'Agents fetch the secret by this name, so it cannot change.' : undefined}>
            <Input value={f.name} readOnly={editing} autoFocus={!editing} onChange={(e) => set({ name: e.target.value })} placeholder="grafana-prod" className="font-mono" />
          </FormField>

          {shape.username && (
            <FormField label="Username" required>
              <Input value={f.username} onChange={(e) => set({ username: e.target.value })} placeholder="admin" autoComplete="off" className="font-mono" />
            </FormField>
          )}

          <FormField label={shape.secretLabel} required={!editing} className="sm:col-span-2">
            <Input
              type="password"
              value={f.secret}
              autoFocus={editing}
              onChange={(e) => set({ secret: e.target.value })}
              placeholder={editing ? 'Leave blank to keep the current value' : undefined}
              autoComplete="new-password"
              className="font-mono"
            />
          </FormField>

          <FormField label="Description" className="sm:col-span-2">
            <Textarea rows={2} value={f.description} onChange={(e) => set({ description: e.target.value })} placeholder="What this secret is for" />
          </FormField>
          <FormField label={shape.baseUrlLabel}>
            <Input value={f.baseUrl} onChange={(e) => set({ baseUrl: e.target.value })} placeholder="https://api.example.com" className="font-mono" />
          </FormField>
          <FormField label="API docs link">
            <Input value={f.docsUrl} onChange={(e) => set({ docsUrl: e.target.value })} placeholder="https://docs.example.com" className="font-mono" />
          </FormField>

          {shape.headerPrefix && (
            <>
              <FormField label="Header" hint="Defaults to Authorization.">
                <Input value={f.header} onChange={(e) => set({ header: e.target.value })} placeholder="Authorization" className="font-mono" />
              </FormField>
              <FormField label="Prefix" hint="Placed before the secret, e.g. Bearer.">
                <Input value={f.prefix} onChange={(e) => set({ prefix: e.target.value })} placeholder="Bearer " className="font-mono" />
              </FormField>
            </>
          )}
        </div>
        {shape.note && <Callout tone="info">{shape.note}</Callout>}
        {error && <Callout tone="danger">{error}</Callout>}
      </DialogBody>
      <DialogFooter>
        <Button variant="outline" type="button" disabled={busy} onClick={onClose}>
          Cancel
        </Button>
        <Button variant="primary" type="submit" disabled={busy}>
          {busy ? 'Saving…' : editing ? 'Save changes' : 'Store secret'}
        </Button>
      </DialogFooter>
    </form>
  )
}

/** A row action that has the keyring exchange the refresh token for a new access token now. */
function TokenRefreshButton({ label, busy, onClick }: { label: string; busy: boolean; onClick: () => void }) {
  return (
    <Button variant="ghost" size="iconSm" aria-label={`Refresh the access token of ${label}`} title="Refresh the access token now" disabled={busy} onClick={onClick}>
      <RefreshCw className={cn(busy && 'animate-spin')} />
    </Button>
  )
}
