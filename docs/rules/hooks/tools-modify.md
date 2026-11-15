# tools/modify

`tools/modify` changes how a visible tool is presented: its name, text, annotations, parameters and output schema. The gateway reverses names and parameter keys when the client calls the tool.

## When it runs

`tools/modify` runs on each tool that passed [tools/filter](tools-filter.md), on every request that needs the tool view: `tools/list`, every `tools/call` and the search tools. After the last rule, the gateway checks for collisions. On the mux route, the gateway adds the `<slug>__` prefix after this hook.

On a call, [tools/call](tools-call.md) rules see the tool as this hook left it.

## Variables

| Variable | Type | Description |
| --- | --- | --- |
| `name`, `title`, `description` | string | The current values |
| `annotations` | map | The current annotations, with specification defaults filled in |
| `annotationsRaw` | map | The current annotations without defaults |
| `meta` | map | The current `_meta`, or `{}` |
| `inputSchema` | map | The exposed input schema, with renames, hidden parameters and constraints applied |
| `outputSchema` | map | The current output schema, or `{}` |
| `params` | map | Each visible parameter's current name mapped to its exposed property schema |
| `icons` | list | The tool's icons, or `[]` |
| `upstream` | map | The tool as the upstream sent it. It never changes |
| `server`, `entitlement`, `caller`, `client`, `route`, `now` | Various | The [common variables](overview.md#common-variables) |

## Fields

`<p>` is a parameter's current name, after any earlier rename.

| Field | Type | Effect | Reversal on call |
| --- | --- | --- | --- |
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

No field stops evaluation. Changes chain. `dropSchema(schema, paths)` is available in this hook. A trailing `*` in a path removes every property at that level and empties `required`. A path through `$ref` is an evaluation error, which hides the tool, because `dropSchema` does not follow references.

## What the client sees

- `tools/list` and the search tools show the modified tool.
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

A rule that fails, such as a rename that collides, hides the tool. The listing reports the problem to administrators.

## Nuances and limits

- Renames are reversible. A chain of renames, such as `cql` to `query` to `q`, maps `q` back to `cql`. Incoming keys map only through exposed names, so a client cannot address a hidden parameter or a renamed parameter's upstream name.
- Two tools with the same final name are both hidden. Two visible parameters with the same final name fail the rule.
- A rename onto the name of a hidden parameter is allowed. If upstream `a` is renamed to `b` and upstream `b` is hidden, the client's `b` means `a`, and upstream `b` receives only values the gateway sets.
- After `hide`, only `pin` and `default` can still change that parameter.
- A parameter that is required upstream can be hidden only with a pin, a default, or an enforce [tools/call](tools-call.md) rule that sets `args.<p>`. Otherwise the tool is hidden. Audit rules never count. On each call, the parameter must actually receive a value after call rules run, or the call is refused.
- Pins and defaults must be static. They cannot read `caller`, `client`, `route`, `now` or request data. To set a value per caller, hide the parameter here and set `args.<p>` in `tools/call`.
- Pins always win, including over `tools/call` patches.
- The gateway compares pins, `enum` values, `minimum` and `maximum` exactly. A large integer that differs from an allowed value in its last digit is refused.
- Because constraints are checked before call rules run, call rules only see values the exposed schema allows.
- A property schema that is a boolean, such as `false`, stays a boolean when no rule changes it. When a rule changes a `false` schema, the gateway lists it as `allOf: [false]` plus the change, so it still accepts no value.
- When the upstream schema sets `additionalProperties: false`, parameters added with `add` count as defined. See [undefined arguments](tools-call.md#undefined-arguments).
- Parameter changes are refused on a schema that uses `allOf`, `anyOf`, `oneOf`, `not`, `if`, `then`, `else`, `dependentRequired`, `dependentSchemas`, `dependencies`, `patternProperties`, `propertyNames`, `$ref` or `unevaluatedProperties`. These can name parameters indirectly. The tool is hidden.
- To address a property whose name contains a dot, escape the dot with a backslash: `params.a\.b.description`, written `"params.a\\.b.description"` in JSON.
- Modify rules cannot change visibility. Use [tools/filter](tools-filter.md).
- `outputSchema` is not validated at runtime. Keep it consistent with what [tools/result](tools-result.md) produces.

## Examples

### Rename tools and correct a hint

```json
[
  {
    "name": "Strip prefix",
    "hook": "tools/modify",
    "when": "name.startsWith('confluence_')",
    "then": { "name": "regex.replace(name, '^confluence_', '')" }
  },
  {
    "name": "Search is read-only",
    "hook": "tools/modify",
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
  "hook": "tools/modify",
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
  "hook": "tools/modify",
  "when": "'space' in params",
  "then": { "params.space.hide": "true", "params.space.pin": "'ENG'" }
}
```

The schema shows only `q`, and `q` is the only required parameter. `{"q": "oncall"}` reaches the upstream as `{"q": "oncall", "space": "ENG"}`. A client that sends `space` receives `-32602`.

### Set a default and a maximum

```json
{
  "name": "Bound limit",
  "hook": "tools/modify",
  "when": "upstream.name == 'search_logs'",
  "then": { "params.limit.default": "50", "params.limit.maximum": "500" }
}
```

The `limit` property becomes `{"type": "integer", "default": 50, "maximum": 500}`. A call without `limit` sends `limit: 50`. A call with `limit: 1000` returns `-32602`, `parameter "limit" must be at most 500`.

## Related pages

- [Hooks and rules overview](overview.md)
- [tools/filter for visibility](tools-filter.md)
- [tools/call for values that depend on the caller](tools-call.md)
- [tools/result for dropping fields with the matching dropSchema](tools-result.md)
