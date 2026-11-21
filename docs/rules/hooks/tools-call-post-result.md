# tools/call.post.result

`tools/call.post.result` shapes what a tool returns before the client sees it, or blocks the result.

## When it runs

`tools/call.post.result` runs once on each `tools/call` result from the server, after the call and before the mux URI prefix. It does not run when the server replies with `input_required`. That reply goes to [tools/call.post.elicit](tools-call-post-elicit.md).

1. The gateway rejects a reply over the size limit.
2. `tools/call.post.result` rules run.
3. On the mux route, `resource_link` and embedded `resource` blocks get the slug prefix. This runs last, so URIs that rules add are prefixed too.
4. The gateway checks the size limit again.

An error the server returns as a JSON-RPC error passes through untouched. No rule sees it.

## What a rule can read

| Branch | Notable fields |
| --- | --- |
| `response.result` | The result as earlier rules left it: `response.result.content`, a list of content blocks. `response.result.structuredContent`, `response.result.isError` and `response.result._meta`, when the server sent them |
| `response.upstream` | The response as the server produced it: `response.upstream.result`. It never changes |
| `request.params` | `request.params.name` and `request.params.arguments`, under the names the client knows, as the `tools/call.pre` rules left them |
| `request.upstream` | The request as the server received it: `request.upstream.params.name` and `request.upstream.params.arguments`, under the server's names, after defaults and pins |
| `request` | Also `request.method`, `request.caller`, `request.client`, `request.headers`, `request.route`, `request.flags`, `request.time` |
| `tool` | The tool as the client sees it, and `tool.upstream` as the server declares it |
| `server` | `server.id`, `server.slug` and what the gateway knows about the server |

MCP requires `content`, so the tree marks it as always there. If a server leaves it out, a rule that reads `response.result.content` fails and the result is refused. `response.result.resultType` is there when the server sent it. A rule that sets `content` must give a list of blocks, each a map with a `type`, and `structuredContent` must be an object.

Each block has a `type`. The other fields of a block depend on its type, so read them with the [optional forms](overview.md#optional-values):

| Read | Use |
| --- | --- |
| The text of a block, or `''` for a block that is not text | `c.?text.orValue('')` |
| Whether the result is an error | `response.result.?isError.orValue(false)` |
| Whether there is structured content | `has(response.result.structuredContent)` |
| A field inside structured content | `response.result.?structuredContent.?items.hasValue()` |
| The first block | `response.result.content[?0]` |

There is no joined text of the whole result. Work on `response.result.content` with CEL's list macros: `exists`, `all`, `filter` and `map`.

## What a rule can set

| Path | Type | Effect | Stops evaluation |
| --- | --- | --- | --- |
| `response.result.content` | list | Replace the content blocks | No |
| `response.result.structuredContent` | any | Replace the structured content. `null` removes it | No |

A rule here can deny. It cannot ask the user to confirm.

| Key | Value | Effect | Stops evaluation |
| --- | --- | --- | --- |
| `decision` | `'deny'` | Block the result | Yes |
| `reason` | string | The text the client receives with a deny | No |

Later rules see earlier changes in `response.result`. `response.upstream` still holds the server's response.

A rule cannot set `response.result.isError`, `response.result._meta` or anything under `response.upstream`. A rule that sets `response.upstream.result` is refused when saved.

`drop(value, paths)` is available in this hook.

## What the client sees

| Outcome | Result |
| --- | --- |
| No rule matches | The server's result, unchanged except for the mux prefix |
| Changed | The result with the new `content` or `structuredContent`. Other keys, such as `isError` and `_meta`, stay |
| Denied | `{"isError": true, "content": [{"type": "text", "text": "<reason>"}]}`. Without a reason, the text is `The call to <tool> was refused by policy.` |
| A rule fails to evaluate | An error result: `The call to <tool> was refused by policy.` |
| Server reply over the size limit | `-32013`, `upstream result exceeds gateway size limit` |
| Shaped result over the size limit | `-32013`, `result exceeds gateway size limit after shaping` |

## Common operations

| Operation | How |
| --- | --- |
| Drop fields | `drop(response.result.structuredContent, ['issues.*.self'])`. `*` matches every key or element |
| Keep only some fields | `map` with a map literal |
| Replace values | `regex.replace` |
| Cap a list | `size(xs) > n ? xs.slice(0, n) : xs` |
| Drop the text copy of structured output | Set `response.result.content` to `[]` |
| Replace blocks of one type | `response.result.content.map(c, c.type == 'image' ? {...} : c)` |

## Examples

### Drop fields, the text copy and the matching schema

```json
[
  {
    "name": "Drop issue noise",
    "hook": "tools/call.post.result",
    "when": "tool.upstream.name == 'search_issues' && has(response.result.structuredContent)",
    "then": {
      "response.result.structuredContent": "drop(response.result.structuredContent, ['issues.*.self', 'issues.*.avatarUrls', 'issues.*.fields.watches'])",
      "response.result.content": "[]"
    }
  },
  {
    "name": "Drop issue noise from schema",
    "hook": "tools/list.post",
    "when": "tool.upstream.name == 'search_issues' && has(tool.outputSchema)",
    "then": {
      "tool.outputSchema": "dropSchema(tool.outputSchema, ['issues.*.self', 'issues.*.avatarUrls', 'issues.*.fields.watches'])"
    }
  }
]
```

An issue `{"key": "OPS-1", "self": "https://jira/1", "avatarUrls": {}, "fields": {"watches": {}, "summary": "Disk full"}}` reaches the client as `{"key": "OPS-1", "fields": {"summary": "Disk full"}}`, with `content: []`. The listed output schema loses the same three properties, and `self` leaves its `required` list.

### Keep chosen fields and cap the list

```json
{
  "name": "First 20 ids and titles",
  "hook": "tools/call.post.result",
  "when": "tool.upstream.name == 'list_issues' && response.result.?structuredContent.?items.hasValue()",
  "then": {
    "response.result.structuredContent": "cel.bind(items, response.result.?structuredContent.?items.orValue([]).map(i, {'number': i.number, 'title': i.title}), {'items': size(items) > 20 ? items.slice(0, 20) : items})"
  }
}
```

Each item keeps only `number` and `title`. A result with 25 items returns the first 20. A result with two items returns both. A result with no structured content passes through unchanged.

### Replace error details from the server

```json
{
  "name": "Generic error",
  "hook": "tools/call.post.result",
  "when": "response.result.?isError.orValue(false)",
  "then": { "response.result.content": "[{'type': 'text', 'text': 'The query failed. Check the table name and try again.'}]" }
}
```

A server error that names an internal host reaches the client as `{"isError": true, "content": [{"type": "text", "text": "The query failed. Check the table name and try again."}]}`. Successful results are unchanged.

### Block a result by its text

```json
{
  "name": "Block keys",
  "hook": "tools/call.post.result",
  "when": "response.result.content.exists(c, c.?text.orValue('').contains('BEGIN PRIVATE KEY'))",
  "then": { "decision": "'deny'", "reason": "'The result contained a private key and was withheld.'" }
}
```

A result with a text block that contains a PEM private key returns the error result `The result contained a private key and was withheld.` Image blocks have no `text`, so the rule reads them as `''` and does not fail on them.

### Replace every image with a notice

```json
{
  "name": "No images",
  "hook": "tools/call.post.result",
  "when": "response.result.content.exists(c, c.type == 'image')",
  "then": {
    "response.result.content": "response.result.content.map(c, c.type == 'image' ? {'type': 'text', 'text': 'This image was removed by policy.'} : c)"
  }
}
```

A result with an image block and a text block reaches the client as the notice and the text block, in the same order. A result with no image is untouched, and the rule records no hit.

## Nuances and limits

- The MCP specification says servers should send structured output twice: as `structuredContent` and as JSON text in `content`. When a rule drops fields from `structuredContent`, drop or rebuild `content` too, or the text copy still carries them. Clients that read only `content` see nothing after `response.result.content` is set to `[]`.
- When a rule changes the shape of `structuredContent`, change `tool.outputSchema` to match in [tools/list.post](tools-list-post.md). `dropSchema` takes the same paths as `drop`. The gateway does not validate results against the schema at runtime, and passes through a server that breaks its own schema.
- `has(a.b.c)` guards only the last step. `has(response.result.structuredContent.items)` fails on a result with no `structuredContent`, which blocks the result. Use `response.result.?structuredContent.?items.hasValue()`.
- `slice(0, n)` fails to evaluate when the list is shorter than `n`, which blocks the result. Write `size(xs) > n ? xs.slice(0, n) : xs`.
- The arguments have two sides. `request.params.arguments` uses the names the client knows. `request.upstream.params.arguments` shows what the server received, under its own names, including values that rules changed, defaults and pins. A hidden pinned parameter appears only on the `request.upstream` side.
- `structuredContent` keeps its JSON type. An array or a scalar passes through without being turned into an object.
- Results are never cached. Two identical calls reach the server twice.

## Related pages

- [Hooks and rules overview](overview.md)
- [tools/list.post for the output schema and dropSchema](tools-list-post.md)
- [tools/call.post.elicit for input_required replies](tools-call-post-elicit.md)
