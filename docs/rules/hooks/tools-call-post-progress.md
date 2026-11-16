# tools/call.post.progress

`tools/call.post.progress` drops or rewords the progress events an upstream server sends while a tool call is running.

## When it runs

A server may answer a call with a stream: progress events first, then the result. `tools/call.post.progress` runs once for each progress event, in the order they arrive, before the event reaches the client.

The gateway relays progress only when all of these are true:

- the client asked for it, by sending a progress token with the call
- the `progress` switch in [server.settings](server-settings.md) is on, which is the default
- the upstream speaks MCP 2026-07-28

When no `tools/call.post.progress` rule exists, events are relayed exactly as the upstream sent them. The result at the end of the stream is handled by [tools/call.post.result](tools-call-post-result.md), or by [tools/call.post.elicit](tools-call-post-elicit.md) when the upstream asks for input.

## Variables

| Variable | Type | Description |
| --- | --- | --- |
| `progress` | map | The event: `progress.progress` (number), `progress.total` (number, present only when the upstream sent it) and `progress.message` (string, `''` when the upstream sent none) |
| `args` | map | The exact arguments forwarded upstream, under upstream names |
| `name`, `title`, `description`, `annotations`, `annotationsRaw`, `meta`, `inputSchema`, `outputSchema`, `icons` | Various | The exposed tool |
| `upstream` | map | The tool as the upstream sent it |
| `server`, `entitlement`, `caller`, `client`, `route`, `flags`, `now` | Various | The [common variables](overview.md#common-variables) |

## Fields

| Field | Type | Effect | Stops evaluation |
| --- | --- | --- | --- |
| `decision` | `'deny'` | Drop this event. The client does not receive it | Yes |
| `reason` | string | A note for the hit record. The client never sees it | No |
| `message` | string | Replace the event's message. Later rules read the new value through `progress.message` | No |

A rule cannot change `progress.progress`, `progress.total` or the progress token.

## What the client sees

| Outcome | Result |
| --- | --- |
| No rule matches | The event as the upstream sent it, byte for byte |
| A rule sets `message` | The same event with the new message. The numbers, the token and any `_meta` are unchanged |
| A rule denies | Nothing for that event. Later events and the result still arrive |
| A rule fails to evaluate | Nothing for that event. Later events and the result still arrive |

A progress rule can only affect its own event. It can never refuse the call: the result at the end of the stream is decided by [tools/call.post.result](tools-call-post-result.md) alone.

## Nuances and limits

- Events already sent cannot be taken back. If [tools/call.post.result](tools-call-post-result.md) refuses the result, the client has still received the progress that came before it.
- Progress rules have their own time allowance, separate from the rest of the request. A server that sends a great many events can use it up; later events are then dropped, and the result keeps its full allowance.
- An event that a rule makes larger than the size limit is dropped.
- Log messages and any other notification on the stream are dropped. No rule runs on them.
- An event that is not a JSON object is dropped when a rule has to read it.
- Use `has(progress.total)` before reading `progress.total`. Reading it when the upstream sent none fails the rule, which drops the event.
- Audit-mode rules never change an event. They record what they would have done.

## Examples

### Remove an internal host name from progress text

```json
{
  "name": "Hide host names",
  "hook": "tools/call.post.progress",
  "when": "progress.message.contains('db-internal')",
  "then": { "message": "regex.replace(progress.message, 'db-internal-[0-9]+', 'the database')" }
}
```

An event with the message `Querying db-internal-7` reaches the client as `Querying the database`. An event with the message `Finishing` is unchanged.

### Drop progress that shows card numbers

```json
{
  "name": "No card numbers in progress",
  "hook": "tools/call.post.progress",
  "when": "progress.message.matches('[0-9]{4} [0-9]{4}')",
  "then": { "decision": "'deny'", "reason": "'card number'" }
}
```

An event with the message `Reading card 4111 1111` never reaches the client. The call continues.

### Keep the numbers, drop the text

```json
{
  "name": "Numbers only",
  "hook": "tools/call.post.progress",
  "when": "progress.message != ''",
  "then": { "message": "''" }
}
```

The client still gets `progress` and `total` for a progress bar, with an empty message.

## Related pages

- [Hooks and rules overview](overview.md)
- [server.settings for the progress switch](server-settings.md)
- [tools/call.post.result for shaping the result](tools-call-post-result.md)
- [prompts/get.post.progress](prompts-get-post-progress.md) and [resources/read.post.progress](resources-read-post-progress.md)
