import { useMemo, useState } from 'react'
import { Button } from '../../ui/components/button'
import { Callout } from '../Callout'
import { RuleTextEditor, type RuleCompletionData } from './RuleTextEditor'
import type { CapabilityDTO, RuleStage, RulesSchema, TextProblem } from '../../api'
import { cn } from '../../lib'

// The Rules tab. Rules are text, written where they run: a stage is a method, a side of it (pre:
// the request; post: what comes back) and, for a reply, the event. The list on the left is every
// stage in the order a request meets them; the editor on the right holds one stage's rules, with
// a worked example of everything that stage can do underneath.

const RULE_LINE = /^\s*(audit\s+)?when(\s|$)/

/** How many rules a stage's text holds: its when lines that are not commented out. */
export function countRules(text: string | undefined): number {
  return (text ?? '').split('\n').filter((l) => RULE_LINE.test(l)).length
}

/** The stage's place under its method: pre, post, or post and the event when the method has several. */
function sideLabel(s: RuleStage, all: RuleStage[]): string {
  if (s.side !== 'post') return s.side
  const posts = all.filter((x) => x.method === s.method && x.side === 'post').length
  return posts > 1 && s.event ? `post · ${s.event}` : 'post'
}

interface ExampleBlock {
  kind: 'comment' | 'rule'
  text: string
}

/** Splits a stage's example into its comments and its rules, so a rule can be picked on its own. */
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

export function RulesStages({
  schema,
  schemaError,
  onReloadSchema,
  text,
  onChange,
  problems,
  validating,
  onValidate,
  rulesError,
  slug,
  caps,
}: {
  schema?: RulesSchema
  schemaError?: string
  onReloadSchema: () => void
  text: Record<string, string>
  onChange: (text: Record<string, string>) => void
  problems: TextProblem[]
  validating: boolean
  onValidate: () => void
  rulesError?: string
  slug: string
  caps: CapabilityDTO[]
}) {
  const stages = useMemo(() => schema?.stages ?? [], [schema])
  // Open on the first stage that has rules, or on the call's pre stage: the one most rules go in.
  const [picked, setPicked] = useState<string>()
  const current = stages.find((s) => s.id === picked) ?? stages.find((s) => countRules(text[s.id]) > 0) ?? stages.find((s) => s.id === 'tools/call.pre') ?? stages[0]

  const groups = useMemo(() => {
    const out: { group: string; methods: { method: string; stages: RuleStage[] }[] }[] = []
    for (const s of stages) {
      let g = out.find((x) => x.group === s.group)
      if (!g) out.push((g = { group: s.group, methods: [] }))
      let m = g.methods.find((x) => x.method === s.method)
      if (!m) g.methods.push((m = { method: s.method, stages: [] }))
      m.stages.push(s)
    }
    return out
  }, [stages])

  const completions = useMemo<RuleCompletionData | undefined>(() => {
    if (!current || !schema) return undefined
    return {
      hook: { hook: current.id, item: current.item, phase: current.side, variables: current.reads, sets: current.sets, helpers: current.helpers },
      flags: schema.flags,
      slug,
      caps,
      actions: current.actions,
      deny: current.deny,
      sets: current.sets,
    }
  }, [current, schema, slug, caps])

  const general = problems.filter((p) => !stages.some((s) => s.id === p.stage))
  const here = current ? problems.filter((p) => p.stage === current.id) : []
  const value = current ? (text[current.id] ?? '') : ''
  const setValue = (v: string) => current && onChange({ ...text, [current.id]: v })
  const add = (rule: string) => setValue((value.trimEnd() ? value.trimEnd() + '\n\n' : '') + tighten(rule) + '\n')

  return (
    <section className="space-y-4">
      {rulesError && (
        <Callout tone="danger">
          <p className="font-medium">This entitlement exposes nothing until its rules are rewritten and saved.</p>
          <p className="break-words font-mono text-xs leading-5 text-muted-foreground">{rulesError}</p>
        </Callout>
      )}
      {schemaError && (
        <Callout tone="danger">
          <div className="flex flex-wrap items-center justify-between gap-3">
            <span className="min-w-0 break-words">Could not load the stages: {schemaError}</span>
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
      <p className="max-w-3xl text-xs leading-5 text-muted-foreground">
        A rule runs where you write it: on a method, either before the request is sent (pre) or on what comes back (post). Within a stage, rules run top to
        bottom and a later rule sees what an earlier one set.
      </p>

      {current && (
        <div className="grid gap-6 lg:grid-cols-[15rem_minmax(0,1fr)]">
          <nav aria-label="Stages" className="space-y-5 lg:border-r lg:border-border lg:pr-4">
            {groups.map((g) => (
              <div key={g.group}>
                <h3 className="mb-2 text-[10px] font-semibold uppercase tracking-[0.16em] text-muted-foreground">{g.group}</h3>
                <div className="space-y-2">
                  {g.methods.map((m) => (
                    <div key={m.method}>
                      <p className="px-2 font-mono text-[11px] leading-5 text-faint-foreground">{m.method}</p>
                      {m.stages.map((s) => {
                        const n = countRules(text[s.id])
                        const bad = problems.some((p) => p.stage === s.id)
                        const active = s.id === current.id
                        return (
                          <button
                            key={s.id}
                            type="button"
                            aria-current={active ? 'true' : undefined}
                            onClick={() => setPicked(s.id)}
                            className={cn(
                              'flex w-full cursor-pointer items-center justify-between gap-2 rounded-md py-1 pl-5 pr-2 text-left font-mono text-xs transition-colors',
                              active ? 'bg-elevated text-foreground' : 'text-muted-foreground hover:bg-elevated/60 hover:text-foreground',
                            )}
                          >
                            <span className="truncate">{sideLabel(s, stages)}</span>
                            {(n > 0 || bad) && (
                              <span className={cn('shrink-0 rounded px-1.5 text-[11px] leading-5', bad ? 'bg-danger/15 text-danger' : 'bg-accent/10 text-accent')}>
                                {bad ? `${n} !` : n}
                              </span>
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
            <div>
              <h2 className="font-mono text-sm font-semibold">
                {current.method} <span className="text-muted-foreground">·</span> {sideLabel(current, stages)}
              </h2>
              <p className="mt-1 text-sm text-muted-foreground">{current.summary}</p>
              {current.notes?.map((n) => (
                <p key={n} className="mt-1 text-xs leading-5 text-muted-foreground">
                  {n}
                </p>
              ))}
            </div>

            <RuleTextEditor
              key={current.id}
              value={value}
              onChange={setValue}
              onBlur={onValidate}
              problemLines={here.map((p) => p.line)}
              completions={completions}
              placeholder={`when <condition>   ${current.actions.join(' | ')}`}
              ariaLabel={`Rules for ${current.id}`}
            />

            {here.length > 0 ? (
              <ul className="space-y-1">
                {here.map((p, i) => (
                  <li key={i} className="flex gap-3 font-mono text-xs leading-5 text-danger">
                    <span className="shrink-0">{p.line ? `line ${p.line}` : 'stage'}</span>
                    <span className="min-w-0 break-words">{p.message}</span>
                  </li>
                ))}
              </ul>
            ) : (
              validating && <p className="text-xs text-muted-foreground">Checking…</p>
            )}

            <div className="space-y-2">
              <h3 className="text-[10px] font-semibold uppercase tracking-[0.16em] text-muted-foreground">
                What a rule can do here <span className="ml-1 normal-case tracking-normal">Click a rule to add it.</span>
              </h3>
              <div className="rounded-xl border border-border bg-card p-2">
                {exampleBlocks(current.example).map((b, i) =>
                  b.kind === 'comment' ? (
                    <pre key={i} className="whitespace-pre-wrap px-2 pb-0.5 pt-2 font-mono text-xs italic leading-5 text-faint-foreground first:pt-0.5">
                      {b.text}
                    </pre>
                  ) : (
                    <button
                      key={i}
                      type="button"
                      onClick={() => add(b.text)}
                      title="Add this rule"
                      className="block w-full cursor-pointer overflow-x-auto whitespace-pre rounded-md px-2 py-0.5 text-left font-mono text-xs leading-5 text-code-foreground transition-colors hover:bg-elevated"
                    >
                      {b.text}
                    </button>
                  ),
                )}
              </div>
            </div>

            <dl className="grid gap-x-4 gap-y-2 text-xs leading-5 sm:grid-cols-[5rem_minmax(0,1fr)]">
              {current.actions.includes('deny') && (
                <>
                  <dt className="font-mono text-accent">deny</dt>
                  <dd className="text-muted-foreground">
                    {current.deny}. A reason in quotes after it is what the client is told: <span className="font-mono">deny "Admins only."</span>
                  </dd>
                </>
              )}
              {current.actions.includes('confirm') && (
                <>
                  <dt className="font-mono text-accent">confirm</dt>
                  <dd className="text-muted-foreground">
                    asks the user first and goes ahead once they agree. Values in braces are filled in: <span className="font-mono">confirm "Delete {'{args.id}'}?"</span>
                  </dd>
                </>
              )}
              {current.actions.includes('set') && (
                <>
                  <dt className="font-mono text-accent">set</dt>
                  <dd className="font-mono text-muted-foreground">
                    {current.sets.map((s) => (
                      <span key={s.field} title={s.type} className="mr-3 inline-block">
                        {s.field}
                      </span>
                    ))}
                  </dd>
                </>
              )}
              <dt className="font-mono text-accent">read</dt>
              <dd className="font-mono text-muted-foreground">
                {current.reads.map((v) => (
                  <span key={v.name} title={v.doc} className="mr-3 inline-block">
                    {v.name}
                  </span>
                ))}
              </dd>
              <dt className="font-mono text-accent">audit</dt>
              <dd className="text-muted-foreground">
                before when, makes a rule record what it would do and change nothing. A line that starts with # is a comment.
              </dd>
            </dl>
          </div>
        </div>
      )}
    </section>
  )
}
