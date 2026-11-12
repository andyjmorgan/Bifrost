import { useCallback, useEffect, useMemo, useState } from 'react'
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
import { KINDS, selectionSummary } from '../components/entitlements/selection'
import {
  api,
  errorMessage,
  type CapabilityDTO,
  type CapabilityKind,
  type EntitlementDTO,
  type GrantInput,
  type MemberDTO,
  type OrgRole,
  type Selection,
} from '../api'
import { cn } from '../lib'

interface Draft {
  name: string
  description: string
  all: boolean
  tools: string[]
  prompts: string[]
  /** URI patterns, including exact URIs picked from the seen resources. */
  resources: string[]
  templates: string[]
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
    all: !!s.all,
    tools: s.tools ?? [],
    prompts: s.prompts ?? [],
    resources: s.resources ?? [],
    templates: s.templates ?? [],
    grants: e.grants.map((g) => ({ subjectType: g.subjectType, subjectId: g.subjectId })),
  }
}

function toSelection(d: Draft): Selection {
  if (d.all) return { all: true }
  return {
    tools: d.tools,
    prompts: d.prompts,
    resources: d.resources.map((r) => r.trim()).filter(Boolean),
    templates: d.templates,
  }
}

const grantKey = (g: GrantInput) => `${g.subjectType}:${g.subjectId}`
const sameSet = (a: string[], b: string[]) => a.length === b.length && [...a].sort().join('\n') === [...b].sort().join('\n')

function isDirty(a: Draft, b: Draft): boolean {
  return (
    a.name !== b.name ||
    a.description !== b.description ||
    a.all !== b.all ||
    (!a.all &&
      (!sameSet(a.tools, b.tools) ||
        !sameSet(a.prompts, b.prompts) ||
        !sameSet(a.resources.filter((r) => r.trim()), b.resources.filter((r) => r.trim())) ||
        !sameSet(a.templates, b.templates))) ||
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

  const load = useCallback(async () => {
    try {
      const e = await api.entitlements.get(id)
      setEnt(e)
      setDraft(toDraft(e))
      setLoadError(undefined)
      const [c, org] = await Promise.all([api.capabilities.list(e.serverId), api.org.get()])
      setCaps(c)
      setMembers(org.members)
    } catch (err) {
      setLoadError(errorMessage(err))
    } finally {
      setLoaded(true)
    }
  }, [id])
  useEffect(() => {
    void load()
  }, [load])

  const dirty = useMemo(() => !!ent && !!draft && isDirty(draft, toDraft(ent)), [ent, draft])

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

  const save = async () => {
    const name = draft.name.trim()
    if (!name) return setSaveError('Name is required.')
    setSaving(true)
    setSaveError(undefined)
    try {
      await api.entitlements.update(ent.id, { name, description: draft.description.trim(), selection: toSelection(draft) })
      const updated = await api.entitlements.putGrants(ent.id, draft.grants)
      setEnt(updated)
      setDraft(toDraft(updated))
      toast.success(`Saved ${updated.slug}`)
    } catch (e) {
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
            <Button variant="primary" onClick={save} disabled={saving || !dirty}>
              <Save /> {saving ? 'Saving…' : 'Save'}
            </Button>
          </>
        }
      />

      {saveError && <Callout tone="danger">{saveError}</Callout>}

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

      <SelectionCard draft={draft} set={set} caps={caps} serverId={ent.serverId} />

      <GrantsCard draft={draft} set={set} members={members} />

      <div className="flex items-center justify-end gap-3">
        {dirty && <span className="text-xs text-muted-foreground">Unsaved changes</span>}
        <Button variant="primary" onClick={save} disabled={saving || !dirty}>
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

type ListKey = 'tools' | 'prompts' | 'templates'

function SelectionCard({ draft, set, caps, serverId }: { draft: Draft; set: (p: Partial<Draft>) => void; caps: CapabilityDTO[]; serverId: string }) {
  const byKind = (k: CapabilityKind) => caps.filter((c) => c.kind === k)
  const seenUris = byKind('resource').map((c) => c.name)
  const patterns = draft.resources.filter((r) => !seenUris.includes(r))
  const summary = selectionSummary(draft.all ? { all: true } : toSelection(draft))

  const toggleIn = (key: ListKey | 'resources', name: string, on: boolean) => {
    const cur = draft[key]
    set({ [key]: on ? [...cur, name] : cur.filter((n) => n !== name) } as Partial<Draft>)
  }
  const setPatterns = (next: string[]) => set({ resources: [...draft.resources.filter((r) => seenUris.includes(r)), ...next] })

  return (
    <section className="space-y-3">
      <div className="flex flex-wrap items-end justify-between gap-3">
        <div>
          <h2 className="text-base font-semibold tracking-tight">Selection</h2>
          <p className="mt-0.5 text-xs text-muted-foreground">Decides both what is listed and what can be called. Only approved capabilities can be exposed.</p>
        </div>
        <div className="flex overflow-hidden rounded-lg border border-border" role="group" aria-label="Selection mode">
          {(
            [
              [true, 'Everything the server offers'],
              [false, 'Only what is selected'],
            ] as const
          ).map(([v, label]) => (
            <button
              key={label}
              type="button"
              onClick={() => set({ all: v })}
              aria-pressed={draft.all === v}
              className={cn(
                'h-9 cursor-pointer px-3 text-xs font-medium transition-colors',
                draft.all === v ? 'bg-elevated text-foreground' : 'text-muted-foreground hover:bg-elevated/60 hover:text-foreground',
              )}
            >
              {label}
            </button>
          ))}
        </div>
      </div>

      {draft.all ? (
        <div className="rounded-2xl border border-border bg-card p-5 text-sm text-muted-foreground">
          Everything approved on the server's{' '}
          <Link to={`/servers/${serverId}`} className="text-foreground underline decoration-accent/60 underline-offset-[3px] hover:text-accent">
            ceiling
          </Link>{' '}
          is exposed, including capabilities approved later.
        </div>
      ) : (
        <>
          {summary === 'Nothing' && <Callout tone="warning">Nothing is selected: this entitlement exposes nothing.</Callout>}
          <div className="grid gap-4 lg:grid-cols-2">
            {KINDS.map((k) => {
              if (k.kind === 'resource') {
                return (
                  <ListCard key={k.kind}>
                    <ListBar>
                      <span className="text-[10px] font-semibold uppercase tracking-[0.16em]">
                        {k.label} <span className="ml-1 font-mono normal-case tracking-normal">{draft.resources.filter((r) => r.trim()).length}</span>
                      </span>
                      <Button variant="secondary" size="sm" onClick={() => setPatterns([...patterns, ''])}>
                        <Plus /> Add pattern
                      </Button>
                    </ListBar>
                    <CapabilityChecks
                      items={byKind('resource')}
                      selected={draft.resources}
                      extra={[]}
                      onToggle={(n, on) => toggleIn('resources', n, on)}
                      empty={patterns.length ? undefined : 'No resources seen. Add a URI pattern to allow resources by address.'}
                    />
                    {patterns.length > 0 && (
                      <div className="space-y-2 border-t border-border p-4">
                        <p className="text-xs text-faint-foreground">Patterns: an exact URI, * within one path segment, ** for any suffix.</p>
                        {patterns.map((p, i) => (
                          <div key={i} className="flex items-center gap-2">
                            <Input
                              value={p}
                              onChange={(e) => setPatterns(patterns.map((x, j) => (j === i ? e.target.value : x)))}
                              placeholder="file:///docs/**"
                              className="h-9 font-mono text-xs"
                              aria-label={`Resource pattern ${i + 1}`}
                            />
                            <Button variant="ghost" size="iconSm" className="hover:text-danger" aria-label="Remove pattern" onClick={() => setPatterns(patterns.filter((_, j) => j !== i))}>
                              <X />
                            </Button>
                          </div>
                        ))}
                      </div>
                    )}
                  </ListCard>
                )
              }
              const key = k.key as ListKey
              const seen = byKind(k.kind).map((c) => c.name)
              return (
                <ListCard key={k.kind}>
                  <ListBar>
                    <span className="text-[10px] font-semibold uppercase tracking-[0.16em]">
                      {k.label} <span className="ml-1 font-mono normal-case tracking-normal">{draft[key].length}</span>
                    </span>
                  </ListBar>
                  <CapabilityChecks
                    items={byKind(k.kind)}
                    selected={draft[key]}
                    extra={draft[key].filter((n) => !seen.includes(n))}
                    onToggle={(n, on) => toggleIn(key, n, on)}
                    empty={`No ${k.many} seen on this server.`}
                  />
                </ListCard>
              )
            })}
          </div>
        </>
      )}
    </section>
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
        const approved = !!c.approvedAt
        const checked = selected.includes(c.name)
        const id = `cap-${c.kind}-${c.name}`
        return (
          <label key={c.name} htmlFor={id} className={cn('flex items-center gap-3 px-5 py-2.5', approved ? 'cursor-pointer hover:bg-elevated/45' : 'cursor-not-allowed opacity-55')}>
            <Checkbox id={id} checked={checked} disabled={!approved && !checked} onCheckedChange={(v) => onToggle(c.name, v === true)} />
            <span className="min-w-0 flex-1 truncate font-mono text-xs" title={c.name}>
              {c.name}
            </span>
            {!approved && <span className="shrink-0 text-[11px] text-muted-foreground">pending approval</span>}
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
      <div>
        <h2 className="text-base font-semibold tracking-tight">Grants</h2>
        <p className="mt-0.5 text-xs text-muted-foreground">
          A caller holds the union of their grants. On an exclusive server, holding two entitlements blocks both.
        </p>
      </div>
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
