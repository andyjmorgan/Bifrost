# tools/list.post

`tools/list.post` decides which of a server's tools are visible, and how a visible tool is presented: its name, text, annotations, parameters and output schema. A hidden tool is left out of listings and cannot be called. The gateway reverses names and parameter keys when the client calls the tool.

## When it runs

`tools/list.post` runs on each of the server's tools, after [server.settings](server-settings.md). It runs on every request that needs the tool view:

- `tools/list`
- every `tools/call`, to find the tool the client named
- the search tools: `tool_search`, `list_tools`, `get_tool_definition` and `execute_tool`

A client can only call what it can list. A `tools/call` first runs this hook for the tool it names, and [tools/call.pre](tools-call-pre.md) rules see the tool as this hook left it.

After the last rule, the gateway checks for collisions. On the mux route, the gateway adds the `<slug>__` prefix after this hook.

## Order of rules

The rules run on each tool from top to bottom, in the order you write them. A rule can do one of two things:

- hide the tool, by setting `decision: 'deny'`
- change the tool, by setting any of the [writable paths](#what-a-rule-can-set)

Changes chain. A later rule reads the tool as earlier rules left it, so a hide rule written after a rename sees the new `tool.name`. `tool.upstream.name` is always the server's own name.

The first deny hides the tool and stops the enforce rules. Audit rules still run, so the audit record is complete.

Writing hide rules first is cheaper, because the gateway never changes a hidden tool. It is not required.

A rule that fails also hides the tool. That covers an enforce rule that fails to evaluate and a change the gateway cannot apply, such as a rename that collides. The tool cannot be called, and the listing reports the problem to administrators.

## What a rule can read

| Branch | Notable fields |
| --- | --- |
| `tool` | The tool as earlier rules left it: `tool.name`, `tool.title`, `tool.description`, `tool.inputSchema`, `tool.outputSchema`, `tool.annotations`, `tool.icons`, `tool._meta` |
| `tool.params` | The gateway's view of the visible parameters. Each current parameter name maps to its property schema, with renames and constraints applied |
| `tool.upstream` | The tool as the server declares it, with the same MCP fields. It never changes |
| `request` | `request.caller`, `request.client`, `request.headers`, `request.route`, `request.flags`, `request.time`. `request.params` holds only `_meta` here |
| `server` | `server.slug`, and `server.name`, `server.title`, `server.description`, `server.instructions` where the server gave them |
| `entitlement` | `entitlement.id`, `entitlement.slug`, `entitlement.name` |

Only `tool.name` and `tool.inputSchema` are always present. Every other field of the tool is absent when the server did not send it. Read those with the [optional forms](overview.md#optional-values):

| Read | Use |
| --- | --- |
| A title, falling back to the name | `tool.?title.orValue(tool.name)` |
| A hint, with your own default | `tool.?annotations.?destructiveHint.orValue(true)` |
| Whether the server declared a hint | `tool.?annotations.?destructiveHint.hasValue()` |
| A `_meta` key | `tool.?_meta[?'team/owner'].orValue('')` |
| Whether a parameter exists | `'limit' in tool.params` |

A tool that has `annotations` always carries `readOnlyHint` and `idempotentHint`, as `false` when the server left them out. `destructiveHint` and `openWorldHint` are absent when the server did not send them.

## What a rule can set

`<p>` is a parameter's current name, after any earlier rename.

| Path | Type | Effect | Reversal on call |
| --- | --- | --- | --- |
| `tool.name` | string | Rename the tool | The name the client sends maps back to the server's name |
| `tool.title`, `tool.description` | string | Replace the text | None |
| `tool.annotations.<hint>` | bool, or string for `title` | Set a hint, such as `tool.annotations.readOnlyHint`. Only the annotations MCP names can be set | None |
| `tool._meta.<key>` | any | Set a `_meta` key. `null` removes the key | None |
| `tool.outputSchema` | map | Replace the output schema | None |
| `tool.params.<p>.name` | string | Rename a parameter | The argument key maps back |
| `tool.params.<p>.description` | string | Replace the parameter description | None |
| `tool.params.<p>.hide` | bool | Remove the parameter from the schema | A client value is rejected |
| `tool.params.<p>.pin` | any | Force a static value. The schema shows it as `const` | A visible pin is in `request.params.arguments` before call rules run. The pinned value is always sent. Any other client value is rejected |
| `tool.params.<p>.default` | any | Fill a static value when the argument is missing. The schema shows it as `default` | Filled before call rules run, including for added parameters |
| `tool.params.<p>.enum` | list | Restrict the allowed values | Checked before call rules run, and again after |
| `tool.params.<p>.minimum`, `tool.params.<p>.maximum` | number | Restrict the range | Checked before call rules run, and again after |
| `tool.params.<p>.add` | map | Add a parameter with this property schema | Removed before forwarding, visible or hidden, after any rename |

A rule here can deny. It cannot ask the user to confirm.

| Key | Value | Effect |
| --- | --- | --- |
| `decision` | `'deny'` | Hide the tool. Stops the enforce rules |
| `reason` | string | Recorded in audit only |

Only `decision` stops evaluation. Every other path chains.

`tool.inputSchema`, `tool.icons` and everything under `tool.upstream` cannot be set. Change the input schema through `tool.params`. A rule that sets `tool.upstream.name` is refused when saved, with `tool.upstream.name cannot be changed on tools/list.post; a rule here can set fields of tool`.

`dropSchema(schema, paths)` is available in this hook. A trailing `*` in a path removes every property at that level and empties `required`. A path through `$ref` is an evaluation error, which hides the tool, because `dropSchema` does not follow references.

## What the client sees

When a rule hides a tool:

- `tools/list` and the search tools omit the tool.
- `tools/call` returns the same error as for a tool that does not exist: `-32602`, `unknown tool <name>`. The server is not called.
- `reason` never reaches the client, so a denial does not reveal that the tool exists.
- A deny is final. A later rule cannot bring a tool back. Write exceptions into the deny condition, such as `&& !(request.caller.email in ['sre-lead@example.com'])`.
- A rule that sets both `decision` and other paths hides the tool. Its other values are discarded.

When a rule changes a tool:

- `tools/list` and the search tools show the changed tool.
- A parameter with a pin or a default is no longer listed as required.
- The old name of a renamed tool returns `-32602`, `unknown tool <name>`.

On a call, invalid arguments return `-32602` with one of these messages:

| Situation | Message |
| --- | --- |
| A value for a hidden parameter, or for the server's name of a renamed parameter | `parameter "<p>" is not available on this tool` |
| A key the tool does not define, when the server's schema sets `additionalProperties: false` | `parameter "<p>" is not defined on this tool` |
| A value different from a visible pin | `parameter "<p>" must be <value>`, such as `must be "ENG"` |
| A value outside `enum` | `parameter "<p>" must be one of [...]` |
| A value below `minimum` | `parameter "<p>" must be at least <n>` |
| A value above `maximum` | `parameter "<p>" must be at most <n>` |

## Examples

### Allow only listed tools

```json
{
  "name": "Allow list",
  "hook": "tools/list.post",
  "when": "!(tool.name in ['search_pages', 'get_page'])",
  "then": { "decision": "'deny'" }
}
```

`tools/list` shows `get_page` and `search_pages`. A call to `delete_page` returns `-32602`, `unknown tool delete_page`.

### Expose read-only tools only

```json
{
  "name": "Read-only",
  "hook": "tools/list.post",
  "when": "!tool.?annotations.?readOnlyHint.orValue(false)",
  "then": { "decision": "'deny'" }
}
```

A tool with `readOnlyHint: true` stays. A tool with `readOnlyHint: false` is hidden. A tool with no annotations is hidden too, because the rule states `false` as its default.

### Hide tools that do not say whether they are destructive

```json
{
  "name": "Undeclared hint",
  "hook": "tools/list.post",
  "when": "!tool.?annotations.?destructiveHint.hasValue()",
  "then": { "decision": "'deny'" }
}
```

A tool that sends `destructiveHint` stays, whatever its value. A tool with no annotations is hidden. So is a tool whose annotations hold only `readOnlyHint`.

### Rename tools and correct a hint

```json
[
  {
    "name": "Strip prefix",
    "hook": "tools/list.post",
    "when": "tool.name.startsWith('confluence_')",
    "then": { "tool.name": "regex.replace(tool.name, '^confluence_', '')" }
  },
  {
    "name": "Search is read-only",
    "hook": "tools/list.post",
    "when": "tool.upstream.name == 'confluence_search'",
    "then": {
      "tool.annotations.readOnlyHint": "true",
      "tool.description": "tool.?description.orValue('') + ' (ENG space only)'"
    }
  }
]
```

`confluence_search` is listed as `search`, with `readOnlyHint: true` and the description `Search pages. (ENG space only)`. A call to `search` reaches the server as `confluence_search`. A call to `confluence_search` returns `-32602`. The second rule matches `tool.upstream.name`, so it keeps working whatever the first rule does.

### Shape parameters

```json
[
  {
    "name": "cql to query",
    "hook": "tools/list.post",
    "when": "tool.upstream.name == 'search'",
    "then": {
      "tool.params.cql.name": "'query'",
      "tool.params.cql.description": "'A CQL query, such as type = page AND space = ENG.'"
    }
  },
  {
    "name": "Pin space",
    "hook": "tools/list.post",
    "when": "'space' in tool.params",
    "then": { "tool.params.space.hide": "true", "tool.params.space.pin": "'ENG'" }
  },
  {
    "name": "Bound limit",
    "hook": "tools/list.post",
    "when": "tool.upstream.name == 'search_logs'",
    "then": { "tool.params.limit.default": "50", "tool.params.limit.maximum": "500" }
  }
]
```

Each rule shapes one parameter:

- "cql to query": the schema lists `query` with the new description. The client's `{"query": "space = ENG", "limit": 5}` reaches the server as `{"cql": "space = ENG", "limit": 5}`. A client that sends `cql` receives `-32602`, `parameter "cql" is not available on this tool`.
- "Pin space": the schema no longer shows `space`. `{"q": "oncall"}` reaches the server as `{"q": "oncall", "space": "ENG"}`. A client that sends `space` receives `-32602`.
- "Bound limit": the `limit` property becomes `{"type": "integer", "default": 50, "maximum": 500}`. A call without `limit` sends `limit: 50`. A call with `limit: 1000` returns `-32602`, `parameter "limit" must be at most 500`.

## Nuances and limits

Hiding:

- A hide rule reads the tool as earlier rules left it. A rename or a corrected hint in an earlier rule feeds a later hide rule. To match the server's own name whatever earlier rules did, read `tool.upstream.name`.
- Annotations are hints from the server and are not trusted.
- A plain read of an absent field, such as `tool.annotations.destructiveHint` on a tool with no annotations, fails the rule. The tool is hidden. See [why a plain read of an absent value denies](overview.md#why-a-plain-read-of-an-absent-value-denies).
- Rules run on every request. A policy change takes effect on the next call.

Names:

- Renames are reversible. A chain of renames, such as `cql` to `query` to `q`, maps `q` back to `cql`. Incoming keys map only through the names the client sees, so a client cannot address a hidden parameter or the server's name of a renamed parameter.
- Two tools with the same final name are both hidden. Two visible parameters with the same final name fail the rule.
- A rename onto the name of a hidden parameter is allowed. If the server's `a` is renamed to `b` and the server's `b` is hidden, the client's `b` means `a`, and the server's `b` receives only values the gateway sets.
- To address a property whose name contains a dot, escape the dot with a backslash: `tool.params.a\.b.description`, written `"tool.params.a\\.b.description"` in JSON.

Hidden parameters, pins and defaults:

- After `hide`, only `pin` and `default` can still change that parameter.
- A parameter that the server requires can be hidden only with a pin, a default, or an enforce [tools/call.pre](tools-call-pre.md) rule that sets `request.params.arguments.<p>`. Otherwise the tool is hidden. Audit rules never count. On each call, the parameter must actually receive a value after call rules run, or the call is refused.
- Pins and defaults must be static. They cannot read `request`. A pin or default that does is refused when saved, and the message points to `tools/call.pre`. To set a value for each caller, hide the parameter here and set `request.params.arguments.<p>` in `tools/call.pre`.
- Pins always win, including over `tools/call.pre` changes.

Constraints and schemas:

- The gateway compares pins, `enum` values, `minimum` and `maximum` exactly. A large integer that differs from an allowed value in its last digit is refused.
- Because constraints are checked before call rules run, call rules only see values the listed schema allows.
- A property schema that is a boolean, such as `false`, stays a boolean when no rule changes it. When a rule changes a `false` schema, the gateway lists it as `allOf: [false]` plus the change, so it still accepts no value.
- When the server's schema sets `additionalProperties: false`, parameters added with `add` count as defined. See [undefined arguments](tools-call-pre.md#undefined-arguments).
- Parameter changes are refused on a schema that uses `allOf`, `anyOf`, `oneOf`, `not`, `if`, `then`, `else`, `dependentRequired`, `dependentSchemas`, `dependencies`, `patternProperties`, `propertyNames`, `$ref` or `unevaluatedProperties`. These can name parameters indirectly. The tool is hidden.
- `tool.outputSchema` is not validated at runtime. Keep it consistent with what [tools/call.post.result](tools-call-post-result.md) produces.

## Related pages

- [Hooks and rules overview](overview.md)
- [tools/call.pre for call decisions and values that depend on the caller](tools-call-pre.md)
- [tools/call.post.result for dropping fields with the matching dropSchema](tools-call-post-result.md)
