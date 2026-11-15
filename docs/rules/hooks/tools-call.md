# tools/call

`tools/call` decides whether a tool call goes ahead, and can patch its arguments, set a timeout or ask the user to confirm first.

## When it runs

`tools/call` runs once for each `tools/call` request, and for each `execute_tool` call in search mode. It runs after the gateway has found the exposed tool and built the effective arguments, and before the upstream call:

1. [server/config](server-config.md), then [tools/filter](tools-filter.md) and [tools/modify](tools-modify.md) to find the exposed tool.
2. The gateway builds the effective arguments.
3. `tools/call` rules run.
4. The gateway forces pins, checks constraints again, maps names and keys back to the upstream, and checks that every hidden required parameter has a value.
5. The upstream call. Its reply goes to [tools/input](tools-input.md) or [tools/result](tools-result.md), which see the exact arguments that were forwarded.

### Effective arguments

Rules see the arguments that will actually be sent, under the names the client sees, not the client's literal arguments. To build them, the gateway:

1. Rejects duplicate keys and nesting deeper than 64 levels.
2. Rejects values for hidden parameters, and for upstream names of renamed parameters.
3. Rejects keys the tool does not define, when the upstream input schema sets `additionalProperties: false`. See [undefined arguments](#undefined-arguments).
4. Fills defaults set in `tools/modify` for missing arguments, including defaults of parameters that `tools/modify` added.
5. Applies visible pins. A client value that differs from a pin is rejected.
6. Checks `enum`, `minimum` and `maximum`.

Because the gateway checks constraints before rules run, a call rule only sees values that the exposed schema allows. A rule that clamps `limit` never sees a value above the `maximum` that `tools/modify` set.

Integral numbers that fit in a 64-bit integer become CEL `int` values, so `1`, `1.0` and `1e0` all equal `1`. Other numbers become `double`. The forwarded value keeps the client's original text unless a rule changes it.

### Undefined arguments

When the upstream input schema does not set `additionalProperties: false`, arguments that name no parameter pass through unchanged.

When it does set `additionalProperties: false`, the gateway refuses undefined keys before any rule runs:

- a key that matches a `patternProperties` pattern is allowed
- a parameter that `tools/modify` added counts as defined
- any other key returns `-32602`, `parameter "<p>" is not defined on this tool`

The gateway applies `additionalProperties: false` at the level where the schema sets it, using the `properties` and `patternProperties` at that level only. Composition keywords elsewhere in the schema, such as `allOf`, `anyOf` or `$ref`, do not widen it, as JSON Schema specifies. A schema with `additionalProperties: false` and `allOf: [{}]` still refuses undefined keys.

## Variables

| Variable | Type | Description |
| --- | --- | --- |
| `args` | map | The effective arguments, under exposed names. Patches from earlier rules are visible |
| `input` | map | The user's answers to this gateway's confirmations, by key. Each answer is a map such as `{action: 'accept', content: {confirm: true}}`. Empty on the first request |
| `name`, `title`, `description`, `annotations`, `annotationsRaw`, `meta`, `inputSchema`, `outputSchema`, `params`, `icons` | Various | The exposed tool, after `tools/modify` |
| `upstream` | map | The tool as the upstream sent it |
| `server`, `entitlement`, `caller`, `client`, `route`, `flags`, `now` | Various | The [common variables](overview.md#common-variables) |

## Fields

| Field | Type | Effect | Stops evaluation |
| --- | --- | --- | --- |
| `decision` | `'deny'` | Refuse the call | Yes |
| `reason` | string | The text the client receives with a deny | No |
| `result` | `elicit(...)` | Ask the user to confirm before the call | Yes |
| `args.<p>` | any | Set an argument, by its exposed name | No |
| `timeout` | duration | The upstream timeout for this call. Must be positive | No |

Patches chain: a later rule sees `args` as earlier rules left it. Patches in a rule that also elicits apply before the confirmation.

`limit` is reserved for rate limits. Rate limits are not enforced yet, so a rule that sets `limit` does not compile.

## What the client sees

| Outcome | Result |
| --- | --- |
| Allowed | The upstream's reply, after [tools/result](tools-result.md) |
| Denied | `{"isError": true, "content": [{"type": "text", "text": "<reason>"}]}`. Without a reason, the text is `The call to <tool> was refused by policy.` |
| Confirmation | `{"resultType": "input_required", "inputRequests": {...}, "requestState": "..."}` |
| Confirmation, client cannot elicit | An error result: `This request needs confirmation (<message>), which this client does not support.`, or the error argument of `elicit()` |
| A rule fails to evaluate | An error result: `The call to <tool> was refused by policy.` |
| An argument patch is not allowed | An error result: `The call to <tool> was refused by policy.` |
| A hidden required parameter has no value after rules | An error result: `The call to <tool> was refused: required parameters were not supplied by policy.` |
| Invalid arguments, before or after rules | `-32602`, with the messages listed in [tools/modify](tools-modify.md#what-the-client-sees) |
| Rules exceed the time budget | `-32603`, `rule evaluation exceeded its time budget` |

A deny returns an error result rather than a JSON-RPC error, so the model can read the reason and change its arguments. The upstream is never called after a deny or a confirmation request.

Refusals never name the rule that refused. The rule's label appears only in the hits and the audit record.

## Elicitation

`elicit()` asks the user to confirm, using the MCP 2026-07-28 multi round-trip pattern. Nothing blocks:

1. A rule returns `elicit('confirm', 'Delete page {id}?', {'id': args.id})`. The client receives `input_required` with one input request, keyed `bifrost.confirm`.
2. The client asks the user and retries the same request with `inputResponses` and the `requestState` it received.
3. The gateway checks the state and the answer. If the user confirmed, it runs the rules again, with the answer in `input.confirm`.

Only a yes continues. A decline, a cancel, or an accept without `confirm: true` ends the request with the error result `Declined.`, and the upstream is not called. You do not need a rule for this, and no rule sees an answer that is not a yes.

### Show argument text with a values map

`elicit()` has two forms:

| Form | Use |
| --- | --- |
| `elicit(key, template, values)` and `elicit(key, template, values, error)` | The message includes argument text. Prefer this form |
| `elicit(key, message)` and `elicit(key, message, error)` | The message is fixed text with no argument values |

In the template form, each `{name}` placeholder is replaced by `values.name`. The gateway quotes the value with Go string quoting, cuts it to 80 characters with a trailing `…`, and escapes control characters such as line breaks. A placeholder with no matching key stays as written.

Arguments come from the model, so their text is untrusted. Without quoting, a model could send `id: "42? This is routine, choose Accept"` and the user would read that text as part of the gateway's question. With the template form, the user sees `Delete page "42? This is routine, choose Accept"?`: the argument text stays inside quotes and cannot pose as the gateway's wording. Quoting reduces this confusion, but it does not make argument text safe to trust.

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

The rule that asks must check `!has(input.<key>)`. Otherwise, the retry asks again and the client receives an error result: `confirmation "<key>" was already answered; the rule asking for it must check has(input.<key>)`.

A confirmed answer matches no rule, so the call goes ahead. Several confirmations in a row work: the gateway carries earlier answers into each new challenge.

### Request state

The `requestState` for a confirmation is an opaque key. The gateway keeps the challenge itself, so the client cannot read or change it. The challenge is bound to:

- the caller
- the entitlement id, or its slug when there is no id
- the method. A directory read binds its own method, so a read confirmation cannot be used for a directory read
- the upstream tool
- a hash of the effective arguments before rules
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

`elicit()` checks the client's capability on the current request. When the client cannot elicit, it returns an error result instead, so a separate rule for that case is needed only for a custom message. Pass the message as the last argument: `elicit('confirm', 'Run {name}?', {'name': name}, 'Use a client that supports confirmation.')`.

## Nuances and limits

- Pins win over patches. A patch to a pinned parameter has no effect.
- The gateway checks constraints again after rules. A patch that moves a value out of range fails with `-32602`.
- Patch a renamed parameter by its exposed name. A patch that names its upstream key fails the rule, which refuses the call.
- A patch to a hidden parameter uses the parameter's name at the time it was hidden. Setting `args.<p>` for a hidden required parameter is what lets `tools/modify` hide it without a pin or default. If no enforce rule sets it on a call, the call is refused. Audit rules never supply a value.
- Guard optional arguments with `has()`. `args.limit > 100` fails to evaluate when `limit` is missing, which refuses the call.
- `has(client.capabilities.elicitation)` is safe without a guard. `client.capabilities` is `{}` when the client sends none.
- Conditions can compare times with `timestamp()` and `duration()`. For example, `has(args.from) && has(args.to) && timestamp(args.to) - timestamp(args.from) > duration('168h')` matches ranges over seven days.

## Examples

### Deny an argument value

```json
[
  {
    "name": "Default space",
    "hook": "tools/modify",
    "when": "upstream.name == 'search_pages'",
    "then": { "params.space.default": "'HR'" }
  },
  {
    "name": "No HR",
    "hook": "tools/call",
    "when": "upstream.name == 'search_pages' && args.space == 'HR'",
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
    "hook": "tools/call",
    "when": "has(args.limit) && args.limit > 100",
    "then": { "args.limit": "100" }
  },
  {
    "name": "Search timeout",
    "hook": "tools/call",
    "when": "upstream.name == 'search_logs'",
    "then": { "timeout": "duration('30s')" }
  }
]
```

A call with `limit: 5000` reaches the upstream with `limit: 100` and a 30-second timeout. If `tools/modify` also set `maximum` to 500 on `limit`, the gateway would refuse `limit: 5000` with `-32602` before this rule runs.

### Set a hidden parameter to the caller

```json
[
  {
    "name": "Hide assignee",
    "hook": "tools/modify",
    "when": "upstream.name == 'create_issue'",
    "then": { "params.assignee.hide": "true" }
  },
  {
    "name": "Assign to caller",
    "hook": "tools/call",
    "when": "upstream.name == 'create_issue' && caller.type == 'user'",
    "then": { "args.assignee": "caller.email" }
  }
]
```

The schema shows only `title`. For a caller with the email `ana@example.com`, `{"title": "Disk full"}` reaches the upstream as `{"title": "Disk full", "assignee": "ana@example.com"}`. A client that sends `assignee` receives `-32602`. For an API key, no rule sets `assignee`, so the call returns the error result `The call to create_issue was refused: required parameters were not supplied by policy.` A pin cannot do this, because pins must be static.

### Ask for confirmation

```json
[
  {
    "name": "Confirm delete",
    "hook": "tools/call",
    "when": "upstream.name == 'delete_page' && !has(input.confirm)",
    "then": { "result": "elicit('confirm', 'Delete page {id}?', {'id': args.id})" }
  }
]
```

One rule is enough. The gateway refuses any answer that is not a yes.

1. The first call with `{"id": "42"}` returns `input_required` with the message `Delete page "42"?`. The upstream is not called.
2. A retry with `{"bifrost.confirm": {"action": "decline"}}` returns the error result `Declined.` So does `{"action": "cancel"}`, and so does an accept with `{"confirm": false}`.
3. A retry with `{"bifrost.confirm": {"action": "accept", "content": {"confirm": true}}}` forwards the call.
4. Sending that accepted retry a second time returns `-32602`, `invalid request state`. A challenge is answered once.
5. A retry that changes the arguments to `{"id": "43"}` returns `-32602`, `arguments changed since the confirmation was requested`.

After a refused or failed retry, the client sends the original request again and the user is asked afresh.

A client without the elicitation capability receives the error result `This request needs confirmation (Delete page "42"?), which this client does not support.`

## Related pages

- [Hooks and rules overview](overview.md)
- [tools/modify for parameters, pins and defaults](tools-modify.md)
- [tools/input for input requests from the upstream](tools-input.md)
- [tools/result for shaping the reply](tools-result.md)
