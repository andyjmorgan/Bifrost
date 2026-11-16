# resources/templates/list.post

`resources/templates/list.post` decides which resource templates are visible, and changes the title, description, annotations and metadata of a listed template. A denied template is hidden, and reads of every URI it matches are refused. The hook cannot change the template or the name.

## When it runs

`resources/templates/list.post` runs in two places:

- On `resources/templates/list`, on each upstream template, after [server.settings](server-settings.md). On the mux route, the gateway adds the slug prefix after this hook.
- In the [visibility check](overview.md#resources-and-templates), which decides `resources/list`, reads, directory children, returned content items and [fallback resolution](overview.md#fallback-resolution) candidates. A denied template removes every URI it covers, alongside [resources/list.post](resources-list-post.md) and [skills/list.post](skills-list-post.md). Any deny wins.

## Order of rules

The rules run on each template from top to bottom, in the order you write them. A rule can do one of two things:

- hide the template, by setting `decision: 'deny'`
- change the template, by setting any of the other [fields](#fields)

Changes chain. A later rule reads the template as earlier rules left it, so a hide rule written after a change to `title` sees the new title. `upstream` always holds the template as the server sent it.

The first deny hides the template and stops the enforce rules. Audit rules still run, so the audit record is complete.

Writing hide rules first is cheaper, because the gateway never changes a hidden template. It is not required.

A rule that fails to evaluate also hides the template, and the listing reports the problem to administrators. The template then counts as denied: URIs it covers are unreadable and dropped from `resources/list`, the same as after a deny. Templates that collide are treated the same way.

## Hiding a template

A rule hides a template by setting `decision` to `'deny'`. It can also set `reason`. A deny hides the template and refuses reads of the URIs it matches.

- `resources/templates/list` omits the template.
- `resources/list` omits listed resources that the template covers.
- A read of a URI the template covers returns `-32602`, `not found: <uri>`. The upstream is not called.
- Directory reads drop children the template covers.
- A rule that sets both `decision` and other fields hides the template. Its other fields are discarded.

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

Matching matters in two places. A denied template covers exactly the URIs it can produce: denying `file:///docs/{name}.md` refuses `file:///docs/a.md`, and leaves `file:///docs/sub/a.md` alone. And on the mux, a template lets the gateway route a URI that has no slug prefix. To close a whole folder, deny its prefix in [resources/list.post](resources-list-post.md).

The gateway never decodes a URI. `file:///a%2Fb` does not match `file:///a/{name}`.

## Changing a template

A rule changes a template by setting any field other than `decision` and `reason`.

- `resources/templates/list` shows the changed template with its original `uriTemplate`.
- On the mux route, the template carries the slug prefix before its scheme, and its variables are unchanged, so RFC 6570 expansion keeps working.
- Text changes affect listings only, not reads.

## Variables

Each variable holds the template as earlier rules left it. Before any rule changes the template, that is the template as the upstream sent it.

| Variable | Type | Description |
| --- | --- | --- |
| `uriTemplate` | string | The template, as the upstream knows it. On the mux route, it has no slug prefix. It never changes |
| `name` | string | The template name. It never changes |
| `title`, `description`, `mimeType` | string | The current values, or `''` |
| `annotations` | map | The current annotations, or `{}` |
| `meta` | map | The current `_meta`, or `{}` |
| `icons` | list | The template's icons, or `[]` |
| `upstream` | map | The template as the upstream sent it |
| `server`, `entitlement`, `caller`, `client`, `route`, `flags`, `now` | Various | The [common variables](overview.md#common-variables) |

The requested URI is not a variable in this hook. To decide on a specific URI, use [resources/list.post](resources-list-post.md).

## Fields

| Field | Type | Effect |
| --- | --- | --- |
| `decision` | `'deny'` | Hide the template and refuse reads of URIs it matches. Stops the enforce rules |
| `reason` | string | Recorded in audit only |
| `title` | string | Replace the title |
| `description` | string | Replace the description |
| `annotations` | map | Replace all annotations |
| `annotations.<key>` | any | Set one annotation |
| `meta.<key>` | any | Set a `_meta` key. `null` removes the key |

Only `decision` stops evaluation. Every other field chains.

## Examples

### Hide a template and refuse reads through it

```json
{
  "name": "No HR template",
  "hook": "resources/templates/list.post",
  "when": "uriTemplate.startsWith('confluence://HR/')",
  "then": { "decision": "'deny'" }
}
```

`resources/templates/list` shows only `confluence://ENG/{page}`. A read of `confluence://HR/salaries` returns `-32602`, `not found: confluence://HR/salaries`. A read of `confluence://ENG/runbook` reaches the upstream.

### Templates for users only

```json
{
  "name": "Users only",
  "hook": "resources/templates/list.post",
  "when": "caller.type == 'key'",
  "then": { "decision": "'deny'" }
}
```

API keys see no templates, and their reads of `file:///repo/a.go` return `not found`. Users can list the template and read through it.

### Describe a template

```json
{
  "name": "Describe page template",
  "hook": "resources/templates/list.post",
  "when": "uriTemplate.startsWith('confluence://ENG/')",
  "then": {
    "title": "'ENG wiki page'",
    "description": "'A page in the ENG space. Replace {page} with the page ID.'"
  }
}
```

The template is listed as `{"uriTemplate": "confluence://ENG/{page}", "name": "eng", "title": "ENG wiki page", "description": "A page in the ENG space. Replace {page} with the page ID."}`.

### Replace annotations

```json
{
  "name": "Low priority",
  "hook": "resources/templates/list.post",
  "when": "true",
  "then": { "annotations": "{'audience': ['assistant'], 'priority': 0.1}" }
}
```

Every template is listed with `{"audience": ["assistant"], "priority": 0.1}`.

### Retitle a template on the mux route

```json
{
  "name": "Title",
  "hook": "resources/templates/list.post",
  "when": "true",
  "then": { "title": "'Repository file'" }
}
```

On the mux route, the template `file:///repo/{+path}` from slug `repo` is listed as `repo+file:///repo/{+path}` with the title `Repository file`.

## Nuances and limits

- A template deny also hides listed resources that the template covers, from listings and from reads.
- A template allow cannot override a deny from another list hook. A URI under a denied skill folder stays unreadable even when a template matches it.
- `uriTemplate` and `name` never change, and no rule can rewrite a template URI. A rule that sets them fails to save.
- Templates are listed by slug, then by `uriTemplate`, whatever order the upstream sends.

## Related pages

- [Hooks and rules overview](overview.md)
- [resources/list.post for decisions on single URIs, and the matching hook for resources](resources-list-post.md)
