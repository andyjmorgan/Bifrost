# resources/progress

`resources/progress` drops or rewords the progress events an upstream server sends while it reads a resource.

## When it runs

`resources/progress` runs once for each progress event on the reply to a forwarded resource read or directory read, before the event reaches the client. It works the same way as [tools/progress](tools-progress.md): the same conditions for relaying progress, the same fields and the same limits.

## Variables

| Variable | Type | Description |
| --- | --- | --- |
| `progress` | map | The event: `progress.progress` (number), `progress.total` (number, present only when the upstream sent it) and `progress.message` (string, `''` when the upstream sent none) |
| `args` | map | `{"uri": <the URI as the upstream knows it>}` |
| `uri`, `name`, `title`, `description`, `mimeType`, `annotations`, `meta`, `size`, `icons` | Various | The resource. For a URI the server does not list, only `uri` is available: reading any other field fails the rule, which drops the event |
| `upstream` | map | The resource as the upstream sent it. For a URI the server does not list, it holds only `uri` |
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
  "name": "No file paths in progress",
  "hook": "resources/progress",
  "when": "uri.startsWith('docs://')",
  "then": { "message": "'Reading the document'" }
}
```

A progress event with the message `reading /srv/docs/a.md` reaches the client as `Reading the document`.

## Related pages

- [tools/progress, for the full description](tools-progress.md)
- [resources/read](resources-read.md)
- [Hooks and rules overview](overview.md)
