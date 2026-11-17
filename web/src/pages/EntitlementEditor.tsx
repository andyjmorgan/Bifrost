import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { Link, useNavigate, useParams } from 'react-router-dom'
import { toast } from 'sonner'
import { ArrowLeft, Check, Plus, Save, Trash2, UserRound, X } from 'lucide-react'
import { Button } from '../ui/components/button'
import { Input } from '../ui/components/input'
import { Textarea } from '../ui/components/textarea'
import { Checkbox } from '../ui/components/checkbox'
import { Badge } from '../ui/components/badge'
import { PageHeader } from '../components/PageHeader'
import { FormField } from '../components/FormField'
import { Callout } from '../components/Callout'
import { CopyButton } from '../components/CopyButton'
import { ConfirmDialog } from '../components/ConfirmDialog'
import { ListBar, ListCard, ListEmpty, ListRows } from '../components/ListCard'
import { KINDS, selectionDetail, selectionSummary } from '../components/entitlements/selection'
import { RulesStages, type RuleText } from '../components/entitlements/RulesStages'
import { Tabs, TabPanel, type Tab } from '../components/Tabs'
import {
  api,
  ApiError,
  errorMessage,
  type CapabilityDTO,
  type CapabilityKind,
  type EntitlementDTO,
  type GrantInput,
  type MemberDTO,
  type OrgRole,
  type RulesSchema,
  type TextProblem,
  type Selection,
} from '../api'
import { cn } from '../lib'

type SelKey = keyof Selection

/** The page's tabs: the entitlement itself, who holds it, one per kind of capability, then the rules. */
type PageTab = 'general' | 'grants' | CapabilityKind | 'rules'

/** Per kind: null is everything of that kind (key absent), an array is only what it names. */
type DraftSelection = Record<SelKey, string[] | null>

interface Draft {
  name: string
  description: string
  /** Resource lists hold URI patterns, including exact URIs picked from the seen resources. */
  sel: DraftSelection
  /** The rules as text, by hook property: the form the API takes. Only hooks that hold text have a key. */
  text: RuleText
  grants: GrantInput[]
}

const ROLES: { id: OrgRole; label: string }[] = [
  { id: 'admin', label: 'Admins' },
  { id: 'member', label: 'Members' },
]

function toDraft(e: EntitlementDTO): Draft {
  const s = e.selection ?? {}
  return {
    name: e.name,
    description: e.description,
    sel: {
      tools: s.tools ?? null,
      prompts: s.prompts ?? null,
      resources: s.resources ?? null,
      templates: s.templates ?? null,
    },
    text: cleanText((e.rules ?? {}) as Record<string, string | null>),
    grants: e.grants.map((g) => ({ subjectType: g.subjectType, subjectId: g.subjectId })),
  }
}

function problemsFromError(e: unknown): TextProblem[] | undefined {
  if (!(e instanceof ApiError) || e.status !== 400) return undefined
  const body = e.body as { textProblems?: TextProblem[] } | undefined
  return Array.isArray(body?.textProblems) ? body.textProblems : undefined
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

const grantKey = (g: GrantInput) => `${g.subjectType}:${g.subjectId}`
const sameSet = (a: string[], b: string[]) => a.length === b.length && [...a].sort().join('\n') === [...b].sort().join('\n')

function sameList(a: string[] | null, b: string[] | null, trim = false): boolean {
  if (!a || !b) return a === b
  return trim ? sameSet(a.filter((r) => r.trim()), b.filter((r) => r.trim())) : sameSet(a, b)
}

function isDirty(a: Draft, b: Draft): boolean {
  return (
    a.name !== b.name ||
    a.description !== b.description ||
    KINDS.some((k) => !sameList(a.sel[k.key], b.sel[k.key], k.key === 'resources')) ||
    JSON.stringify(Object.entries(cleanText(a.text)).sort()) !== JSON.stringify(Object.entries(cleanText(b.text)).sort()) ||
    !sameSet(a.grants.map(grantKey), b.grants.map(grantKey))
  )
}

export function EntitlementEditorPage() {
  const { id = '' } = useParams()
  const navigate = useNavigate()
  const [ent, setEnt] = useState<EntitlementDTO>()
  const [draft, setDraft] = useState<Draft>()
  const [caps, setCaps] = useState<CapabilityDTO[]>([])
  const [members, setMembers] = useState<MemberDTO[]>([])
  const [loadError, setLoadError] = useState<string>()
  const [loaded, setLoaded] = useState(false)
  const [saving, setSaving] = useState(false)
  const [saveError, setSaveError] = useState<string>()
  const [deleting, setDeleting] = useState(false)
  const [deleteBusy, setDeleteBusy] = useState(false)
  const [deleteErr, setDeleteErr] = useState<string>()
  const [problems, setProblems] = useState<TextProblem[]>([])
  const [warnings, setWarnings] = useState<TextProblem[]>([])
  // Whether the last check the admin asked for found any rules; undefined once the text changes.
  const [checked, setChecked] = useState<boolean>()
  // The server's catalog by kind, for the check that a rule names something the server has.
  const catalog = useRef<Record<string, string[]>>({})
  const [validating, setValidating] = useState(false)
  // Only the latest validation may land; an older one racing it would show stale problems.
  const validateSeq = useRef(0)
  const [schema, setSchema] = useState<RulesSchema>()
  const [schemaError, setSchemaError] = useState<string>()
  const [tab, setTab] = useState<PageTab>('general')

  /**
   * POST /entitlements/validate: the engine's compiler, then the sanity checks. Returns true when
   * the text compiles; warnings do not count against it. `asked` marks a check the admin asked for.
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
      const r = await api.entitlements.validateRules(sent, catalog.current)
      if (seq === validateSeq.current) {
        setProblems(r.textProblems ?? [])
        setWarnings(r.warnings ?? [])
        if (asked) setChecked(r.hasRules)
      }
      return !(r.textProblems ?? []).length
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
      setSchema(await api.entitlements.rulesSchema())
      setSchemaError(undefined)
    } catch (err) {
      setSchemaError(errorMessage(err))
    }
  }, [])
  useEffect(() => {
    void loadSchema()
  }, [loadSchema])

  const load = useCallback(async () => {
    try {
      const e = await api.entitlements.get(id)
      const d = toDraft(e)
      setEnt(e)
      setDraft(d)
      setLoadError(undefined)
      // Stored rules that do not compile: place each problem on its line.
      if (e.rulesError && Object.keys(d.text).length) void validate(d.text)
      // Stored rules that cannot be used are the thing to fix: open on them.
      if (e.rulesError) setTab('rules')
      const [c, org] = await Promise.all([api.capabilities.list(e.serverId), api.org.get()])
      setCaps(c)
      catalog.current = c.reduce<Record<string, string[]>>((by, cap) => ({ ...by, [cap.kind]: [...(by[cap.kind] ?? []), cap.name] }), {})
      setMembers(org.members)
    } catch (err) {
      setLoadError(errorMessage(err))
    } finally {
      setLoaded(true)
    }
  }, [id, validate])
  useEffect(() => {
    void load()
  }, [load])

  // Unusable stored rules are rewritten by any save, including one that leaves the list as shown.
  const dirty = useMemo(() => !!ent && !!draft && (isDirty(draft, toDraft(ent)) || !!ent.rulesError), [ent, draft])
  const hasProblems = problems.length > 0

  if (!ent || !draft) {
    return (
      <div className="space-y-6">
        <Button variant="link" asChild>
          <Link to="/servers">
            <ArrowLeft /> Servers
          </Link>
        </Button>
        {loadError ? <Callout tone="danger">{loadError}</Callout> : <p className="text-sm text-muted-foreground">{loaded ? 'Entitlement not found.' : 'Loading…'}</p>}
      </div>
    )
  }

  const set = (patch: Partial<Draft>) => setDraft({ ...draft, ...patch })
  const setText = (text: RuleText) => {
    // A hook whose text changed is unchecked until the next blur or save. Problems name the hook; the text is kept by its property.
    const moved = Object.keys({ ...draft.text, ...text }).filter((prop) => (draft.text[prop] ?? '') !== (text[prop] ?? ''))
    const changed = new Set(moved.map((prop) => schema?.stages.find((st) => st.property === prop)?.id ?? prop))
    if (problems.some((p) => changed.has(p.stage))) setProblems(problems.filter((p) => !changed.has(p.stage)))
    if (warnings.some((p) => changed.has(p.stage))) setWarnings(warnings.filter((p) => !changed.has(p.stage)))
    if (changed.size) setChecked(undefined)
    set({ text })
  }

  const save = async () => {
    const name = draft.name.trim()
    if (!name) return setSaveError('Name is required.')
    setSaving(true)
    setSaveError(undefined)
    const text = cleanText(draft.text)
    if (!(await validate(text))) {
      setSaving(false)
      setTab('rules')
      setSaveError('Some rules do not compile. Fix the lines marked on the Rules tab, then save.')
      return
    }
    try {
      await api.entitlements.update(ent.id, { name, description: draft.description.trim(), selection: toSelection(draft), rules: text })
      const updated = await api.entitlements.putGrants(ent.id, draft.grants)
      setEnt(updated)
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

  const remove = async () => {
    setDeleteBusy(true)
    setDeleteErr(undefined)
    try {
      await api.entitlements.remove(ent.id)
      toast.success(`Deleted ${ent.slug}`)
      navigate(`/servers/${ent.serverId}`, { replace: true })
    } catch (e) {
      setDeleteErr(errorMessage(e))
      setDeleteBusy(false)
    }
  }

  const rulesBroken = hasProblems || !!ent.rulesError
  const tabs: Tab<PageTab>[] = [
    { key: 'general', label: 'General' },
    { key: 'grants', label: 'Grants', count: draft.grants.length },
    ...KINDS.map((k) => {
      const list = draft.sel[k.key]
      const seen = caps.filter((c) => c.kind === k.kind).length
      return { key: k.kind, label: k.label, count: list === null ? seen : list.filter((n) => n.trim()).length }
    }),
    {
      key: 'rules',
      label: 'Rules',
      // A dot says the entitlement has rules; rules are not counted.
      icon: rulesBroken ? (
        <span className="size-1.5 rounded-full bg-danger" aria-label="Rules have problems" />
      ) : Object.keys(cleanText(draft.text)).length ? (
        <span className="size-1.5 rounded-full bg-accent" aria-label="Has rules" />
      ) : undefined,
    },
  ]
  const kind = KINDS.find((k) => k.kind === tab)

  return (
    <div className="space-y-6">
      <Button variant="link" asChild>
        <Link to={`/servers/${ent.serverId}`}>
          <ArrowLeft /> {ent.serverName}
        </Link>
      </Button>
      <PageHeader
        eyebrow="Entitlement"
        title={ent.name}
        description={`A grantable view of ${ent.serverName}. Clients see its tools as ${ent.slug}__tool on /mcp, or reach it alone at its MCP URL.`}
        actions={
          <>
            <Button variant="danger" onClick={() => setDeleting(true)}>
              <Trash2 /> Delete
            </Button>
            <Button variant="primary" onClick={save} disabled={saving || !dirty || hasProblems}>
              <Save /> {saving ? 'Saving…' : 'Save'}
            </Button>
          </>
        }
      />

      {saveError && <Callout tone="danger">{saveError}</Callout>}

      <Tabs tabs={tabs} value={tab} onChange={setTab} label="Entitlement" className="flex-wrap" />

      {tab === 'general' && (
        <TabPanel tabKey="general">
          <div className="space-y-4 rounded-2xl border border-border bg-card p-5">
            <div className="grid gap-4 sm:grid-cols-2">
              <FormField label="Name" required>
                <Input value={draft.name} onChange={(e) => set({ name: e.target.value })} />
              </FormField>
              <FormField label="Slug" hint="Fixed at creation.">
                <div className="flex h-10 items-center gap-1">
                  <code className="min-w-0 truncate rounded-md bg-code px-2 py-1 font-mono text-xs text-code-foreground">{ent.slug}</code>
                  <CopyButton value={ent.slug} label="Copy slug" />
                </div>
              </FormField>
            </div>
            <FormField label="MCP URL">
              <div className="flex items-center gap-1">
                <code className="min-w-0 break-all rounded-md bg-code px-2 py-1 font-mono text-xs text-code-foreground">{ent.mcpUrl}</code>
                <CopyButton value={ent.mcpUrl} label="Copy MCP URL" />
              </div>
            </FormField>
            <FormField label="Description">
              <Textarea value={draft.description} onChange={(e) => set({ description: e.target.value })} placeholder="Who this is for" rows={2} />
            </FormField>
          </div>
        </TabPanel>
      )}

      {tab === 'grants' && (
        <TabPanel tabKey="grants">
          <GrantsCard draft={draft} set={set} members={members} />
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
          <RulesStages
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
            rulesError={ent.rulesError}
            slug={ent.slug}
            caps={caps}
          />
        </TabPanel>
      )}

      <div className="flex items-center justify-end gap-3">
        {hasProblems ? (
          <span className="text-xs text-danger">Fix the rule problems to save</span>
        ) : (
          dirty && <span className="text-xs text-muted-foreground">Unsaved changes</span>
        )}
        <Button variant="primary" onClick={save} disabled={saving || !dirty || hasProblems}>
          <Save /> {saving ? 'Saving…' : 'Save'}
        </Button>
      </div>

      <ConfirmDialog
        open={deleting}
        title={`Delete ${ent.slug}?`}
        description="Everyone granted this entitlement loses it, and agents using its slug or MCP URL will start failing."
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

/* ---------- selection ---------- */

function KindSection({
  kind: k,
  list,
  caps,
  onChange,
}: {
  kind: (typeof KINDS)[number]
  list: string[] | null
  caps: CapabilityDTO[]
  onChange: (list: string[] | null) => void
}) {
  const only = list !== null
  const seen = caps.map((c) => c.name)
  const resources = k.kind === 'resource'
  // Resource entries not among the seen URIs are free-text patterns; for other kinds they are names
  // the server no longer offers, shown so they can be cleared.
  const others = (list ?? []).filter((n) => !seen.includes(n))
  const count = (list ?? []).filter((n) => n.trim()).length
  const toggle = (name: string, on: boolean) => onChange(on ? [...(list ?? []), name] : (list ?? []).filter((n) => n !== name))
  const setPatterns = (next: string[]) => onChange([...(list ?? []).filter((r) => seen.includes(r)), ...next])

  return (
    <ListCard>
      <ListBar>
        <span className="text-[10px] font-semibold uppercase tracking-[0.16em]">
          {k.label}
          {only && <span className="ml-1 font-mono normal-case tracking-normal">{count}</span>}
        </span>
        <div className="flex overflow-hidden rounded-lg border border-border" role="group" aria-label={`${k.label} selection`}>
          {(
            [
              [false, 'Everything'],
              [true, 'Only selected'],
            ] as const
          ).map(([v, label]) => (
            <button
              key={label}
              type="button"
              onClick={() => v !== only && onChange(v ? [] : null)}
              aria-pressed={only === v}
              className={cn(
                'h-8 cursor-pointer px-3 text-xs font-medium transition-colors',
                only === v ? 'bg-elevated text-foreground' : 'text-muted-foreground hover:bg-elevated/60 hover:text-foreground',
              )}
            >
              {label}
            </button>
          ))}
        </div>
      </ListBar>
      {!only ? (
        <p className="px-5 py-4 text-xs text-muted-foreground">
          Every {k.one} the server offers is exposed{seen.length ? ` (${seen.length} seen)` : ''}, including ones it adds later.
        </p>
      ) : (
        <>
          {count === 0 && (
            <div className="border-b border-border px-5 py-3">
              <Callout tone="warning">No {k.many} will be exposed.</Callout>
            </div>
          )}
          <CapabilityChecks
            items={caps}
            selected={list}
            extra={resources ? [] : others}
            onToggle={toggle}
            empty={resources ? (others.length ? undefined : 'No resources seen. Add a URI pattern to allow resources by address.') : `No ${k.many} seen on this server.`}
          />
          {resources && (
            <div className="space-y-2 border-t border-border p-4">
              <div className="flex items-center justify-between gap-3">
                <p className="text-xs text-faint-foreground">Patterns: an exact URI, * within one path segment, ** for any suffix.</p>
                <Button variant="secondary" size="sm" onClick={() => setPatterns([...others, ''])}>
                  <Plus /> Add pattern
                </Button>
              </div>
              {others.map((p, i) => (
                <div key={i} className="flex items-center gap-2">
                  <Input
                    value={p}
                    onChange={(e) => setPatterns(others.map((x, j) => (j === i ? e.target.value : x)))}
                    placeholder="file:///docs/**"
                    className="h-9 font-mono text-xs"
                    aria-label={`Resource pattern ${i + 1}`}
                  />
                  <Button variant="ghost" size="iconSm" className="hover:text-danger" aria-label="Remove pattern" onClick={() => setPatterns(others.filter((_, j) => j !== i))}>
                    <X />
                  </Button>
                </div>
              ))}
            </div>
          )}
        </>
      )}
    </ListCard>
  )
}

function CapabilityChecks({
  items,
  selected,
  extra,
  onToggle,
  empty,
}: {
  items: CapabilityDTO[]
  selected: string[]
  /** Selected names the server no longer offers: shown so they can be cleared. */
  extra: string[]
  onToggle: (name: string, on: boolean) => void
  empty?: string
}) {
  if (!items.length && !extra.length) return empty ? <p className="px-5 py-6 text-center text-xs text-muted-foreground">{empty}</p> : null
  return (
    <ListRows>
      {items.map((c) => {
        const id = `cap-${c.kind}-${c.name}`
        return (
          <label key={c.name} htmlFor={id} className="flex cursor-pointer items-center gap-3 px-5 py-2.5 hover:bg-elevated/45">
            <Checkbox id={id} checked={selected.includes(c.name)} onCheckedChange={(v) => onToggle(c.name, v === true)} />
            <span className="min-w-0 flex-1 truncate font-mono text-xs" title={c.name}>
              {c.name}
            </span>
          </label>
        )
      })}
      {extra.map((n) => (
        <label key={n} className="flex cursor-pointer items-center gap-3 px-5 py-2.5 hover:bg-elevated/45">
          <Checkbox checked onCheckedChange={(v) => onToggle(n, v === true)} aria-label={n} />
          <span className="min-w-0 flex-1 truncate font-mono text-xs">{n}</span>
          <span className="shrink-0 text-[11px] text-warning">not seen on the server</span>
        </label>
      ))}
    </ListRows>
  )
}

/* ---------- grants ---------- */

function GrantsCard({ draft, set, members }: { draft: Draft; set: (p: Partial<Draft>) => void; members: MemberDTO[] }) {
  const has = (t: GrantInput['subjectType'], id: string) => draft.grants.some((g) => g.subjectType === t && g.subjectId === id)
  const toggle = (t: GrantInput['subjectType'], id: string) =>
    set({ grants: has(t, id) ? draft.grants.filter((g) => !(g.subjectType === t && g.subjectId === id)) : [...draft.grants, { subjectType: t, subjectId: id }] })
  const known = new Set(members.map((m) => m.userId))
  const orphans = draft.grants.filter((g) => g.subjectType === 'user' && !known.has(g.subjectId))
  const userCount = draft.grants.filter((g) => g.subjectType === 'user').length

  return (
    <section className="space-y-3">
      <p className="max-w-2xl text-xs text-muted-foreground">
        A caller holds the union of their grants. On an exclusive server, holding two entitlements blocks both.
      </p>
      <ListCard>
        <ListBar>
          <span className="flex flex-wrap items-center gap-2">
            <span className="mr-1 text-[10px] font-semibold uppercase tracking-[0.16em]">Roles</span>
            {ROLES.map((r) => {
              const on = has('role', r.id)
              return (
                <button
                  key={r.id}
                  type="button"
                  aria-pressed={on}
                  onClick={() => toggle('role', r.id)}
                  className={cn(
                    'inline-flex h-8 cursor-pointer items-center gap-1.5 rounded-lg border px-3 text-xs font-medium transition-colors',
                    on ? 'border-accent/20 bg-accent/10 text-accent' : 'bg-transparent text-muted-foreground hover:bg-elevated hover:text-foreground',
                  )}
                >
                  {on && <Check className="size-3.5" />}
                  {r.label}
                </button>
              )
            })}
          </span>
          <span>
            {userCount} {userCount === 1 ? 'person' : 'people'}
          </span>
        </ListBar>
        {members.length === 0 && orphans.length === 0 ? (
          <ListEmpty icon={UserRound} title="No members" hint="Invite people from Members to grant them this entitlement directly." />
        ) : (
          <ListRows>
            {members.map((m) => {
              const id = `grant-${m.userId}`
              return (
                <label key={m.userId} htmlFor={id} className="flex cursor-pointer items-center gap-3 px-5 py-3 hover:bg-elevated/45">
                  <Checkbox id={id} checked={has('user', m.userId)} onCheckedChange={() => toggle('user', m.userId)} />
                  <span className="min-w-0 flex-1">
                    <span className="block truncate text-sm font-medium">
                      {m.name || m.email}
                      {m.self && <span className="ml-1.5 text-xs font-normal text-muted-foreground">(you)</span>}
                    </span>
                    <span className="block truncate font-mono text-xs text-muted-foreground">{m.email}</span>
                  </span>
                  <Badge variant={m.role === 'admin' ? 'accent' : 'muted'}>{m.role === 'admin' ? 'Admin' : 'Member'}</Badge>
                </label>
              )
            })}
            {orphans.map((g) => (
              <label key={g.subjectId} className="flex cursor-pointer items-center gap-3 px-5 py-3 hover:bg-elevated/45">
                <Checkbox checked onCheckedChange={() => toggle('user', g.subjectId)} aria-label={g.subjectId} />
                <span className="min-w-0 flex-1 truncate font-mono text-xs">{g.subjectId}</span>
                <span className="shrink-0 text-[11px] text-warning">not in this org</span>
              </label>
            ))}
          </ListRows>
        )}
      </ListCard>
    </section>
  )
}
