# skills/filter

`skills/filter` decides which skills are visible. A deny always applies to the skill's whole folder, however a client reaches it.

## When it runs

`skills/filter` runs on each upstream skill entry, after [server/config](server-config.md). Skills follow the `resources` switch. The hook runs on:

- `skills/list` and `skills/get`
- `resources/list`, to drop resources under denied folders
- `resources/read` and directory reads, to refuse URIs under denied folders
- [fallback resolution](overview.md#fallback-resolution) on the mux route

## Variables

| Variable | Type | Description |
| --- | --- | --- |
| `uri` | string | The skill's URI, such as `skill://notebooks/SKILL.md`, without the mux prefix |
| `frontmatter` | map | The parsed `SKILL.md` frontmatter |
| `resources` | list or string | The manifest: a list of `{uri, digest, size}`, or `"dynamic"` |
| `name` | string | `frontmatter.name`, or `''` |
| `description` | string | `frontmatter.description`, or `''` |
| `upstream` | map | The entry as the upstream sent it |
| `server`, `entitlement`, `caller`, `client`, `route`, `flags`, `now` | Various | The [common variables](overview.md#common-variables) |

## Fields

| Field | Type | Effect |
| --- | --- | --- |
| `decision` | `'deny'` | Deny the skill's folder. Stops evaluation |
| `reason` | string | Recorded in audit only |

Every filter rule must set `decision`.

## What the client sees

The denied folder is the skill's `uri` without `/SKILL.md`. Every URI equal to or under that folder is denied:

| Request | Result |
| --- | --- |
| `skills/list` | The skill is omitted |
| `skills/get` | `-32602`, `unknown skill <uri>` |
| `resources/list` | Resources under the folder are omitted |
| `resources/read` | `-32602`, `not found: <uri>`, even when a template matches |
| Directory read | Children under the folder are dropped. A read of the folder itself returns `not found` |

## Nuances and limits

- A rule can match any field, but the effect is always the whole folder. To deny a single file, use [resources/filter](resources-filter.md).
- Skills nested under a denied skill are denied with it.
- A template allow cannot override a skill deny.
- Skill names are not unique. A rule on `name` that matches two skills denies both folders. Match `uri` to target one.
- Denying a single file inside a skill is allowed, but can break the skill, because its manifest and `SKILL.md` still reference the file.
- Skill names never change. A `resources/result` rewrite of the skill's own root `SKILL.md` that changes `name` is rejected, and the whole skill keeps its upstream bytes.
- `skills/list` is sorted by slug, then by skill URI, whatever order the upstream sends.

## Examples

### Allow only listed skills

```json
{
  "name": "Allowed skills",
  "hook": "skills/filter",
  "when": "!(name in ['notebooks', 'widgets'])",
  "then": { "decision": "'deny'" }
}
```

`skills/list` shows only `skill://notebooks/SKILL.md`. For the `billing` skill, `skills/get` returns `-32602`, `unknown skill skill://billing/SKILL.md`. The upstream also lists `skill://billing/refunds.md` as a resource, but `resources/list` omits it. A read of it returns `not found`, even though the template `skill://{+path}` matches it.

### Deny one of two skills with the same name

```json
{
  "name": "Hide support refunds",
  "hook": "skills/filter",
  "when": "uri == 'skill://acme/support/refunds/SKILL.md'",
  "then": { "decision": "'deny'" }
}
```

Two skills are named `refunds`. Only `skill://acme/billing/refunds/SKILL.md` stays visible.

### Deny by frontmatter

```json
{
  "name": "Internal skills",
  "hook": "skills/filter",
  "when": "'internal' in frontmatter.?metadata.?tags.orValue([]) && caller.type == 'key'",
  "then": { "decision": "'deny'" }
}
```

For API keys, a skill whose frontmatter has `metadata.tags: [internal]` is hidden. The optional selectors `.?` make the rule safe for skills without `metadata`. Users see every skill.

## Related pages

- [Hooks and rules overview](overview.md)
- [resources/filter for single files](resources-filter.md)
- [resources/result for rewriting skill files](resources-result.md)
