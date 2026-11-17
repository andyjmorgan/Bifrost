# tools/call.post.elicit

`tools/call.post.elicit` decides whether a server may ask the user a question during a tool call, and can reword the question.

## When it runs

`tools/call.post.elicit` runs when the server answers a forwarded `tools/call` with `resultType: "input_required"`. It runs once for each question in the reply, in key order. [tools/call.post.result](tools-call-post-result.md) does not run on that reply.

The gateway recognizes an `input_required` reply that carries input requests, a `requestState`, or both. A reply with only a `requestState` has no questions for rules to check, so the gateway wraps the state and returns it to the client.

A server asks for input only if the gateway advertised elicitation to it. That is off by default and controlled by [server.session](server-session.md).

This is the server's own question. A `confirm` rule in [tools/call.pre](tools-call-pre.md) is the gateway's question and does not pass through this hook. Gateway confirmations always resolve before the server is called, so the gateway and the server never ask in the same round.

## What a rule can read

| Branch | Notable fields |
| --- | --- |
| `elicitation` | One question. `elicitation.key`, the key the server gave it. `elicitation.method`, always `elicitation/create`. `elicitation.params`, with `mode`, `message`, `requestedSchema`, `url` and `elicitationId` where the server sent them |
| `elicitation.upstream` | The question as the server asked it: `elicitation.upstream.params`. It never changes |
| `request.params` | `request.params.name` and `request.params.arguments`, under the names the client knows |
| `request.upstream` | The call as the server received it: `request.upstream.params.name` and `request.upstream.params.arguments`, under the server's names, after rule changes, pins and removal of added parameters |
| `request` | Also `request.method`, `request.caller`, `request.client`, `request.headers`, `request.route`, `request.flags`, `request.time` |
| `tool` | The tool as the client sees it, and `tool.upstream` as the server declares it |
| `server` | `server.slug` and what the gateway knows about the server |
| `entitlement` | `entitlement.id`, `entitlement.slug`, `entitlement.name` |

Every field of `elicitation.params` is optional. Read them with the [optional forms](overview.md#optional-values), for example `elicitation.params.?mode.orValue('form')`.

## What a rule can set

| Path | Type | Effect | Stops evaluation |
| --- | --- | --- | --- |
| `elicitation.params.message` | string | Replace the message the user sees. Later rules read the new value | No |

A rule here can deny. It cannot ask the user to confirm.

| Key | Value | Effect | Stops evaluation |
| --- | --- | --- | --- |
| `decision` | `'deny'` | Refuse the whole round of questions and end the call | Yes |
| `reason` | string | The text the client receives with a deny | No |

A rule cannot change the schema, the mode or the URL of a question.

## What the client sees

| Outcome | Result |
| --- | --- |
| Allowed | `{"resultType": "input_required", "inputRequests": {...}, "requestState": "..."}`, with each question's message as the rules left it |
| Denied | `{"isError": true, "content": [{"type": "text", "text": "<reason>"}]}`. Without a reason, the text is `The upstream server asked for input, which policy does not allow.` |
| A rule fails to evaluate | An error result: `The upstream server asked for input, which policy does not allow.` |
| Shaped reply over the size limit | `-32013`, `result exceeds gateway size limit after shaping` |

The gateway keeps the server's `requestState`, if any, in its own store and gives the client an opaque key. The client never sees the server's state. The key works once: a second retry with it returns `invalid request state`. On the retry, the gateway checks its state, then forwards only the server's answers and, when the server sent one, its original `requestState`. The answers are forwarded even when the server sent no `requestState`.

## Examples

### Label questions from the server

```json
{
  "name": "Label Jira prompts",
  "hook": "tools/call.post.elicit",
  "when": "has(elicitation.params.message)",
  "then": { "elicitation.params.message": "'Jira asks: ' + elicitation.params.message" }
}
```

A question with the message `Which project?` reaches the client as `Jira asks: Which project?`. When the client retries with `{"project": {"action": "accept", "content": {"p": "OPS"}}}`, the server receives that answer and its own state.

### Refuse questions for API keys

```json
{
  "name": "No prompts for keys",
  "hook": "tools/call.post.elicit",
  "when": "request.caller.type == 'apikey'",
  "then": { "decision": "'deny'", "reason": "'This tool needs user input, which API keys cannot give.'" }
}
```

For an API key, the client receives `{"isError": true, "content": [{"type": "text", "text": "This tool needs user input, which API keys cannot give."}]}`.

### Refuse URL-mode questions

```json
{
  "name": "No URL mode",
  "hook": "tools/call.post.elicit",
  "when": "elicitation.params.?mode.orValue('form') == 'url'",
  "then": { "decision": "'deny'" }
}
```

A question that sends the user to a URL returns the error result `The upstream server asked for input, which policy does not allow.` Form questions pass through, including one that names no mode.

## Nuances and limits

- A deny on any one question refuses the whole round.
- Only a question, an `elicitation/create` request, reaches this hook. Any other method, such as a request for sampling or roots, is refused with an error result. No rule runs on it.
- The server's keys must not start with `bifrost.`, which the gateway reserves for its own confirmations. Such a reply returns an error result.
- `request.upstream.params.arguments` holds the server's parameter names, not the names the client sees, because these are the arguments the server received. A value that a `tools/call.pre` rule changed or a pin forced appears as forwarded.
- The size limit applies to the reply after rules reword messages, so a long message cannot bypass it.
- On the retry, an answer to a key that the server did not ask returns `-32602`, `no pending input request "<key>"`.
- A request with `request.flags.cli` set has no client to show a question to. The server is not told that elicitation is available, and a question it asks anyway is refused.

## Related pages

- [Hooks and rules overview](overview.md)
- [server.session for advertising elicitation to the server](server-session.md)
- [tools/call.pre for gateway confirmations](tools-call-pre.md)
- [prompts/get.post.elicit](prompts-get-post-elicit.md) and [resources/read.post.elicit](resources-read-post-elicit.md), the matching hooks for prompts and reads
