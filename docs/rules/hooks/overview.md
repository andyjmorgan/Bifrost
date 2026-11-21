# Hooks and rules overview

Rules control what an MCP client can see and do through Bifrost. Each rule attaches to one hook, which is a point in request handling where the gateway runs rules. A rule reads one tree of values and can change one branch of it.

This page explains the tree, how to read values that may be absent, the rule shape, how rules are evaluated, and the order in which hooks run. For the details of one hook, see its page in the [list of hooks](#hooks).

## Concepts

- Route: how a client reaches the gateway. `/<slug>/mcp` is the direct route and exposes one server. `/mcp` is the mux route and exposes every server the caller holds. `?search=true` on either route is search mode, which replaces the tool list with search tools.
- Server: one MCP server behind the gateway, with its own slug, its rules and the users and roles it is granted to.
- Catalog: what the server offers. The gateway fetches and caches it per server. Rules never change the catalog. They shape a view of it for each request.
- Hook: a point in request handling where rules run. Each hook has its own tree.
- Rule: one CEL condition and the paths it sets when the condition is true.

## Hooks

A hook is named after where it runs in a request: the MCP method, then `pre` for the request or `post` for the reply. Where a reply can be several things, the name ends with what came back: `progress`, `elicit`, `result` or `task`. `server.settings` and `server.session` belong to no method. The name is the exact text of a rule's `hook` value. There are 20 hooks.

Server hooks:

- [server.settings](server-settings.md): switches for what a server exposes
- [server.session](server-session.md): what the gateway tells the server about the client
- [server/discover.post](server-discover-post.md): the server's title, description and instructions

Tool hooks:

- [tools/list.post](tools-list-post.md): which tools are visible, and their names, text, annotations and parameters
- [tools/call.pre](tools-call-pre.md): call decisions, argument changes, timeouts and confirmations
- [tools/call.post.progress](tools-call-post-progress.md): progress messages during a call
- [tools/call.post.elicit](tools-call-post-elicit.md): questions the server asks the user during a call
- [tools/call.post.result](tools-call-post-result.md): the content of tool results
- [tools/call.post.task](tools-call-post-task.md): the status of a call that runs as a task

Prompt hooks:

- [prompts/list.post](prompts-list-post.md): which prompts are visible, and their names, text and argument names
- [prompts/get.pre](prompts-get-pre.md): `prompts/get` decisions, argument changes and confirmations
- [prompts/get.post.progress](prompts-get-post-progress.md): progress messages during `prompts/get`
- [prompts/get.post.elicit](prompts-get-post-elicit.md): questions the server asks the user during `prompts/get`

Resource, template and skill hooks:

- [resources/list.post](resources-list-post.md): which resources are visible and readable, and their text and annotations
- [resources/templates/list.post](resources-templates-list-post.md): which templates are visible and usable, and their text and annotations
- [resources/read.pre](resources-read-pre.md): read decisions and confirmations
- [resources/read.post.progress](resources-read-post-progress.md): progress messages during a read
- [resources/read.post.elicit](resources-read-post-elicit.md): questions the server asks the user during a read
- [resources/read.post.result](resources-read-post-result.md): the contents of read results and skill files
- [skills/list.post](skills-list-post.md): which skills and skill folders are visible

## The tree

What a rule can see on a hook is one tree. Hooks differ only in which branches exist and which one branch a rule can change.

| Branch | What it holds | Hooks that have it |
| --- | --- | --- |
| `tool`, `prompt`, `resource`, `template`, `skill` | The item the hook is about, as the gateway presents it. Its fields are MCP's fields | The hooks of that item |
| `request` | The request: MCP's `method` and `params`, plus what the gateway knows about who sent it and how | Every hook |
| `response` | The response the client will get: `response.result` | The two `post.result` hooks |
| `progress` | One progress notification: `progress.params` | The three `post.progress` hooks |
| `task` | The status of a call that runs as a task: `task.status`, `task.statusMessage` | `tools/call.post.task` |
| `elicitation` | One question the server asks the user: `elicitation.key`, `elicitation.method`, `elicitation.params` | The three `post.elicit` hooks |
| `server` | The server as the gateway presents it, with its `id` and `slug` | Every hook |
| `settings` | The switches a rule sets. Rules cannot read it | `server.settings` |
| `session` | What the server is told about the client. Rules cannot read it | `server.session` |

Names in the tree are MCP's names wherever MCP has one, spelled as MCP spells them: `arguments`, `_meta`, `clientInfo`, `inputSchema`, `structuredContent`, `contents`. A branch that is a message looks like the message on the wire. A request is `request.method` and `request.params`. A tool result is `response.result.content`, `response.result.structuredContent`, `response.result.isError` and `response.result._meta`.

The gateway's own additions sit beside MCP's part, never inside it: `request.caller`, `request.headers`, `tool.params` and every `upstream`.

### What upstream means

`X` is the gateway's form of a thing, after earlier rules changed it. `X.upstream` is the same thing in the server's own terms:

- `tool.upstream` is the tool as the server declares it
- `request.upstream` is the request as the server receives it
- `response.upstream` is the response as the server produced it

Nothing can set `upstream`, and it never changes while rules run. Match `tool.upstream.name` when a rule must keep working after a rename. Match `tool.name` when a rule builds on earlier rules.

After a `tools/list.post` rule renames the tool `foo` to `baz`:

| Path | Value |
| --- | --- |
| `tool.name` | `baz`, the name the client sends |
| `tool.upstream.name` | `foo`, the server's name |
| `request.params.name` | `baz` |
| `request.upstream.params.name` | `foo` |

`request.upstream` exists only on the post hooks. On a pre hook nothing has been sent to the server yet, so a rule that reads it is refused when saved.

On the post hooks, `request.params.arguments` holds the arguments under the names the client knows, as the pre rules left them. `request.upstream.params.arguments` holds what the server received, after renames, defaults and pins.

`server.upstream` exists only on `server/discover.post`. `settings` and `session` have no `upstream`.

### Each hook changes one branch

A rule sets a value by naming its path. The `set` paths are the same paths a rule reads: `tool.name`, `request.params.arguments.limit`, `response.result.content`, `settings.prompts`.

| Hooks | The branch a rule can change |
| --- | --- |
| The four list hooks | The item: `tool`, `prompt`, `resource` or `template` |
| `tools/call.pre`, `prompts/get.pre` | `request.params.arguments` |
| `resources/read.pre`, `skills/list.post` | Nothing. These rules only decide |
| The `post.result` hooks | `response.result` |
| The `post.progress` hooks | `progress.params` |
| `tools/call.post.task` | `task` |
| The `post.elicit` hooks | `elicitation.params` |
| `server/discover.post` | `server` |
| `server.settings` | `settings` |
| `server.session` | `session` |

`timeout` is the one plain name a rule sets, on `tools/call.pre`. It is the gateway's limit on the forwarded call, not part of the request.

Within its branch, a hook lists exactly which paths are writable. Each hook page has the list.

### What the gateway refuses when a rule is saved

The gateway checks the paths in a rule against the hook's tree when the rule is saved, so a wrong name is found then. The check follows plain paths, `has()`, index forms, macro variables and `cel.bind`. It does not follow a value through a ternary, a list or map literal, `filter(...)[0]`, or a key worked out when the rule runs. A wrong name there is found when a request arrives: the rule fails, and a failed rule denies.

| Mistake | Message |
| --- | --- |
| Reading a name the tree does not have | `tool has no "readOnly"; it has _meta, annotations, description, icons, inputSchema, name, outputSchema, params, title, upstream` |
| Reading `request.upstream` on a pre hook | `request.upstream is not available on tools/call.pre: nothing has been sent to the server yet; it is there on the post hooks` |
| Setting a path that exists but is not writable | `tool.upstream.name cannot be changed on tools/list.post; a rule here can set fields of tool` |
| Setting a path that does not exist | `there is no limit on tools/call.pre; a rule here can set request.params.arguments.*, timeout` |
| A flag name that is not a flag | `unknown flag "serach"; the flags are cli, search` |

### The request branch

| Path | Type | What it holds |
| --- | --- | --- |
| `request.method` | string | The MCP method the hook belongs to, such as `tools/call`. It is `resources/directory/read` on a directory read, and `server/discover` on the three server hooks |
| `request.params` | map | MCP's part of the request. See the table below |
| `request.caller` | map | Who sent the request. Each gateway defines these fields |
| `request.client` | map | `clientInfo` (`name`, `version`, `title`) and `capabilities`, as the client declared them on this request |
| `request.headers` | map | The HTTP request headers on the gateway's allow-list, by lower-case name. The list starts empty |
| `request.route` | string | `direct` or `mux` |
| `request.flags` | map | The request's switches. See [flags](#flags) |
| `request.time` | timestamp | When the request arrived |
| `request.upstream` | map | The request in the server's terms: `request.upstream.params`. Only on the post hooks |

`request.params` depends on the method:

| Hooks | `request.params` holds |
| --- | --- |
| `tools/call` hooks | `name`, `arguments`, `_meta` |
| `prompts/get` hooks | `name`, `arguments` with string values, `_meta` |
| `resources/read` hooks | `uri`, `_meta` |
| List hooks and server hooks | `_meta` only |

`request.params.arguments` is always a map. It is empty when the client sent no arguments. An argument the client did not send is absent.

On Bifrost, `request.caller` has `userId` and `type`, and `email`, `name` and `keyName` when the caller has them. A caller with no email has no `email` field, so read it with `request.caller.?email.orValue('')`. `type` is `oidc` for a signed-in user, `apikey` for an API key, `dev` or `system`. The engine treats the caller as opaque, so another gateway can supply other fields.

`request.client.clientInfo` is self-reported, and absent when the client sent none. Do not use it for security decisions. `request.client.capabilities` is always a map, so `has(request.client.capabilities.elicitation)` never fails.

`request.headers` holds only the headers the operator names in `MCPGW_RULE_HEADERS`. Headers that carry credentials never reach a rule, even when the operator names them: any header whose name contains `auth`, `cookie`, `token`, `secret`, `password`, `credential`, `signature`, `session`, `api-key` or `apikey`. Read a header with `request.headers[?'x-team'].orValue('')`, because a header the client did not send is absent.

### The server branch

| Path | Type | What it holds |
| --- | --- | --- |
| `server.id` | string | The server's identity, which never changes. The slug when the gateway has no id |
| `server.slug` | string | The server's slug |
| `server.name`, `server.title`, `server.description`, `server.instructions`, `server.protocolVersion`, `server.url` | string | What the gateway knows about the server. Each is absent when the server did not say |

## Optional values

Nothing in the tree is filled in. An optional field that the server or client did not send is absent, not empty. A plain read of an absent value fails the rule, and an enforce rule that fails denies.

Read a value that may be absent with CEL's optional forms, and state your own default:

| Form | Returns |
| --- | --- |
| `tool.?title` | An optional value. It holds the title when there is one, and is empty otherwise |
| `tool.?annotations.?destructiveHint` | An optional value. It is empty when any step of the chain is absent |
| `.orValue(true)` | The value, or the default you give when it is absent |
| `.hasValue()` | `true` when the value is present |
| `tool.?_meta[?'team/owner']` | An optional value for a map key, for keys that are not plain names |
| `response.result.content[?0]` | An optional value for a list index. It is empty when the list is shorter |
| `{?'mimeType': c.?mimeType}` | A map that has the key only when the optional value is present |
| `has(tool.title)` | `true` when the last step is present. Every step before it must exist |

`has(a.b.c)` guards only the last step. `has(tool.annotations.destructiveHint)` fails on a tool with no `annotations`. Write `tool.?annotations.?destructiveHint.hasValue()` for a chain.

CEL's `&&` and `||` tolerate an error on one side when the other side decides the result. So `has(request.params.arguments.limit) && request.params.arguments.limit > 100` is false when `limit` is missing, and does not fail.

### Why a plain read of an absent value denies

The gateway fails closed. If an absent value read as `false`, a deny rule would let through exactly the items it knows least about.

This rule is meant to hide destructive tools:

```json
{
  "hook": "tools/list.post",
  "when": "tool.annotations.destructiveHint",
  "then": { "decision": "'deny'" }
}
```

A tool with no annotations might delete anything. MCP says a missing `destructiveHint` means the tool may be destructive. If "missing" read as `false`, this rule would list that tool. Instead the read fails, the rule fails, and the tool is hidden.

Write the rule with its own default, so that it does not depend on a failure:

```json
{
  "hook": "tools/list.post",
  "when": "tool.?annotations.?destructiveHint.orValue(true)",
  "then": { "decision": "'deny'" }
}
```

Both rules list only tools that declare `destructiveHint: false`. The second one records no error.

A rule that reads an optional value with a plain `.` still saves. The gateway records a note against the rule, saying that it denies when the value is absent. A read gets no note where a test guards it: `has(x) && x`, `!has(x) || x`, `has(x) ? x : y`, a `has()` in the condition for the same path in a value, or a step after `.?`. A test guards only what runs when it holds, so `has(x) || x` is noted. The check follows macro variables: in `response.result.content.exists(c, c.text.contains('x'))` it knows `c` is a content block and notes that `text` may be absent.

A value whose type is known when the rule is saved must fit its path. `settings.tools: 'off'`, `tool.name: 5` and `tool.title: null` are refused then, not when a request arrives. A value whose form is wrong below its type, such as a duration that is not positive, is found when a request arrives.

### Values that are always present

- `request.params.arguments` and `request.client.capabilities` are always maps.
- Every flag in `request.flags` is always present, as `true` or `false`.
- A tool that has `annotations` always carries `readOnlyHint` and `idempotentHint`. They are `false` when the server left them out, because the Go SDK types them as plain booleans. `destructiveHint` and `openWorldHint` are absent when the server did not send them.
- A listed resource or template always has `name`.

An empty string and a size of zero in a catalog read as absent. So does an explicit `null`.

## Rule shape

A rule is a JSON object:

```json
{
  "id": "r1",
  "name": "Hide delete tools",
  "enabled": true,
  "mode": "enforce",
  "hook": "tools/list.post",
  "when": "tool.name.startsWith('delete_') && !(request.caller.email in ['sre-lead@example.com'])",
  "then": { "decision": "'deny'", "reason": "'Deleting is restricted.'" }
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
| `then` | Yes | The paths to set when `when` is true. Each value is a CEL expression |

Each key of `then` is a path in the hook's tree, or one of three special keys:

| Key | Value | Effect |
| --- | --- | --- |
| `decision` | The literal `'deny'` | Deny. What a deny does depends on the hook |
| `reason` | A string | The reason for the deny. On pre, elicit and result hooks the client receives it. On list and progress hooks it is recorded only |
| `confirm` | `elicit(...)` | Ask the user to confirm before the request goes ahead. Only on the three pre hooks |

Every value in `then` is a CEL expression, not a literal. A string value therefore needs CEL quotes inside the JSON string: write `"'deny'"`, not `"deny"`. Numbers and booleans need no quotes: `"100"`, `"true"`.

A rule does not compile when:

- the hook does not exist
- `when` or a `then` value reads a path the hook's tree does not have, such as `request.params.arguments` in `tools/list.post`
- `then` sets a path that does not exist on the hook, or that a rule there cannot change
- `when` does not produce a boolean
- `decision` is anything other than the literal `'deny'`. There is no allow decision
- a `skills/list.post` rule does not set `decision`
- a list rule sets `reason` without `decision`. A reason only has an effect beside a deny
- a pin or default in `tools/list.post` reads the request, such as `request.caller`

An enabled rule that does not compile fails the whole server closed. The server exposes nothing, and listings report the problem to administrators.

A disabled rule that does not compile is ignored. The gateway reports it as a warning, so nobody enables the rule without seeing the error. The server keeps working.

`when` has no `else`. A rule either matches or does not. For two outcomes, write two rules, or use a ternary (`cond ? a : b`) inside a `then` value.

## Evaluation

The gateway runs a hook's rules on each item separately, from top to bottom. An item is one tool, prompt, resource, template, skill, request, response, progress notification or question.

| Hook kind | Changes that chain | What stops evaluation |
| --- | --- | --- |
| List (`tools/list.post`, `prompts/list.post`, `resources/list.post`, `resources/templates/list.post`) | Every writable path of the item | `decision: 'deny'` |
| `skills/list.post` | None | `decision: 'deny'` |
| Pre (`tools/call.pre`, `prompts/get.pre`, `resources/read.pre`) | `request.params.arguments.*`, and `timeout` on `tools/call.pre` | `decision: 'deny'`, `confirm: elicit(...)` |
| Elicit (the `post.elicit` hooks) | `elicitation.params.message` | `decision: 'deny'` |
| Progress (the `post.progress` hooks) | `progress.params.message` | `decision: 'deny'` |
| Task (`tools/call.post.task`) | `task.statusMessage` | `decision: 'deny'` |
| Result (the `post.result` hooks) | `response.result.content` and `response.result.structuredContent`, or `response.result.contents` for a read | `decision: 'deny'` |
| Server (`server.settings`, `server.session`, `server/discover.post`) | Every writable path | Nothing |

### List hooks hide and change

The four list hooks can both hide an item and change it. They are `tools/list.post`, `prompts/list.post`, `resources/list.post` and `resources/templates/list.post`.

- The rules run from top to bottom, in the order you write them. A rule either hides the item, with `decision: 'deny'` and an optional `reason`, or changes it.
- Changes chain into hide rules. A hide rule written after a rename sees the new `tool.name`. `tool.upstream.name` is always the server's own name.
- The first deny hides the item and stops the enforce rules. Audit rules still run for the log.
- A rule that sets both `decision` and other paths hides the item. Its other values are discarded.
- Writing hide rules first is cheaper, because a hidden item is never changed. It is not required.

`skills/list.post` can only hide. Its rules must set `decision: 'deny'`.

You can only call what you can list. A `tools/call`, `prompts/get` or `resources/read` first runs the matching list hook for the item it names, so a hidden item cannot be called, fetched or read.

A `resources/read` of a URI that no listing contains is allowed unless something denies it. It still runs `resources/list.post`, but only its rules that deny, with only `resource.uri` to read. See [URIs that no listing contains](resources-list-post.md#uris-that-no-listing-contains).

### Changes chain

A later rule sees the values that earlier rules set. In this example, the second rule matches the name the first rule produced, and reads the server's name from `tool.upstream`:

```json
[
  {
    "hook": "tools/list.post",
    "when": "tool.name.startsWith('confluence_')",
    "then": { "tool.name": "regex.replace(tool.name, '^confluence_', 'wiki_')" }
  },
  {
    "hook": "tools/list.post",
    "when": "tool.name.startsWith('wiki_')",
    "then": { "tool.description": "'[' + tool.upstream.name + '] ' + tool.?description.orValue('')" }
  }
]
```

The server's tool `confluence_search` with the description `Search.` is listed as `wiki_search` with the description `[confluence_search] Search.`.

A rule evaluates all of its `then` values before it applies any of them. If one value fails, the rule applies nothing.

### A deny is final

The first deny stops evaluation for that item, and no later rule can undo it. Because there is no allow decision, write exceptions into the deny condition itself:

```json
{
  "hook": "tools/list.post",
  "when": "tool.name.startsWith('delete_') && !(request.caller.email in ['sre-lead@example.com'])",
  "then": { "decision": "'deny'" }
}
```

A deny rule for all `delete_` tools followed by a rule meant to re-allow some callers hides the tools from them too.

To give a group of callers different rules, leave them out of the deny itself, for example with `&& !(request.caller.?email.orValue('') in ['sre-lead@example.com'])`. A later rule cannot undo a deny.

### Audit mode

A rule with `"mode": "audit"` runs and records what it would have done, such as `would deny` or `would set`, but never changes the outcome. Audit rules still run after an enforce rule stops evaluation, so the audit record is complete.

On Bifrost the record is the request's audit event. The Audit page shows a count beside each request that a rule applied to. The event's detail lists every hit: the hook, the rule's line, the server, the item, the effect and the reason. Enforce rules are listed too. An event keeps 100 hits. When a request has more, plain `set` hits are dropped first and the detail says how many.

An audit rule that fails to evaluate is recorded as `would error` and changes nothing. Audit rules have their own time allowance, separate from the enforcement budget. When the audit allowance runs out, the remaining audit rules are recorded as `would error`, and the request continues as if they did not exist.

### Errors fail closed

An enforce rule that fails to evaluate counts as a deny and stops evaluation. Common causes are:

- a plain read of an absent value
- a value of the wrong type for its path
- a CEL cost limit
- the request time budget

What the failure means depends on the hook:

| Hook kind | Effect of an evaluation error |
| --- | --- |
| List | The item is hidden and cannot be called or read. The listing reports the problem to administrators |
| `skills/list.post` | The skill is hidden |
| Pre, elicit and result | The request is refused with the hook's generic refusal, such as `The call to <tool> was refused by policy.` |
| Progress | The progress notification is dropped. The request continues |
| `server.settings` | Every switch is off |
| `server.session` | The gateway uses its anonymous defaults |
| `server/discover.post` | Title, description and instructions are empty |

### Refusals never name rules

A client never sees a rule's name, id or position. A deny without a `reason`, a rule that fails to evaluate, and a change the gateway does not allow all return the same generic refusal for the hook. The rule's label appears only in the hits and the audit record.

A `reason` that an administrator writes reaches the client unchanged, so write reasons that are safe to show.

## Flags

A request reaches the gateway by one route, `direct` or `mux`, and can also carry flags. A flag is a switch on the request that is independent of the route: any flag can be on for either route.

| Flag | Type | True when |
| --- | --- | --- |
| `request.flags.search` | bool | The request is in search mode, set with `?search=true`. The gateway offers its search tools in place of the full listing |
| `request.flags.cli` | bool | The request comes from the command line client, through the gateway's API. The gateway sets this flag and a client cannot. The request does not come in through MCP, but the call the gateway sends to the server is the same |

- Read a flag by name, as `request.flags.search`. Every flag is always present, and is `false` when the request did not set it, so no guard is needed.
- A name that is not a flag is refused when the rule is saved. `request.flags.serach` does not compile, and neither does `'search' in request.flags`. `request.flags['search']` compiles, because the gateway can check the key.
- New flags are added to this list over time. A rule that does not mention a flag is not affected by it.
- `request.flags.search` is something the client says about its own request. Use it to tailor behaviour. A rule that relaxes a deny because `request.flags.search` is set is trusting the caller.
- `request.flags.cli` says how the request arrived, and the gateway sets it. An MCP client that sends `?cli=true` does not get it, so a rule can rely on it.
- A `request.flags.cli` request has no client to show a question to. A rule that asks for confirmation refuses such a request, the server is not told that elicitation is available, and a question the server asks is refused.

```
request.route == 'mux' && request.flags.search && !request.flags.cli
```

## Numbers

An integral JSON number that fits in a 64-bit integer reaches CEL as an `int`, whatever its spelling: `1`, `1.0` and `1e0` all equal `1`. Other numbers are `double` values.

The gateway compares pins, `enum` values, `minimum` and `maximum` exactly, without rounding. It forwards every value with the client's original text, unless a rule changes it.

## Order of hooks per request

Rules run on every request over the cached catalog. The gateway does not cache rule output, results or content.

Every request the gateway forwards to a server carries the result of [server.session](server-session.md): the client identity, the capabilities and whether `_meta` is forwarded.

### Discover

1. `server.settings` sets the switches.
2. `server/discover.post` rewrites title, description and instructions.

The result always carries `supportedVersions: ["2026-07-28"]`, `ttlMs` and `cacheScope: "private"`. Display identity is in `_meta["io.modelcontextprotocol/serverInfo"]`, not in a top-level `serverInfo`. Where at least one server serves skills, `capabilities.extensions["io.modelcontextprotocol/skills"]` is `{"directoryRead": true}`.

On the direct route, the result is the gateway's own `server/discover` answer for that server. On the mux route, instructions appear as slug-labelled excerpts, and the `get_server_instructions` tool returns the full text. Every line of an excerpt starts with `> `, so a server's text cannot start a heading that poses as another server's section. Discover always advertises `listChanged: false`, and never advertises completions or logging.

### Listing tools

1. `server.settings`. If `settings.tools` is off, the server contributes nothing.
2. The catalog, from cache or from the server.
3. `tools/list.post` on each tool. Its rules run in the order written, and each one hides the tool or changes it.
4. The gateway checks for name collisions. Tools that share a final name are all hidden.
5. On the mux route, each name gets the prefix `<slug>__`, and the gateway adds its `get_server_instructions` tool.

Output is sorted by slug, then by name. The output is the same, byte for byte, whatever order the server sends.

### Calling a tool

1. `server.settings`. If `settings.tools` is off, the call fails as an unknown tool.
2. `tools/list.post` runs over the catalog, and the gateway looks up the name the client sent. A missing name fails with `-32602`.
3. The gateway builds the effective arguments under the names the client knows. It rejects values for hidden parameters and for the server's names of renamed parameters. It rejects undefined keys when the server's schema sets `additionalProperties: false`. It fills defaults, applies visible pins, and checks `enum`, `minimum` and `maximum`.
4. `tools/call.pre` rules run on the effective arguments, as `request.params.arguments`. Changes chain. The first deny or `confirm` stops evaluation.
5. The gateway forces pinned values, checks constraints again, maps names and keys back to the server's, and removes added parameters. A hidden required parameter that still has no value refuses the call.
6. The gateway calls the server.
7. If the server replies with `input_required`, `tools/call.post.elicit` runs on each question. `tools/call.post.result` does not run.
8. If the server replies with a result, `tools/call.post.result` shapes it. Then, on the mux route, structured URIs in the result get the slug prefix.

On the post hooks, `request.upstream.params.arguments` holds the exact arguments that were forwarded in step 6. Gateway confirmations from step 4 resolve before step 6. If the rules ask a new question on the round that answers a server's question, the request is refused and must be sent again. See [effective arguments](tools-call-pre.md#effective-arguments) and [confirmation](tools-call-pre.md#confirmation).

### Prompts

- `prompts/list`: `server.settings`, the catalog, `prompts/list.post`, then the mux name prefix. Sorted by slug, then by name.
- `prompts/get`: `server.settings`, name lookup through the `prompts/list.post` rules, `prompts/get.pre` rules, reversal of renames, then the call to the server. An `input_required` reply goes to `prompts/get.post.elicit`. Otherwise, on the mux route, structured URIs in the messages get the slug prefix. There is no result hook for prompts.

### Resources and templates

One visibility check decides every resource URI. Listings, reads, directory children and the entries of a read result all use it, so they cannot disagree.

A read is allowed unless something denies it. That includes a URI the server never listed: servers hand out URIs they do not list, in tool results and through templates. A URI is hidden and unreadable when any of these is true:

- it is under a denied skill folder
- a template that covers it is denied, failed in a `resources/templates/list.post` rule, or collided
- it is listed, but a `resources/list.post` rule denied it or failed on it, or it collided with another resource
- a `resources/list.post` deny rule denies it. The deny rules run on every read URI, listed or not. For a URI that no listing contains, they have only `resource.uri` to read
- it is not in canonical form

To close a folder, deny its prefix in [resources/list.post](resources-list-post.md), for example `resource.uri.startsWith('file:///secret/')`. That covers listed resources, templates, deeper paths and unlisted URIs alike, because a URI has one spelling the gateway accepts: see [canonical form](#uri-form). Not listing something does not hide it. A rule that compares with `==` matches one URI only. A listed resource that a rule hides, and a template a rule denies, also cover the same URI with a query or a trailing slash added, changed or taken away. The one exception is a URI the server lists itself: a listed resource is judged by the rules on its own.

The gateway never decodes a URI. `file:///a%2Fb` is not `file:///a/b`. It is forwarded exactly as the client sent it, as its own resource.

The listings use that check:

- `resources/list`: `server.settings`, then `skills/list.post` to find denied skill folders, then `resources/list.post` and the visibility check, then the mux URI prefix.
- `resources/templates/list`: `server.settings`, the catalog, `resources/templates/list.post`, then the mux URI prefix.

Resources, templates and skills are sorted by slug, then by URI within each slug. The output is the same, byte for byte, whatever order the server sends.

A `resources/read` runs these steps:

1. `server.settings`. If `settings.resources` is off, the URI is not found.
2. The gateway refuses a URI that is not in canonical form.
3. On the mux route, the gateway removes the `<slug>+` prefix to find the server. A URI without a prefix goes through [fallback resolution](#fallback-resolution).
4. The visibility check. A URI it rejects is unknown.
5. `resources/read.pre` rules.
6. The gateway reads from the server, with the URI as the server knows it.
7. An `input_required` reply goes to `resources/read.post.elicit`. Otherwise, the gateway drops entries with a non-canonical URI or a URI the caller cannot read. Then `resources/read.post.result` runs once over the whole response, with every remaining entry in `response.result.contents`.
8. On the mux route, structured URIs in the result get the slug prefix.

The result carries the gateway's `ttlMs` and `cacheScope: "private"`, whatever the server sent.

### Directory reads

`resources/directory/read` follows the same steps as `resources/read` up to the call to the server, including the `resources/read.pre` rules. In the reply, the gateway:

- drops children that are not under the requested URI, or that are not in canonical form
- drops children that fail the visibility check, whatever their MIME type, and whether or not they are marked as directories
- does not run `resources/read.post.result`
- sets the gateway's `ttlMs` and `cacheScope: "private"`

### Skills

- `skills/list` and `skills/get` follow `settings.resources` in `server.settings`.
- On the mux route, the gateway removes the prefix from a `skills/get` URI.
- `skills/list.post` runs on each skill entry. Skills nested under a denied skill are denied with it.
- The gateway drops manifest entries that are outside the skill's folder.
- If `resources/read.post.result` rules exist, the gateway builds one rewrite plan per skill and uses it for both the entry and every file read. See [skill files](resources-read-post-result.md#skill-files).
- On the mux route, every URI in the entry gets the slug prefix last.

### Search mode

Search mode uses the same pipeline, so it can never expose a tool that the flat list would hide:

- `tool_search`: the catalog, `tools/list.post` rules, then ranking over the exposed tools. When `settings.searchRoster` is on, ranking also uses the server's title and description after `server/discover.post`.
- `list_tools`: the exposed tools, grouped by slug, with names indented under each slug.
- `get_tool_definition`: the exposed definition. Names match exactly, including case.
- `execute_tool`: a `tools/call`, with every step listed above.

Tool names carry the `<slug>__` prefix only on the mux route. On the direct route, search returns the names that route lists, so they work in `execute_tool` and `tools/call` unchanged. Search mode does not change `request.route`: it stays `direct` or `mux`, and `request.flags.search` is `true`.

### Notifications

A server may answer a request with a stream: notifications first, then the result. The gateway relays progress notifications from that stream when all of these are true:

- the client sent a progress token
- `settings.progress` in [server.settings](server-settings.md) is on
- the server speaks MCP 2026-07-28

Each progress notification passes through the progress hook for its kind of request: [tools/call.post.progress](tools-call-post-progress.md), [prompts/get.post.progress](prompts-get-post-progress.md) or [resources/read.post.progress](resources-read-post-progress.md). A rule there can drop it or reword its message.

Log messages and every other notification on the stream are dropped. No rule runs on them. The gateway sends no change notifications. Clients stay current through `ttlMs`.

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

The gateway removes the prefix from read and `skills/get` URIs before it calls the server. Rules always see the URI as the server knows it, without the prefix.

Template variables keep working, because the prefix comes before the scheme. Digests do not change, because they cover file bytes, not URIs.

Slugs match `[a-z][a-z0-9]*`. Slugs cannot contain `+`, so a mux URI splits at the first `+`: `docs+git+ssh://x` is slug `docs` and URI `git+ssh://x`. Tool and prompt names split at the first `__`.

A prefix that names a slug the caller does not hold returns `-32602` with `not found: <uri>`, before any call to a server.

Text inside resources and skills is not rewritten. A URI written in text reaches the client without a prefix. The mux instructions explain the prefix to the model. To rewrite URIs in text, use `slugUris()` in a [resources/read.post.result](resources-read-post-result.md) rule.

### Fallback resolution

On the mux route, a URI without a prefix resolves only when exactly one of the caller's servers is a confident match. The gateway never guesses, and never sends the URI to servers that do not claim it. A server claims a URI when it lists it or one of its templates could produce it. Candidates must also pass the same visibility check as a read.

A URI that no server claims is not found on the mux, even though the same URI with a slug prefix is readable. A link in a tool result always carries the prefix, so this only affects URIs typed without one.

1. An exact match is a visible listed resource, a skill URI, or a skill manifest URI.
2. A match through a [template](resources-templates-list-post.md#how-templates-match-uris) counts only when no server has an exact match.
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

If the catalog of one of the caller's servers is unavailable, the gateway refuses to pick a match and returns `-32602` with `cannot resolve <uri>: <slug> unavailable, and may be the server this URI belongs to; retry when it is back`. This also holds up a prefixed URI that the named server does not list. `a+docs://h/x` can be server `a` and `docs://h/x`, or a URI of the scheme `a+docs` that the unavailable server owns, and the gateway does not guess. A prefixed URI that the named server lists or covers with a template is not held up.

Every fallback resolution reports an event for the gateway to audit and count. The event has:

- the `uri` and the `method`
- an `outcome`: `resolved`, `ambiguous`, `not_found` or `unavailable`
- the resolved `slug` and the number of `candidates`
- `template`, which is true when the match came through a template

### Canonical URIs

<a id="uri-form"></a>A rule reads a URI as text, and a server may read several spellings as the same URI. So the gateway says what a URI must look like and refuses everything else, on every route:

- before the query, only the characters a URI may hold as they are: letters, digits and `-._~!$&'()*+,;=:@/%[]`. Anything else must be percent-encoded, including a letter that is not ASCII: `caf%C3%A9`, not `café`
- no spaces, control characters or backslashes anywhere, and no fragment
- a percent escape is `%` and two upper-case hex digits, and never for a letter, a digit or `-._~`
- the scheme in lower case
- `//` after the scheme when the path starts with a slash, and always for `file`, `http`, `https`, `ws`, `wss` and `ftp`: `file:/secrets/key` is refused
- for those six schemes, the host is a name in lower case, a dotted IPv4 address, or an IPv6 address in its shortest form. A name in another script comes as punycode. There is no user. A port is not empty, not the default and has no leading zero. `file://localhost/` is refused
- no dot segments (`.` or `..`) and no empty segments (`//` in the path)

For any other scheme, such as `confluence://ENG/home`, the text after `//` is the server's own and is compared as written; only a port is checked. The query is not checked: it often carries another URL. A hidden resource, a denied template and a denied skill folder are matched without the query.

A server may list a resource under a spelling that is not canonical, such as `https://WIKI.example/private`. The gateway works out the canonical spelling, and that is the one rules read as `resource.uri`, the one in the listing and the one a client asks for. `resource.upstream.uri` keeps the server's spelling on every hook, and the read goes to the server in that spelling. So a deny by prefix or by name covers the resource however the server wrote it. The same holds for a template: `template.uriTemplate` and the listing have its scheme and host in canonical form, and a denied template covers the URIs a client can send. It is matched with and without its fixed query and trailing slash, as the URI is. A denied template covers what it spells, not what a server makes of a value: if a server expands `id=..` in `file:///public/{id}/secret`, the path folds to `file:///secret`, which is an ordinary read, refused only if a rule denies it.

Two exceptions are refused rather than guessed at:

- A resource whose URI the gateway cannot put in canonical form, such as one with a user name in it, is not listed, and the gateway records an issue.
- A template whose fixed text is not canonical, such as `https://wiki.example/%73ecret/{id}`, or that has an expression in the host or port of one of the six schemes above, is not listed, and the gateway records an issue. If a rule denies it, the deny covers everything under the canonical form of its fixed text before the first expression, or under the fixed start of the host when the expression is in the host or port, and the gateway records how far it reaches.
- Two resources the server lists under spellings of one URI, such as `https://WIKI.example/doc` and `https://wiki.example/doc`, are both hidden, whatever the rules say, and the gateway records a collision. A client asking for that URI could not be told which one it gets. The error is `-32602` with `URI is not in canonical form`. Entries and directory children with such URIs are dropped from a server's replies. This stops a server from resolving a path differently from the gateway.

Reserved escapes, such as `%2F`, stay opaque. The gateway does not decode them, so `file:///a%2Fb` is not under `file:///a/`.

## Limits

| Limit | Default | Effect when exceeded |
| --- | --- | --- |
| Server response size | 10 MiB | `-32013`, `upstream result exceeds gateway size limit` |
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

- optional values: `.?`, `[?key]`, `orValue`, `hasValue` and `optional.of`
- strings, including `format`, `replace`, `split` and `lowerAscii`
- lists, including `slice`, `flatten` and `sortBy`
- sets: `sets.contains`, `sets.intersects` and `sets.equivalent`
- two-variable comprehensions, such as `transformMap` and `all(k, v, ...)`
- regular expressions: `regex.replace`, `regex.extract` and `regex.extractAll`
- bindings: `cel.bind`
- encoders: `base64.encode` and `base64.decode`
- math, such as `math.least` and `math.greatest`

CEL's list macros are the way to work with lists: `exists`, `all`, `filter` and `map`.

Ordering comparisons (`<`, `<=`, `>`, `>=`) work across `int`, `uint` and `double` values. Equality between literals of different types does not: `1 == 1.0` fails to compile. Every branch of the tree is dynamically typed, so CEL checks the types of tree values when a rule runs.

`slice(0, n)` fails to evaluate when the list has fewer than `n` items. To cap a list, write `size(xs) > n ? xs.slice(0, n) : xs`.

The gateway adds four helpers. Each helper is available only in the hooks listed:

| Helper | Hooks | Purpose |
| --- | --- | --- |
| `drop(value, paths)` | `tools/call.post.result` | Remove fields at dot-separated paths. `*` matches every key of a map or every element of a list |
| `dropSchema(schema, paths)` | `tools/list.post` | Remove the same properties from a JSON Schema, including from `required`. A trailing `*` removes every property. A path through `$ref` is an evaluation error |
| `elicit(key, template, values)` and `elicit(key, template, values, error)` | `tools/call.pre`, `prompts/get.pre`, `resources/read.pre` | Ask the user to confirm. Each `{name}` in the template is replaced by `values.name`, quoted and bounded. See [confirmation](tools-call-pre.md#confirmation) |
| `elicit(key, message)` and `elicit(key, message, error)` | `tools/call.pre`, `prompts/get.pre`, `resources/read.pre` | Ask the user to confirm with fixed text. Returns an error with `error` as the text when the client cannot elicit |
| `slugUris(text)` | `resources/read.post.result` | Rewrite this server's URIs in text to the mux form. Changes nothing on the direct route |

All helpers are pure: they have no side effects and give the same answer for the same input.

These are not available in CEL: rate limits, state across requests, external lookups, and rules across a whole list, such as a maximum number of tools.

## Print the tree of a hook

The command `hookctl hooks` prints every hook as JSON: its `tree`, the paths its rules can set (`sets`), whether its rules can deny (`denies`) and confirm (`confirms`), and its helpers. It also prints the flags. `hookctl hooks tools/call.pre` prints one hook.

In the tree, `optional` marks a value that may be absent, `writable` marks a value a rule can set, and `writeOnly` marks a value a rule sets but cannot read.
