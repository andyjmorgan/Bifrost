# tools/list.post

`tools/list.post` decides which upstream tools are visible, and how a visible tool is presented: its name, text, annotations, parameters and output schema. A hidden tool is left out of listings and cannot be called. The gateway reverses names and parameter keys when the client calls the tool.

## When it runs

`tools/list.post` runs on each upstream tool, after [server.settings](server-settings.md). It runs on every request that needs the tool view:

- `tools/list`
- every `tools/call`, to find the exposed tool
- the search tools: `tool_search`, `list_tools`, `get_tool_definition` and `execute_tool`

A client can only call what it can list. A `tools/call` first runs this hook for the tool it names, and [tools/call.pre](tools-call-pre.md) rules see the tool as this hook left it.

After the last rule, the gateway checks for collisions. On the mux route, the gateway adds the `<slug>__` prefix after this hook.

## Order of rules

The rules run on each tool from top to bottom, in the order you write them. A rule can do one of two things:

- hide the tool, by setting `decision: 'deny'`
- change the tool, by setting any of the other [fields](#fields)

Changes chain. A later rule reads the tool as earlier rules left it, so a hide rule written after a rename sees the new `name`. `upstream.name` is always the server's own name.

The first deny hides the tool and stops the enforce rules. Audit rules still run, so the audit record is complete.

Writing hide rules first is cheaper, because the gateway never changes a hidden tool. It is not required.

A rule that fails also hides the tool. That covers an enforce rule that fails to evaluate and a change the gateway cannot apply, such as a rename that collides. The tool cannot be called, and the listing reports the problem to administrators.

## Hiding a tool

A rule hides a tool by setting `decision` to `'deny'`. It can also set `reason`.

- `tools/list` and the search tools omit the tool.
- `tools/call` returns the same error as for a tool that does not exist: `-32602`, `unknown tool <name>`. The upstream is not called.
- `reason` never reaches the client, so a denial does not reveal that the tool exists.
- A deny is final. A later rule cannot bring a tool back. Write exceptions into the deny condition, such as `&& !(caller.email in ['sre-lead@example.com'])`.
- A rule that sets both `decision` and other fields hides the tool. Its other fields are discarded.

## Changing a tool

A rule changes a tool by setting any field other than `decision` and `reason`.

- `tools/list` and the search tools show the changed tool.
- A parameter with a pin or a default is no longer listed as required.
- The old name of a renamed tool returns `-32602`, `unknown tool <name>`.

On a call, invalid arguments return `-32602` with one of these messages:

| Situation | Message |
| --- | --- |
| A value for a hidden parameter, or for a renamed parameter's upstream name | `parameter "<p>" is not available on this tool` |
| A key the tool does not define, when the upstream schema sets `additionalProperties: false` | `parameter "<p>" is not defined on this tool` |
| A value different from a visible pin | `parameter "<p>" must be <value>`, such as `must be "ENG"` |
| A value outside `enum` | `parameter "<p>" must be one of [...]` |
| A value below `minimum` | `parameter "<p>" must be at least <n>` |
| A value above `maximum` | `parameter "<p>" must be at most <n>` |

## Variables

Each variable holds the tool as earlier rules left it. Before any rule changes the tool, that is the tool as the upstream sent it.

| Variable | Type | Description |
| --- | --- | --- |
| `name` | string | The current tool name |
| `title` | string | The current tool title, or `''` |
| `description` | string | The current tool description, or `''` |
| `annotations` | map | The current annotations, with the specification defaults filled in: `readOnlyHint` false, `destructiveHint` true, `idempotentHint` false, `openWorldHint` true |
| `annotationsRaw` | map | The current annotations without defaults, so a rule can test whether a hint is present |
| `meta` | map | The current `_meta`, or `{}` |
| `inputSchema` | map | The exposed input schema, with renames, hidden parameters and constraints applied |
| `outputSchema` | map | The current output schema, or `{}` |
| `params` | map | Each visible parameter's current name mapped to its exposed property schema |
| `icons` | list | The tool's icons, or `[]` |
| `upstream` | map | The tool as the upstream sent it, with the same derived fields. It never changes |
| `server`, `entitlement`, `caller`, `client`, `route`, `flags`, `now` | Various | The [common variables](overview.md#common-variables) |

## Fields

`<p>` is a parameter's current name, after any earlier rename.

| Field | Type | Effect | Reversal on call |
| --- | --- | --- | --- |
| `decision` | `'deny'` | Hide the tool. Stops the enforce rules | None |
| `reason` | string | Recorded in audit only | None |
| `name` | string | Rename the tool | The exposed name maps back to the upstream name |
| `title`, `description` | string | Replace the text | None |
| `annotations.<hint>` | any | Override a hint, such as `annotations.readOnlyHint` | None |
| `meta.<key>` | any | Set a `_meta` key. `null` removes the key | None |
| `params.<p>.name` | string | Rename a parameter | The argument key maps back |
| `params.<p>.description` | string | Replace the parameter description | None |
| `params.<p>.hide` | bool | Remove the parameter from the schema | A client value is rejected |
| `params.<p>.pin` | any | Force a static value. The schema shows it as `const` | A visible pin is in `args` before call rules run. The pinned value is always sent. Any other client value is rejected |
| `params.<p>.default` | any | Fill a static value when the argument is missing. The schema shows it as `default` | Filled before call rules run, including for added parameters |
| `params.<p>.enum` | list | Restrict the allowed values | Checked before call rules run, and again after |
| `params.<p>.minimum`, `params.<p>.maximum` | number | Restrict the range | Checked before call rules run, and again after |
| `params.<p>.add` | map | Add a parameter with this property schema | Removed before forwarding, visible or hidden, after any rename |
| `outputSchema` | any | Replace the output schema | None |

Only `decision` stops evaluation. Every other field chains.

`dropSchema(schema, paths)` is available in this hook. A trailing `*` in a path removes every property at that level and empties `required`. A path through `$ref` is an evaluation error, which hides the tool, because `dropSchema` does not follow references.

## Examples

### Allow only listed tools

```json
{
  "name": "Allow list",
  "hook": "tools/list.post",
  "when": "!(name in ['search_pages', 'get_page'])",
  "then": { "decision": "'deny'" }
}
```

`tools/list` shows `get_page` and `search_pages`. A call to `delete_page` returns `-32602`, `unknown tool delete_page`.

### Expose read-only tools only

```json
{
  "name": "Read-only",
  "hook": "tools/list.post",
  "when": "!annotations.readOnlyHint",
  "then": { "decision": "'deny'" }
}
```

A tool with `readOnlyHint: true` stays. A tool with `readOnlyHint: false`, or with no hint, is hidden, because the default is false.

### Restrict admin tools to named operators

```json
{
  "name": "Admin tools",
  "hook": "tools/list.post",
  "when": "!(caller.email in ['sre-lead@example.com']) && name.startsWith('admin_')",
  "then": { "decision": "'deny'", "reason": "'admin tools are restricted'" }
}
```

Most callers see `status` only. The listed operator sees `admin_reset` and `status`. The reason appears in the audit record for other callers' listings. For a larger group, grant them a separate entitlement without this rule.

### Hide tools that do not declare a hint

```json
{
  "name": "Undeclared hint",
  "hook": "tools/list.post",
  "when": "!has(annotationsRaw.readOnlyHint)",
  "then": { "decision": "'deny'" }
}
```

A tool that sends `readOnlyHint` stays, whatever its value. A tool with no annotations is hidden.

### Rename tools and correct a hint

```json
[
  {
    "name": "Strip prefix",
    "hook": "tools/list.post",
    "when": "name.startsWith('confluence_')",
    "then": { "name": "regex.replace(name, '^confluence_', '')" }
  },
  {
    "name": "Search is read-only",
    "hook": "tools/list.post",
    "when": "upstream.name == 'confluence_search'",
    "then": {
      "annotations.readOnlyHint": "true",
      "description": "description + ' (ENG space only)'"
    }
  }
]
```

`confluence_search` is listed as `search`, with `readOnlyHint: true` and the description `Search pages. (ENG space only)`. A call to `search` reaches the upstream as `confluence_search`. A call to `confluence_search` returns `-32602`. The second rule matches `upstream.name`, so it keeps working whatever the first rule does.

### Rename and describe a parameter

```json
{
  "name": "cql to query",
  "hook": "tools/list.post",
  "when": "upstream.name == 'search'",
  "then": {
    "params.cql.name": "'query'",
    "params.cql.description": "'A CQL query, such as type = page AND space = ENG.'"
  }
}
```

The schema lists `query` as required, with the new description. The client's `{"query": "space = ENG", "limit": 5}` reaches the upstream as `{"cql": "space = ENG", "limit": 5}`. A client that sends `cql` receives `-32602`, `parameter "cql" is not available on this tool`.

### Hide and pin a parameter

```json
{
  "name": "Pin space",
  "hook": "tools/list.post",
  "when": "'space' in params",
  "then": { "params.space.hide": "true", "params.space.pin": "'ENG'" }
}
```

The schema shows only `q`, and `q` is the only required parameter. `{"q": "oncall"}` reaches the upstream as `{"q": "oncall", "space": "ENG"}`. A client that sends `space` receives `-32602`.

### Set a default and a maximum

```json
{
  "name": "Bound limit",
  "hook": "tools/list.post",
  "when": "upstream.name == 'search_logs'",
  "then": { "params.limit.default": "50", "params.limit.maximum": "500" }
}
```

The `limit` property becomes `{"type": "integer", "default": 50, "maximum": 500}`. A call without `limit` sends `limit: 50`. A call with `limit: 1000` returns `-32602`, `parameter "limit" must be at most 500`.

## Nuances and limits

Hiding:

- A hide rule reads the tool as earlier rules left it. A rename or a corrected hint in an earlier rule feeds a later hide rule. To match the server's own name whatever earlier rules did, read `upstream.name`.
- Annotations are hints from the upstream and are not trusted. Use `annotationsRaw` to tell a missing hint from a default.
- Rules run on every request. A policy change takes effect on the next call.

Names:

- Renames are reversible. A chain of renames, such as `cql` to `query` to `q`, maps `q` back to `cql`. Incoming keys map only through exposed names, so a client cannot address a hidden parameter or a renamed parameter's upstream name.
- Two tools with the same final name are both hidden. Two visible parameters with the same final name fail the rule.
- A rename onto the name of a hidden parameter is allowed. If upstream `a` is renamed to `b` and upstream `b` is hidden, the client's `b` means `a`, and upstream `b` receives only values the gateway sets.
- To address a property whose name contains a dot, escape the dot with a backslash: `params.a\.b.description`, written `"params.a\\.b.description"` in JSON.

Hidden parameters, pins and defaults:

- After `hide`, only `pin` and `default` can still change that parameter.
- A parameter that is required upstream can be hidden only with a pin, a default, or an enforce [tools/call.pre](tools-call-pre.md) rule that sets `args.<p>`. Otherwise the tool is hidden. Audit rules never count. On each call, the parameter must actually receive a value after call rules run, or the call is refused.
- Pins and defaults must be static. They cannot read `caller`, `client`, `route`, `flags`, `now` or request data. A pin or default that reads one of them does not compile, and the error points to `tools/call.pre`. To set a value per caller, hide the parameter here and set `args.<p>` in `tools/call.pre`.
- Pins always win, including over `tools/call.pre` patches.

Constraints and schemas:

- The gateway compares pins, `enum` values, `minimum` and `maximum` exactly. A large integer that differs from an allowed value in its last digit is refused.
- Because constraints are checked before call rules run, call rules only see values the exposed schema allows.
- A property schema that is a boolean, such as `false`, stays a boolean when no rule changes it. When a rule changes a `false` schema, the gateway lists it as `allOf: [false]` plus the change, so it still accepts no value.
- When the upstream schema sets `additionalProperties: false`, parameters added with `add` count as defined. See [undefined arguments](tools-call-pre.md#undefined-arguments).
- Parameter changes are refused on a schema that uses `allOf`, `anyOf`, `oneOf`, `not`, `if`, `then`, `else`, `dependentRequired`, `dependentSchemas`, `dependencies`, `patternProperties`, `propertyNames`, `$ref` or `unevaluatedProperties`. These can name parameters indirectly. The tool is hidden.
- `outputSchema` is not validated at runtime. Keep it consistent with what [tools/call.post.result](tools-call-post-result.md) produces.

## Related pages

- [Hooks and rules overview](overview.md)
- [tools/call.pre for call decisions and values that depend on the caller](tools-call-pre.md)
- [tools/call.post.result for dropping fields with the matching dropSchema](tools-call-post-result.md)
