# templates/modify

`templates/modify` changes the title, description, annotations and metadata of a listed resource template. It cannot change the template or the name.

## When it runs

`templates/modify` runs on each template that passed [templates/filter](templates-filter.md), on every `resources/templates/list`. On the mux route, the gateway adds the slug prefix after this hook.

## Variables

| Variable | Type | Description |
| --- | --- | --- |
| `uriTemplate`, `name` | string | The template and its name. They never change |
| `title`, `description`, `mimeType` | string | The current values, or `''` |
| `annotations` | map | The current annotations, or `{}` |
| `meta` | map | The current `_meta`, or `{}` |
| `icons` | list | The template's icons, or `[]` |
| `upstream` | map | The template as the upstream sent it |
| `server`, `entitlement`, `caller`, `client`, `route`, `flags`, `now` | Various | The [common variables](overview.md#common-variables) |

## Fields

| Field | Type | Effect |
| --- | --- | --- |
| `title` | string | Replace the title |
| `description` | string | Replace the description |
| `annotations` | map | Replace all annotations |
| `annotations.<key>` | any | Set one annotation |
| `meta.<key>` | any | Set a `_meta` key. `null` removes the key |

No field stops evaluation. Changes chain.

## What the client sees

`resources/templates/list` shows the modified template with its original `uriTemplate`. On the mux route, the template carries the slug prefix before its scheme, and its variables are unchanged, so RFC 6570 expansion keeps working.

A rule that fails to evaluate hides the template, and the listing reports the problem to administrators. The template then counts as denied: URIs it covers are unreadable and dropped from `resources/list`, the same as after a [templates/filter](templates-filter.md) deny. Templates that collide are treated the same way.

## Nuances and limits

- `uriTemplate` and `name` never change. A rule that sets them fails to save.
- Text changes affect listings only, not reads.
- Templates are listed by slug, then by `uriTemplate`, whatever order the upstream sends.

## Examples

### Describe a template

```json
{
  "name": "Describe page template",
  "hook": "templates/modify",
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
  "hook": "templates/modify",
  "when": "true",
  "then": { "annotations": "{'audience': ['assistant'], 'priority': 0.1}" }
}
```

Every template is listed with `{"audience": ["assistant"], "priority": 0.1}`.

### Retitle a template on the mux route

```json
{
  "name": "Title",
  "hook": "templates/modify",
  "when": "true",
  "then": { "title": "'Repository file'" }
}
```

On the mux route, the template `file:///repo/{+path}` from slug `repo` is listed as `repo+file:///repo/{+path}` with the title `Repository file`.

## Related pages

- [Hooks and rules overview](overview.md)
- [templates/filter for visibility](templates-filter.md)
- [resources/modify, the matching hook for resources](resources-modify.md)
