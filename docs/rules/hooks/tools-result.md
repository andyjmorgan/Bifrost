# tools/result

`tools/result` shapes what an upstream tool returns before the client sees it, or withholds the result.

## When it runs

`tools/result` runs once on each upstream `tools/call` result, after the upstream call and before the mux URI prefix. It does not run when the upstream replies with `input_required`. That reply goes to [tools/input](tools-input.md).

1. The gateway rejects a reply over the size limit.
2. `tools/result` rules run.
3. On the mux route, `resource_link` and embedded `resource` blocks get the slug prefix. This runs last, so URIs that rules add are prefixed too.
4. The gateway checks the size limit again.

## Variables

| Variable | Type | Description |
| --- | --- | --- |
| `result` | map | The result: `isError` (false when absent), `content`, `structuredContent`, `_meta`, `meta` (a read-only alias of `_meta`, or `{}`), and `text`, which joins every text content item with line breaks. Later rules see earlier changes |
| `args` | map | The exact arguments forwarded upstream, under upstream names, after call patches and pins |
| `name`, `title`, `description`, `annotations`, `annotationsRaw`, `meta`, `inputSchema`, `outputSchema`, `icons` | Various | The exposed tool |
| `upstream` | map | The tool as the upstream sent it |
| `server`, `entitlement`, `caller`, `client`, `route`, `flags`, `now` | Various | The [common variables](overview.md#common-variables) |

## Fields

| Field | Type | Effect | Stops evaluation |
| --- | --- | --- | --- |
| `decision` | `'deny'` | Withhold the result | Yes |
| `reason` | string | The text the client receives with a deny | No |
| `content` | list | Replace the content blocks | No |
| `structuredContent` | any | Replace the structured content | No |

`drop(value, paths)` is available in this hook.

## What the client sees

| Outcome | Result |
| --- | --- |
| No rule matches | The upstream result, unchanged except for the mux prefix |
| Changed | The result with the new `content` or `structuredContent`. Other keys, such as `isError` and `_meta`, stay |
| Denied | `{"isError": true, "content": [{"type": "text", "text": "<reason>"}]}`. Without a reason, the text is `The call to <tool> was refused by policy.` |
| A rule fails to evaluate | An error result: `The call to <tool> was refused by policy.` |
| Upstream reply over the size limit | `-32013`, `upstream result exceeds gateway size limit` |
| Shaped result over the size limit | `-32013`, `result exceeds gateway size limit after shaping` |

## Common operations

| Operation | How |
| --- | --- |
| Drop fields | `drop(result.structuredContent, ['issues.*.self'])`. `*` matches every key or element |
| Keep only some fields | `map` with a map literal |
| Replace values | `regex.replace` |
| Cap a list | `size(xs) > n ? xs.slice(0, n) : xs` |
| Drop the text copy of structured output | `content: []` |

## Nuances and limits

- The MCP specification says servers should send structured output twice: as `structuredContent` and as JSON text in `content`. When a rule drops fields from `structuredContent`, drop or rebuild `content` too, or the text copy still carries them. Clients that read only `content` see nothing after `content: []`.
- When a rule changes the shape of `structuredContent`, change `outputSchema` to match in [tools/modify](tools-modify.md). `dropSchema` takes the same paths as `drop`. The gateway does not validate results against the schema at runtime, and passes through an upstream that breaks its own schema.
- `slice(0, n)` fails to evaluate when the list is shorter than `n`, which withholds the result. Write `size(xs) > n ? xs.slice(0, n) : xs`.
- `args` holds upstream parameter names, not the names the client sees. It shows what the upstream actually received, including patched and pinned values.
- `structuredContent` keeps its JSON type. An array, a scalar or `null` passes through without being turned into an object.
- Results are never cached. Two identical calls reach the upstream twice.

## Examples

### Drop fields, the text copy and the matching schema

```json
[
  {
    "name": "Drop issue noise",
    "hook": "tools/result",
    "when": "upstream.name == 'search_issues' && has(result.structuredContent)",
    "then": {
      "structuredContent": "drop(result.structuredContent, ['issues.*.self', 'issues.*.avatarUrls', 'issues.*.fields.watches'])",
      "content": "[]"
    }
  },
  {
    "name": "Drop issue noise from schema",
    "hook": "tools/modify",
    "when": "upstream.name == 'search_issues'",
    "then": {
      "outputSchema": "dropSchema(outputSchema, ['issues.*.self', 'issues.*.avatarUrls', 'issues.*.fields.watches'])"
    }
  }
]
```

An issue `{"key": "OPS-1", "self": "https://jira/1", "avatarUrls": {}, "fields": {"watches": {}, "summary": "Disk full"}}` reaches the client as `{"key": "OPS-1", "fields": {"summary": "Disk full"}}`, with `content: []`. The listed output schema loses the same three properties, and `self` leaves its `required` list.

### Keep chosen fields and cap the list

```json
{
  "name": "First 20 ids and titles",
  "hook": "tools/result",
  "when": "upstream.name == 'list_issues' && has(result.structuredContent.items)",
  "then": {
    "structuredContent": "cel.bind(items, result.structuredContent.items.map(i, {'number': i.number, 'title': i.title}), {'items': size(items) > 20 ? items.slice(0, 20) : items})"
  }
}
```

Each item keeps only `number` and `title`. A result with 25 items returns the first 20. A result with two items returns both.

### Replace upstream error details

```json
{
  "name": "Generic error",
  "hook": "tools/result",
  "when": "result.isError",
  "then": { "content": "[{'type': 'text', 'text': 'The query failed. Check the table name and try again.'}]" }
}
```

An upstream error that names an internal host reaches the client as `{"isError": true, "content": [{"type": "text", "text": "The query failed. Check the table name and try again."}]}`. Successful results are unchanged.

### Withhold a result by its text

```json
{
  "name": "Block keys",
  "hook": "tools/result",
  "when": "result.text.contains('BEGIN PRIVATE KEY')",
  "then": { "decision": "'deny'", "reason": "'The result contained a private key and was withheld.'" }
}
```

A result whose text contains a PEM private key returns the error result `The result contained a private key and was withheld.`

## Related pages

- [Hooks and rules overview](overview.md)
- [tools/modify for outputSchema and dropSchema](tools-modify.md)
- [tools/input for input_required replies](tools-input.md)
