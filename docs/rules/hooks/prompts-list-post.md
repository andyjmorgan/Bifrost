# prompts/list.post

`prompts/list.post` decides which upstream prompts are visible, and how a visible prompt is presented: its name, text and argument names. A hidden prompt is left out of listings and cannot be fetched. The gateway reverses renames when the client fetches the prompt.

## When it runs

`prompts/list.post` runs on each upstream prompt, after [server.settings](server-settings.md). It runs on every `prompts/list` and on every `prompts/get`, to find the exposed prompt.

A client can only get what it can list. A `prompts/get` first runs this hook for the prompt it names, and [prompts/get.pre](prompts-get-pre.md) rules see the prompt as this hook left it.

After the last rule, the gateway checks for name collisions. On the mux route, the gateway adds the `<slug>__` prefix after this hook.

## Order of rules

The rules run on each prompt from top to bottom, in the order you write them. A rule can do one of two things:

- hide the prompt, by setting `decision: 'deny'`
- change the prompt, by setting any of the other [fields](#fields)

Changes chain. A later rule reads the prompt as earlier rules left it, so a hide rule written after a rename sees the new `name`. `upstream.name` is always the server's own name.

The first deny hides the prompt and stops the enforce rules. Audit rules still run, so the audit record is complete.

Writing hide rules first is cheaper, because the gateway never changes a hidden prompt. It is not required.

A rule that fails also hides the prompt. The prompt cannot be fetched, and the listing reports the problem to administrators. A rule fails when it fails to evaluate, names an argument that does not exist, or renames an argument onto an existing name or an empty name.

## Hiding a prompt

A rule hides a prompt by setting `decision` to `'deny'`. It can also set `reason`.

- `prompts/list` omits the prompt.
- `prompts/get` returns `-32602`, `unknown prompt <name>`, the same as for a prompt that does not exist. The upstream is not called.
- A deny is final. Write exceptions into the deny condition.
- A rule that sets both `decision` and other fields hides the prompt. Its other fields are discarded.

## Changing a prompt

A rule changes a prompt by setting any field other than `decision` and `reason`.

- `prompts/list` shows the changed prompt.
- The old name of a renamed prompt returns `-32602`, `unknown prompt <name>`.
- A `prompts/get` that uses a renamed argument's upstream name returns `-32602`, `argument "<a>" is not available on this prompt`.

## Variables

Each variable holds the prompt as earlier rules left it. Before any rule changes the prompt, that is the prompt as the upstream sent it.

| Variable | Type | Description |
| --- | --- | --- |
| `name` | string | The current prompt name |
| `title` | string | The current prompt title, or `''` |
| `description` | string | The current prompt description, or `''` |
| `arguments` | list | The current arguments, with renames and description changes applied. Each is a map with `name`, and optionally `description` and `required` |
| `meta` | map | The current `_meta`, or `{}` |
| `icons` | list | The prompt's icons, or `[]` |
| `upstream` | map | The prompt as the upstream sent it. It never changes |
| `server`, `entitlement`, `caller`, `client`, `route`, `flags`, `now` | Various | The [common variables](overview.md#common-variables) |

## Fields

`<a>` is an argument's current name, after any earlier rename.

| Field | Type | Effect | Reversal on `prompts/get` |
| --- | --- | --- | --- |
| `decision` | `'deny'` | Hide the prompt. Stops the enforce rules | None |
| `reason` | string | Recorded in audit only | None |
| `name` | string | Rename the prompt | The exposed name maps back to the upstream name |
| `title`, `description` | string | Replace the text | None |
| `arguments.<a>.name` | string | Rename an argument | The argument name maps back |
| `arguments.<a>.description` | string | Replace the argument description | None |
| `meta.<key>` | any | Set a `_meta` key. `null` removes the key | None |

Only `decision` stops evaluation. Every other field chains.

## Examples

### Hide prompts by name

```json
{
  "name": "Hide internal",
  "hook": "prompts/list.post",
  "when": "name.startsWith('internal_')",
  "then": { "decision": "'deny'" }
}
```

`prompts/list` shows only `summary`. `prompts/get` for `internal_review` returns `-32602`, `unknown prompt internal_review`.

### Hide prompts from API keys

```json
{
  "name": "Users only",
  "hook": "prompts/list.post",
  "when": "caller.type == 'key'",
  "then": { "decision": "'deny'" }
}
```

API keys see no prompts from this server. Users see all of them.

### Hide prompts that take a given argument

```json
{
  "name": "No raw SQL",
  "hook": "prompts/list.post",
  "when": "arguments.exists(a, a.name == 'sql')",
  "then": { "decision": "'deny'" }
}
```

A prompt with a `sql` argument is hidden. A prompt with only a `topic` argument stays.

### Rename a prompt and an argument

```json
{
  "name": "Rename summary",
  "hook": "prompts/list.post",
  "when": "upstream.name == 'confluence_summary'",
  "then": {
    "name": "'summary'",
    "arguments.space.name": "'area'",
    "arguments.space.description": "'The wiki area to summarize, such as ENG.'"
  }
}
```

The prompt is listed as `summary` with the argument `{"name": "area", "required": true, "description": "The wiki area to summarize, such as ENG."}`. A `prompts/get` for `summary` with `{"area": "ENG"}` reaches the upstream as `confluence_summary` with `{"space": "ENG"}`. Sending `space` returns `-32602`.

### Rewrite descriptions

```json
{
  "name": "Neutral wording",
  "hook": "prompts/list.post",
  "when": "true",
  "then": { "description": "regex.replace(description, '(?i)confluence', 'the wiki')" }
}
```

The description `Summarize a Confluence space` becomes `Summarize a the wiki space`. Plain replacement does not fix grammar, so check the result in preview.

### Prefix titles, falling back to the name

```json
{
  "name": "Wiki titles",
  "hook": "prompts/list.post",
  "when": "true",
  "then": { "title": "'Wiki: ' + (title != '' ? title : name)" }
}
```

A prompt titled `Page review` becomes `Wiki: Page review`. A prompt without a title, named `summary`, becomes `Wiki: summary`.

## Nuances and limits

- A hide rule reads the prompt as earlier rules left it. A rename in an earlier rule feeds a later hide rule. To match the server's own name whatever earlier rules did, read `upstream.name`.
- Two prompts with the same final name are both hidden.
- A prompt with an empty final name is hidden.
- Arguments cannot be hidden, pinned or given defaults. To fill an argument, set `args.<a>` in [prompts/get.pre](prompts-get-pre.md).

## Related pages

- [Hooks and rules overview](overview.md)
- [prompts/get.pre for request decisions and argument patches](prompts-get-pre.md)
