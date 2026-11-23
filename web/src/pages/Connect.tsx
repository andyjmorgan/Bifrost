import { useCallback, useEffect, useMemo, useState } from 'react'
import { Link, useSearchParams } from 'react-router-dom'
import { toast } from 'sonner'
import { ExternalLink, Plug, Trash2 } from 'lucide-react'
import { Button } from '../ui/components/button'
import { Badge } from '../ui/components/badge'
import { Checkbox } from '../ui/components/checkbox'
import { PageHeader } from '../components/PageHeader'
import { ListCard, ListEmpty } from '../components/ListCard'
import { StatusPill } from '../components/StatusPill'
import { Callout } from '../components/Callout'
import { ConfirmDialog } from '../components/ConfirmDialog'
import { FormSection } from '../components/FormField'
import { ProviderIcon } from '../components/keyring/ProviderIcon'
import { PROVIDERS_ROUTE, useOAuthOutcome } from '../components/keyring/oauth'
import { errorMessage } from '../api'
import { keyring, type OAuthAccount, type OAuthAppConfig, type OAuthProvider, type OAuthScope } from '../api/keyring'
import { absoluteTime, cn, relativeTime } from '../lib'

const linkClass = 'underline decoration-accent/60 underline-offset-[3px] hover:text-accent'

/** A scope offered for selection: a catalog entry, or a bare default scope with nothing but its value. */
type ScopeChoice = Pick<OAuthScope, 'value'> & Partial<OAuthScope>

/** Connect an account: pick a provider, choose the scopes its token should carry, authorize. */
export function ConnectPage() {
  const [providers, setProviders] = useState<OAuthProvider[]>([])
  const [configs, setConfigs] = useState<OAuthAppConfig[]>([])
  const [accounts, setAccounts] = useState<OAuthAccount[]>([])
  const [loaded, setLoaded] = useState(false)
  const [error, setError] = useState<string>()
  const [params, setParams] = useSearchParams()
  // The chosen provider lives in the URL so a reload or a link lands on the same one.
  const selected = params.get('provider') ?? undefined
  const select = (key: string) => {
    const next = new URLSearchParams(params)
    next.set('provider', key)
    setParams(next, { replace: true })
  }
  // A callback that lands here is reported and forwarded to the OAuth accounts tab.
  useOAuthOutcome()

  const load = useCallback(
    () =>
      Promise.all([keyring.providers.list(), keyring.oauth.appConfigs(), keyring.oauth.accounts()])
        .then(([p, c, a]) => {
          setProviders(p ?? [])
          setConfigs(c ?? [])
          setAccounts(a ?? [])
          setError(undefined)
        })
        .catch((e) => setError(errorMessage(e)))
        .finally(() => setLoaded(true)),
    [],
  )
  useEffect(() => {
    void load()
  }, [load])

  const statusOf = (key: string) => (accounts.some((a) => a.provider === key) ? 'connected' : configs.some((c) => c.provider === key) ? 'ready' : 'new')
  const current = providers.find((p) => p.key === selected)

  return (
    <div className="space-y-6">
      <PageHeader
        eyebrow="Keyring"
        title="Connect"
        description="Pick a provider, choose the scopes the token should carry, and authorize. The keyring stores the refresh token and keeps the access token fresh."
      />

      {error && <Callout tone="danger">{error}</Callout>}

      {providers.length === 0 ? (
        <ListCard>
          <ListEmpty
            icon={Plug}
            title={loaded ? 'No OAuth providers' : 'Loading…'}
            hint={loaded ? 'Add one from the library or define a custom provider first.' : undefined}
            action={
              loaded ? (
                <Button variant="secondary" asChild>
                  <Link to={PROVIDERS_ROUTE}>Go to providers</Link>
                </Button>
              ) : undefined
            }
          />
        </ListCard>
      ) : (
        <div className="grid grid-cols-2 gap-3 sm:grid-cols-3 lg:grid-cols-4">
          {providers.map((p) => {
            const status = statusOf(p.key)
            const active = selected === p.key
            return (
              <button
                key={p.key}
                type="button"
                onClick={() => select(p.key)}
                aria-pressed={active}
                className={cn(
                  'flex min-w-0 flex-col gap-3 rounded-2xl border bg-card p-4 text-left transition-all duration-200 hover:!border-accent/50',
                  // `!`: index.css sets an unlayered `* { border-color }` that otherwise beats the accent border.
                  active && '!border-accent ring-[3px] ring-accent/15',
                )}
              >
                <div className="flex min-w-0 items-center gap-3">
                  <ProviderIcon src={p.iconUrl} name={p.name || p.key} />
                  <div className="min-w-0">
                    <div className="truncate text-sm font-medium">{p.name || p.key}</div>
                    <div className="truncate font-mono text-xs text-muted-foreground">{p.key}</div>
                  </div>
                </div>
                {status === 'connected' ? (
                  <StatusPill tone="success" className="w-fit">
                    Connected
                  </StatusPill>
                ) : status === 'ready' ? (
                  <StatusPill tone="info" className="w-fit">
                    Ready
                  </StatusPill>
                ) : (
                  <StatusPill tone="muted" className="w-fit">
                    Needs credentials
                  </StatusPill>
                )}
              </button>
            )
          })}
        </div>
      )}

      {current && (
        <ProviderConnect
          key={current.key}
          provider={current}
          config={configs.find((c) => c.provider === current.key)}
          accounts={accounts.filter((a) => a.provider === current.key)}
          onChanged={load}
        />
      )}
    </div>
  )
}

function ProviderConnect({ provider, config, accounts, onChanged }: { provider: OAuthProvider; config?: OAuthAppConfig; accounts: OAuthAccount[]; onChanged: () => void }) {
  const name = provider.name || provider.key
  const [scopes, setScopes] = useState<string[]>(config?.scopes?.length ? config.scopes : (provider.defaultScopes ?? []))
  const [error, setError] = useState<string>()
  const [busy, setBusy] = useState(false)
  const [removing, setRemoving] = useState<OAuthAccount>()
  const [removeBusy, setRemoveBusy] = useState(false)
  const [removeErr, setRemoveErr] = useState<string>()

  const toggle = (value: string, on: boolean) => setScopes((list) => (on ? [...new Set([...list, value])] : list.filter((x) => x !== value)))

  // What can be picked: the provider's scope catalog, falling back to its default scopes as plain
  // rows. Scopes are defined on the Providers page and only chosen here.
  const catalog = useMemo<ScopeChoice[]>(
    () => (provider.scopes?.length ? provider.scopes : (provider.defaultScopes ?? []).map((value) => ({ value }))),
    [provider],
  )
  const groups = useMemo(() => {
    const out: Record<string, ScopeChoice[]> = {}
    for (const s of catalog) (out[s.category || 'Scopes'] ??= []).push(s)
    return out
  }, [catalog])

  const connect = async () => {
    setError(undefined)
    setBusy(true)
    try {
      const { authorizeUrl } = await keyring.oauth.connect(provider.key, scopes)
      // Off to the provider; its redirect comes back through the server's callback.
      window.location.assign(authorizeUrl)
    } catch (e) {
      setError(errorMessage(e))
      setBusy(false)
    }
  }

  const disconnect = async () => {
    if (!removing) return
    setRemoveBusy(true)
    setRemoveErr(undefined)
    try {
      await keyring.oauth.disconnect(removing.id)
      toast.success(`Disconnected ${removing.account || name}`)
      setRemoving(undefined)
      onChanged()
    } catch (e) {
      setRemoveErr(errorMessage(e))
    } finally {
      setRemoveBusy(false)
    }
  }

  return (
    <ListCard>
      <div className="flex flex-wrap items-center justify-between gap-4 border-b border-border p-5">
        <div className="flex min-w-0 items-center gap-3">
          <ProviderIcon src={provider.iconUrl} name={name} className="size-10" />
          <div className="min-w-0">
            <h2 className="truncate text-base font-semibold">{name}</h2>
            {provider.docsUrl ? (
              <a className={cn('inline-flex items-center gap-1 text-xs text-muted-foreground', linkClass)} href={provider.docsUrl} target="_blank" rel="noreferrer">
                Scopes and docs <ExternalLink className="size-3" />
              </a>
            ) : (
              <span className="font-mono text-xs text-muted-foreground">{provider.key}</span>
            )}
          </div>
        </div>
        <Button variant="primary" onClick={connect} disabled={!config || busy}>
          <Plug /> {busy ? 'Redirecting…' : accounts.length ? 'Connect another account' : 'Connect'}
        </Button>
      </div>

      <div className="space-y-5 p-5">
        {!config && (
          <Callout tone="warning">
            No app credentials yet. Set the client ID and secret for <span className="font-medium">{name}</span> on the{' '}
            <Link to={PROVIDERS_ROUTE} className={linkClass}>
              Providers
            </Link>{' '}
            page before connecting.
          </Callout>
        )}

        {catalog.length === 0 ? (
          <p className="text-sm text-muted-foreground">
            This provider defines no scopes. Add them on the{' '}
            <Link to={PROVIDERS_ROUTE} className={linkClass}>
              Providers
            </Link>{' '}
            page.
          </p>
        ) : (
          Object.entries(groups).map(([category, items]) => (
            <FormSection
              key={category}
              title={category}
              actions={
                <span className="font-mono text-xs text-muted-foreground">
                  {items.filter((s) => scopes.includes(s.value)).length}/{items.length}
                </span>
              }
            >
              <div className="grid gap-2 sm:grid-cols-2">
                {items.map((s) => {
                  const value = s.value
                  const on = scopes.includes(value)
                  const id = `scope-${provider.key}-${value.replace(/[^a-zA-Z0-9]/g, '-')}`
                  return (
                    <label
                      key={value}
                      htmlFor={id}
                      className={cn(
                        'flex min-w-0 cursor-pointer items-start gap-3 rounded-lg border px-3 py-2.5 transition-colors hover:bg-elevated/60',
                        on && '!border-accent/40 bg-accent/5',
                      )}
                    >
                      <Checkbox id={id} className="mt-0.5" checked={on} onCheckedChange={(v) => toggle(value, v === true)} />
                      <span className="min-w-0 flex-1">
                        {s.description ? (
                          <>
                            <span className="flex flex-wrap items-center gap-2 text-sm font-medium">
                              {s.description}
                              {s.sensitive && <Badge variant="warning">Sensitive</Badge>}
                            </span>
                            <span className="mt-0.5 block truncate font-mono text-xs text-muted-foreground" title={value}>
                              {value}
                            </span>
                          </>
                        ) : (
                          <span className="flex flex-wrap items-center gap-2">
                            <span className="min-w-0 break-all font-mono text-xs font-medium leading-5">{value}</span>
                            {s.sensitive && <Badge variant="warning">Sensitive</Badge>}
                          </span>
                        )}
                      </span>
                    </label>
                  )
                })}
              </div>
            </FormSection>
          ))
        )}

        {error && <Callout tone="danger">{error}</Callout>}

        {accounts.length > 0 && (
          <div className="border-t border-border pt-5">
            <FormSection title="Connected accounts">
              <div className="divide-y divide-border rounded-lg border border-border">
                {accounts.map((a) => (
                  <div key={a.id} className="flex items-center justify-between gap-3 py-1.5 pl-3 pr-1.5 text-sm">
                    <span className="min-w-0 truncate font-medium">{a.account || name}</span>
                    <div className="flex shrink-0 items-center gap-2">
                      <span className="hidden font-mono text-xs text-muted-foreground sm:inline" title={absoluteTime(a.expiresAt)}>
                        {a.expiresAt ? `token expires ${relativeTime(a.expiresAt)}` : 'no expiry'}
                      </span>
                      <Button
                        variant="ghost"
                        size="iconSm"
                        className="hover:text-danger"
                        aria-label={`Disconnect ${a.account || name}`}
                        title="Disconnect"
                        onClick={() => {
                          setRemoveErr(undefined)
                          setRemoving(a)
                        }}
                      >
                        <Trash2 />
                      </Button>
                    </div>
                  </div>
                ))}
              </div>
            </FormSection>
          </div>
        )}
      </div>

      <ConfirmDialog
        open={!!removing}
        title="Disconnect account?"
        description={removing ? `This removes the stored ${name} token${removing.account ? ` for ${removing.account}` : ''}. You can connect it again later.` : undefined}
        confirmLabel="Disconnect"
        busy={removeBusy}
        error={removeErr}
        onCancel={() => setRemoving(undefined)}
        onConfirm={disconnect}
      />
    </ListCard>
  )
}
