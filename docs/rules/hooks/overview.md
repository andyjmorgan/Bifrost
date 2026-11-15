# Hooks and rules overview

Rules control what an MCP client can see and do through Bifrost. Each rule attaches to one hook, which is a point in request handling where the gateway runs rules. A hook defines which variables a rule can read and which fields it can set.

This page explains the rule shape, how rules are evaluated, what rules can read, and the order in which hooks run for each request. For the details of one hook, see the pages listed in [hook reference pages](#hook-reference-pages).

## Concepts

- Route: how a client reaches the gateway. `/<slug>/mcp` is the direct route and exposes one entitlement. `/mcp` is the mux route and exposes every entitlement the caller holds. `?search=true` on either route is search mode, which replaces the tool list with search tools.
- Entitlement: a named, grantable view of one upstream server, with its own slug.
- Catalog: what the upstream server offers. The gateway fetches and caches it per server. Rules never change the catalog. They shape a view of it for each request.
- Hook: a point in request handling where rules run, with a fixed set of variables and settable fields.
- Rule: one CEL condition and the fields it sets when the condition is true.

## Rule shape

A rule is a JSON object:

```json
{
  "id": "r1",
  "name": "Hide delete tools",
  "enabled": true,
  "mode": "enforce",
  "hook": "tools/filter",
  "when": "name.startsWith('delete_') && caller.role != 'admin'",
  "then": { "decision": "'deny'", "reason": "'Deleting is for admins.'" }
}
```

| Key | Required | Description |
| --- | --- | --- |
| `id` | No | A stable identifier |
| `name` | No | A label for audit records. Clients never see it. Without a name, the rule is labelled by `id`, or by its position, such as `rule 3` |
| `enabled` | No | `false` skips the rule. Defaults to `true` |
| `mode` | No | `enforce` or `audit`. Defaults to `enforce` |
| `hook` | Yes | The hook the rule belongs to |
| `when` | Yes | A CEL expression that must produce a boolean |
| `then` | Yes | The fields to set when `when` is true. Each value is a CEL expression |

Every value in `then` is a CEL expression, not a literal. A string value therefore needs CEL quotes inside the JSON string: write `"'deny'"`, not `"deny"`. Numbers and booleans need no quotes: `"100"`, `"true"`.

A rule does not compile when:

- the hook does not exist
- `when` reads a variable the hook does not have, such as `args` in `tools/filter`
- `then` sets a field the hook does not have
- `when` does not produce a boolean
- `decision` is anything other than the literal `'deny'`. There is no allow decision
- a filter rule does not set `decision`
- a pin or default in `tools/modify` reads a request variable, such as `caller`
- a `tools/call` rule sets `limit`. Rate limits are not enforced yet, so the gateway refuses the field rather than accept a limit that does nothing

An enabled rule that does not compile fails the whole entitlement closed. The entitlement exposes nothing, and listings report the problem to administrators.

A disabled rule that does not compile is ignored. The gateway reports it as a warning, so nobody enables the rule without seeing the error. The entitlement keeps working.

`when` has no `else`. A rule either matches or does not. For two outcomes, write two rules, or use a ternary (`cond ? a : b`) inside a `then` value.

## Evaluation

The gateway runs a hook's rules on each item separately, from top to bottom. An item is one tool, prompt, resource, template, skill, request or result.

| Hook kind | Changes that chain | Decisions that stop evaluation |
| --- | --- | --- |
| Filter | None | `decision: 'deny'` |
| Modify | Every settable field | None |
| Call (`tools/call`, `prompts/get`, `resources/read`) | `args.*`, `timeout` | `decision: 'deny'`, `result: elicit(...)` |
| Input | `message` | `decision: 'deny'` |
| Result | `content`, `structuredContent`, `text` | `decision: 'deny'` |
| Server | Every settable field | None |

### Changes chain

A later rule sees the values that earlier rules set. In this example, the second rule matches the name the first rule produced, and reads the original name from `upstream`:

```json
[
  {
    "hook": "tools/modify",
    "when": "name.startsWith('confluence_')",
    "then": { "name": "regex.replace(name, '^confluence_', 'wiki_')" }
  },
  {
    "hook": "tools/modify",
    "when": "name.startsWith('wiki_')",
    "then": { "description": "'[' + upstream.name + '] ' + description" }
  }
]
```

The upstream tool `confluence_search` with the description `Search.` is listed as `wiki_search` with the description `[confluence_search] Search.`.

A rule evaluates all of its `then` values before it applies any of them. If one value fails, the rule applies nothing.

### A deny is final

The first deny stops evaluation for that item, and no later rule can undo it. Because there is no allow decision, write exceptions into the deny condition itself:

```json
{
  "hook": "tools/filter",
  "when": "name.startsWith('delete_') && caller.role != 'admin'",
  "then": { "decision": "'deny'" }
}
```

A deny rule for all `delete_` tools followed by a rule meant to re-allow admins hides the tools from admins too.

### Audit mode

A rule with `"mode": "audit"` runs and records what it would have done, such as `would deny` or `would set`, but never changes the outcome. Audit rules still run after an enforce rule stops evaluation, so the audit record is complete.

An audit rule that fails to evaluate is recorded as `would error` and changes nothing. Audit rules have their own time allowance, separate from the enforcement budget. When the audit allowance runs out, the remaining audit rules are recorded as `would error`, and the request continues as if they did not exist.

### Errors fail closed

An enforce rule that fails to evaluate counts as a deny and stops evaluation. Common causes are reading a key that does not exist, a value of the wrong type for its field, a CEL cost limit, and the request time budget.

What the failure means depends on the hook:

| Hook kind | Effect of an evaluation error |
| --- | --- |
| Filter | The item is hidden |
| Modify | The item is hidden and cannot be called or read. The listing reports the problem to administrators |
| Call, input and result | The request is refused with the hook's generic refusal, such as `The call to <tool> was refused by policy.` |
| `server/config` | Every switch is off |
| `server/connect` | The gateway uses its anonymous defaults |
| `server/modify` | Title, description and instructions are empty |

### Refusals never name rules

A client never sees a rule's name, id or position. A deny without a `reason`, a rule that fails to evaluate, and a patch the gateway does not allow all return the same generic refusal for the hook. The rule's label appears only in the hits and the audit record.

A `reason` that an administrator writes reaches the client unchanged, so write reasons that are safe to show.

### Guard optional values

Guard optional values with `has()`. CEL's `&&` and `||` tolerate an error on one side when the other side decides the result, so `has(args.limit) && args.limit > 100` is false when `limit` is missing.

## Item model

Each hook works on one kind of item: a tool, prompt, resource, template, skill entry or server. Hooks that handle a request or response add one object for it.

- The item's current fields are top-level variables, such as `name`, `description` and `inputSchema`. They hold the item as earlier rules left it.
- `upstream` holds the item exactly as the upstream sent it. It never changes. Match `upstream.name` when a rule must keep working after a rename. Match `name` when a rule builds on earlier rules.
- `args`, `input`, `request` and `result` hold the request or response the hook handles. Rules change them only through the hook's fields, such as `args.limit` or `message`.
- `server`, `entitlement`, `caller`, `client`, `route` and `now` are read-only in every hook.
- A field the upstream left out reads as an empty value: `''` for text, `{}` for objects and `[]` for lists. `meta` reads `_meta`, or `{}` when there is none.

Anything a rule adds to `meta` reaches the client unless a later rule removes it. Setting a `meta.<key>` field to `null` removes that key.

### Common variables

Every hook can read these variables:

| Variable | Type | Fields and values |
| --- | --- | --- |
| `server` | map | `slug`, `name`, `title`, `description`, `instructions`, `protocolVersion`, `url`, as the upstream reported them |
| `entitlement` | map | `id`, `slug`, `name` of the entitlement serving the request |
| `caller` | map | `userId`, `email`, `role` (`admin` or `member`), `type` (`user` or `key`), `keyName`, `orgId`, `clientId` |
| `client` | map | `name`, `version`, `capabilities`, from the current request's `_meta`. `capabilities` is always present, and is `{}` when the client sent none |
| `route` | string | `direct`, `mux` or `search` |
| `now` | timestamp | The time of the request |

Use `caller.clientId`, the verified OAuth client, for security decisions about the client. `client.name` and `client.version` are self-reported.

`client.capabilities` is safe to branch on, because the client declares it on every request. `has(client.capabilities.elicitation)` never fails to evaluate: it is false when the client sent no capabilities.

Reading any other key that the request did not provide is an evaluation error. If a caller or client might lack a key, guard it with `has()`, for example `has(client.name) && client.name == 'claude-code'`.

### Numbers

An integral JSON number that fits in a 64-bit integer reaches CEL as an `int`, whatever its spelling: `1`, `1.0` and `1e0` all equal `1`. Other numbers are `double` values.

The gateway compares pins, `enum` values, `minimum` and `maximum` exactly, without rounding. It forwards every value with the client's original text, unless a rule changes it.

## Order of hooks per request

Rules run on every request over the cached catalog. The gateway does not cache rule output, results or content.

Every upstream request the gateway forwards carries the result of [server/connect](server-connect.md): the client identity, the capabilities and whether `_meta` is forwarded.

### Discover

1. `server/config` sets the switches.
2. `server/modify` rewrites title, description and instructions.

The result always carries `supportedVersions: ["2026-07-28"]`, `ttlMs` and `cacheScope: "private"`. Display identity is in `_meta["io.modelcontextprotocol/serverInfo"]`, not in a top-level `serverInfo`. Where at least one server serves skills, `capabilities.extensions["io.modelcontextprotocol/skills"]` is `{"directoryRead": true}`.

On the direct route, the result is the gateway's own `server/discover` answer for that server. On the mux route, instructions appear as slug-labelled excerpts, and the `get_server_instructions` tool returns the full text. Every line of an excerpt starts with `> `, so upstream text cannot start a heading that poses as another server's section. Discover always advertises `listChanged: false`, and never advertises completions or logging.

### tools/list

1. `server/config`. If `tools` is off, the server contributes nothing.
2. The catalog, from cache or from the upstream.
3. `tools/filter` on each upstream tool.
4. `tools/modify` on each tool that passed.
5. The gateway checks for name collisions. Tools that share a final name are all hidden.
6. On the mux route, each name gets the prefix `<slug>__`, and the gateway adds its `get_server_instructions` tool.

Output is sorted by slug, then by name. The output is the same, byte for byte, whatever order the upstream sends.

### tools/call

1. `server/config`. If `tools` is off, the call fails as an unknown tool.
2. `tools/filter` and `tools/modify` run over the catalog, and the gateway looks up the exposed name. A missing name fails with `-32004`.
3. The gateway builds the effective arguments under the exposed names. It rejects values for hidden parameters and for upstream names that were renamed, rejects undefined keys when the upstream schema sets `additionalProperties: false`, fills defaults, applies visible pins, and checks `enum`, `minimum` and `maximum`.
4. `tools/call` rules run on the effective arguments. Patches chain. The first deny or `elicit()` stops evaluation.
5. The gateway forces pinned values, checks constraints again, maps names and keys back to the upstream, and removes added parameters. A hidden required parameter that still has no value refuses the call.
6. The gateway calls the upstream.
7. If the upstream replies with `input_required`, `tools/input` runs on each input request. `tools/result` does not run.
8. If the upstream replies with a result, `tools/result` shapes it. Then, on the mux route, structured URIs in the result get the slug prefix.

`tools/input` and `tools/result` see the exact arguments that were forwarded in step 6. Gateway confirmations from step 4 always resolve before step 6, so the gateway and the upstream never ask for input in the same round. See [effective arguments and elicitation](tools-call.md).

### Prompts

- `prompts/list`: `server/config`, the catalog, `prompts/filter`, `prompts/modify`, then the mux name prefix. Sorted by slug, then by name.
- `prompts/get`: `server/config`, name lookup through the filter and modify rules, `prompts/get` rules, reversal of renames, the upstream call, then `prompts/input` for an `input_required` reply, or the mux prefix on structured URIs in the messages. `prompts/input` sees the arguments as they were sent upstream.

### Resources and templates

One visibility check decides every resource URI. Listings, reads, directory children, content items in a read result and fallback candidates all use it, so they cannot disagree. A URI is hidden and unreadable when any of these is true:

- it is under a denied skill folder
- a template that covers it is denied, failed to modify, or collided
- it is listed, but `resources/filter` denied it, `resources/modify` failed on it, or it collided with another resource
- `resources/filter` denies it when the gateway checks it as an unlisted URI
- it is not in canonical form
- no visible listing, supported template or visible skill covers it

The listings use that check:

- `resources/list`: `server/config`, then `skills/filter` to find denied skill folders, then `resources/filter`, `resources/modify` and the visibility check, then the mux URI prefix.
- `resources/templates/list`: `server/config`, the catalog, `templates/filter`, `templates/modify`, then the mux URI prefix.

Resources, templates and skills are sorted by slug, then by URI within each slug. The output is the same, byte for byte, whatever order the upstream sends.

`resources/read`:

1. `server/config`. If `resources` is off, the URI is not found.
2. The gateway refuses a URI that is not in canonical form.
3. On the mux route, the gateway removes the `<slug>+` prefix to find the upstream. A URI without a prefix goes through [fallback resolution](#fallback-resolution).
4. The visibility check. A URI it rejects is unknown.
5. `resources/read` rules.
6. The gateway reads from the upstream, with the URI as the upstream knows it.
7. `resources/input` for an `input_required` reply. Otherwise, the gateway drops content items with a non-canonical URI or a URI the caller cannot read, and `resources/result` runs on each remaining content item, text or binary.
8. On the mux route, structured URIs in the result get the slug prefix.

The result carries the gateway's `ttlMs` and `cacheScope: "private"`, whatever the upstream sent.

### Directory reads

`resources/directory/read` follows the same steps as `resources/read` up to the upstream call, including the `resources/read` rules. In the reply, the gateway:

- drops children that are not under the requested URI, or that are not in canonical form
- drops children that fail the visibility check, whatever their MIME type, and whether or not they are marked as directories
- does not run `resources/result`
- sets the gateway's `ttlMs` and `cacheScope: "private"`

### Skills

- `skills/list` and `skills/get` follow the `resources` switch in `server/config`.
- On the mux route, the gateway removes the prefix from a `skills/get` URI.
- `skills/filter` runs on each skill entry. Skills nested under a denied skill are denied with it.
- The gateway drops manifest entries that are outside the skill's folder.
- If `resources/result` rules exist, the gateway builds one rewrite plan per skill and uses it for both the entry and every file read. See [resources/result](resources-result.md#skill-files).
- On the mux route, every URI in the entry gets the slug prefix last.

### Search mode

Search mode uses the same pipeline, so it can never expose a tool that the flat list would hide:

- `tool_search`: the catalog, filter and modify rules, then ranking over the exposed tools. When the `searchRoster` switch is on, ranking also uses the server's title and description after `server/modify`.
- `list_tools`: the exposed tools, grouped by slug, with names indented under each slug.
- `get_tool_definition`: the exposed definition. Names match exactly, including case.
- `execute_tool`: a `tools/call`, with every step listed above.

Tool names carry the `<slug>__` prefix only on the mux route. On the direct route, search returns the names that route lists, so they work in `execute_tool` and `tools/call` unchanged. In search mode on the mux route, `route` is `search`.

### Notifications

The gateway forwards progress notifications on a request's own response stream when the `progress` switch in `server/config` allows it. No rules run on notifications. The gateway sends no change notifications. Clients stay current through `ttlMs`.

## Mux URIs

On the direct route, URIs pass through unchanged. On the mux route, every structured URI carries the slug as a scheme prefix, `<slug>+<scheme>`:

```
file:///README.md            ->  docs+file:///README.md
file:///docs/{path}          ->  docs+file:///docs/{path}
skill://notebooks/SKILL.md   ->  docs+skill://notebooks/SKILL.md
```

The gateway adds the prefix to:

- resource and template listings
- `skills/list` and `skills/get` entries, including every manifest URI
- `contents[].uri` in read results, and children in directory read results
- `resource_link` and embedded `resource` blocks in tool results and prompt messages

The gateway removes the prefix from read and `skills/get` URIs before it calls the upstream. Rules always see the URI as the upstream knows it, without the prefix.

Template variables keep working, because the prefix comes before the scheme. Digests do not change, because they cover file bytes, not URIs.

Slugs match `[a-z][a-z0-9]*`. Slugs cannot contain `+`, so a mux URI splits at the first `+`: `docs+git+ssh://x` is slug `docs` and URI `git+ssh://x`. Tool and prompt names split at the first `__`.

A prefix that names a slug the caller does not hold returns `-32602` with `not found: <uri>`, before any upstream call.

Text inside resources and skills is not rewritten. A URI written in text reaches the client without a prefix. The mux instructions explain the prefix to the model. To rewrite URIs in text, use `slugUris()` in a [resources/result](resources-result.md) rule.

### Fallback resolution

On the mux route, a URI without a prefix resolves only when exactly one of the caller's servers is a confident match. The gateway never guesses, and never sends the URI to servers that do not list it. Candidates must pass the same visibility check as a read.

1. An exact match is a visible listed resource, a skill URI, or a skill manifest URI.
2. A match through a [supported template](templates-filter.md#how-templates-match-uris) counts only when no server has an exact match.
3. One match resolves. The response carries the prefixed URI, so the client uses the prefixed form from then on.
4. Two or more matches return `-32602` with the candidates in the message and in `data`.
5. No match returns `-32602` with `not found: <uri>`.

```json
{
  "code": -32602,
  "message": "Ambiguous URI file:///shared.md: matches docs+file:///shared.md, eng+file:///shared.md. Retry with one of these.",
  "data": { "candidates": ["docs+file:///shared.md", "eng+file:///shared.md"] }
}
```

If the catalog of one of the caller's servers is unavailable, the gateway refuses to pick a match and returns `-32602` with `cannot resolve <uri>: <slug> unavailable; retry with a prefixed URI`.

Every fallback resolution reports an event for the gateway to audit and count. The event has the `uri`, the `method`, an `outcome` (`resolved`, `ambiguous`, `not_found` or `unavailable`), the resolved `slug`, the number of `candidates`, and `template`, which is true when the match came through a template.

### Canonical URIs

The gateway refuses URIs with dot segments (`.` or `..`), empty segments (`//` in the path) or percent-encoded unreserved characters, on every route. The error is `-32602` with `URI is not in canonical form`. Content items and directory children with such URIs are dropped from upstream replies. This stops an upstream from resolving a path differently from the gateway.

Reserved escapes, such as `%2F`, stay opaque. The gateway does not decode them, so `file:///a%2Fb` is not under `file:///a/`.

## Limits

| Limit | Default | Effect when exceeded |
| --- | --- | --- |
| Upstream response size | 10 MiB | `-32013`, `upstream result exceeds gateway size limit` |
| Shaped response size | 10 MiB | `-32013`, `result exceeds gateway size limit after shaping`. Applies to every reply and listing after rules run, including `input_required` replies |
| CEL cost per evaluation | 1,000,000 | An evaluation error, which denies |
| Nodes per `drop` or `dropSchema` call | 100,000 | An evaluation error, which denies |
| Enforce rule time per request | Two seconds | `-32603`, `rule evaluation exceeded its time budget` |
| Audit rule time per request | 100 ms | Remaining audit rules are recorded as `would error`. The outcome does not change |
| Argument nesting | 64 levels | `-32602` |
| Duplicate argument keys | Not allowed | `-32602`, `arguments: duplicate key "<key>"` |
| Confirmation state | Five minutes | `-32602`, `request state expired` |
| Mux instructions | 600 characters per server, 4,000 in total | Excerpts end with a truncation mark |

The rule budget counts the time rules spend evaluating, not wall-clock time for the whole request. Large integers in arguments are forwarded exactly. Listings, reads and directory reads carry `ttlMs` (300,000 by default) and `cacheScope: "private"`.

## CEL environment

Rules use CEL with these cel-go extensions:

- strings, including `format`, `replace`, `split` and `lowerAscii`
- lists, including `slice`, `flatten` and `sortBy`
- sets: `sets.contains`, `sets.intersects` and `sets.equivalent`
- two-variable comprehensions, such as `transformMap` and `all(k, v, ...)`
- regular expressions: `regex.replace`, `regex.extract` and `regex.extractAll`
- bindings: `cel.bind`
- encoders: `base64.encode` and `base64.decode`
- math, such as `math.least` and `math.greatest`
- optional values, such as `frontmatter.?metadata.?tags.orValue([])`

Ordering comparisons (`<`, `<=`, `>`, `>=`) work across `int`, `uint` and `double` values. Equality between literals of different types does not: `1 == 1.0` fails to compile. `route` is a string, `now` is a timestamp, and every other variable is dynamic.

`slice(0, n)` fails to evaluate when the list has fewer than `n` items. To cap a list, write `size(xs) > n ? xs.slice(0, n) : xs`.

The gateway adds four helpers. Each helper is available only in the hooks listed:

| Helper | Hooks | Purpose |
| --- | --- | --- |
| `drop(value, paths)` | `tools/result` | Remove fields at dot-separated paths. `*` matches every key of a map or every element of a list |
| `dropSchema(schema, paths)` | `tools/modify` | Remove the same properties from a JSON Schema, including from `required`. A trailing `*` removes every property. A path through `$ref` is an evaluation error |
| `elicit(key, template, values)` and `elicit(key, template, values, error)` | `tools/call`, `prompts/get`, `resources/read` | Ask the user to confirm. Each `{name}` in the template is replaced by `values.name`, quoted and bounded. See [elicitation](tools-call.md#elicitation) |
| `elicit(key, message)` and `elicit(key, message, error)` | `tools/call`, `prompts/get`, `resources/read` | Ask the user to confirm with fixed text. Returns an error result with `error` as the text when the client cannot elicit |
| `slugUris(text)` | `resources/result` | Rewrite this upstream's URIs in text to the mux form. Changes nothing on the direct route |

All helpers are pure: they have no side effects and give the same answer for the same input.

These are not available in CEL: rate limits, state across requests, external lookups, and rules across a whole list, such as a maximum number of tools.

## Hook reference pages

Server hooks:

- [server/connect](server-connect.md): what the gateway tells the upstream about itself
- [server/config](server-config.md): switches for what a server exposes
- [server/modify](server-modify.md): server title, description and instructions

Tool hooks:

- [tools/filter](tools-filter.md): which tools are visible
- [tools/modify](tools-modify.md): tool names, text, annotations and parameters
- [tools/call](tools-call.md): call decisions, argument patches and confirmations
- [tools/input](tools-input.md): upstream requests for user input during a call
- [tools/result](tools-result.md): the shape of tool results

Prompt hooks:

- [prompts/filter](prompts-filter.md): which prompts are visible
- [prompts/modify](prompts-modify.md): prompt names, text and arguments
- [prompts/get](prompts-get.md): `prompts/get` decisions, argument patches and confirmations
- [prompts/input](prompts-input.md): upstream requests for user input during `prompts/get`

Resource, template and skill hooks:

- [resources/filter](resources-filter.md): which resources are visible and readable
- [resources/modify](resources-modify.md): resource text and annotations
- [templates/filter](templates-filter.md): which templates are visible and usable
- [templates/modify](templates-modify.md): template text and annotations
- [resources/read](resources-read.md): read decisions and confirmations
- [resources/input](resources-input.md): upstream requests for user input during a read
- [resources/result](resources-result.md): the content of read results and skill files
- [skills/filter](skills-filter.md): which skills and skill folders are visible
