import { useMemo, useState } from 'react'
import { CircleHelp } from 'lucide-react'
import { Button } from '../../ui/components/button'
import { Sheet, SheetBody, SheetContent, SheetDescription, SheetHeader, SheetTitle } from '../../ui/components/sheet'
import { Callout } from '../Callout'
import { RuleTextEditor, type RuleCompletionData } from './RuleTextEditor'
import type { CapabilityDTO, HookInfo, HooksSchema, RuleProblem } from '../../api'

/** Rule text by hook property; a hook with no text has no key. */
export type RuleText = Record<string, string>
import { cn } from '../../lib'

// The Hooks tab. Rules are text, written on the hook where they run: a hook is a method, a side
// of it (pre: the request; post: what comes back) and, for a reply, the event. The list on the
// left is every hook in the order a request meets them; the editor on the right holds one hook's
// rules. The Help button opens a panel with a worked example of everything that hook can do.

const RULE_LINE = /^\s*(audit\s+)?when(\s|$)/

/** Whether a hook has a value: any text at all. Rules are not counted. */
export function hasText(text: string | undefined): boolean {
  return (text ?? '').trim() !== ''
}

/** The hook's place under its method: pre, post, or post and the event when the method has several. */
function sideLabel(s: HookInfo, all: HookInfo[]): string {
  if (s.side !== 'post') return s.side
  const posts = all.filter((x) => x.method === s.method && x.side === 'post').length
  return posts > 1 && s.event ? `post · ${s.event}` : 'post'
}

interface ExampleBlock {
  kind: 'comment' | 'rule'
  text: string
}

/** Splits a hook's example into its comments and its rules, so a rule can be picked on its own. */
function exampleBlocks(example: string): ExampleBlock[] {
  const out: ExampleBlock[] = []
  for (const line of example.split('\n')) {
    if (!line.trim()) continue
    const last = out[out.length - 1]
    if (line.trimStart().startsWith('#')) {
      if (last?.kind === 'comment') last.text += '\n' + line
      else out.push({ kind: 'comment', text: line })
    } else if (RULE_LINE.test(line) || !last || last.kind !== 'rule') {
      out.push({ kind: 'rule', text: line })
    } else {
      last.text += '\n' + line
    }
  }
  return out
}

/** Example rules line their actions up in a column; one rule on its own reads better without the gap. */
const tighten = (rule: string) => rule.replace(/^((?:audit\s+)?when\s.*?\S) {3,}(deny|confirm|set)\b/, '$1  $2')

export function HookRules({
  schema,
  schemaError,
  onReloadSchema,
  text,
  onChange,
  problems,
  warnings,
  validating,
  onValidate,
  onCheck,
  checked,
  rulesError,
  slug,
  caps,
}: {
  schema?: HooksSchema
  schemaError?: string
  onReloadSchema: () => void
  /** The rules as text, by hook property. */
  text: RuleText
  onChange: (text: RuleText) => void
  problems: RuleProblem[]
  /** Rules that compile but look wrong. They do not stop a save. */
  warnings: RuleProblem[]
  validating: boolean
  /** The quiet check, run when an editor loses focus. */
  onValidate: () => void
  /** The check the admin asks for with the button. */
  onCheck: () => void
  /** Whether the last check the admin asked for found any rules; undefined when none was asked for, or the text changed since. */
  checked?: boolean
  rulesError?: string
  slug: string
  caps: CapabilityDTO[]
}) {
  // The methods come first. The server's own hooks (settings, session, discover) are not about
  // any one method, so they sit at the bottom of the list.
  const hooks = useMemo(() => {
    const all = schema?.hooks ?? []
    return [...all.filter((s) => s.group !== 'Server'), ...all.filter((s) => s.group === 'Server')]
  }, [schema])
  // Open on the first hook that has rules, or on tools/call.pre: the one most rules go in.
  const [picked, setPicked] = useState<string>()
  const current = hooks.find((s) => s.name === picked) ?? hooks.find((s) => hasText(text[s.property])) ?? hooks.find((s) => s.name === 'tools/call.pre') ?? hooks[0]
  const [help, setHelp] = useState(false)

  const groups = useMemo(() => {
    const out: { group: string; methods: { method: string; hooks: HookInfo[] }[] }[] = []
    for (const s of hooks) {
      let g = out.find((x) => x.group === s.group)
      if (!g) out.push((g = { group: s.group, methods: [] }))
      let m = g.methods.find((x) => x.method === s.method)
      if (!m) g.methods.push((m = { method: s.method, hooks: [] }))
      m.hooks.push(s)
    }
    return out
  }, [hooks])

  const completions = useMemo<RuleCompletionData | undefined>(() => {
    if (!current || !schema) return undefined
    return {
      hook: current,
      caller: schema.caller,
      slug,
      caps,
      actions: current.actions,
      deny: current.deny,
      sets: current.sets,
    }
  }, [current, schema, slug, caps])

  const general = problems.filter((p) => !hooks.some((s) => s.name === p.hook))
  const findings = [...problems, ...warnings]
  const here = current ? findings.filter((p) => p.hook === current.name) : []
  const title = (id: string) => {
    const s = hooks.find((x) => x.name === id)
    return s ? `${s.method} · ${sideLabel(s, hooks)}` : id
  }
  const value = current ? (text[current.property] ?? '') : ''
  const setValue = (v: string) => current && onChange({ ...text, [current.property]: v })
  const add = (rule: string) => setValue((value.trimEnd() ? value.trimEnd() + '\n\n' : '') + tighten(rule) + '\n')

  return (
    <section className="space-y-4">
      {rulesError && (
        <Callout tone="danger">
          <p className="font-medium">This server exposes nothing until its rules are rewritten and saved.</p>
          <p className="break-words font-mono text-xs leading-5 text-muted-foreground">{rulesError}</p>
        </Callout>
      )}
      {schemaError && (
        <Callout tone="danger">
          <div className="flex flex-wrap items-center justify-between gap-3">
            <span className="min-w-0 break-words">Could not load the hooks: {schemaError}</span>
            <Button variant="secondary" size="sm" onClick={onReloadSchema}>
              Retry
            </Button>
          </div>
        </Callout>
      )}
      {general.map((p, i) => (
        <Callout key={i} tone="danger">
          {p.message}
        </Callout>
      ))}
      <div className="flex flex-wrap items-start justify-between gap-3">
        <p className="max-w-3xl text-xs leading-5 text-muted-foreground">
          A rule runs where you write it: on a method, either before the request is sent (pre) or on what comes back (post). Each of those places is a hook. Within a hook, rules run top
          to bottom and a later rule sees what an earlier one set.
        </p>
        {/* Never disabled: leaving the editor to press it starts the quiet check, and a button that
            disabled itself then would swallow the click. */}
        <Button variant="secondary" size="sm" onClick={onCheck} aria-busy={validating}>
          Check rules
        </Button>
      </div>

      {checked !== undefined &&
        (findings.length === 0 ? (
          <p className="text-xs text-accent">
            {checked ? "The rules pass the engine's checks." : 'There are no rules to check.'}
          </p>
        ) : (
          <div className="rounded-xl border border-border bg-card p-2">
            <p className="px-2 py-1 text-xs text-muted-foreground">
              {problems.length > 0
                ? `${problems.length} ${problems.length === 1 ? 'problem stops' : 'problems stop'} these rules being saved.`
                : `The rules compile. ${warnings.length} ${warnings.length === 1 ? 'warning' : 'warnings'} to look at.`}
            </p>
            {findings.map((p, i) => (
              <button
                key={i}
                type="button"
                onClick={() => setPicked(p.hook)}
                className="grid w-full cursor-pointer grid-cols-[13rem_4rem_minmax(0,1fr)] gap-3 rounded-md px-2 py-1 text-left font-mono text-xs leading-5 transition-colors hover:bg-elevated"
              >
                <span className="truncate text-muted-foreground">{title(p.hook)}</span>
                <span className={p.level === 'warning' ? 'text-warning' : 'text-danger'}>{p.line ? `line ${p.line}` : 'hook'}</span>
                <span className={cn('min-w-0 break-words', p.level === 'warning' ? 'text-warning' : 'text-danger')}>{p.message}</span>
              </button>
            ))}
          </div>
        ))}

      {current && (
        <div className="grid gap-6 lg:grid-cols-[15rem_minmax(0,1fr)]">
          <nav aria-label="Hooks" className="space-y-5 lg:border-r lg:border-border lg:pr-4">
            {groups.map((g) => (
              <div key={g.group}>
                <h3 className="mb-2 text-[10px] font-semibold uppercase tracking-[0.16em] text-muted-foreground">{g.group}</h3>
                <div className="space-y-2">
                  {g.methods.map((m) => (
                    <div key={m.method}>
                      <p className="px-2 font-mono text-[11px] leading-5 text-faint-foreground">{m.method}</p>
                      {m.hooks.map((s) => {
                        const has = hasText(text[s.property])
                        const bad = problems.some((p) => p.hook === s.name)
                        const warned = !bad && warnings.some((p) => p.hook === s.name)
                        const active = s.name === current.name
                        return (
                          <button
                            key={s.name}
                            type="button"
                            aria-current={active ? 'true' : undefined}
                            onClick={() => setPicked(s.name)}
                            className={cn(
                              'flex w-full cursor-pointer items-center justify-between gap-2 rounded-md py-1 pl-5 pr-2 text-left font-mono text-xs transition-colors',
                              active ? 'bg-elevated text-foreground' : 'text-muted-foreground hover:bg-elevated/60 hover:text-foreground',
                            )}
                          >
                            <span className="truncate">{sideLabel(s, hooks)}</span>
                            {(has || bad) && (
                              <span
                                title={bad ? 'Has problems' : warned ? 'Has warnings' : 'Has rules'}
                                className={cn('size-1.5 shrink-0 rounded-full', bad ? 'bg-danger' : warned ? 'bg-warning' : 'bg-accent')}
                              />
                            )}
                          </button>
                        )
                      })}
                    </div>
                  ))}
                </div>
              </div>
            ))}
          </nav>

          <div className="min-w-0 space-y-4">
            <div className="flex items-start justify-between gap-3">
              <div className="min-w-0">
                <h2 className="font-mono text-sm font-semibold">
                  {current.method} <span className="text-muted-foreground">·</span> {sideLabel(current, hooks)}
                </h2>
                <p className="mt-1 text-sm text-muted-foreground">{current.summary}</p>
                {current.notes?.map((n) => (
                  <p key={n} className="mt-1 text-xs leading-5 text-muted-foreground">
                    {n}
                  </p>
                ))}
              </div>
              <Button variant="secondary" size="sm" className="shrink-0" onClick={() => setHelp(true)}>
                <CircleHelp className="size-4" />
                Help
              </Button>
            </div>

            <RuleTextEditor
              key={current.name}
              value={value}
              onChange={setValue}
              onBlur={onValidate}
              problemLines={here.filter((p) => p.level !== 'warning').map((p) => p.line)}
              warningLines={here.filter((p) => p.level === 'warning').map((p) => p.line)}
              completions={completions}
              placeholder={`when <condition>   ${current.actions.join(' | ')}`}
              ariaLabel={`Rules for ${current.name}`}
            />

            {here.length > 0 ? (
              <ul className="space-y-1">
                {here.map((p, i) => (
                  <li key={i} className={cn('flex gap-3 font-mono text-xs leading-5', p.level === 'warning' ? 'text-warning' : 'text-danger')}>
                    <span className="shrink-0">{p.line ? `line ${p.line}` : 'hook'}</span>
                    <span className="min-w-0 break-words">{p.message}</span>
                  </li>
                ))}
              </ul>
            ) : (
              validating && <p className="text-xs text-muted-foreground">Checking…</p>
            )}

          </div>
        </div>
      )}
      {current && (
        <HookHelp
          hook={current}
          title={`${current.method} · ${sideLabel(current, hooks)}`}
          open={help}
          onClose={() => setHelp(false)}
          onAdd={(rule) => {
            add(rule)
            setHelp(false)
          }}
        />
      )}
    </section>
  )
}

/**
 * The help for one hook, in a panel that slides in from the right: a worked example of everything
 * a rule can do there (a click adds the rule and closes the panel), then what each action does and
 * what a rule may set and read.
 */
function HookHelp({ hook, title, open, onClose, onAdd }: { hook: HookInfo; title: string; open: boolean; onClose: () => void; onAdd: (rule: string) => void }) {
  return (
    <Sheet open={open} onOpenChange={(o) => !o && onClose()}>
      <SheetContent onOpenAutoFocus={(ev) => ev.preventDefault()}>
        <SheetHeader>
          <SheetTitle className="font-mono text-base">{title}</SheetTitle>
          <SheetDescription>What a rule can do here. Click a rule to add it.</SheetDescription>
        </SheetHeader>
        <SheetBody>
          <div className="rounded-xl border border-border bg-background p-2">
            {exampleBlocks(hook.example).map((b, i) =>
              b.kind === 'comment' ? (
                <pre key={i} className="whitespace-pre-wrap px-2 pb-0.5 pt-2 font-mono text-xs italic leading-5 text-faint-foreground first:pt-0.5">
                  {b.text}
                </pre>
              ) : (
                <button
                  key={i}
                  type="button"
                  onClick={() => onAdd(b.text)}
                  title="Add this rule"
                  className="block w-full cursor-pointer overflow-x-auto whitespace-pre rounded-md px-2 py-0.5 text-left font-mono text-xs leading-5 text-code-foreground transition-colors hover:bg-elevated"
                >
                  {tighten(b.text)}
                </button>
              ),
            )}
          </div>

          <dl className="grid gap-x-4 gap-y-3 text-xs leading-5 sm:grid-cols-[5rem_minmax(0,1fr)]">
            {hook.actions.includes('deny') && (
              <>
                <dt className="font-mono text-accent">deny</dt>
                <dd className="text-muted-foreground">
                  {hook.deny}. A reason in quotes after it is what the client is told: <span className="font-mono">deny "Admins only."</span>
                </dd>
              </>
            )}
            {hook.actions.includes('confirm') && (
              <>
                <dt className="font-mono text-accent">confirm</dt>
                <dd className="text-muted-foreground">
                  asks the user first and goes ahead once they agree. Values in braces are filled in: <span className="font-mono">confirm "Delete {'{tool.name}'}?"</span>
                </dd>
              </>
            )}
            {hook.actions.includes('set') && (
              <>
                <dt className="font-mono text-accent">set</dt>
                <dd className="font-mono text-muted-foreground">
                  {hook.sets.map((s) => (
                    <span key={s.path} title={s.type} className="mr-3 inline-block">
                      {s.path}
                    </span>
                  ))}
                </dd>
              </>
            )}
            <dt className="font-mono text-accent">read</dt>
            <dd className="font-mono text-muted-foreground">
              {Object.entries(hook.tree.keys ?? {})
                .filter(([, n]) => !n.writeOnly)
                .map(([name, n]) => (
                  <span key={name} title={n.doc} className="mr-3 inline-block">
                    {name}
                  </span>
                ))}
              <span className="mt-1 block font-sans">
                Type a name and a dot to see what is under it. A value that may be absent is read as <span className="font-mono">tool.?title.orValue('')</span>.
              </span>
            </dd>
            <dt className="font-mono text-accent">audit</dt>
            <dd className="text-muted-foreground">before when, makes a rule record what it would do and change nothing. A line that starts with # is a comment.</dd>
          </dl>
        </SheetBody>
      </SheetContent>
    </Sheet>
  )
}
