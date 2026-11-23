import { useCallback, useEffect, useState, type FormEvent } from 'react'
import { Link } from 'react-router-dom'
import { toast } from 'sonner'
import { Boxes, Pencil, Plus, Search, Trash2 } from 'lucide-react'
import { Button } from '../ui/components/button'
import { Input } from '../ui/components/input'
import { Checkbox } from '../ui/components/checkbox'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '../ui/components/select'
import { Dialog, DialogBody, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '../ui/components/dialog'
import { PageHeader } from '../components/PageHeader'
import { Cell, ListBar, ListCard, ListEmpty, ListHead, ListRow, ListRows, Primary, RowActions } from '../components/ListCard'
import { StatusPill } from '../components/StatusPill'
import { CopyButton } from '../components/CopyButton'
import { ConfirmDialog } from '../components/ConfirmDialog'
import { FormField, FormSection } from '../components/FormField'
import { Callout } from '../components/Callout'
import { ProviderIcon } from '../components/keyring/ProviderIcon'
import { CONNECT_ROUTE } from '../components/keyring/oauth'
import { errorMessage } from '../api'
import { keyring, type OAuthAppConfig, type OAuthProvider, type OAuthProviderDraft, type OAuthScope, type TokenEndpointAuthMethod } from '../api/keyring'

const COLS = 'minmax(0,1.4fr) minmax(0,1.7fr) 150px 80px'
const CALLBACK_PATH = '/api/oauth/callback'

type Editing = { value: OAuthProviderDraft; isNew: boolean }

const blankProvider = (): OAuthProviderDraft => ({
  key: '',
  name: '',
  iconUrl: '',
  docsUrl: '',
  authorizationEndpoint: '',
  tokenEndpoint: '',
  userinfoEndpoint: '',
  scopeDelimiter: ' ',
  defaultScopes: [],
  scopes: [],
})

/**
 * The provider catalog. The built-in templates are a library: adding one copies the whole template
 * into a self-contained, editable provider (remembering which template it came from), and a custom
 * provider can be filled in from an issuer's OIDC discovery document. A provider is connectable
 * once it has been added and given app credentials. All editing lives here; Connect only picks
 * scopes and authorizes.
 */
export function ProvidersPage() {
  const [providers, setProviders] = useState<OAuthProvider[]>([])
  const [templates, setTemplates] = useState<OAuthProvider[]>([])
  const [configs, setConfigs] = useState<OAuthAppConfig[]>([])
  const [loaded, setLoaded] = useState(false)
  const [error, setError] = useState<string>()
  const [editing, setEditing] = useState<Editing>()
  const [deleting, setDeleting] = useState<OAuthProvider>()
  const [deleteBusy, setDeleteBusy] = useState(false)
  const [deleteErr, setDeleteErr] = useState<string>()

  const load = useCallback(
    () =>
      Promise.all([keyring.providers.list(), keyring.providers.templates().catch(() => [] as OAuthProvider[]), keyring.oauth.appConfigs().catch(() => [] as OAuthAppConfig[])])
        .then(([p, t, c]) => {
          setProviders(p ?? [])
          setTemplates(t ?? [])
          setConfigs(c ?? [])
          setError(undefined)
        })
        .catch((e) => setError(errorMessage(e)))
        .finally(() => setLoaded(true)),
    [],
  )
  useEffect(() => {
    void load()
  }, [load])

  const configFor = (key: string) => configs.find((c) => c.provider === key)
  // A template already added (matched by slug) drops out of the library.
  const added = new Set(providers.map((p) => p.key))
  const library = templates.filter((t) => !added.has(t.key))
  // The keyring's callback URL is not in its config document; an existing app's stored redirect
  // URI is the only place the console can learn it from.
  const knownRedirect = configs.map((c) => c.redirectUri).find((uri): uri is string => !!uri)

  const edit = (p: OAuthProvider) => setEditing({ value: structuredClone(p), isNew: false })
  const addFromTemplate = (t: OAuthProvider) => setEditing({ value: { ...structuredClone(t), id: undefined, parentId: t.id, template: false }, isNew: true })

  const remove = async () => {
    if (!deleting) return
    setDeleteBusy(true)
    setDeleteErr(undefined)
    try {
      await keyring.providers.remove(deleting.key)
      toast.success(`Removed ${deleting.name || deleting.key}`)
      setDeleting(undefined)
      void load()
    } catch (e) {
      setDeleteErr(errorMessage(e))
    } finally {
      setDeleteBusy(false)
    }
  }

  return (
    <div className="space-y-6">
      <PageHeader
        eyebrow="Keyring"
        title="Providers"
        description="The OAuth providers accounts can be connected through. Add one from the library or define a custom OIDC provider, give it app credentials, then connect an account."
        actions={
          <Button variant="primary" onClick={() => setEditing({ value: blankProvider(), isNew: true })}>
            <Plus /> Custom provider
          </Button>
        }
      />

      {error && <Callout tone="danger">{error}</Callout>}

      <ListCard>
        <ListBar>
          <span>
            {providers.length.toLocaleString()} {providers.length === 1 ? 'provider' : 'providers'}
          </span>
          {providers.length > 0 && (
            <Link to={CONNECT_ROUTE} className="underline decoration-accent/60 underline-offset-[3px] hover:text-accent">
              Connect an account
            </Link>
          )}
        </ListBar>
        {providers.length > 0 && (
          <ListHead cols={COLS}>
            <span>Provider</span>
            <span>Authorization endpoint</span>
            <span>App credentials</span>
            <span />
          </ListHead>
        )}
        {providers.length === 0 ? (
          <ListEmpty
            icon={Boxes}
            title={loaded ? 'No providers yet' : 'Loading…'}
            hint={loaded ? (library.length ? 'Pick one from the library below or add a custom provider.' : 'Add a custom provider to get started.') : undefined}
          />
        ) : (
          <ListRows>
            {providers.map((p) => (
              <ListRow key={p.key} cols={COLS} onClick={() => edit(p)}>
                <div className="flex min-w-0 items-center gap-3">
                  <ProviderIcon src={p.iconUrl} name={p.name || p.key} />
                  <Primary title={p.name || p.key} subtitle={p.key} mono />
                </div>
                <Cell label="Authorization endpoint">
                  <span className="block truncate font-mono text-xs text-muted-foreground" title={p.authorizationEndpoint}>
                    {p.authorizationEndpoint || '—'}
                  </span>
                </Cell>
                <Cell label="App credentials">{configFor(p.key) ? <StatusPill tone="success">Configured</StatusPill> : <StatusPill tone="muted">Not set</StatusPill>}</Cell>
                <RowActions>
                  <Button variant="ghost" size="iconSm" aria-label={`Edit ${p.name || p.key}`} title="Edit" onClick={() => edit(p)}>
                    <Pencil />
                  </Button>
                  <Button
                    variant="ghost"
                    size="iconSm"
                    className="hover:text-danger"
                    aria-label={`Remove ${p.name || p.key}`}
                    title="Remove"
                    onClick={() => {
                      setDeleteErr(undefined)
                      setDeleting(p)
                    }}
                  >
                    <Trash2 />
                  </Button>
                </RowActions>
              </ListRow>
            ))}
          </ListRows>
        )}
      </ListCard>

      {library.length > 0 && (
        <section className="space-y-3">
          <div>
            <h2 className="text-base font-semibold">Library</h2>
            <p className="mt-1 max-w-2xl text-sm leading-6 text-muted-foreground">Built-in templates. Adding one copies it into your providers, where you set its app credentials and connect.</p>
          </div>
          <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-3">
            {library.map((t) => (
              <div key={t.key} className="flex items-center justify-between gap-3 rounded-2xl border border-border bg-card p-4">
                <div className="flex min-w-0 items-center gap-3">
                  <ProviderIcon src={t.iconUrl} name={t.name || t.key} />
                  <Primary title={t.name || t.key} subtitle={t.key} mono />
                </div>
                <Button variant="secondary" size="sm" aria-label={`Add ${t.name || t.key}`} onClick={() => addFromTemplate(t)}>
                  <Plus /> Add
                </Button>
              </div>
            ))}
          </div>
        </section>
      )}

      {editing && (
        <ProviderDialog
          value={editing.value}
          config={configFor(editing.value.key)}
          isNew={editing.isNew}
          knownRedirect={knownRedirect}
          onClose={() => setEditing(undefined)}
          onSaved={(name) => {
            toast.success(`Saved ${name}`)
            setEditing(undefined)
            void load()
          }}
        />
      )}

      <ConfirmDialog
        open={!!deleting}
        title={`Remove ${deleting?.name || deleting?.key || 'provider'}?`}
        description="This removes the provider and its app credentials. Accounts connected through it will stop refreshing."
        confirmLabel="Remove"
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

type Param = { key: string; value: string }

function ProviderDialog({
  value,
  config,
  isNew,
  knownRedirect,
  onClose,
  onSaved,
}: {
  value: OAuthProviderDraft
  config?: OAuthAppConfig
  isNew: boolean
  knownRedirect?: string
  onClose: () => void
  onSaved: (name: string) => void
}) {
  const [m, setM] = useState<OAuthProviderDraft>(value)
  const [issuer, setIssuer] = useState('')
  const [discovering, setDiscovering] = useState(false)
  const [scopes, setScopes] = useState<OAuthScope[]>(
    value.scopes?.length ? value.scopes : (value.defaultScopes ?? []).map((v) => ({ value: v, description: '', category: '', sensitive: false })),
  )
  const [clientId, setClientId] = useState('')
  const [clientSecret, setClientSecret] = useState('')
  const [redirect, setRedirect] = useState(config?.redirectUri ?? '')
  const [authParams, setAuthParams] = useState<Param[]>(Object.entries(value.authorizeParams ?? {}).map(([key, val]) => ({ key, value: val })))
  const [error, setError] = useState<string>()
  const [busy, setBusy] = useState(false)
  const set = (patch: Partial<OAuthProviderDraft>) => setM((cur) => ({ ...cur, ...patch }))
  const label = m.name || m.key

  const discover = async () => {
    if (!issuer.trim() || discovering) return
    setDiscovering(true)
    setError(undefined)
    try {
      const d = await keyring.providers.discover(issuer.trim())
      setM((cur) => ({
        ...cur,
        name: cur.name || d.name || '',
        key: cur.key || d.key || '',
        authorizationEndpoint: d.authorizationEndpoint || cur.authorizationEndpoint,
        tokenEndpoint: d.tokenEndpoint || cur.tokenEndpoint,
        userinfoEndpoint: d.userinfoEndpoint || cur.userinfoEndpoint,
      }))
      if (d.scopes?.length) setScopes(d.scopes)
      const n = d.scopes?.length ?? 0
      toast.success(`Discovered the endpoints and ${n} ${n === 1 ? 'scope' : 'scopes'}`)
    } catch (e) {
      setError(errorMessage(e))
    } finally {
      setDiscovering(false)
    }
  }

  const setScope = (i: number, patch: Partial<OAuthScope>) => setScopes((rows) => rows.map((row, j) => (j === i ? { ...row, ...patch } : row)))
  const setParam = (i: number, patch: Partial<Param>) => setAuthParams((rows) => rows.map((row, j) => (j === i ? { ...row, ...patch } : row)))

  const removeCredentials = async () => {
    if (!config) return
    setError(undefined)
    setBusy(true)
    try {
      await keyring.oauth.removeAppConfig(config.id)
      toast.success('Removed app credentials')
      onSaved(label)
    } catch (e) {
      setError(errorMessage(e))
    } finally {
      setBusy(false)
    }
  }

  const save = async (e: FormEvent) => {
    e.preventDefault()
    if (!m.key.trim()) return setError('Slug is required.')
    setError(undefined)
    setBusy(true)
    const cleanScopes = scopes
      .filter((s) => s.value.trim())
      .map((s) => ({ value: s.value.trim(), description: s.description || '', category: s.category || '', sensitive: !!s.sensitive }))
    const values = cleanScopes.map((s) => s.value)
    // Keep the provider's chosen defaults that still exist; with none chosen, every scope is a default.
    const defaultScopes = m.defaultScopes?.length ? m.defaultScopes.filter((v) => values.includes(v)) : values
    const authorizeParams = Object.fromEntries(authParams.filter((p) => p.key.trim()).map((p) => [p.key.trim(), p.value]))
    const key = m.key.trim()
    try {
      await keyring.providers.save({ ...m, key, scopeDelimiter: m.scopeDelimiter || ' ', defaultScopes, scopes: cleanScopes, authorizeParams })
      if (clientId.trim()) {
        await keyring.oauth.saveAppConfig({
          provider: key,
          clientId: clientId.trim(),
          clientSecret: clientSecret || undefined,
          scopes: defaultScopes,
          redirectUri: redirect.trim() || undefined,
        })
      }
      onSaved(label || key)
    } catch (err) {
      setError(errorMessage(err))
    } finally {
      setBusy(false)
    }
  }

  return (
    <Dialog open onOpenChange={(o) => !o && !busy && onClose()}>
      {/* Only a blank custom provider starts with something to type (the issuer); otherwise keep
          focus off the close button. */}
      <DialogContent className="max-w-3xl" onOpenAutoFocus={value.key ? (e) => e.preventDefault() : undefined}>
        <form className="contents" onSubmit={save}>
          <DialogHeader>
            <div className="flex items-center gap-3">
              <ProviderIcon src={m.iconUrl} name={label || '?'} className="size-10" />
              <div className="min-w-0">
                <DialogTitle className="truncate">{isNew ? (value.key ? `Add ${value.name || value.key}` : 'New custom provider') : `Edit ${value.name || value.key}`}</DialogTitle>
                <DialogDescription className="mt-0.5">Endpoints, scopes, authorization parameters and the OAuth app credentials.</DialogDescription>
              </div>
            </div>
          </DialogHeader>
          <DialogBody className="space-y-7">
            {isNew && (
              <FormSection title="Discover">
                <FormField label="OIDC issuer URL" hint="Fills the endpoints and scopes from the issuer's discovery document.">
                  <div className="flex flex-col gap-2 sm:flex-row">
                    <Input
                      value={issuer}
                      onChange={(e) => setIssuer(e.target.value)}
                      onKeyDown={(e) => {
                        // Enter here means "discover", not "save the half-filled provider".
                        if (e.key === 'Enter') {
                          e.preventDefault()
                          void discover()
                        }
                      }}
                      placeholder="https://issuer.example.com"
                      className="font-mono"
                      autoFocus={!value.key}
                    />
                    <Button variant="secondary" className="shrink-0" onClick={discover} disabled={!issuer.trim() || discovering}>
                      <Search /> {discovering ? 'Discovering…' : 'Discover'}
                    </Button>
                  </div>
                </FormField>
              </FormSection>
            )}

            <FormSection title="Provider">
              <div className="grid gap-4 sm:grid-cols-2">
                <FormField label="Slug" required hint={isNew ? 'Letters, digits, - or _. Agents name the provider by this.' : 'Agents name the provider by this, so it cannot change.'}>
                  <Input value={m.key} onChange={(e) => set({ key: e.target.value })} readOnly={!isNew} placeholder="github" className="font-mono" />
                </FormField>
                <FormField label="Name">
                  <Input value={m.name ?? ''} onChange={(e) => set({ name: e.target.value })} placeholder="GitHub" />
                </FormField>
                <FormField label="Icon URL">
                  <Input value={m.iconUrl ?? ''} onChange={(e) => set({ iconUrl: e.target.value })} placeholder="https://…/logo.svg" className="font-mono" />
                </FormField>
                <FormField label="Docs URL" hint="Linked from Connect, beside the scope list.">
                  <Input value={m.docsUrl ?? ''} onChange={(e) => set({ docsUrl: e.target.value })} placeholder="https://docs.example.com/scopes" className="font-mono" />
                </FormField>
                <FormField label="Authorization endpoint" className="sm:col-span-2">
                  <Input value={m.authorizationEndpoint ?? ''} onChange={(e) => set({ authorizationEndpoint: e.target.value })} className="font-mono" />
                </FormField>
                <FormField label="Token endpoint" className="sm:col-span-2">
                  <Input value={m.tokenEndpoint ?? ''} onChange={(e) => set({ tokenEndpoint: e.target.value })} className="font-mono" />
                </FormField>
                <FormField label="Userinfo endpoint" className="sm:col-span-2">
                  <Input value={m.userinfoEndpoint ?? ''} onChange={(e) => set({ userinfoEndpoint: e.target.value })} className="font-mono" />
                </FormField>
                <FormField label="Token endpoint authentication">
                  <Select value={m.tokenEndpointAuthMethod || 'client_secret_post'} onValueChange={(v) => set({ tokenEndpointAuthMethod: v as TokenEndpointAuthMethod })}>
                    <SelectTrigger aria-label="Token endpoint authentication">
                      <SelectValue />
                    </SelectTrigger>
                    <SelectContent>
                      <SelectItem value="client_secret_post">Client secret in the request body</SelectItem>
                      <SelectItem value="client_secret_basic">HTTP Basic</SelectItem>
                    </SelectContent>
                  </Select>
                </FormField>
                <FormField label="Account field" hint="Path into the userinfo response that names the account, e.g. data.username.">
                  <Input value={m.userinfoAccountPath ?? ''} onChange={(e) => set({ userinfoAccountPath: e.target.value })} placeholder="email" className="font-mono" />
                </FormField>
              </div>
            </FormSection>

            <FormSection
              title="OAuth app credentials"
              actions={
                config && (
                  <Button variant="ghost" size="sm" className="text-danger hover:text-danger" disabled={busy} onClick={removeCredentials}>
                    <Trash2 /> Remove credentials
                  </Button>
                )
              }
            >
              <div className="grid gap-4 sm:grid-cols-2">
                <FormField label="Client ID" hint={config ? 'Leave blank to keep the stored credentials untouched.' : undefined}>
                  <Input value={clientId} onChange={(e) => setClientId(e.target.value)} placeholder={config?.clientIdMasked} autoComplete="off" className="font-mono" />
                </FormField>
                <FormField label="Client secret">
                  <Input
                    type="password"
                    value={clientSecret}
                    onChange={(e) => setClientSecret(e.target.value)}
                    placeholder={config ? 'Blank keeps the stored secret' : undefined}
                    autoComplete="new-password"
                    className="font-mono"
                  />
                </FormField>
                <FormField
                  label="Redirect URI"
                  className="sm:col-span-2"
                  hint={
                    <>
                      Allow-list this exact URL in the provider's OAuth app settings. It is the keyring's public address followed by{' '}
                      <code className="font-mono">{CALLBACK_PATH}</code>.
                    </>
                  }
                >
                  <div className="flex items-center gap-2">
                    <Input value={redirect} onChange={(e) => setRedirect(e.target.value)} placeholder={knownRedirect ?? `https://…${CALLBACK_PATH}`} className="font-mono" />
                    {(redirect || knownRedirect) && <CopyButton value={redirect || knownRedirect || ''} label="Copy redirect URI" />}
                  </div>
                </FormField>
              </div>
            </FormSection>

            <FormSection
              title="Scopes"
              actions={
                <Button variant="secondary" size="sm" onClick={() => setScopes((rows) => [...rows, { value: '', description: '', category: '', sensitive: false }])}>
                  <Plus /> Add scope
                </Button>
              }
            >
              {scopes.length === 0 ? (
                <p className="text-xs leading-5 text-muted-foreground">No scopes yet. Add the scopes this provider supports so they can be picked on Connect.</p>
              ) : (
                <div className="space-y-2">
                  {scopes.map((s, i) => (
                    <div key={i} className="grid gap-2 rounded-lg border border-border p-2 sm:grid-cols-[minmax(0,1fr)_minmax(0,1fr)_minmax(0,0.7fr)_auto_auto] sm:items-center">
                      <Input value={s.value} onChange={(e) => setScope(i, { value: e.target.value })} placeholder="scope, e.g. repo" className="h-9 font-mono text-xs" aria-label="Scope value" />
                      <Input value={s.description || ''} onChange={(e) => setScope(i, { description: e.target.value })} placeholder="Description shown on Connect" className="h-9" aria-label="Scope description" />
                      <Input value={s.category || ''} onChange={(e) => setScope(i, { category: e.target.value })} placeholder="Category" className="h-9" aria-label="Scope category" />
                      <label className="flex cursor-pointer items-center gap-2 whitespace-nowrap px-1 text-xs text-muted-foreground">
                        <Checkbox checked={!!s.sensitive} onCheckedChange={(v) => setScope(i, { sensitive: v === true })} /> Sensitive
                      </label>
                      <Button variant="ghost" size="iconSm" className="hover:text-danger" aria-label="Remove scope" title="Remove scope" onClick={() => setScopes((rows) => rows.filter((_, j) => j !== i))}>
                        <Trash2 />
                      </Button>
                    </div>
                  ))}
                </div>
              )}
            </FormSection>

            <FormSection
              title="Authorization parameters"
              actions={
                <Button variant="secondary" size="sm" onClick={() => setAuthParams((rows) => [...rows, { key: '', value: '' }])}>
                  <Plus /> Add parameter
                </Button>
              }
            >
              <p className="text-xs leading-5 text-muted-foreground">
                Extra query parameters on the authorize URL. <code className="font-mono text-foreground">access_type=offline</code> (Google) or{' '}
                <code className="font-mono text-foreground">token_access_type=offline</code> (Dropbox) ask for a refresh token.
              </p>
              {authParams.map((p, i) => (
                <div key={i} className="flex items-center gap-2">
                  <Input value={p.key} onChange={(e) => setParam(i, { key: e.target.value })} placeholder="parameter" className="h-9 flex-1 font-mono text-xs" aria-label="Parameter name" />
                  <span className="font-mono text-muted-foreground">=</span>
                  <Input value={p.value} onChange={(e) => setParam(i, { value: e.target.value })} placeholder="value" className="h-9 flex-1 font-mono text-xs" aria-label="Parameter value" />
                  <Button variant="ghost" size="iconSm" className="hover:text-danger" aria-label="Remove parameter" title="Remove parameter" onClick={() => setAuthParams((rows) => rows.filter((_, j) => j !== i))}>
                    <Trash2 />
                  </Button>
                </div>
              ))}
            </FormSection>

            {error && <Callout tone="danger">{error}</Callout>}
          </DialogBody>
          <DialogFooter>
            <Button variant="outline" type="button" disabled={busy} onClick={onClose}>
              Cancel
            </Button>
            <Button variant="primary" type="submit" disabled={busy}>
              {busy ? 'Saving…' : isNew ? 'Add provider' : 'Save changes'}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
