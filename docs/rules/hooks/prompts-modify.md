# prompts/modify

`prompts/modify` changes how a visible prompt is presented: its name, text and argument names. The gateway reverses renames when the client fetches the prompt.

## When it runs

`prompts/modify` runs on each prompt that passed [prompts/filter](prompts-filter.md), on every `prompts/list` and every `prompts/get`. After the last rule, the gateway checks for name collisions. On the mux route, the gateway adds the `<slug>__` prefix after this hook.

On `prompts/get`, [prompts/get](prompts-get.md) rules see the prompt as this hook left it.

## Variables

| Variable | Type | Description |
| --- | --- | --- |
| `name`, `title`, `description` | string | The current values |
| `arguments` | list | The current arguments, with renames and description changes applied |
| `meta` | map | The current `_meta`, or `{}` |
| `icons` | list | The prompt's icons, or `[]` |
| `upstream` | map | The prompt as the upstream sent it. It never changes |
| `server`, `entitlement`, `caller`, `client`, `route`, `flags`, `now` | Various | The [common variables](overview.md#common-variables) |

## Fields

`<a>` is an argument's current name, after any earlier rename.

| Field | Type | Effect | Reversal on `prompts/get` |
| --- | --- | --- | --- |
| `name` | string | Rename the prompt | The exposed name maps back to the upstream name |
| `title`, `description` | string | Replace the text | None |
| `arguments.<a>.name` | string | Rename an argument | The argument name maps back |
| `arguments.<a>.description` | string | Replace the argument description | None |
| `meta.<key>` | any | Set a `_meta` key. `null` removes the key | None |

No field stops evaluation. Changes chain.

## What the client sees

- `prompts/list` shows the modified prompt.
- The old name of a renamed prompt returns `-32602`, `unknown prompt <name>`.
- A `prompts/get` that uses a renamed argument's upstream name returns `-32602`, `argument "<a>" is not available on this prompt`.

A rule that fails hides the prompt, and the listing reports the problem to administrators. Rules fail when they name an argument that does not exist, or rename an argument onto an existing name or an empty name.

## Nuances and limits

- Two prompts with the same final name are both hidden.
- A prompt with an empty final name is hidden.
- Arguments cannot be hidden, pinned or given defaults. To fill an argument, set `args.<a>` in [prompts/get](prompts-get.md).
- Modify rules cannot change visibility. Use [prompts/filter](prompts-filter.md).

## Examples

### Rename a prompt and an argument

```json
{
  "name": "Rename summary",
  "hook": "prompts/modify",
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
  "hook": "prompts/modify",
  "when": "true",
  "then": { "description": "regex.replace(description, '(?i)confluence', 'the wiki')" }
}
```

The description `Summarize a Confluence space` becomes `Summarize a the wiki space`. Plain replacement does not fix grammar, so check the result in preview.

### Prefix titles, falling back to the name

```json
{
  "name": "Wiki titles",
  "hook": "prompts/modify",
  "when": "true",
  "then": { "title": "'Wiki: ' + (title != '' ? title : name)" }
}
```

A prompt titled `Page review` becomes `Wiki: Page review`. A prompt without a title, named `summary`, becomes `Wiki: summary`.

## Related pages

- [Hooks and rules overview](overview.md)
- [prompts/filter for visibility](prompts-filter.md)
- [prompts/get for argument patches](prompts-get.md)
