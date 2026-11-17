# resources/read.post.progress

`resources/read.post.progress` drops or rewords the progress notifications a server sends while it reads a resource.

## When it runs

`resources/read.post.progress` runs once for each progress notification on the reply to a forwarded resource read or directory read, before the notification reaches the client. It works the same way as [tools/call.post.progress](tools-call-post-progress.md): the same conditions for relaying progress, the same writable path and the same limits.

## What a rule can read

| Branch | Notable fields |
| --- | --- |
| `progress` | One notification: `progress.params.progress`, a number. `progress.params.total`, `progress.params.message` and `progress.params.progressToken`, when the server sent them |
| `progress.upstream` | The notification as the server sent it. It never changes |
| `resource` | The listed resource for this URI, or only `resource.uri` when no listing contains it |
| `resource.upstream` | The resource as the server declares it. For a URI the server does not list, it holds only `uri` |
| `request.params` | `request.params.uri`, the URI the read named, as the server knows it |
| `request.upstream` | The read as the server received it: `request.upstream.params.uri` |
| `request` | Also `request.method`, `request.caller`, `request.client`, `request.headers`, `request.route`, `request.flags`, `request.time` |
| `server` | `server.slug` and what the gateway knows about the server |
| `entitlement` | `entitlement.id`, `entitlement.slug`, `entitlement.name` |

`progress.params.message` and `progress.params.total` are absent when the server sent none. Read them with `progress.params.?message.orValue('')`, or guard with `has(progress.params.message)`.

## What a rule can set

| Path | Type | Effect | Stops evaluation |
| --- | --- | --- | --- |
| `progress.params.message` | string | Replace the notification's message | No |

A rule here can deny. It cannot ask the user to confirm.

| Key | Value | Effect | Stops evaluation |
| --- | --- | --- | --- |
| `decision` | `'deny'` | Drop this notification | Yes |
| `reason` | string | A note for the hit record. The client never sees it | No |

## What the client sees

| Outcome | Result |
| --- | --- |
| No rule matches | The notification as the server sent it |
| A rule sets `progress.params.message` | The same notification with the new message. Its numbers are unchanged |
| A rule denies, or fails to evaluate | Nothing for that notification. Later notifications and the contents still arrive |

## Examples

### Replace file paths in progress text

```json
{
  "name": "No file paths in progress",
  "hook": "resources/read.post.progress",
  "when": "resource.uri.startsWith('docs://')",
  "then": { "progress.params.message": "'Reading the document'" }
}
```

A progress notification with the message `reading /srv/docs/a.md` reaches the client as `Reading the document`.

### Drop progress for one folder

```json
{
  "name": "Quiet secrets",
  "hook": "resources/read.post.progress",
  "when": "resource.uri.startsWith('file:///secrets/')",
  "then": { "decision": "'deny'" }
}
```

No progress reaches the client for reads under `file:///secrets/`. The contents still arrive.

## Nuances and limits

- A progress rule can only affect its own notification. It can never refuse the read.
- Notifications already sent cannot be taken back.
- Progress rules have their own time allowance. When it is used up, later notifications are dropped.
- For a URI the server does not list, only `resource.uri` is present. A plain read of any other field fails the rule, which drops the notification.

## Related pages

- [tools/call.post.progress, for the full description](tools-call-post-progress.md)
- [resources/read.pre](resources-read-pre.md)
- [Hooks and rules overview](overview.md)
