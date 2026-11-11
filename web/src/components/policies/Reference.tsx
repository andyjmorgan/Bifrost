// The CEL cheat sheet shown under the rule list. Mirrors the reference in the policies contract.

const VARIABLES: [string, string][] = [
  ['tool.name', 'The upstream tool name, never prefixed: "flip", not "coinserver__flip".'],
  ['tool.fullName', 'slug__name on every route, for rules written against what clients see.'],
  ['tool.title, tool.description', 'Strings, "" when unset.'],
  ['tool.annotations.readOnlyHint', 'Bool, default false.'],
  ['tool.annotations.destructiveHint', 'Bool, default true.'],
  ['tool.annotations.idempotentHint', 'Bool, default false.'],
  ['tool.annotations.openWorldHint', 'Bool, default true.'],
  ['tool.annotations.title', 'String.'],
  ['tool.annotationsRaw', 'Annotations as the upstream sent them. Test with has(tool.annotationsRaw.destructiveHint).'],
  ['tool.inputSchema, tool.outputSchema', 'Maps. outputSchema is {} when absent.'],
  ['tool.icons', 'List.'],
  ['tool.meta', 'The _meta object, {} when absent.'],
  ['tool.execution', 'Only from 2025-11-25 servers: {taskSupport: "forbidden" | "optional" | "required"}.'],
  ['tool.visible, tool.confirm', 'The decision so far, after earlier rules.'],
  ['server.slug, server.name, server.description', 'Strings.'],
  ['server.instructions, server.protocolVersion, server.url', 'Strings.'],
  ['route', '"mux" or "direct".'],
]

const FUNCTIONS = ['startsWith', 'endsWith', 'contains', 'matches (RE2)', 'lowerAscii()', 'in', 'has(x.y)', '&&', '||', '!', '? :']

const EXAMPLES: { when: string; then?: [string, string][]; note?: string }[] = [
  {
    when: 'tool.name.startsWith("delete") || (tool.annotations.destructiveHint && !tool.annotations.readOnlyHint)',
    then: [['visible', 'false']],
  },
  {
    when: 'server.slug == "telegram"',
    then: [
      ['confirm', 'true'],
      ['confirmMessage', '"Send a Telegram message via " + tool.name + "?"'],
    ],
  },
  { when: 'tool.description.lowerAscii().contains("deprecated")', then: [['visible', 'false']] },
  {
    when: 'tool.inputSchema.properties.exists(k, has(tool.inputSchema.properties[k]["x-mcp-header"]))',
    note: 'Not "x-mcp-header" in string(tool.inputSchema).',
  },
  { when: 'has(tool.meta["com.example/tier"]) && tool.meta["com.example/tier"] == "gold"' },
  { when: 'tool.name.matches("^(get|list|read)_")', then: [['annotations.readOnlyHint', 'true']] },
]

const label = 'text-[10px] font-semibold uppercase tracking-[0.16em] text-muted-foreground'

export function CelReference() {
  return (
    <details className="group text-sm">
      <summary className="cursor-pointer select-none text-xs font-semibold uppercase tracking-[0.16em] text-muted-foreground hover:text-foreground">
        Reference
      </summary>
      <div className="mt-3 space-y-5 rounded-2xl border border-border bg-card p-5">
        <p className="text-xs leading-5 text-muted-foreground">
          Every tool starts visible, or hidden when the policy is an allow-list. Rules run top to bottom; a matching rule applies its assignments
          before the next rule sees the tool. Values are CEL expressions: <code className="font-mono">true</code>,{' '}
          <code className="font-mono">"text"</code>, or a template like <code className="font-mono">tool.description + " (read-only)"</code>.
        </p>
        <section className="space-y-2">
          <h3 className={label}>Variables</h3>
          <dl className="divide-y divide-border rounded-lg border border-border">
            {VARIABLES.map(([k, v]) => (
              <div key={k} className="grid gap-1 px-3 py-2 sm:grid-cols-[minmax(0,1fr)_minmax(0,1.2fr)] sm:gap-4">
                <dt className="break-words font-mono text-xs text-foreground">{k}</dt>
                <dd className="text-xs leading-5 text-muted-foreground">{v}</dd>
              </div>
            ))}
          </dl>
        </section>
        <section className="space-y-2">
          <h3 className={label}>Functions and operators</h3>
          <div className="flex flex-wrap gap-1">
            {FUNCTIONS.map((f) => (
              <code key={f} className="rounded-md border border-border bg-elevated px-2 py-0.5 font-mono text-[11px] text-muted-foreground">
                {f}
              </code>
            ))}
          </div>
        </section>
        <section className="space-y-2">
          <h3 className={label}>Examples</h3>
          <div className="space-y-2">
            {EXAMPLES.map((e) => (
              <div key={e.when} className="rounded-lg bg-code p-3 font-mono text-xs leading-5 text-code-foreground">
                <div className="break-words">
                  <span className="text-faint-foreground">when </span>
                  {e.when}
                </div>
                {e.then?.map(([f, v]) => (
                  <div key={f} className="break-words">
                    <span className="text-faint-foreground">then </span>
                    {f} = {v}
                  </div>
                ))}
                {e.note && <div className="mt-1 font-sans text-faint-foreground">{e.note}</div>}
              </div>
            ))}
          </div>
        </section>
      </div>
    </details>
  )
}
