# prompts/get.pre

`prompts/get.pre` decides whether a `prompts/get` request goes ahead. It can also change the request's arguments, or ask the user to confirm first.

## When it runs

`prompts/get.pre` runs once for each `prompts/get` request:

1. [server.settings](server-settings.md). If `settings.prompts` is off, the prompt is unknown.
2. [prompts/list.post](prompts-list-post.md), to find the prompt the client named.
3. The gateway rejects arguments that use the server's name of a renamed argument.
4. `prompts/get.pre` rules run.
5. The gateway maps the prompt name and argument names back to the server's.
6. The call to the server. An `input_required` reply goes to [prompts/get.post.elicit](prompts-get-post-elicit.md). On the mux route, structured URIs in the returned messages get the slug prefix.

## What a rule can read

| Branch | Notable fields |
| --- | --- |
| `request.params` | `request.params.name`, the prompt name the client sent. `request.params.arguments`, the arguments the client sent, under the names the client knows, with changes from earlier rules. Values are strings. `request.params._meta`, when the client sent one |
| `request` | Also `request.method`, `request.caller`, `request.client`, `request.headers`, `request.route`, `request.flags`, `request.time` |
| `prompt` | The prompt after `prompts/list.post`: `prompt.name`, `prompt.title`, `prompt.description`, `prompt.arguments`, `prompt.icons`, `prompt._meta` |
| `prompt.upstream` | The prompt as the server declares it |
| `server` | `server.id`, `server.slug` and what the gateway knows about the server |

`request.upstream` does not exist here, because nothing has been sent to the server yet.

`request.params.arguments` is always a map. An argument the client did not send is absent. Read it with `request.params.arguments.?tone.orValue('')`, or guard with `has(request.params.arguments.tone)`.

## What a rule can set

| Path | Type | Effect | Stops evaluation |
| --- | --- | --- | --- |
| `request.params.arguments.<a>` | string | Set an argument, by the name the client knows | No |

A rule here can deny, and can ask the user to confirm.

| Key | Value | Effect | Stops evaluation |
| --- | --- | --- | --- |
| `decision` | `'deny'` | Refuse the request. The server is not called | Yes |
| `reason` | string | The error message the client receives with a deny | No |
| `confirm` | `elicit(...)` | Ask the user to confirm first | Yes |

Changes chain. A rule cannot set `prompt`, `request.params.name` or any other path.

## What the client sees

`prompts/get` has no error result, so refusals are JSON-RPC errors.

| Outcome | Result |
| --- | --- |
| Allowed | The server's messages. On the mux route, `resource_link` and embedded `resource` blocks carry the slug prefix |
| Denied | `-32602` with `reason` as the message. Without a reason, the message is `prompts/get was refused by policy` |
| Confirmation | `{"resultType": "input_required", "inputRequests": {...}, "requestState": "..."}` |
| Confirmation, client cannot elicit | `-32602`, `This request needs confirmation (<message>), which this client does not support.`, or the error argument of `elicit()` |
| A rule fails to evaluate | `-32602`, `prompts/get was refused by policy` |
| Unknown or hidden prompt | `-32602`, `unknown prompt <name>` |
| Mux name without a slug | `-32602`, `prompt name must be <slug>__<prompt>` |

Confirmations work as in [tools/call.pre](tools-call-pre.md#confirmation). The challenge is bound to the method, the server's name for the prompt, a hash of the client's arguments and the policy generation. Refusals never name the rule that refused.

## Examples

### Deny an argument value

```json
{
  "name": "No HR",
  "hook": "prompts/get.pre",
  "when": "request.params.arguments.?space.orValue('') == 'HR'",
  "then": { "decision": "'deny'", "reason": "'HR prompts are restricted.'" }
}
```

`{"space": "HR"}` returns `{"code": -32602, "message": "HR prompts are restricted."}`. `{"space": "ENG"}` reaches the server, and so does a request with no `space`.

### Fill a missing argument

```json
{
  "name": "Default tone",
  "hook": "prompts/get.pre",
  "when": "!has(request.params.arguments.tone)",
  "then": { "request.params.arguments.tone": "'neutral'" }
}
```

`{"topic": "oncall"}` reaches the server as `{"topic": "oncall", "tone": "neutral"}`. A client that sends `tone: 'formal'` keeps it.

### Confirm before a prompt runs

```json
{
  "name": "Confirm export",
  "hook": "prompts/get.pre",
  "when": "prompt.upstream.name == 'export_space'",
  "then": { "confirm": "elicit('ok', 'Export the whole {space} space?', {'space': request.params.arguments.?space.orValue('')})" }
}
```

The first request returns `input_required` with the message `Export the whole "ENG" space?`. A retry that declines returns `-32602`, `Declined.`, with no rule needed. A retry that accepts with `{"confirm": true}` reaches the server. Each challenge is answered once, so after a decline the client sends the request again to be asked afresh.

## Nuances and limits

- An argument value must be a string. A rule that sets a number or a boolean fails, which refuses the request.
- Set a renamed argument by the name the client knows. A rule that names the server's name returns `-32602`, `argument "<a>" is not available on this prompt`.
- When a message includes argument text, use the template form of `elicit()`, so the text is quoted and cannot pose as the gateway's wording.
- A plain read of an argument the client did not send fails the rule, which refuses the request.
- The server is never called after a deny or a confirmation request.
- Prompt content is never cached. Every `prompts/get` reaches the server.
- There is no result hook for prompts. The prompt the server returns passes through.

## Related pages

- [Hooks and rules overview](overview.md)
- [prompts/list.post for argument renames](prompts-list-post.md)
- [prompts/get.post.elicit for questions from the server](prompts-get-post-elicit.md)
- [Confirmation in tools/call.pre](tools-call-pre.md#confirmation)
