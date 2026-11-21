# tools/call.post.progress

`tools/call.post.progress` drops or rewords the progress notifications a server sends while a tool call is running.

## When it runs

A server may answer a call with a stream: progress notifications first, then the result. `tools/call.post.progress` runs once for each progress notification, in the order they arrive, before the notification reaches the client.

The gateway relays progress only when all of these are true:

- the client asked for it, by sending a progress token with the call
- `settings.progress` in [server.settings](server-settings.md) is on, which is the default
- the server speaks MCP 2026-07-28

When no `tools/call.post.progress` rule exists, notifications are relayed exactly as the server sent them. The result at the end of the stream is handled by [tools/call.post.result](tools-call-post-result.md), or by [tools/call.post.elicit](tools-call-post-elicit.md) when the server asks a question.

## What a rule can read

| Branch | Notable fields |
| --- | --- |
| `progress` | One notification: `progress.params.progress`, a number. `progress.params.total`, `progress.params.message` and `progress.params.progressToken`, when the server sent them |
| `progress.upstream` | The notification as the server sent it: `progress.upstream.params`. It never changes |
| `request.params` | `request.params.name` and `request.params.arguments`, under the names the client knows |
| `request.upstream` | The call as the server received it: `request.upstream.params.name` and `request.upstream.params.arguments` |
| `request` | Also `request.method`, `request.caller`, `request.client`, `request.headers`, `request.route`, `request.flags`, `request.time` |
| `tool` | The tool as the client sees it, and `tool.upstream` as the server declares it |
| `server` | `server.id`, `server.slug` and what the gateway knows about the server |

`progress.params.message` and `progress.params.total` are absent when the server sent none. Read them with the [optional forms](overview.md#optional-values): `progress.params.?message.orValue('')`, or guard with `has(progress.params.message)`.

## What a rule can set

| Path | Type | Effect | Stops evaluation |
| --- | --- | --- | --- |
| `progress.params.message` | string | Replace the notification's message. Later rules read the new value | No |

A rule here can deny. It cannot ask the user to confirm.

| Key | Value | Effect | Stops evaluation |
| --- | --- | --- | --- |
| `decision` | `'deny'` | Drop this notification. The client does not receive it | Yes |
| `reason` | string | A note for the hit record. The client never sees it | No |

A rule cannot change `progress.params.progress`, `progress.params.total` or the progress token.

## What the client sees

| Outcome | Result |
| --- | --- |
| No rule matches | The notification as the server sent it, byte for byte |
| A rule sets `progress.params.message` | The same notification with the new message. The numbers, the token and any `_meta` are unchanged |
| A rule denies | Nothing for that notification. Later notifications and the result still arrive |
| A rule fails to evaluate | Nothing for that notification. Later notifications and the result still arrive |

A progress rule can only affect its own notification. It can never refuse the call: the result at the end of the stream is decided by [tools/call.post.result](tools-call-post-result.md) alone.

## Examples

### Remove an internal host name from progress text

```json
{
  "name": "Hide host names",
  "hook": "tools/call.post.progress",
  "when": "progress.params.?message.orValue('').contains('db-internal')",
  "then": { "progress.params.message": "regex.replace(progress.params.?message.orValue(''), 'db-internal-[0-9]+', 'the database')" }
}
```

A notification with the message `Querying db-internal-7` reaches the client as `Querying the database`. A notification with the message `Finishing`, or with no message, is unchanged.

### Drop progress that shows card numbers

```json
{
  "name": "No card numbers in progress",
  "hook": "tools/call.post.progress",
  "when": "progress.params.?message.orValue('').matches('[0-9]{4} [0-9]{4}')",
  "then": { "decision": "'deny'", "reason": "'card number'" }
}
```

A notification with the message `Reading card 4111 1111` never reaches the client. The call continues.

### Keep the numbers, drop the text

```json
{
  "name": "Numbers only",
  "hook": "tools/call.post.progress",
  "when": "has(progress.params.message)",
  "then": { "progress.params.message": "''" }
}
```

The client still gets `progress` and `total` for a progress bar, with an empty message.

## Nuances and limits

- Notifications already sent cannot be taken back. If [tools/call.post.result](tools-call-post-result.md) blocks the result, the client has still received the progress that came before it.
- Progress rules have their own time allowance, separate from the rest of the request. A server that sends a great many notifications can use it up. Later notifications are then dropped, and the result keeps its full allowance.
- A notification that a rule makes larger than the size limit is dropped.
- Log messages and any other notification on the stream are dropped. No rule runs on them.
- A notification that is not a JSON object is dropped when a rule has to read it.
- A plain read of `progress.params.message` or `progress.params.total` fails when the server sent none, which drops the notification. Use the optional forms.
- Audit-mode rules never change a notification. They record what they would have done.

## Related pages

- [Hooks and rules overview](overview.md)
- [server.settings for the progress switch](server-settings.md)
- [tools/call.post.result for shaping the result](tools-call-post-result.md)
- [prompts/get.post.progress](prompts-get-post-progress.md) and [resources/read.post.progress](resources-read-post-progress.md)
