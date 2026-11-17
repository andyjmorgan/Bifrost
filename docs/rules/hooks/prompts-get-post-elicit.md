# prompts/get.post.elicit

`prompts/get.post.elicit` decides whether a server may ask the user a question during `prompts/get`, and can reword the question.

## When it runs

`prompts/get.post.elicit` runs when the server answers a forwarded `prompts/get` with `resultType: "input_required"`. It runs once for each question in the reply, in key order. A reply with only a `requestState` and no questions is wrapped and returned without running rules.

A server asks for input only if the gateway advertised elicitation to it, which [server.session](server-session.md) controls. Gateway confirmations from [prompts/get.pre](prompts-get-pre.md) always resolve before the server is called.

## What a rule can read

| Branch | Notable fields |
| --- | --- |
| `elicitation` | One question. `elicitation.key`, the key the server gave it. `elicitation.method`, always `elicitation/create`. `elicitation.params`, with `mode`, `message`, `requestedSchema`, `url` and `elicitationId` where the server sent them |
| `elicitation.upstream` | The question as the server asked it: `elicitation.upstream.params`. It never changes |
| `request.params` | `request.params.name` and `request.params.arguments`, under the names the client knows, as the `prompts/get.pre` rules left them |
| `request.upstream` | The request as the server received it: `request.upstream.params.name` and `request.upstream.params.arguments`, under the server's names |
| `request` | Also `request.method`, `request.caller`, `request.client`, `request.headers`, `request.route`, `request.flags`, `request.time` |
| `prompt` | The prompt as the client sees it, and `prompt.upstream` as the server declares it |
| `server` | `server.slug` and what the gateway knows about the server |
| `entitlement` | `entitlement.id`, `entitlement.slug`, `entitlement.name` |

Every field of `elicitation.params` is optional. Read them with the [optional forms](overview.md#optional-values).

## What a rule can set

| Path | Type | Effect | Stops evaluation |
| --- | --- | --- | --- |
| `elicitation.params.message` | string | Replace the message the user sees | No |

A rule here can deny. It cannot ask the user to confirm.

| Key | Value | Effect | Stops evaluation |
| --- | --- | --- | --- |
| `decision` | `'deny'` | Refuse the whole round of questions and end the request | Yes |
| `reason` | string | The error message the client receives with a deny | No |

## What the client sees

| Outcome | Result |
| --- | --- |
| Allowed | `{"resultType": "input_required", "inputRequests": {...}, "requestState": "..."}` |
| Denied | `-32602` with `reason` as the message. Without a reason, the message is `The upstream server asked for input, which policy does not allow.` |
| A rule fails to evaluate | `-32602`, `The upstream server asked for input, which policy does not allow.` |

The gateway keeps the server's `requestState`, if any, in its own store and gives the client an opaque key. The client never sees the server's state. The key works once: a second retry with it returns `invalid request state`. On the retry, it forwards only the server's answers and, when the server sent one, its original state. The answers are forwarded even when the server sent no state.

## Examples

### Label questions from the server

```json
{
  "name": "Label wiki prompts",
  "hook": "prompts/get.post.elicit",
  "when": "has(elicitation.params.message)",
  "then": { "elicitation.params.message": "'The wiki asks: ' + elicitation.params.message" }
}
```

A question with the message `How detailed?` reaches the client as `The wiki asks: How detailed?`. The retry forwards the user's answer and the server's state.

### Refuse questions for API keys

```json
{
  "name": "No prompts for keys",
  "hook": "prompts/get.post.elicit",
  "when": "request.caller.type == 'apikey'",
  "then": { "decision": "'deny'", "reason": "'This prompt needs user input, which API keys cannot give.'" }
}
```

For an API key, the client receives `{"code": -32602, "message": "This prompt needs user input, which API keys cannot give."}`.

## Nuances and limits

- A deny on any one question refuses the whole round.
- Only `elicitation/create` requests reach this hook. A request for sampling or roots is refused, and no rule runs on it.
- The server's keys must not start with `bifrost.`.
- `request.upstream.params.arguments` uses the server's argument names, with `prompts/get.pre` changes applied. `request.params.arguments` uses the names the client sees.
- There is no result hook for prompts. The prompt the server returns after the question passes through.

## Related pages

- [Hooks and rules overview](overview.md)
- [tools/call.post.elicit, the matching hook for tool calls](tools-call-post-elicit.md)
- [server.session for advertising elicitation to the server](server-session.md)
