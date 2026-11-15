# prompts/get

`prompts/get` decides whether a `prompts/get` request goes ahead, and can patch its arguments or ask the user to confirm first.

## When it runs

`prompts/get` runs once for each `prompts/get` request:

1. [server/config](server-config.md). If `prompts` is off, the prompt is unknown.
2. [prompts/filter](prompts-filter.md) and [prompts/modify](prompts-modify.md), to find the exposed prompt.
3. The gateway rejects arguments that use a renamed argument's upstream name.
4. `prompts/get` rules run.
5. The gateway maps the prompt name and argument names back to the upstream.
6. The upstream call. An `input_required` reply goes to [prompts/input](prompts-input.md), which sees the arguments as sent upstream. On the mux route, structured URIs in the returned messages get the slug prefix.

## Variables

| Variable | Type | Description |
| --- | --- | --- |
| `args` | map | The arguments the client sent, under exposed names. Values are strings. Patches from earlier rules are visible |
| `input` | map | The user's answers to this gateway's confirmations, by key. Empty on the first request |
| `name`, `title`, `description`, `arguments`, `meta`, `icons` | Various | The exposed prompt, after `prompts/modify` |
| `upstream` | map | The prompt as the upstream sent it |
| `server`, `entitlement`, `caller`, `client`, `route`, `now` | Various | The [common variables](overview.md#common-variables) |

## Fields

| Field | Type | Effect | Stops evaluation |
| --- | --- | --- | --- |
| `decision` | `'deny'` | Refuse the request | Yes |
| `reason` | string | The error message the client receives with a deny | No |
| `result` | `elicit(...)` | Ask the user to confirm first | Yes |
| `args.<a>` | any | Set an argument, by its exposed name. Values that are not strings are converted to strings | No |

Patches chain.

## What the client sees

`prompts/get` has no error result, so refusals are JSON-RPC errors.

| Outcome | Result |
| --- | --- |
| Allowed | The upstream's messages. On the mux route, `resource_link` and embedded `resource` blocks carry the slug prefix |
| Denied | `-32602` with `reason` as the message. Without a reason, the message is `prompts/get was refused by policy` |
| Confirmation | `{"resultType": "input_required", "inputRequests": {...}, "requestState": "..."}` |
| Confirmation, client cannot elicit | `-32602`, `This request needs confirmation (<message>), which this client does not support.`, or the error argument of `elicit()` |
| A rule fails to evaluate | `-32602`, `prompts/get was refused by policy` |
| Unknown or hidden prompt | `-32602`, `unknown prompt <name>` |
| Mux name without a slug | `-32602`, `prompt name must be <slug>__<prompt>` |

Confirmations work as in [tools/call](tools-call.md#elicitation). The signed state binds the method, the upstream prompt name, a hash of the client's arguments and the policy generation. Refusals never name the rule that refused.

## Nuances and limits

- Patch a renamed argument by its exposed name. A patch that names its upstream name returns `-32602`, `argument "<a>" is not available on this prompt`.
- When a message includes argument text, use the template form of `elicit()`, so the text is quoted and cannot pose as the gateway's wording.
- Guard optional arguments with `has()`, such as `has(args.space) && args.space == 'HR'`.
- The upstream is never called after a deny or a confirmation request.
- Prompt content is never cached. Every `prompts/get` reaches the upstream.

## Examples

### Deny an argument value

```json
{
  "name": "No HR",
  "hook": "prompts/get",
  "when": "has(args.space) && args.space == 'HR'",
  "then": { "decision": "'deny'", "reason": "'HR prompts are restricted.'" }
}
```

`{"space": "HR"}` returns `{"code": -32602, "message": "HR prompts are restricted."}`. `{"space": "ENG"}` reaches the upstream.

### Fill a missing argument

```json
{
  "name": "Default tone",
  "hook": "prompts/get",
  "when": "!has(args.tone)",
  "then": { "args.tone": "'neutral'" }
}
```

`{"topic": "oncall"}` reaches the upstream as `{"topic": "oncall", "tone": "neutral"}`. A client that sends `tone: 'formal'` keeps it.

### Confirm before a prompt runs

```json
[
  {
    "name": "Confirm export",
    "hook": "prompts/get",
    "when": "upstream.name == 'export_space' && !has(input.ok)",
    "then": { "result": "elicit('ok', 'Export the whole {space} space?', {'space': args.space})" }
  },
  {
    "name": "Export declined",
    "hook": "prompts/get",
    "when": "has(input.ok) && input.ok.action != 'accept'",
    "then": { "decision": "'deny'", "reason": "'Export was not confirmed.'" }
  }
]
```

The first request returns `input_required` with the message `Export the whole "ENG" space?`. A retry that declines returns `-32602`, `Export was not confirmed.` A retry that accepts reaches the upstream.

## Related pages

- [Hooks and rules overview](overview.md)
- [prompts/modify for argument renames](prompts-modify.md)
- [prompts/input for input requests from the upstream](prompts-input.md)
- [Elicitation in tools/call](tools-call.md#elicitation)
