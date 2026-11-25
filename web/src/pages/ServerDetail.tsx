import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { Link, useNavigate, useParams, useSearchParams } from 'react-router-dom'
import { toast } from 'sonner'
import { Activity, ArrowLeft, KeyRound, ListTree, Pencil, RefreshCw, Save, Telescope, Trash2 } from 'lucide-react'
import { Button } from '../ui/components/button'
import { Badge } from '../ui/components/badge'
import { Switch } from '../ui/components/switch'
import { PageHeader } from '../components/PageHeader'
import { Definition, DefinitionList } from '../components/DefinitionList'
import { StatusPill } from '../components/StatusPill'
import { Callout } from '../components/Callout'
import { CopyButton } from '../components/CopyButton'
import { ConfirmDialog } from '../components/ConfirmDialog'
import { Tabs, TabPanel, type Tab } from '../components/Tabs'
import { CatalogCard } from '../components/servers/CatalogCard'
import { GrantsEditor } from '../components/servers/GrantsEditor'
import { KindSection } from '../components/servers/SelectionEditor'
import { ServerDialog } from '../components/servers/ServerDialog'
import { CredentialDialog } from '../components/servers/CredentialDialog'
import {
  authKindBadge,
  authKindLabel,
  credentialStatus,
  heldEntryName,
  heldForServer,
  isKeyringKind,
  keyringCredentialName,
  keyringOwnerPhrase,
  notMoved,
  notMovedDetail,
} from '../components/servers/credential'
import { useMe } from '../me'
import { HookRules, type RuleText } from '../components/rules/HookRules'
import { KINDS, selectionDetail, selectionSummary } from '../components/rules/selection'
import {
  api,
  ApiError,
  errorMessage,
  type CapabilityDTO,
  type CapabilityKind,
  type CreateServerResult,
  type GrantInput,
  type HooksSchema,
  type RuleProblem,
  type Selection,
  type ServerDTO,
  type UserDTO,
} from '../api'
import { ServerProbeSheet, type ServerProbe } from '../components/servers/ServerProbeSheet'
import { absoluteTime, relativeTime } from '../lib'

/** The page's tabs: the server itself, who holds it, one per kind of capability, then the rules. */
type PageTab = 'overview' | 'access' | CapabilityKind | 'rules'
const TAB_KEYS: PageTab[] = ['overview', 'access', ...KINDS.map((k) => k.kind), 'rules']

/** Per kind: null is everything of that kind (key absent), an array is only what it names. */
type DraftSelection = Record<keyof Selection, string[] | null>

/** What the Save button sends: the selection and rules (PATCH), and the grants (PUT). */
interface Draft {
  /** Resource lists hold URI patterns, including exact URIs picked from the seen resources. */
  sel: DraftSelection
  /** The rules as text, by hook property: the form the API takes. Only hooks that hold text have a key. */
  text: RuleText
  grants: GrantInput[]
}

function toDraft(s: ServerDTO): Draft {
  const sel = s.selection ?? {}
  return {
    sel: { tools: sel.tools ?? null, prompts: sel.prompts ?? null, resources: sel.resources ?? null, templates: sel.templates ?? null },
    text: cleanText((s.hooks ?? {}) as Record<string, string | null>),
    grants: (s.grants ?? []).map((g) => ({ userId: g.userId })),
  }
}

function problemsFromError(e: unknown): RuleProblem[] | undefined {
  if (!(e instanceof ApiError) || e.status !== 400) return undefined
  const body = e.body as { problems?: RuleProblem[] } | undefined
  return Array.isArray(body?.problems) ? body.problems : undefined
}

/** Only hooks that hold something are sent and compared; a hook with no text has no rules. */
function cleanText(text: Record<string, string | null | undefined>): RuleText {
  return Object.fromEntries(Object.entries(text).filter((e): e is [string, string] => typeof e[1] === 'string' && e[1].trim() !== ''))
}

/** Only the narrowed kinds become keys; an "Only selected" kind with nothing ticked is []. */
function toSelection(d: Draft): Selection {
  const out: Selection = {}
  for (const k of KINDS) {
    const list = d.sel[k.key]
    if (!list) continue
    out[k.key] = k.key === 'resources' ? list.map((r) => r.trim()).filter(Boolean) : list
  }
  return out
}

const sameSet = (a: string[], b: string[]) => a.length === b.length && [...a].sort().join('\n') === [...b].sort().join('\n')

function sameList(a: string[] | null, b: string[] | null, trim = false): boolean {
  if (!a || !b) return a === b
  return trim ? sameSet(a.filter((r) => r.trim()), b.filter((r) => r.trim())) : sameSet(a, b)
}

/** The selection or rules differ: a PATCH is due. */
function configDirty(a: Draft, b: Draft): boolean {
  return (
    KINDS.some((k) => !sameList(a.sel[k.key], b.sel[k.key], k.key === 'resources')) ||
    JSON.stringify(Object.entries(cleanText(a.text)).sort()) !== JSON.stringify(Object.entries(cleanText(b.text)).sort())
  )
}

/** The grants differ: a PUT is due. */
const grantsDirty = (a: Draft, b: Draft) => !sameSet(
    a.grants.map((g) => g.userId),
    b.grants.map((g) => g.userId),
  )

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
  const [draft, setDraft] = useState<Draft>()
  const [error, setError] = useState<string>()
  const [loaded, setLoaded] = useState(false)
  const [editing, setEditing] = useState(false)
  const [replacing, setReplacing] = useState(false)
  const [deleting, setDeleting] = useState(false)
  const [deleteBusy, setDeleteBusy] = useState(false)
  const [deleteErr, setDeleteErr] = useState<string>()
  const [probe, setProbe] = useState<ServerProbe>()
  useEffect(() => setProbe(undefined), [id])
  const [reauthBusy, setReauthBusy] = useState(false)
  const [caps, setCaps] = useState<CapabilityDTO[]>([])
  const [capsLoaded, setCapsLoaded] = useState(false)
  const [users, setUsers] = useState<UserDTO[]>([])
  const { userId } = useMe()
  const [saving, setSaving] = useState(false)
  const [saveError, setSaveError] = useState<string>()
  const [problems, setProblems] = useState<RuleProblem[]>([])
  const [warnings, setWarnings] = useState<RuleProblem[]>([])
  // Whether the last check you asked for found any rules; undefined once the text changes.
  const [checked, setChecked] = useState<boolean>()
  const [validating, setValidating] = useState(false)
  // Only the latest validation may land; an older one racing it would show stale problems.
  const validateSeq = useRef(0)
  const [schema, setSchema] = useState<HooksSchema>()
  const [schemaError, setSchemaError] = useState<string>()

  const tabParam = params.get('tab') as PageTab | null
  const tab: PageTab = tabParam && TAB_KEYS.includes(tabParam) ? tabParam : 'overview'
  const setTab = useCallback(
    (t: PageTab) =>
      setParams(
        (prev) => {
          const next = new URLSearchParams(prev)
          if (t === 'overview') next.delete('tab')
          else next.set('tab', t)
          return next
        },
        { replace: true },
      ),
    [setParams],
  )
  // setParams changes with the query, so the load effect reaches the latest setTab through a ref.
  const setTabRef = useRef(setTab)
  setTabRef.current = setTab

  // The server's catalog by kind, for the check that a rule names something the server has.
  const catalog = useMemo(() => caps.reduce<Record<string, string[]>>((by, c) => ({ ...by, [c.kind]: [...(by[c.kind] ?? []), c.name] }), {}), [caps])
  const catalogRef = useRef(catalog)
  catalogRef.current = catalog

  // A refresh (after a ping, a toggle, an edit) replaces the draft only when it holds no unsaved changes.
  const serverRef = useRef(server)
  serverRef.current = server
  const draftRef = useRef(draft)
  draftRef.current = draft
  const adopt = useCallback((s: ServerDTO) => {
    const prev = serverRef.current
    const d = draftRef.current
    setServer(s)
    if (!d || !prev || (!configDirty(d, toDraft(prev)) && !grantsDirty(d, toDraft(prev)))) setDraft(toDraft(s))
  }, [])

  /**
   * POST /rules/validate: the engine's compiler, then the sanity checks. Returns true when the text
   * compiles; warnings do not count against it. `asked` marks a check you asked for.
   */
  const validate = useCallback(async (text: RuleText, asked = false): Promise<boolean> => {
    const seq = ++validateSeq.current
    const sent = cleanText(text)
    if (!Object.keys(sent).length) {
      setProblems([])
      setWarnings([])
      if (asked) setChecked(false)
      return true
    }
    setValidating(true)
    try {
      const r = await api.rules.validate(sent, catalogRef.current)
      if (seq === validateSeq.current) {
        setProblems(r.problems ?? [])
        setWarnings(r.warnings ?? [])
        if (asked) setChecked(r.hasRules)
      }
      return !(r.problems ?? []).length
    } catch (e) {
      // A failed validation call should not block editing; the save will report compile errors.
      toast.error('Could not check the rules', { description: errorMessage(e) })
      return true
    } finally {
      if (seq === validateSeq.current) setValidating(false)
    }
  }, [])

  const loadSchema = useCallback(async () => {
    try {
      setSchema(await api.rules.hooksSchema())
      setSchemaError(undefined)
    } catch (err) {
      setSchemaError(errorMessage(err))
    }
  }, [])

  const load = useCallback(
    () =>
      api.servers
        .get(id)
        .then((s) => {
          adopt(s)
          setError(undefined)
          return s
        })
        .catch((e) => {
          setError(errorMessage(e))
          return undefined
        })
        .finally(() => setLoaded(true)),
    [id, adopt],
  )
  const loadCaps = useCallback(
    () =>
      api.capabilities
        .list(id)
        .then(setCaps)
        .catch((e) => toast.error('Could not load the catalog', { description: errorMessage(e) }))
        .finally(() => setCapsLoaded(true)),
    [id],
  )
  useEffect(() => {
    void (async () => {
      const [s] = await Promise.all([load(), loadCaps()])
      // Stored rules that cannot be used are the thing to fix: open on them, each problem on its line.
      if (s?.rulesError) {
        const text = cleanText((s.hooks ?? {}) as Record<string, string | null>)
        if (Object.keys(text).length) void validate(text)
        setTabRef.current('rules')
      }
    })()
    void loadSchema()
    api.users
      .list()
      .then(setUsers)
      .catch((e) => toast.error('Could not load users', { description: errorMessage(e) }))
    // Only on arriving at a server: every dependency changes with the id alone.
  }, [load, loadCaps, loadSchema, validate])

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

  const base = useMemo(() => (server ? toDraft(server) : undefined), [server])
  // Unusable stored rules are rewritten by any save, including one that leaves the list as shown.
  const needsPatch = !!server && !!draft && !!base && (configDirty(draft, base) || !!server.rulesError)
  const needsGrants = !!draft && !!base && grantsDirty(draft, base)
  const dirty = needsPatch || needsGrants
  const hasProblems = problems.length > 0

  // Ping and discover talk to the upstream, which can grow the catalog: refresh it afterwards.
  const afterProbe = () => {
    void loadCaps()
    void load()
  }

  const toggleEnabled = async () => {
    if (!server) return
    try {
      const updated = await api.servers.update(server.id, { enabled: !server.enabled })
      adopt(updated)
      toast.success(`${updated.enabled ? 'Enabled' : 'Disabled'} ${updated.name}`)
    } catch (e) {
      toast.error('Could not update server', { description: errorMessage(e) })
    }
  }

  const doDiscover = () => server && setProbe({ server, kind: 'discover' })
  const doPing = () => server && setProbe({ server, kind: 'ping' })

  const followAuthorize = (result: CreateServerResult) => {
    setReplacing(false)
    adopt(result.server)
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

  if (!server || !draft) {
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

  const set = (patch: Partial<Draft>) => setDraft({ ...draft, ...patch })
  const setText = (text: RuleText) => {
    // A hook whose text changed is unchecked until the next blur or save. Problems name the hook; the text is kept by its property.
    const moved = Object.keys({ ...draft.text, ...text }).filter((prop) => (draft.text[prop] ?? '') !== (text[prop] ?? ''))
    const changed = new Set(moved.map((prop) => schema?.hooks.find((h) => h.property === prop)?.name ?? prop))
    if (problems.some((p) => changed.has(p.hook))) setProblems(problems.filter((p) => !changed.has(p.hook)))
    if (warnings.some((p) => changed.has(p.hook))) setWarnings(warnings.filter((p) => !changed.has(p.hook)))
    if (changed.size) setChecked(undefined)
    set({ text })
  }

  const save = async () => {
    setSaving(true)
    setSaveError(undefined)
    const text = cleanText(draft.text)
    if (needsPatch && !(await validate(text))) {
      setSaving(false)
      setTab('rules')
      setSaveError('Some rules do not compile. Fix the lines marked on the Rules tab, then save.')
      return
    }
    try {
      let updated = server
      if (needsPatch) {
        updated = await api.servers.update(server.id, { selection: toSelection(draft), hooks: text })
        setServer(updated)
      }
      if (needsGrants) updated = await api.servers.putGrants(server.id, draft.grants)
      setServer(updated)
      setDraft(toDraft(updated))
      toast.success(`Saved ${updated.slug}`)
    } catch (e) {
      const p = problemsFromError(e)
      if (p) setProblems(p)
      setSaveError(errorMessage(e))
      toast.error('Could not save', { description: errorMessage(e) })
    } finally {
      setSaving(false)
    }
  }

  const cred = server.credential
  const status = credentialStatus(server)
  const oauth = server.authKind === 'oauth'
  const keyringOwner = cred && isKeyringKind(cred.kind) ? keyringOwnerPhrase(cred, userId, users) : undefined
  const rulesBroken = hasProblems || !!server.rulesError
  const tabs: Tab<PageTab>[] = [
    { key: 'overview', label: 'Overview' },
    { key: 'access', label: 'Access', count: draft.grants.length },
    ...KINDS.map((k) => {
      const list = draft.sel[k.key]
      const seen = caps.filter((c) => c.kind === k.kind).length
      return { key: k.kind, label: k.label, count: list === null ? seen : list.filter((n) => n.trim()).length }
    }),
    {
      key: 'rules',
      label: 'Rules',
      // A dot says the server has rules; rules are not counted.
      icon: rulesBroken ? (
        <span className="size-1.5 rounded-full bg-danger" aria-label="Rules have problems" />
      ) : Object.keys(cleanText(draft.text)).length ? (
        <span className="size-1.5 rounded-full bg-accent" aria-label="Has rules" />
      ) : undefined,
    },
  ]
  const kind = KINDS.find((k) => k.kind === tab)
  const saveButton = (
    <Button variant="primary" onClick={save} disabled={saving || !dirty || hasProblems}>
      <Save /> {saving ? 'Saving…' : 'Save'}
    </Button>
  )

  return (
    <div className="space-y-6">
      <Button variant="link" asChild>
        <Link to="/servers">
          <ArrowLeft /> Servers
        </Link>
      </Button>
      <PageHeader
        eyebrow="Server"
        title={server.name}
        description={server.description || 'No description.'}
        actions={
          <>
            <Button variant="secondary" onClick={doPing}>
              <Activity /> Ping
            </Button>
            <Button variant="secondary" onClick={doDiscover}>
              <Telescope /> Discover
            </Button>
            <Button variant="secondary" asChild>
              <Link to={`/explorer?route=${encodeURIComponent(server.slug)}`}>
                <ListTree /> Open in Explorer
              </Link>
            </Button>
            <Button variant="secondary" onClick={() => setEditing(true)}>
              <Pencil /> Edit
            </Button>
            <Button variant="danger" onClick={() => setDeleting(true)}>
              <Trash2 /> Delete
            </Button>
            {(tab !== 'overview' || dirty) && saveButton}
          </>
        }
      />

      {error && <Callout tone="danger">{error}</Callout>}
      {saveError && <Callout tone="danger">{saveError}</Callout>}
      <ServerProbeSheet probe={probe?.server.id === id ? probe : undefined} onClose={() => setProbe(undefined)} onComplete={afterProbe} />

      <Tabs tabs={tabs} value={tab} onChange={setTab} label="Server" className="flex-wrap" />

      {tab === 'overview' && (
        <TabPanel tabKey="overview" className="space-y-6">
          {notMoved(cred) && (
            <Callout tone="danger">
              <span className="font-medium">Credential not moved to the keyring.</span> It is still in the gateway's old store, which is no longer read, so requests to{' '}
              {server.name} fail. An operator moves it with <code className="break-words font-mono text-xs">gateway migrate-credentials</code>. Replacing the credential
              here works too.
            </Callout>
          )}
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
            <Definition label="MCP URL" mono>
              <span className="flex min-w-0 items-center gap-1">
                <span className="break-all">{server.mcpUrl}</span>
                <CopyButton value={server.mcpUrl} label="Copy MCP URL" />
              </span>
            </Definition>
            <Definition label="Slug" mono>
              <span className="flex min-w-0 items-center gap-1">
                <span>{server.slug}</span>
                <CopyButton value={server.slug} label="Copy slug" />
                <span className="ml-2 font-sans text-faint-foreground">Tools appear as {server.slug}__tool on /mcp.</span>
              </span>
            </Definition>
            <Definition label="Upstream" mono>
              <span className="break-all">{server.url}</span>
            </Definition>
            <Definition label="Credential">
              <div className="flex flex-wrap items-center gap-2">
                <Badge mono variant={authKindBadge[server.authKind]}>
                  {authKindLabel(server.authKind)}
                </Badge>
                <StatusPill tone={status.tone}>
                  <span title={status.title}>{status.label}</span>
                </StatusPill>
                <CredentialSummary cred={cred} keyringOwner={keyringOwner} />
                <Button variant="link" size="sm" onClick={() => setReplacing(true)}>
                  <KeyRound /> {server.authKind === 'none' ? 'Set credential' : 'Replace'}
                </Button>
                {oauth && (
                  <Button variant="link" size="sm" onClick={reauthorize} disabled={reauthBusy}>
                    <RefreshCw /> {reauthBusy ? 'Starting…' : 'Re-authorize'}
                  </Button>
                )}
              </div>
            </Definition>
          </DefinitionList>

          <section className="space-y-3">
            <SectionTitle title="Catalog" hint="What the gateway has seen on this server. The Tools, Prompts, Resources and Templates tabs choose from it." />
            <CatalogCard
              caps={caps}
              loaded={capsLoaded}
              seedActions={
                <Button variant="secondary" onClick={doPing}>
                  <Activity /> Ping
                </Button>
              }
            />
          </section>

          <details className="group text-sm">
            <summary className="cursor-pointer select-none text-xs font-semibold uppercase tracking-[0.16em] text-muted-foreground hover:text-foreground">
              Details
            </summary>
            <DefinitionList className="mt-3">
              {cred && !isKeyringKind(cred.kind) && heldForServer(cred) && (
                <Definition label="Kept in">
                  <span>The keyring</span>
                  {heldEntryName(cred) && <span className="ml-2 break-all font-mono text-xs text-muted-foreground">{heldEntryName(cred)}</span>}
                  <span className="mt-0.5 block text-faint-foreground">Held for this server, in nobody's own keyring. It cannot be revealed.</span>
                </Definition>
              )}
              {notMoved(cred) && (
                <Definition label="Kept in">
                  <span>The gateway's old store</span>
                  <span className="mt-0.5 block text-faint-foreground">Not read any more. The credential works once it is in the keyring.</span>
                </Definition>
              )}
              {cred && isKeyringKind(cred.kind) && (
                <>
                  <Definition label="Keyring credential" mono>
                    <span className="break-all">{keyringCredentialName(cred) || '—'}</span>
                  </Definition>
                  <Definition label="Held in">
                    <span>{keyringOwner}</span>
                    <span className="ml-2 text-faint-foreground">The gateway stores this name and asks the keyring for the value on each request.</span>
                  </Definition>
                </>
              )}
              {/* The API says nothing about an OAuth connection that is not in the keyring. */}
              {cred?.kind === 'oauth' && !notMoved(cred) && (
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
              <Definition label="Generation" mono>
                {server.generation}
              </Definition>
              <Definition label="ID" mono>
                {server.id}
              </Definition>
            </DefinitionList>
          </details>
        </TabPanel>
      )}

      {tab === 'access' && (
        <TabPanel tabKey="access">
          <GrantsEditor grants={draft.grants} onChange={(grants) => set({ grants })} users={users} />
        </TabPanel>
      )}

      {kind && (
        <TabPanel tabKey={kind.kind} className="space-y-3">
          <div className="flex flex-wrap items-end justify-between gap-3">
            <p className="max-w-2xl text-xs text-muted-foreground">
              The selection decides both what is listed and what can be called. Each kind is set on its own, then the rules run over the result.
            </p>
            <span className="text-xs text-muted-foreground" title={selectionDetail(toSelection(draft))}>
              {selectionSummary(toSelection(draft))}
            </span>
          </div>
          <KindSection
            kind={kind}
            list={draft.sel[kind.key]}
            caps={caps.filter((c) => c.kind === kind.kind)}
            onChange={(l) => set({ sel: { ...draft.sel, [kind.key]: l } })}
          />
        </TabPanel>
      )}

      {tab === 'rules' && (
        <TabPanel tabKey="rules">
          <HookRules
            schema={schema}
            schemaError={schemaError}
            onReloadSchema={() => void loadSchema()}
            text={draft.text}
            onChange={setText}
            problems={problems}
            warnings={warnings}
            validating={validating}
            onValidate={() => void validate(draft.text)}
            onCheck={() => void validate(draft.text, true)}
            checked={checked}
            rulesError={server.rulesError}
            slug={server.slug}
            caps={caps}
          />
        </TabPanel>
      )}

      {tab !== 'overview' && (
        <div className="flex items-center justify-end gap-3">
          {hasProblems ? (
            <span className="text-xs text-danger">Fix the rule problems to save</span>
          ) : (
            dirty && <span className="text-xs text-muted-foreground">Unsaved changes</span>
          )}
          {saveButton}
        </div>
      )}

      <ServerDialog
        open={editing}
        server={server}
        onClose={() => setEditing(false)}
        onUpdated={(updated) => {
          setEditing(false)
          adopt(updated)
          toast.success(`Saved ${updated.name}`)
        }}
      />
      <CredentialDialog open={replacing} server={server} users={users} onClose={() => setReplacing(false)} onSaved={followAuthorize} />
      <ConfirmDialog
        open={deleting}
        title={`Delete ${server.name}?`}
        description="The server, its credential, rules and grants are removed. Agents using its slug or MCP URL will start failing."
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

function SectionTitle({ title, hint }: { title: string; hint: string }) {
  return (
    <div>
      <h2 className="text-base font-semibold tracking-tight">{title}</h2>
      <p className="mt-0.5 text-xs text-muted-foreground">{hint}</p>
    </div>
  )
}

/**
 * One muted phrase describing the credential: who/what header, scopes + expiry for OAuth, or the
 * name of the keyring credential and whose keyring it is in.
 */
function CredentialSummary({ cred, keyringOwner }: { cred?: ServerDTO['credential']; keyringOwner?: string }) {
  if (!cred) return null
  const parts: string[] = []
  if (isKeyringKind(cred.kind)) {
    const name = keyringCredentialName(cred)
    if (name) parts.push(name)
    if (keyringOwner) parts.push(`from ${keyringOwner}`)
  }
  if (cred.kind === 'basic' && cred.basicUsername) parts.push(`user ${cred.basicUsername}`)
  if (cred.kind === 'header' && cred.headerName) parts.push(cred.headerName)
  if (cred.kind === 'oauth') {
    if (cred.oauthScopes.length) parts.push(cred.oauthScopes.join(' '))
    if (cred.oauthExpiresAt) parts.push(`${new Date(cred.oauthExpiresAt).getTime() < Date.now() ? 'expired' : 'expires'} ${relativeTime(cred.oauthExpiresAt)}`)
    if (!cred.oauthHasRefreshToken && cred.state === 'ok') parts.push('no refresh token')
  }
  // A credential the server owns itself says where it is kept; one lent from a keyring already does.
  if (!isKeyringKind(cred.kind) && cred.store === 'keyring') parts.push('kept in the keyring')
  // The notice and the pill already say an unmoved credential is not moved: keep only what the reason adds.
  const reason = notMoved(cred) ? notMovedDetail(cred) : cred.stateReason
  if (reason) parts.push(reason)
  if (!parts.length) return null
  return <span className="font-mono text-xs text-muted-foreground">{parts.join(' · ')}</span>
}
