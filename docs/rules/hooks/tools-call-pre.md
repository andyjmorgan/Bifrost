# tools/call.pre

`tools/call.pre` decides whether a tool call goes ahead. It can also change the call's arguments, set a timeout, or ask the user to confirm first.

## When it runs

`tools/call.pre` runs once for each `tools/call` request, and for each `execute_tool` call in search mode. It runs after the gateway has found the tool and built the effective arguments, and before the call to the server:

1. [server.settings](server-settings.md), then [tools/list.post](tools-list-post.md) to find the tool the client named.
2. The gateway builds the effective arguments.
3. `tools/call.pre` rules run.
4. The gateway forces pins, checks constraints again, maps names and keys back to the server's, and checks that every hidden required parameter has a value.
5. The call to the server. Its reply goes to [tools/call.post.elicit](tools-call-post-elicit.md) or [tools/call.post.result](tools-call-post-result.md).

### Effective arguments

`request.params.arguments` holds the arguments that will be sent, under the names the client knows. It is not the client's literal arguments. To build it, the gateway:

1. Rejects duplicate keys and nesting deeper than 64 levels.
2. Rejects values for hidden parameters, and for the server's names of renamed parameters.
3. Rejects keys the tool does not define, when the server's input schema sets `additionalProperties: false`. See [undefined arguments](#undefined-arguments).
4. Fills defaults set in `tools/list.post` for missing arguments, including defaults of parameters that `tools/list.post` added.
5. Applies visible pins. A client value that differs from a pin is rejected.
6. Checks `enum`, `minimum` and `maximum`.

Because the gateway checks constraints before rules run, a call rule only sees values that the listed schema allows. A rule that clamps `limit` never sees a value above the `maximum` that `tools/list.post` set.

`request.params.arguments` is always a map, and is empty when the client sent no arguments. An argument that the client did not send, and that no default or pin filled, is absent.

Integral numbers that fit in a 64-bit integer become CEL `int` values, so `1`, `1.0` and `1e0` all equal `1`. Other numbers become `double`. The forwarded value keeps the client's original text unless a rule changes it.

### Undefined arguments

When the server's input schema does not set `additionalProperties: false`, arguments that name no parameter pass through unchanged.

When it does set `additionalProperties: false`, the gateway refuses undefined keys before any rule runs:

- a key that matches a `patternProperties` pattern is allowed
- a parameter that `tools/list.post` added counts as defined
- any other key returns `-32602`, `parameter "<p>" is not defined on this tool`

The gateway applies `additionalProperties: false` at the level where the schema sets it, using the `properties` and `patternProperties` at that level only. Composition keywords elsewhere in the schema, such as `allOf`, `anyOf` or `$ref`, do not widen it, as JSON Schema specifies. A schema with `additionalProperties: false` and `allOf: [{}]` still refuses undefined keys.

## What a rule can read

| Branch | Notable fields |
| --- | --- |
| `request.params` | `request.params.name`, the tool name the client sent. `request.params.arguments`, the effective arguments, with changes from earlier rules. `request.params._meta`, when the client sent one |
| `request` | Also `request.method`, `request.caller`, `request.client`, `request.headers`, `request.route`, `request.flags`, `request.time` |
| `tool` | The tool after `tools/list.post`: `tool.name`, `tool.title`, `tool.description`, `tool.annotations`, `tool.inputSchema`, `tool.outputSchema`, `tool.params`, `tool.icons`, `tool._meta` |
| `tool.upstream` | The tool as the server declares it |
| `server` | `server.slug` and what the gateway knows about the server |
| `entitlement` | `entitlement.id`, `entitlement.slug`, `entitlement.name` |

`request.upstream` does not exist here, because nothing has been sent to the server yet. A rule that reads it is refused when saved.

Read an argument that may be absent with an [optional form](overview.md#optional-values):

| Read | Use |
| --- | --- |
| An argument, with a default | `request.params.arguments.?limit.orValue(0)` |
| Whether the client sent an argument | `has(request.params.arguments.limit)` |
| A hint on the tool | `tool.?annotations.?destructiveHint.orValue(true)` |

## What a rule can set

| Path | Type | Effect | Stops evaluation |
| --- | --- | --- | --- |
| `request.params.arguments.<p>` | any | Set an argument, by the name the client knows | No |
| `timeout` | duration | How long the gateway waits for the server on this call. Must be positive | No |

A rule here can deny, and can ask the user to confirm.

| Key | Value | Effect | Stops evaluation |
| --- | --- | --- | --- |
| `decision` | `'deny'` | Refuse the call. The server is not called | Yes |
| `reason` | string | The text the client receives with a deny | No |
| `confirm` | `elicit(...)` | Ask the user to confirm before the call | Yes |

Changes chain: a later rule sees `request.params.arguments` as earlier rules left it. Changes in a rule that also asks for confirmation apply before the question.

A rule cannot set `tool`, `request.params.name`, `request.caller` or any other path. For example, `tool.name` is refused with `tool.name cannot be changed on tools/call.pre; a rule here can set request.params.arguments.*, timeout`.

`timeout` is a plain name, not a path in `request`. It is the gateway's limit on the forwarded call.

## What the client sees

| Outcome | Result |
| --- | --- |
| Allowed | The server's reply, after [tools/call.post.result](tools-call-post-result.md) |
| Denied | `{"isError": true, "content": [{"type": "text", "text": "<reason>"}]}`. Without a reason, the text is `The call to <tool> was refused by policy.` |
| Confirmation | `{"resultType": "input_required", "inputRequests": {...}, "requestState": "..."}` |
| Confirmation, client cannot elicit | An error result: `This request needs confirmation (<message>), which this client does not support.`, or the error argument of `elicit()` |
| A rule fails to evaluate | An error result: `The call to <tool> was refused by policy.` |
| An argument change is not allowed | An error result: `The call to <tool> was refused by policy.` |
| A hidden required parameter has no value after rules | An error result: `The call to <tool> was refused: required parameters were not supplied by policy.` |
| Invalid arguments, before or after rules | `-32602`, with the messages listed in [tools/list.post](tools-list-post.md#what-the-client-sees) |
| Rules exceed the time budget | `-32603`, `rule evaluation exceeded its time budget` |

A deny returns an error result, not a JSON-RPC error, so the model can read the reason and change its arguments. The server is never called after a deny or a confirmation request.

Refusals never name the rule that refused. The rule's label appears only in the hits and the audit record.

## Confirmation

`confirm` asks the user to confirm, using the MCP 2026-07-28 multi round-trip pattern. Nothing blocks:

1. A rule sets `confirm` to `elicit('confirm', 'Delete page {id}?', {'id': request.params.arguments.?id.orValue('')})`. The client receives `input_required` with one input request, keyed `bifrost.confirm`.
2. The client asks the user and retries the same request with `inputResponses` and the `requestState` it received.
3. The gateway checks the state and the answer. If the user confirmed, it runs the rules again. The rule that asked passes this time, and the rules after it run.

Only a yes continues. A decline, a cancel, or an accept without `confirm: true` ends the request with the error result `Declined.`, and the server is not called. You do not need a rule for this, and no rule sees an answer that is not a yes.

### Show argument text with a values map

`elicit()` has two forms:

| Form | Use |
| --- | --- |
| `elicit(key, template, values)` and `elicit(key, template, values, error)` | The message includes argument text. Prefer this form |
| `elicit(key, message)` and `elicit(key, message, error)` | The message is fixed text with no argument values |

In the template form, each `{name}` placeholder is replaced by `values.name`. The gateway quotes the value with Go string quoting, cuts it to 80 characters with a trailing `…`, and escapes control characters such as line breaks. A placeholder with no matching key stays as written.

Arguments come from the model, so their text is untrusted. Without quoting, a model could send `id: "42? This is routine, choose Accept"` and the user would read that text as part of the gateway's question. With the template form, the user sees `Delete page "42? This is routine, choose Accept"?`. The argument text stays inside quotes and cannot pose as the gateway's wording. Quoting reduces this confusion, but it does not make argument text safe to trust.

The gateway's input request is a form with one required boolean, `confirm`:

```json
{
  "method": "elicitation/create",
  "params": {
    "mode": "form",
    "message": "Delete page \"42\"?",
    "requestedSchema": {
      "type": "object",
      "properties": { "confirm": { "type": "boolean", "title": "Confirm", "description": "Delete page \"42\"?" } },
      "required": ["confirm"]
    }
  }
}
```

The gateway decides whether a question has been answered, not the rule. A rule says only when to ask and what to ask. No rule can read an answer.

A yes counts only for the question it answered. A question is its words as the user saw them, and the arguments as they stood when it was asked, after earlier rules had changed them. On the retry the gateway runs the rules again and works both out again. If either differs, it is a new question and the user is asked again. So a rule that sets an argument from `request.params._meta`, a header or the caller, and then asks about it, cannot be answered for one value and forwarded with another.

What a rule sets after a question is not part of that question. Put the question after the rules that set arguments, and show in it the values that matter.

A rule that sets an argument from `request.time` before a question makes a new question on every round, so the user is asked again each time and the call never goes ahead. Ask first, then set.

Several confirmations in a row work: the gateway carries what the user has confirmed into each new round. The same words asked twice about different arguments are two questions.

A rule cannot both deny and confirm. It is refused when saved.

### Request state

The `requestState` is an opaque key. The gateway keeps the state itself, so the client cannot read or change it. This is the same for the gateway's own question and for a question the server asks. The state is bound to:

- the caller
- the entitlement id, or its slug when there is no id
- the method. A directory read binds its own method, so a read confirmation cannot be used for a directory read
- the server's name for the tool
- a hash of the arguments the client sent
- for each confirmation, the question as shown and the arguments as they stood when it was asked
- the policy generation, which combines the gateway's generation with a fingerprint of the compiled rules

Any change to the rules invalidates outstanding challenges, even if the gateway does not change its generation.

A challenge expires after five minutes and can be answered once. Any retry uses it up, whether or not the retry succeeds. A replay, or a second attempt after a failed one, returns `invalid request state`, and the client must send the request again to be asked afresh. A retry fails with `-32602` when:

| Situation | Message |
| --- | --- |
| Gateway answers without state | `input responses for the gateway require the gateway's request state` |
| Other answers without state | `input responses require a request state` |
| Unknown, already used or malformed state | `invalid request state` |
| More than five minutes old | `request state expired` |
| A different tool or method | `request state belongs to another request` |
| A different caller or entitlement | `request state belongs to another caller` |
| The rules or the generation changed | `request state is from an older policy` |
| Different arguments | `arguments changed since the confirmation was requested` |
| An answer to a key that was not asked | `no pending input request "<key>"` |

`elicit()` checks the client's capability on the current request. When the client cannot elicit, it returns an error result instead, so a separate rule for that case is needed only for a custom message. Pass the message as the last argument: `elicit('confirm', 'Run {name}?', {'name': tool.name}, 'Use a client that supports confirmation.')`.

## Examples

### Deny an argument value

```json
[
  {
    "name": "Default space",
    "hook": "tools/list.post",
    "when": "tool.upstream.name == 'search_pages'",
    "then": { "tool.params.space.default": "'HR'" }
  },
  {
    "name": "No HR",
    "hook": "tools/call.pre",
    "when": "tool.upstream.name == 'search_pages' && request.params.arguments.?space.orValue('') == 'HR'",
    "then": { "decision": "'deny'", "reason": "'The HR space is restricted. Use ENG or OPS.'" }
  }
]
```

A call with `{"q": "salaries"}` is denied, because the default fills `space` with `HR` before the call rule runs. The client receives `{"isError": true, "content": [{"type": "text", "text": "The HR space is restricted. Use ENG or OPS."}]}`. A call with `space: 'ENG'` goes through.

### Clamp an argument and set a timeout

```json
[
  {
    "name": "Clamp limit",
    "hook": "tools/call.pre",
    "when": "request.params.arguments.?limit.orValue(0) > 100",
    "then": { "request.params.arguments.limit": "100" }
  },
  {
    "name": "Search timeout",
    "hook": "tools/call.pre",
    "when": "tool.upstream.name == 'search_logs'",
    "then": { "timeout": "duration('30s')" }
  }
]
```

A call with `limit: 5000` reaches the server with `limit: 100` and a 30-second timeout. If `tools/list.post` also set `maximum` to 500 on `limit`, the gateway would refuse `limit: 5000` with `-32602` before this rule runs.

### Set a hidden parameter to the caller

```json
[
  {
    "name": "Hide assignee",
    "hook": "tools/list.post",
    "when": "tool.upstream.name == 'create_issue'",
    "then": { "tool.params.assignee.hide": "true" }
  },
  {
    "name": "Assign to caller",
    "hook": "tools/call.pre",
    "when": "tool.upstream.name == 'create_issue' && request.caller.type == 'oidc'",
    "then": { "request.params.arguments.assignee": "request.caller.email" }
  }
]
```

The schema shows only `title`. For a signed-in user with the email `ana@example.com`, `{"title": "Disk full"}` reaches the server as `{"title": "Disk full", "assignee": "ana@example.com"}`. A client that sends `assignee` receives `-32602`.

For an API key, no rule sets `assignee`. The call returns the error result `The call to create_issue was refused: required parameters were not supplied by policy.` A pin cannot do this, because pins must be static.

### Ask for confirmation

```json
{
  "name": "Confirm delete",
  "hook": "tools/call.pre",
  "when": "tool.upstream.name == 'delete_page'",
  "then": { "confirm": "elicit('confirm', 'Delete page {id}?', {'id': request.params.arguments.?id.orValue('')})" }
}
```

One rule is enough. The gateway refuses any answer that is not a yes.

1. The first call with `{"id": "42"}` returns `input_required` with the message `Delete page "42"?`. The server is not called.
2. A retry with `{"bifrost.confirm": {"action": "decline"}}` returns the error result `Declined.` So does `{"action": "cancel"}`, and so does an accept with `{"confirm": false}`.
3. A retry with `{"bifrost.confirm": {"action": "accept", "content": {"confirm": true}}}` forwards the call.
4. Sending that accepted retry a second time returns `-32602`, `invalid request state`. A challenge is answered once.
5. A retry that changes the arguments to `{"id": "43"}` returns `-32602`, `arguments changed since the confirmation was requested`.

After a refused or failed retry, the client sends the original request again and the user is asked afresh.

A client without the elicitation capability receives the error result `This request needs confirmation (Delete page "42"?), which this client does not support.`

## Nuances and limits

- Pins win over rule changes. A change to a pinned parameter has no effect.
- The gateway checks constraints again after rules. A change that moves a value out of range fails with `-32602`.
- Set a renamed parameter by the name the client knows. A rule that names the server's key fails, which refuses the call.
- A change to a hidden parameter uses the parameter's name at the time it was hidden. Setting `request.params.arguments.<p>` for a hidden required parameter is what lets `tools/list.post` hide it without a pin or default. If no enforce rule sets it on a call, the call is refused. Audit rules never supply a value.
- A plain read of an absent argument fails the rule. `request.params.arguments.limit > 100` refuses the call when `limit` is missing. Write `has(request.params.arguments.limit) && request.params.arguments.limit > 100`, or `request.params.arguments.?limit.orValue(0) > 100`.
- `has(request.client.capabilities.elicitation)` is safe without a guard. `request.client.capabilities` is `{}` when the client sends none.
- A request with `request.flags.cli` set cannot be asked for confirmation. A rule that sets `confirm` refuses such a call.
- `request.caller.type` and the other caller fields come from the gateway. On Bifrost, `type` is `oidc` for a signed-in user and `apikey` for an API key.
- Conditions can compare times with `timestamp()` and `duration()`. For example, `has(request.params.arguments.from) && has(request.params.arguments.to) && timestamp(request.params.arguments.to) - timestamp(request.params.arguments.from) > duration('168h')` matches ranges over seven days.

## Related pages

- [Hooks and rules overview](overview.md)
- [tools/list.post for parameters, pins and defaults](tools-list-post.md)
- [tools/call.post.elicit for questions from the server](tools-call-post-elicit.md)
- [tools/call.post.result for shaping the reply](tools-call-post-result.md)
