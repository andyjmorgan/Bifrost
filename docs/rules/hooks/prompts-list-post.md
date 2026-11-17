# prompts/list.post

`prompts/list.post` decides which of a server's prompts are visible, and how a visible prompt is presented: its name, text and argument names. A hidden prompt is left out of listings and cannot be fetched. The gateway reverses renames when the client fetches the prompt.

## When it runs

`prompts/list.post` runs on each of the server's prompts, after [server.settings](server-settings.md). It runs on every `prompts/list` and on every `prompts/get`, to find the prompt the client named.

A client can only get what it can list. A `prompts/get` first runs this hook for the prompt it names, and [prompts/get.pre](prompts-get-pre.md) rules see the prompt as this hook left it.

After the last rule, the gateway checks for name collisions. On the mux route, the gateway adds the `<slug>__` prefix after this hook.

## Order of rules

The rules run on each prompt from top to bottom, in the order you write them. A rule can do one of two things:

- hide the prompt, by setting `decision: 'deny'`
- change the prompt, by setting any of the [writable paths](#what-a-rule-can-set)

Changes chain. A later rule reads the prompt as earlier rules left it, so a hide rule written after a rename sees the new `prompt.name`. `prompt.upstream.name` is always the server's own name.

The first deny hides the prompt and stops the enforce rules. Audit rules still run, so the audit record is complete.

Writing hide rules first is cheaper, because the gateway never changes a hidden prompt. It is not required.

A rule that fails also hides the prompt. The prompt cannot be fetched, and the listing reports the problem to administrators. A rule fails when it fails to evaluate, names an argument that does not exist, or renames an argument onto an existing name or an empty name.

## What a rule can read

| Branch | Notable fields |
| --- | --- |
| `prompt` | The prompt as earlier rules left it: `prompt.name`, `prompt.title`, `prompt.description`, `prompt.arguments`, `prompt.icons`, `prompt._meta` |
| `prompt.arguments` | MCP's list of arguments, with renames and description changes applied. Each entry has `name`, and `title`, `description` and `required` where the server sent them |
| `prompt.params` | The gateway's view of the same arguments, by name |
| `prompt.upstream` | The prompt as the server declares it. It never changes |
| `request` | `request.caller`, `request.client`, `request.headers`, `request.route`, `request.flags`, `request.time`. `request.params` holds only `_meta` here |
| `server` | `server.slug` and what the gateway knows about the server |
| `entitlement` | `entitlement.id`, `entitlement.slug`, `entitlement.name` |

Only `prompt.name` is always present. Read the other fields with the [optional forms](overview.md#optional-values):

| Read | Use |
| --- | --- |
| A title, falling back to the name | `prompt.?title.orValue(prompt.name)` |
| The arguments, or an empty list | `prompt.?arguments.orValue([])` |
| Whether there is a description | `has(prompt.description)` |

## What a rule can set

`<a>` is an argument's current name, after any earlier rename.

| Path | Type | Effect | Reversal on `prompts/get` |
| --- | --- | --- | --- |
| `prompt.name` | string | Rename the prompt | The name the client sends maps back to the server's name |
| `prompt.title`, `prompt.description` | string | Replace the text | None |
| `prompt.params.<a>.name` | string | Rename an argument | The argument name maps back |
| `prompt.params.<a>.description` | string | Replace the argument description | None |
| `prompt._meta.<key>` | any | Set a `_meta` key. `null` removes the key | None |

A rule here can deny. It cannot ask the user to confirm.

| Key | Value | Effect |
| --- | --- | --- |
| `decision` | `'deny'` | Hide the prompt. Stops the enforce rules |
| `reason` | string | Recorded in audit only |

Only `decision` stops evaluation. Every other path chains.

Argument shaping lives at `prompt.params`, not at `prompt.arguments`. On the wire, `arguments` is a list. Addressing an argument by name is the gateway's view. A rule cannot set `prompt.arguments` or anything under `prompt.upstream`.

## What the client sees

When a rule hides a prompt:

- `prompts/list` omits the prompt.
- `prompts/get` returns `-32602`, `unknown prompt <name>`, the same as for a prompt that does not exist. The server is not called.
- A deny is final. Write exceptions into the deny condition.
- A rule that sets both `decision` and other paths hides the prompt. Its other values are discarded.

When a rule changes a prompt:

- `prompts/list` shows the changed prompt.
- The old name of a renamed prompt returns `-32602`, `unknown prompt <name>`.
- A `prompts/get` that uses the server's name of a renamed argument returns `-32602`, `argument "<a>" is not available on this prompt`.

## Examples

### Hide prompts by name

```json
{
  "name": "Hide internal",
  "hook": "prompts/list.post",
  "when": "prompt.name.startsWith('internal_')",
  "then": { "decision": "'deny'" }
}
```

`prompts/list` shows only `summary`. `prompts/get` for `internal_review` returns `-32602`, `unknown prompt internal_review`.

### Hide prompts from API keys

```json
{
  "name": "Users only",
  "hook": "prompts/list.post",
  "when": "request.caller.type == 'apikey'",
  "then": { "decision": "'deny'" }
}
```

API keys see no prompts from this server. Other callers see all of them.

### Hide prompts that take a given argument

```json
{
  "name": "No raw SQL",
  "hook": "prompts/list.post",
  "when": "prompt.?arguments.orValue([]).exists(a, a.name == 'sql')",
  "then": { "decision": "'deny'" }
}
```

A prompt with a `sql` argument is hidden. A prompt with only a `topic` argument stays. So does a prompt with no arguments.

### Rename a prompt and an argument

```json
{
  "name": "Rename summary",
  "hook": "prompts/list.post",
  "when": "prompt.upstream.name == 'confluence_summary'",
  "then": {
    "prompt.name": "'summary'",
    "prompt.params.space.name": "'area'",
    "prompt.params.space.description": "'The wiki area to summarize, such as ENG.'"
  }
}
```

The prompt is listed as `summary` with the argument `{"name": "area", "required": true, "description": "The wiki area to summarize, such as ENG."}`. A `prompts/get` for `summary` with `{"area": "ENG"}` reaches the server as `confluence_summary` with `{"space": "ENG"}`. Sending `space` returns `-32602`.

### Prefix titles, falling back to the name

```json
{
  "name": "Wiki titles",
  "hook": "prompts/list.post",
  "when": "true",
  "then": { "prompt.title": "'Wiki: ' + prompt.?title.orValue(prompt.name)" }
}
```

A prompt titled `Page review` becomes `Wiki: Page review`. A prompt without a title, named `summary`, becomes `Wiki: summary`.

## Nuances and limits

- A hide rule reads the prompt as earlier rules left it. A rename in an earlier rule feeds a later hide rule. To match the server's own name whatever earlier rules did, read `prompt.upstream.name`.
- A plain read of an absent field fails the rule and hides the prompt. `prompt.arguments.exists(...)` hides every prompt that has no arguments. `regex.replace(prompt.description, ...)` needs `has(prompt.description)` in its condition.
- Two prompts with the same final name are both hidden.
- A prompt with an empty final name is hidden.
- Arguments cannot be hidden, pinned or given defaults. To fill an argument, set `request.params.arguments.<a>` in [prompts/get.pre](prompts-get-pre.md).

## Related pages

- [Hooks and rules overview](overview.md)
- [prompts/get.pre for request decisions and argument changes](prompts-get-pre.md)
