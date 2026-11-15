# prompts/filter

`prompts/filter` decides which upstream prompts are visible. A denied prompt is hidden from listings and cannot be fetched.

## When it runs

`prompts/filter` runs on each upstream prompt, after [server/config](server-config.md) and before [prompts/modify](prompts-modify.md). It runs on every `prompts/list` and on every `prompts/get`, to find the exposed prompt.

## Variables

| Variable | Type | Description |
| --- | --- | --- |
| `name` | string | The prompt name |
| `title` | string | The prompt title, or `''` |
| `description` | string | The prompt description, or `''` |
| `arguments` | list | The prompt's arguments, each a map with `name`, and optionally `description` and `required` |
| `meta` | map | The prompt's `_meta`, or `{}` |
| `icons` | list | The prompt's icons, or `[]` |
| `upstream` | map | The prompt as the upstream sent it |
| `server`, `entitlement`, `caller`, `client`, `route`, `now` | Various | The [common variables](overview.md#common-variables) |

## Fields

| Field | Type | Effect |
| --- | --- | --- |
| `decision` | `'deny'` | Hide the prompt. Stops evaluation |
| `reason` | string | Recorded in audit only |

Every filter rule must set `decision`.

## What the client sees

- `prompts/list` omits the prompt.
- `prompts/get` returns `-32602`, `unknown prompt <name>`, the same as for a prompt that does not exist. The upstream is not called.

If an enforce rule fails to evaluate, the prompt is hidden.

## Nuances and limits

- A deny is final. Write exceptions into the deny condition.
- Filters see the upstream view. A rename in `prompts/modify` does not feed a filter.

## Examples

### Hide prompts by name

```json
{
  "name": "Hide internal",
  "hook": "prompts/filter",
  "when": "name.startsWith('internal_')",
  "then": { "decision": "'deny'" }
}
```

`prompts/list` shows only `summary`. `prompts/get` for `internal_review` returns `-32602`, `unknown prompt internal_review`.

### Hide prompts from API keys

```json
{
  "name": "Users only",
  "hook": "prompts/filter",
  "when": "caller.type == 'key'",
  "then": { "decision": "'deny'" }
}
```

API keys see no prompts from this server. Users see all of them.

### Hide prompts that take a given argument

```json
{
  "name": "No raw SQL",
  "hook": "prompts/filter",
  "when": "arguments.exists(a, a.name == 'sql')",
  "then": { "decision": "'deny'" }
}
```

A prompt with a `sql` argument is hidden. A prompt with only a `topic` argument stays.

## Related pages

- [Hooks and rules overview](overview.md)
- [prompts/modify for names, text and arguments](prompts-modify.md)
- [prompts/get for request decisions](prompts-get.md)
