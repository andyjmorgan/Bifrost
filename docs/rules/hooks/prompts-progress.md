# prompts/progress

`prompts/progress` drops or rewords the progress events an upstream server sends while it builds a prompt.

## When it runs

`prompts/progress` runs once for each progress event on the reply to a forwarded `prompts/get`, before the event reaches the client. It works the same way as [tools/progress](tools-progress.md): the same conditions for relaying progress, the same fields and the same limits.

## Variables

| Variable | Type | Description |
| --- | --- | --- |
| `progress` | map | The event: `progress.progress` (number), `progress.total` (number, present only when the upstream sent it) and `progress.message` (string, `''` when the upstream sent none) |
| `args` | map | The exact arguments forwarded upstream |
| `name`, `title`, `description`, `arguments`, `meta`, `icons` | Various | The exposed prompt |
| `upstream` | map | The prompt as the upstream sent it |
| `server`, `entitlement`, `caller`, `client`, `route`, `flags`, `now` | Various | The [common variables](overview.md#common-variables) |

## Fields

| Field | Type | Effect | Stops evaluation |
| --- | --- | --- | --- |
| `decision` | `'deny'` | Drop this event | Yes |
| `reason` | string | A note for the hit record | No |
| `message` | string | Replace the event's message | No |

## Example

```json
{
  "name": "Plain prompt progress",
  "hook": "prompts/progress",
  "when": "name == 'export_space'",
  "then": { "message": "'Building the prompt'" }
}
```

Every progress event for `export_space` reaches the client with the message `Building the prompt`. Its numbers are unchanged.

## Related pages

- [tools/progress, for the full description](tools-progress.md)
- [prompts/get](prompts-get.md)
- [Hooks and rules overview](overview.md)
