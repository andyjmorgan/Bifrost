# tools/filter

`tools/filter` decides which upstream tools are visible. A denied tool is hidden from listings and cannot be called.

## When it runs

`tools/filter` runs on each upstream tool, after [server/config](server-config.md) and before [tools/modify](tools-modify.md). It runs on every request that needs the tool view:

- `tools/list`
- every `tools/call`, to find the exposed tool
- the search tools: `tool_search`, `list_tools`, `get_tool_definition` and `execute_tool`

## Variables

| Variable | Type | Description |
| --- | --- | --- |
| `name` | string | The tool name |
| `title` | string | The tool title, or `''` |
| `description` | string | The tool description, or `''` |
| `annotations` | map | The annotations with the specification defaults filled in: `readOnlyHint` false, `destructiveHint` true, `idempotentHint` false, `openWorldHint` true |
| `annotationsRaw` | map | The annotations exactly as sent, so a rule can test whether a hint is present |
| `meta` | map | The tool's `_meta`, or `{}` |
| `inputSchema` | map | The input schema |
| `outputSchema` | map | The output schema, or `{}` |
| `params` | map | Each parameter name mapped to its property schema |
| `icons` | list | The tool's icons, or `[]` |
| `upstream` | map | The tool as the upstream sent it, with the same derived fields |
| `server`, `entitlement`, `caller`, `client`, `route`, `now` | Various | The [common variables](overview.md#common-variables) |

## Fields

| Field | Type | Effect |
| --- | --- | --- |
| `decision` | `'deny'` | Hide the tool. Stops evaluation |
| `reason` | string | Recorded in audit only |

Every filter rule must set `decision`.

## What the client sees

- `tools/list` and the search tools omit the tool.
- `tools/call` returns the same error as for a tool that does not exist: `-32004`, `unknown tool <name>`. The upstream is not called.
- `reason` never reaches the client, so a denial does not reveal that the tool exists.

If an enforce rule fails to evaluate, the tool is hidden.

## Nuances and limits

- A deny is final. A later rule cannot bring a tool back. Write exceptions into the deny condition, such as `&& caller.role != 'admin'`.
- Filters see the upstream view. `tools/modify` runs after `tools/filter`, so a renamed tool or a corrected hint does not feed a filter. To correct a wrong hint, write the correction into the filter condition.
- Annotations are hints from the upstream and are not trusted. Use `annotationsRaw` to tell a missing hint from a default.
- Rules run on every request. A policy change takes effect on the next call.

## Examples

### Allow only listed tools

```json
{
  "name": "Allow list",
  "hook": "tools/filter",
  "when": "!(name in ['search_pages', 'get_page'])",
  "then": { "decision": "'deny'" }
}
```

`tools/list` shows `get_page` and `search_pages`. A call to `delete_page` returns `-32004`, `unknown tool delete_page`.

### Expose read-only tools only

```json
{
  "name": "Read-only",
  "hook": "tools/filter",
  "when": "!annotations.readOnlyHint",
  "then": { "decision": "'deny'" }
}
```

A tool with `readOnlyHint: true` stays. A tool with `readOnlyHint: false`, or with no hint, is hidden, because the default is false.

### Restrict admin tools to admins

```json
{
  "name": "Admin tools",
  "hook": "tools/filter",
  "when": "caller.role != 'admin' && name.startsWith('admin_')",
  "then": { "decision": "'deny'", "reason": "'admin tools are for admins'" }
}
```

Members see `status` only. Admins see `admin_reset` and `status`. The reason appears in the audit record for members' listings.

### Hide tools that do not declare a hint

```json
{
  "name": "Undeclared hint",
  "hook": "tools/filter",
  "when": "!has(annotationsRaw.readOnlyHint)",
  "then": { "decision": "'deny'" }
}
```

A tool that sends `readOnlyHint` stays, whatever its value. A tool with no annotations is hidden.

## Related pages

- [Hooks and rules overview](overview.md)
- [tools/modify for names, text and parameters](tools-modify.md)
- [tools/call for call decisions](tools-call.md)
