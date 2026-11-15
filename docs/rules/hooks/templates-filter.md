# templates/filter

`templates/filter` decides which resource templates are visible, and refuses reads of every URI that a denied template matches.

## When it runs

`templates/filter` runs in two places:

- On `resources/templates/list`, on each upstream template, after [server/config](server-config.md) and before [templates/modify](templates-modify.md).
- In the [visibility check](overview.md#resources-and-templates), which decides `resources/list`, reads, directory children, returned content items and [fallback resolution](overview.md#fallback-resolution) candidates. A denied template removes every URI it covers, alongside [resources/filter](resources-filter.md) and [skills/filter](skills-filter.md). Any deny wins.

## How templates match URIs

The gateway supports a subset of RFC 6570. A supported template is literal text plus any number of these expressions:

| Expression | Matches |
| --- | --- |
| `{var}` | Any text without `/`, `?` or `#`, including empty text |
| `{+var}` | Any text, including `/` and empty text |

Every literal part must match exactly, and the whole URI must match. For example, `file:///docs/{name}.md` matches `file:///docs/a.md` but not `file:///docs/sub/a.md` or `file:///docs/a.txt`.

The gateway does not parse any other form, such as `{?q}`, `{/path*}` or `{#frag}`:

- The template is listed as the server sent it, and reads under it work, because a read needs no template to allow it.
- A deny on such a template applies to every URI that starts with its literal text before the first expression. This can deny more than the template would expand to, but a deny is never lost.

Matching matters in two places. A denied template covers exactly the URIs it can produce: denying `file:///docs/{name}.md` refuses `file:///docs/a.md`, and leaves `file:///docs/sub/a.md` alone. And on the mux, a template lets the gateway route a URI that has no slug prefix. To close a whole folder, deny its prefix in [resources/filter](resources-filter.md).

The gateway never decodes a URI. `file:///a%2Fb` does not match `file:///a/{name}`.

## Variables

| Variable | Type | Description |
| --- | --- | --- |
| `uriTemplate` | string | The template, as the upstream knows it. On the mux route, it has no slug prefix |
| `name` | string | The template name |
| `title`, `description`, `mimeType` | string | The current values, or `''` |
| `annotations` | map | The template annotations, or `{}` |
| `meta` | map | The template's `_meta`, or `{}` |
| `icons` | list | The template's icons, or `[]` |
| `upstream` | map | The template as the upstream sent it |
| `server`, `entitlement`, `caller`, `client`, `route`, `now` | Various | The [common variables](overview.md#common-variables) |

The requested URI is not a variable in this hook. To decide on a specific URI, use [resources/filter](resources-filter.md).

## Fields

| Field | Type | Effect |
| --- | --- | --- |
| `decision` | `'deny'` | Hide the template and refuse reads of URIs it matches. Stops evaluation |
| `reason` | string | Recorded in audit only |

Every filter rule must set `decision`.

## What the client sees

- `resources/templates/list` omits the template.
- `resources/list` omits listed resources that the template covers.
- A read of a URI the template covers returns `-32602`, `not found: <uri>`. The upstream is not called.
- Directory reads drop children the template covers.

## Nuances and limits

- A template deny also hides listed resources that the template covers, from listings and from reads.
- A template allow cannot override a deny from another filter. A URI under a denied skill folder stays unreadable even when a template matches it.
- Template URIs cannot be rewritten by any rule.

## Examples

### Hide a template and refuse reads through it

```json
{
  "name": "No HR template",
  "hook": "templates/filter",
  "when": "uriTemplate.startsWith('confluence://HR/')",
  "then": { "decision": "'deny'" }
}
```

`resources/templates/list` shows only `confluence://ENG/{page}`. A read of `confluence://HR/salaries` returns `-32602`, `not found: confluence://HR/salaries`. A read of `confluence://ENG/runbook` reaches the upstream.

### Templates for users only

```json
{
  "name": "Users only",
  "hook": "templates/filter",
  "when": "caller.type == 'key'",
  "then": { "decision": "'deny'" }
}
```

API keys see no templates, and their reads of `file:///repo/a.go` return `not found`. Users can list the template and read through it.

## Related pages

- [Hooks and rules overview](overview.md)
- [resources/filter for decisions on single URIs](resources-filter.md)
- [templates/modify for template text](templates-modify.md)
