# resources/modify

`resources/modify` changes the title, description, annotations and metadata of a listed resource. It cannot change the URI or the name.

## When it runs

`resources/modify` runs on each resource that passed [resources/filter](resources-filter.md), on every `resources/list`. On the mux route, the gateway adds the slug prefix to the URI after this hook.

## Variables

| Variable | Type | Description |
| --- | --- | --- |
| `uri`, `name` | string | The resource URI and name. They never change |
| `title`, `description`, `mimeType` | string | The current values, or `''` |
| `annotations` | map | The current annotations, or `{}` |
| `meta` | map | The current `_meta`, or `{}` |
| `size` | int | The size in bytes, or `0` |
| `icons` | list | The resource's icons, or `[]` |
| `upstream` | map | The resource as the upstream sent it |
| `server`, `entitlement`, `caller`, `client`, `route`, `now` | Various | The [common variables](overview.md#common-variables) |

## Fields

| Field | Type | Effect |
| --- | --- | --- |
| `title` | string | Replace the title |
| `description` | string | Replace the description |
| `annotations` | map | Replace all annotations |
| `annotations.<key>` | any | Set one annotation, such as `annotations.priority` |
| `meta.<key>` | any | Set a `_meta` key. `null` removes the key |

No field stops evaluation. Changes chain.

## What the client sees

`resources/list` shows the modified resource with its original URI. On the mux route, the URI carries the slug prefix.

A rule that fails to evaluate hides the resource, and the listing reports the problem to administrators. The hidden resource is also unreadable: a read returns `-32602`, `not found: <uri>`, and fallback resolution does not offer it.

## Nuances and limits

- URIs and names never change. A rule that sets `uri` or `name` fails to save, with `resources/modify cannot set "name"`.
- Text changes affect listings only. They do not change read results. To change content, use [resources/result](resources-result.md).

## Examples

### Prefix titles for one space

```json
{
  "name": "ENG titles",
  "hook": "resources/modify",
  "when": "uri.startsWith('confluence://ENG/')",
  "then": { "title": "'ENG wiki: ' + (title != '' ? title : name)" }
}
```

`confluence://ENG/runbook` is listed with the title `ENG wiki: runbook`. `confluence://OPS/oncall` keeps its title.

### Set audience and priority

```json
{
  "name": "Assistant only",
  "hook": "resources/modify",
  "when": "uri.endsWith('.json')",
  "then": { "annotations.audience": "['assistant']", "annotations.priority": "0.2" }
}
```

`file:///schema.json` is listed with `{"audience": ["assistant"], "priority": 0.2}`.

### Rewrite descriptions

```json
{
  "name": "Wiki wording",
  "hook": "resources/modify",
  "when": "true",
  "then": { "description": "regex.replace(description, '(?i)confluence', 'the wiki')" }
}
```

`Exported from Confluence.` becomes `Exported from the wiki.`

## Related pages

- [Hooks and rules overview](overview.md)
- [resources/filter for visibility](resources-filter.md)
- [templates/modify, the matching hook for templates](templates-modify.md)
