import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { Link, useNavigate, useParams } from 'react-router-dom'
import { toast } from 'sonner'
import { ArrowDown, ArrowLeft, ArrowUp, Copy, ListChecks, Plus, Save, Trash2, X } from 'lucide-react'
import { Button } from '../ui/components/button'
import { Input } from '../ui/components/input'
import { Switch } from '../ui/components/switch'
import { Badge } from '../ui/components/badge'
import { Select, SelectContent, SelectGroup, SelectItem, SelectLabel, SelectTrigger, SelectValue } from '../ui/components/select'
import { PageHeader } from '../components/PageHeader'
import { FormField, FormSection } from '../components/FormField'
import { Callout } from '../components/Callout'
import { ListBar, ListCard, ListEmpty } from '../components/ListCard'
import { CelReference } from '../components/policies/Reference'
import { MUX, PolicyPreview } from '../components/policies/Preview'
import { CelEditor, type CelCompletionData } from '../components/policies/CelEditor'
import { ApiError, api, errorMessage, problemsOf, type PolicyDTO, type PolicyField, type PolicyInput, type PolicyProblem, type PolicyRule, type ServerDTO } from '../api'
import { cn } from '../lib'
import { useMe } from '../me'

// Served by GET /policies/fields; this copy only fills in if that call fails.
const FALLBACK_FIELDS: PolicyField[] = [
  { name: 'visible', kind: 'bool', doc: 'Whether the tool is listed.' },
  { name: 'confirm', kind: 'bool', doc: 'Ask the user before a call.' },
  { name: 'confirmMessage', kind: 'string', doc: 'The confirmation prompt.' },
  { name: 'name', kind: 'string', doc: 'The tool name clients see.' },
  { name: 'title', kind: 'string', doc: 'The display title.' },
  { name: 'description', kind: 'string', doc: 'The tool description.' },
  { name: 'annotations.readOnlyHint', kind: 'bool', doc: '' },
  { name: 'annotations.destructiveHint', kind: 'bool', doc: '' },
  { name: 'annotations.idempotentHint', kind: 'bool', doc: '' },
  { name: 'annotations.openWorldHint', kind: 'bool', doc: '' },
]

const GROUPS: { label: string; match: (f: string) => boolean }[] = [
  { label: 'Visibility', match: (f) => f === 'visible' },
  { label: 'Confirmation', match: (f) => f.startsWith('confirm') },
  { label: 'Identity', match: (f) => f === 'name' || f === 'title' || f === 'description' },
  { label: 'Annotations', match: (f) => f.startsWith('annotations.') },
]

interface Assignment {
  key: string
  field: string
  expr: string
}
interface EditorRule {
  id: string
  name: string
  enabled: boolean
  when: string
  then: Assignment[]
}

// randomUUID needs a secure context; fall back so plain-http dev hosts still work.
const newKey = () =>
  typeof crypto !== 'undefined' && 'randomUUID' in crypto ? crypto.randomUUID() : `${Date.now().toString(36)}-${Math.random().toString(36).slice(2, 10)}`

function fromRule(r: PolicyRule): EditorRule {
  return {
    id: r.id || newKey(),
    name: r.name,
    enabled: r.enabled !== false,
    when: r.when,
    then: Object.entries(r.then ?? {}).map(([field, expr]) => ({ key: newKey(), field, expr })),
  }
}

function toRule(r: EditorRule): PolicyRule {
  const then: Record<string, string> = {}
  for (const a of r.then) if (a.field) then[a.field] = a.expr
  return { id: r.id, name: r.name, enabled: r.enabled, when: r.when, then }
}

function blankRule(n: number): EditorRule {
  return { id: newKey(), name: `Rule ${n}`, enabled: true, when: '', then: [{ key: newKey(), field: 'visible', expr: 'false' }] }
}

/** Problems keyed by where they render. Fields may arrive as "then.visible" or "visible". */
function indexProblems(problems: PolicyProblem[], rules: EditorRule[]) {
  const general: string[] = []
  const byRule = new Map<number, { rule: string[]; when: string[]; fields: Map<string, string[]> }>()
  for (const p of problems) {
    if (p.rule < 0 || p.rule >= rules.length) {
      general.push(p.message)
      continue
    }
    let slot = byRule.get(p.rule)
    if (!slot) byRule.set(p.rule, (slot = { rule: [], when: [], fields: new Map() }))
    const field = (p.field ?? '').replace(/^then\./, '')
    if (field === 'when') slot.when.push(p.message)
    else if (field && rules[p.rule].then.some((a) => a.field === field)) slot.fields.set(field, [...(slot.fields.get(field) ?? []), p.message])
    else slot.rule.push(p.field && field !== 'name' ? `${p.field}: ${p.message}` : p.message)
  }
  return { general, byRule }
}

export function PolicyEditorPage() {
  const { id = 'new' } = useParams()
  return <PolicyEditor key={id} id={id} />
}

function PolicyEditor({ id }: { id: string }) {
  const navigate = useNavigate()
  const { isAdmin } = useMe()
  const isNew = id === 'new'
  const [policy, setPolicy] = useState<PolicyDTO>()
  const [loaded, setLoaded] = useState(isNew)
  const [loadError, setLoadError] = useState<string>()
  const [fields, setFields] = useState<PolicyField[]>(FALLBACK_FIELDS)
  const [name, setName] = useState('')
  const [description, setDescription] = useState('')
  const [invert, setInvert] = useState(false)
  const [rules, setRules] = useState<EditorRule[]>([])
  const [problems, setProblems] = useState<PolicyProblem[]>([])
  const [nameError, setNameError] = useState<string>()
  const [saving, setSaving] = useState(false)
  const [saveError, setSaveError] = useState<string>()
  const [servers, setServers] = useState<ServerDTO[]>([])
  const [route, setRoute] = useState(MUX)

  useEffect(() => {
    api.servers
      .list()
      .then(setServers)
      .catch(() => {})
  }, [])

  // Tool names for completion come from a no-rules preview of the selected route, fetched the
  // first time they are asked for and kept per route. A failed fetch is dropped so it can retry.
  const toolNames = useRef(new Map<string, Promise<{ name: string; server: string }[]>>())
  const completions = useMemo<CelCompletionData>(
    () => ({
      servers: servers.map((s) => ({ slug: s.slug, name: s.name })),
      loadToolNames: () => {
        let p = toolNames.current.get(route)
        if (!p) {
          p = api.policies
            .preview({ policy: { name: '', description: '', invert: false, rules: [] }, route })
            .then((r) => r.tools.map((t) => ({ name: t.upstream, server: t.server })))
          p.catch(() => toolNames.current.delete(route))
          toolNames.current.set(route, p)
        }
        return p
      },
    }),
    [servers, route],
  )

  useEffect(() => {
    api.policies
      .fields()
      .then((f) => f.length && setFields(f))
      .catch(() => {})
  }, [])

  useEffect(() => {
    if (isNew) return
    api.policies
      .get(id)
      .then((p) => {
        setPolicy(p)
        setName(p.name)
        setDescription(p.description)
        setInvert(p.invert)
        setRules(p.rules.map(fromRule))
      })
      .catch((e) => setLoadError(errorMessage(e)))
      .finally(() => setLoaded(true))
  }, [id, isNew])

  const payload = useMemo(() => rules.map(toRule), [rules])
  const payloadKey = useMemo(() => JSON.stringify(payload), [payload])

  // Debounced validation; a sequence number drops responses that arrive out of order.
  const seq = useRef(0)
  const validate = useCallback((list: PolicyRule[]) => {
    const n = ++seq.current
    if (!list.length) {
      setProblems([])
      return
    }
    api.policies
      .validate(list)
      .then((r) => n === seq.current && setProblems(r.problems ?? []))
      .catch(() => {})
  }, [])
  useEffect(() => {
    if (!loaded) return
    const t = setTimeout(() => validate(JSON.parse(payloadKey) as PolicyRule[]), 600)
    return () => clearTimeout(t)
  }, [payloadKey, loaded, validate])
  const validateNow = () => validate(payload)

  const index = useMemo(() => indexProblems(problems, rules), [problems, rules])

  const input = (): PolicyInput => ({ name: name.trim(), description: description.trim(), invert, rules: payload })

  const updateRule = (i: number, patch: Partial<EditorRule>) => setRules((rs) => rs.map((r, j) => (j === i ? { ...r, ...patch } : r)))
  const move = (i: number, d: -1 | 1) =>
    setRules((rs) => {
      const j = i + d
      if (j < 0 || j >= rs.length) return rs
      const next = [...rs]
      ;[next[i], next[j]] = [next[j], next[i]]
      return next
    })
  const duplicate = (i: number) =>
    setRules((rs) => {
      const r = rs[i]
      const copy: EditorRule = { ...r, id: newKey(), name: `${r.name} copy`, then: r.then.map((a) => ({ ...a, key: newKey() })) }
      return [...rs.slice(0, i + 1), copy, ...rs.slice(i + 1)]
    })
  const removeRule = (i: number) => setRules((rs) => rs.filter((_, j) => j !== i))
  const addRule = () => setRules((rs) => [...rs, blankRule(rs.length + 1)])

  const save = async () => {
    if (!name.trim()) {
      setNameError('Name is required.')
      return
    }
    setSaving(true)
    setNameError(undefined)
    setSaveError(undefined)
    try {
      if (isNew) {
        const created = await api.policies.create(input())
        toast.success(`Created ${created.name}`)
        navigate(`/policies/${encodeURIComponent(created.id)}`, { replace: true })
      } else {
        const updated = await api.policies.update(id, input())
        setPolicy(updated)
        toast.success(`Saved ${updated.name}`)
      }
    } catch (e) {
      const list = problemsOf(e)
      if (list) {
        setProblems(list)
        toast.error('Fix the highlighted rules')
      } else if (e instanceof ApiError && e.status === 409) {
        setNameError(e.message)
      } else {
        setSaveError(errorMessage(e))
      }
    } finally {
      setSaving(false)
    }
  }

  const back = (
    <Button variant="link" asChild>
      <Link to="/policies">
        <ArrowLeft /> Policies
      </Link>
    </Button>
  )

  if (!isNew && !policy) {
    return (
      <div className="space-y-6">
        {back}
        {loadError ? <Callout tone="danger">{loadError}</Callout> : <p className="text-sm text-muted-foreground">{loaded ? 'Policy not found.' : 'Loading…'}</p>}
      </div>
    )
  }

  const problemCount = problems.length

  return (
    <div className="space-y-6">
      {back}
      <PageHeader
        eyebrow="Policy"
        title={isNew ? 'New policy' : policy?.name || 'Policy'}
        description="Rules run top to bottom. A matching rule applies its assignments before the next rule sees the tool."
        actions={
          isAdmin ? (
            <Button variant="primary" onClick={save} disabled={saving}>
              <Save /> {saving ? 'Saving…' : isNew ? 'Create policy' : 'Save'}
            </Button>
          ) : undefined
        }
      />

      {!isAdmin && <Callout tone="info">Read-only. Only admins of this org can change policies; edits here are not saved.</Callout>}

      {saveError && <Callout tone="danger">{saveError}</Callout>}

      <div className="space-y-5 rounded-2xl border border-border bg-card p-5">
        <div className="grid gap-4 sm:grid-cols-2">
          <FormField label="Name" required hint={nameError ? <span className="text-danger">{nameError}</span> : undefined}>
            <Input
              value={name}
              onChange={(e) => {
                setName(e.target.value)
                setNameError(undefined)
              }}
              placeholder="hide-destructive"
              className={cn(nameError && 'border-danger/40')}
              autoFocus={isNew}
            />
          </FormField>
          <FormField label="Description">
            <Input value={description} onChange={(e) => setDescription(e.target.value)} placeholder="What this policy is for" />
          </FormField>
        </div>
        <div className="flex flex-wrap items-center gap-4">
          <span className="text-sm font-medium">Before any rule runs</span>
          <div className="flex overflow-hidden rounded-lg border border-border" role="group" aria-label="Starting visibility">
            {(
              [
                [false, 'All tools visible'],
                [true, 'All tools hidden'],
              ] as const
            ).map(([v, label]) => (
              <button
                key={label}
                type="button"
                onClick={() => setInvert(v)}
                aria-pressed={invert === v}
                className={cn(
                  'h-9 cursor-pointer px-3 text-xs font-medium transition-colors',
                  invert === v ? 'bg-elevated text-foreground' : 'text-muted-foreground hover:bg-elevated/60 hover:text-foreground',
                )}
              >
                {label}
              </button>
            ))}
          </div>
          <span className="text-xs leading-5 text-muted-foreground">
            {invert ? 'Rules opt tools in with visible = true (an allow-list).' : 'Rules hide tools with visible = false.'}
          </span>
        </div>
      </div>

      <ListCard>
        <ListBar>
          <span className="flex items-center gap-2">
            {rules.length} {rules.length === 1 ? 'rule' : 'rules'}
            {problemCount > 0 && (
              <Badge variant="danger">
                {problemCount} {problemCount === 1 ? 'problem' : 'problems'}
              </Badge>
            )}
          </span>
          {isAdmin && (
            <Button variant="secondary" size="sm" onClick={addRule}>
              <Plus /> Add rule
            </Button>
          )}
        </ListBar>
        {index.general.length > 0 && (
          <div className="border-b border-border p-5">
            <Callout tone="danger">
              {index.general.map((m, i) => (
                <p key={i} className="break-words">
                  {m}
                </p>
              ))}
            </Callout>
          </div>
        )}
        {rules.length === 0 ? (
          <ListEmpty
            icon={ListChecks}
            title="No rules yet"
            hint={invert ? 'Starting hidden with no rules hides every tool.' : 'A policy with no rules changes nothing.'}
            action={
              isAdmin ? (
                <Button variant="secondary" onClick={addRule}>
                  <Plus /> Add rule
                </Button>
              ) : undefined
            }
          />
        ) : (
          <ol className="divide-y divide-border">
            {rules.map((r, i) => (
              <RuleRow
                key={r.id}
                index={i}
                count={rules.length}
                rule={r}
                fields={fields}
                problems={index.byRule.get(i)}
                onChange={(patch) => updateRule(i, patch)}
                onMove={(d) => move(i, d)}
                onDuplicate={() => duplicate(i)}
                onRemove={() => removeRule(i)}
                onBlur={validateNow}
                completions={completions}
              />
            ))}
          </ol>
        )}
      </ListCard>

      <CelReference />

      <div className="space-y-3">
        <div>
          <h2 className="text-base font-semibold tracking-tight">Preview</h2>
          <p className="mt-1 text-xs text-muted-foreground">Runs the unsaved policy against a route's current tools. Hidden tools stay listed, dimmed.</p>
        </div>
        <PolicyPreview input={input} onProblems={setProblems} servers={servers} route={route} onRouteChange={setRoute} />
      </div>
    </div>
  )
}

function RuleRow({
  index,
  count,
  rule,
  fields,
  problems,
  onChange,
  onMove,
  onDuplicate,
  onRemove,
  onBlur,
  completions,
}: {
  index: number
  count: number
  rule: EditorRule
  fields: PolicyField[]
  problems?: { rule: string[]; when: string[]; fields: Map<string, string[]> }
  onChange: (patch: Partial<EditorRule>) => void
  onMove: (d: -1 | 1) => void
  onDuplicate: () => void
  onRemove: () => void
  onBlur: () => void
  completions: CelCompletionData
}) {
  const used = new Set(rule.then.map((a) => a.field))
  const setAssignment = (key: string, patch: Partial<Assignment>) => onChange({ then: rule.then.map((a) => (a.key === key ? { ...a, ...patch } : a)) })
  const addAssignment = () => {
    const next = fields.find((f) => !used.has(f.name))
    onChange({ then: [...rule.then, { key: newKey(), field: next?.name ?? '', expr: next?.kind === 'bool' ? 'true' : '' }] })
  }

  return (
    <li className="space-y-4 p-5">
      <div className="flex flex-wrap items-center gap-3">
        <span className="w-6 shrink-0 font-mono text-xs text-faint-foreground">{index + 1}</span>
        <Switch checked={rule.enabled} onCheckedChange={(v) => onChange({ enabled: v })} aria-label="Enabled" title={rule.enabled ? 'Enabled' : 'Disabled'} />
        <Input
          value={rule.name}
          onChange={(e) => onChange({ name: e.target.value })}
          onBlur={onBlur}
          placeholder="Rule name"
          aria-label="Rule name"
          className="h-9 min-w-40 flex-1 font-medium"
        />
        <div className="flex items-center gap-1">
          <Button variant="ghost" size="iconSm" aria-label="Move up" title="Move up" disabled={index === 0} onClick={() => onMove(-1)}>
            <ArrowUp />
          </Button>
          <Button variant="ghost" size="iconSm" aria-label="Move down" title="Move down" disabled={index === count - 1} onClick={() => onMove(1)}>
            <ArrowDown />
          </Button>
          <Button variant="ghost" size="iconSm" aria-label="Duplicate" title="Duplicate" onClick={onDuplicate}>
            <Copy />
          </Button>
          <Button variant="ghost" size="iconSm" className="hover:text-danger" aria-label="Remove rule" title="Remove rule" onClick={onRemove}>
            <Trash2 />
          </Button>
        </div>
      </div>

      <div className={cn('space-y-4 md:pl-9', !rule.enabled && 'opacity-60')}>
        {problems?.rule.map((m, i) => <Problem key={i}>{m}</Problem>)}
        <FormField label="When" hint={problems?.when.length ? undefined : 'A CEL boolean. tool.name is slug__name on every route; tool.baseName is the bare upstream name.'}>
          <CelEditor
            value={rule.when}
            onChange={(when) => onChange({ when })}
            onBlur={onBlur}
            kind="bool"
            condition
            completions={completions}
            placeholder='tool.name.startsWith("delete")'
            ariaLabel="When"
            invalid={!!problems?.when.length}
          />
          {problems?.when.map((m, i) => <Problem key={i}>{m}</Problem>)}
        </FormField>

        <FormSection
          title="Then"
          actions={
            <Button variant="ghost" size="sm" onClick={addAssignment} disabled={used.size >= fields.length}>
              <Plus /> Add field
            </Button>
          }
        >
          {rule.then.length === 0 ? (
            <p className="text-xs text-faint-foreground">No assignments. The rule matches but changes nothing.</p>
          ) : (
            <div className="space-y-3">
              {rule.then.map((a) => (
                <AssignmentRow
                  key={a.key}
                  assignment={a}
                  fields={fields}
                  used={used}
                  problems={problems?.fields.get(a.field)}
                  onChange={(patch) => setAssignment(a.key, patch)}
                  onRemove={() => onChange({ then: rule.then.filter((x) => x.key !== a.key) })}
                  onBlur={onBlur}
                  completions={completions}
                />
              ))}
            </div>
          )}
        </FormSection>
      </div>
    </li>
  )
}

function AssignmentRow({
  assignment: a,
  fields,
  used,
  problems,
  onChange,
  onRemove,
  onBlur,
  completions,
}: {
  assignment: Assignment
  fields: PolicyField[]
  used: Set<string>
  problems?: string[]
  onChange: (patch: Partial<Assignment>) => void
  onRemove: () => void
  onBlur: () => void
  completions: CelCompletionData
}) {
  const field = fields.find((f) => f.name === a.field)
  const groups = useMemo(() => {
    const out = GROUPS.map((g) => ({ label: g.label, items: fields.filter((f) => g.match(f.name)) }))
    const other = fields.filter((f) => !GROUPS.some((g) => g.match(f.name)))
    if (other.length) out.push({ label: 'Other', items: other })
    return out.filter((g) => g.items.length)
  }, [fields])

  return (
    <div className="space-y-1.5">
      <div className="grid gap-2 sm:grid-cols-[220px_minmax(0,1fr)_auto] sm:items-start">
        <Select
          value={a.field || undefined}
          onValueChange={(v) => {
            const next = fields.find((f) => f.name === v)
            const wasLiteral = a.expr === '' || a.expr === 'true' || a.expr === 'false'
            onChange({ field: v, expr: wasLiteral ? (next?.kind === 'bool' ? 'true' : '') : a.expr })
          }}
        >
          <SelectTrigger aria-label="Field" className="font-mono text-xs">
            <SelectValue placeholder="Field" />
          </SelectTrigger>
          <SelectContent>
            {groups.map((g) => (
              <SelectGroup key={g.label}>
                <SelectLabel>{g.label}</SelectLabel>
                {g.items.map((f) => (
                  <SelectItem key={f.name} value={f.name} disabled={used.has(f.name) && f.name !== a.field} title={f.doc || undefined} className="font-mono text-xs">
                    {f.name}
                  </SelectItem>
                ))}
              </SelectGroup>
            ))}
          </SelectContent>
        </Select>
        <div className="flex min-w-0 items-center gap-2">
          <CelEditor
            value={a.expr}
            onChange={(expr) => onChange({ expr })}
            onBlur={onBlur}
            kind={field?.kind ?? 'string'}
            singleLine
            completions={completions}
            placeholder={field?.kind === 'string' ? '"text" or tool.description + " …"' : 'true'}
            ariaLabel={`${a.field || 'Field'} expression`}
            invalid={!!problems?.length}
            className="flex-1"
          />
          {field?.kind === 'bool' && (
            <div className="flex shrink-0 overflow-hidden rounded-lg border border-border" role="group" aria-label="Literal">
              {(['true', 'false'] as const).map((v) => (
                <button
                  key={v}
                  type="button"
                  onClick={() => onChange({ expr: v })}
                  aria-pressed={a.expr === v}
                  className={cn(
                    'h-10 cursor-pointer px-3 font-mono text-xs transition-colors',
                    a.expr === v ? 'bg-elevated text-foreground' : 'text-muted-foreground hover:bg-elevated/60 hover:text-foreground',
                  )}
                >
                  {v}
                </button>
              ))}
            </div>
          )}
        </div>
        <Button variant="ghost" size="icon" className="hover:text-danger" aria-label="Remove field" title="Remove field" onClick={onRemove}>
          <X />
        </Button>
      </div>
      {problems?.map((m, i) => <Problem key={i}>{m}</Problem>)}
      {!problems?.length && field?.kind === 'string' && <p className="text-xs leading-5 text-faint-foreground sm:pl-[228px]">String literals need quotes: "text".</p>}
    </div>
  )
}

function Problem({ children }: { children: string }) {
  return <p className="break-words font-mono text-xs leading-5 text-danger">{children}</p>
}
