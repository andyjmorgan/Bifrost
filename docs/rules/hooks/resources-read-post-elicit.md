# resources/read.post.elicit

`resources/read.post.elicit` decides whether a server may ask the user a question during a read, and can reword the question.

## When it runs

`resources/read.post.elicit` runs when the server answers a forwarded `resources/read` or directory read with `resultType: "input_required"`. It runs once for each question in the reply, in key order. [resources/read.post.result](resources-read-post-result.md) does not run on that reply. A reply with only a `requestState` and no questions is wrapped and returned without running rules.

A server asks for input only if the gateway advertised elicitation to it, which [server.session](server-session.md) controls. Gateway confirmations from [resources/read.pre](resources-read-pre.md) always resolve before the server is called.

## What a rule can read

| Branch | Notable fields |
| --- | --- |
| `elicitation` | One question. `elicitation.key`, the key the server gave it. `elicitation.method`, always `elicitation/create`. `elicitation.params`, with `mode`, `message`, `requestedSchema`, `url` and `elicitationId` where the server sent them |
| `elicitation.upstream` | The question as the server asked it: `elicitation.upstream.params`. It never changes |
| `resource` | The listed resource for this URI, or only `resource.uri` when no listing contains it |
| `resource.upstream` | The resource as the server declares it |
| `request.params` | `request.params.uri`, the URI the read named, as the server knows it |
| `request.upstream` | The read as the server received it: `request.upstream.params.uri` |
| `request` | Also `request.method`, `request.caller`, `request.client`, `request.headers`, `request.route`, `request.flags`, `request.time` |
| `server` | `server.id`, `server.slug` and what the gateway knows about the server |

Every field of `elicitation.params` is optional. Read them with the [optional forms](overview.md#optional-values).

## What a rule can set

| Path | Type | Effect | Stops evaluation |
| --- | --- | --- | --- |
| `elicitation.params.message` | string | Replace the message the user sees | No |

A rule here can deny. It cannot ask the user to confirm.

| Key | Value | Effect | Stops evaluation |
| --- | --- | --- | --- |
| `decision` | `'deny'` | Refuse the whole round of questions and end the read | Yes |
| `reason` | string | The error message the client receives with a deny | No |

## What the client sees

| Outcome | Result |
| --- | --- |
| Allowed | `{"resultType": "input_required", "inputRequests": {...}, "requestState": "..."}` |
| Denied | `-32602` with `reason` as the message. Without a reason, the message is `The upstream server asked for input, which policy does not allow.` |
| A rule fails to evaluate | `-32602`, `The upstream server asked for input, which policy does not allow.` |
| Shaped reply over the size limit | `-32013`, `result exceeds gateway size limit after shaping` |

The gateway keeps the server's `requestState`, if any, in its own store and gives the client an opaque key. The client never sees the server's state. The key works once: a second retry with it returns `invalid request state`. On the retry, it forwards only the server's answers and, when the server sent one, its original state. The answers are forwarded even when the server sent no state.

## Examples

### Label questions from the server

```json
{
  "name": "Label drive prompts",
  "hook": "resources/read.post.elicit",
  "when": "has(elicitation.params.message)",
  "then": { "elicitation.params.message": "'Drive asks: ' + elicitation.params.message" }
}
```

A question with the message `Why do you need this file?` reaches the client as `Drive asks: Why do you need this file?`. The retry forwards the user's answer and the server's state.

### Refuse questions on reads

```json
{
  "name": "No upstream prompts on reads",
  "hook": "resources/read.post.elicit",
  "when": "true",
  "then": { "decision": "'deny'", "reason": "'This server may not ask for input during reads.'" }
}
```

The client receives `{"code": -32602, "message": "This server may not ask for input during reads."}`.

## Nuances and limits

- A deny on any one question refuses the whole round.
- Only `elicitation/create` requests reach this hook. A request for sampling or roots is refused, and no rule runs on it.
- The server's keys must not start with `bifrost.`.
- For a URI that no listing contains, a plain read of `resource.name` or any field other than `resource.uri` fails the rule, which refuses the round.

## Related pages

- [Hooks and rules overview](overview.md)
- [tools/call.post.elicit, the matching hook for tool calls](tools-call-post-elicit.md)
- [server.session for advertising elicitation to the server](server-session.md)
