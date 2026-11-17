# skills/list.post

`skills/list.post` decides which skills are visible. A deny always applies to the skill's whole folder, however a client reaches it.

## When it runs

`skills/list.post` runs on each skill entry from the server, after [server.settings](server-settings.md). Skills follow `settings.resources`. The hook runs on:

- `skills/list` and `skills/get`
- `resources/list`, to drop resources under denied folders
- `resources/read` and directory reads, to refuse URIs under denied folders
- [fallback resolution](overview.md#fallback-resolution) on the mux route

## What a rule can read

| Branch | Notable fields |
| --- | --- |
| `skill` | `skill.uri`, the skill's URI, such as `skill://notebooks/SKILL.md`, without the mux prefix. `skill.frontmatter`, the parsed `SKILL.md` frontmatter. `skill.name` and `skill.description`, from the frontmatter. `skill.resources`, the manifest: a list of `{uri, digest, size}`, or `"dynamic"` |
| `skill.upstream` | The entry as the server sent it |
| `request` | `request.caller`, `request.client`, `request.headers`, `request.route`, `request.flags`, `request.time`. `request.params` holds only `_meta` here |
| `server` | `server.slug` and what the gateway knows about the server |
| `entitlement` | `entitlement.id`, `entitlement.slug`, `entitlement.name` |

Only `skill.uri` is always present. Read the other fields with the [optional forms](overview.md#optional-values):

| Read | Use |
| --- | --- |
| The name, or `''` | `skill.?name.orValue('')` |
| A value deep in the frontmatter | `skill.?frontmatter.?metadata.?tags.orValue([])` |

## What a rule can set

A rule here sets no path. It can only deny, and every `skills/list.post` rule must set `decision`.

| Key | Value | Effect |
| --- | --- | --- |
| `decision` | `'deny'` | Deny the skill's folder. Stops evaluation |
| `reason` | string | Recorded in audit only |

A rule here cannot ask the user to confirm. A rule without `decision` is refused when saved, with `a skills/list.post rule must set decision: "deny"`.

## What the client sees

The denied folder is the skill's `uri` without `/SKILL.md`. Every URI equal to or under that folder is denied:

| Request | Result |
| --- | --- |
| `skills/list` | The skill is omitted |
| `skills/get` | `-32602`, `unknown skill <uri>` |
| `resources/list` | Resources under the folder are omitted |
| `resources/read` | `-32602`, `not found: <uri>`, even when a template matches |
| Directory read | Children under the folder are dropped. A read of the folder itself returns `not found` |

## Examples

### Allow only listed skills

```json
{
  "name": "Allowed skills",
  "hook": "skills/list.post",
  "when": "!(skill.?name.orValue('') in ['notebooks', 'widgets'])",
  "then": { "decision": "'deny'" }
}
```

`skills/list` shows only `skill://notebooks/SKILL.md`. For the `billing` skill, `skills/get` returns `-32602`, `unknown skill skill://billing/SKILL.md`. The server also lists `skill://billing/refunds.md` as a resource, but `resources/list` omits it. A read of it returns `not found`, even though the template `skill://{+path}` matches it.

### Deny one of two skills with the same name

```json
{
  "name": "Hide support refunds",
  "hook": "skills/list.post",
  "when": "skill.uri == 'skill://acme/support/refunds/SKILL.md'",
  "then": { "decision": "'deny'" }
}
```

Two skills are named `refunds`. Only `skill://acme/billing/refunds/SKILL.md` stays visible.

### Deny by frontmatter

```json
{
  "name": "Internal skills",
  "hook": "skills/list.post",
  "when": "'internal' in skill.?frontmatter.?metadata.?tags.orValue([]) && request.caller.type == 'apikey'",
  "then": { "decision": "'deny'" }
}
```

For API keys, a skill whose frontmatter has `metadata.tags: [internal]` is hidden. The optional selectors `.?` make the rule safe for skills without `metadata`. Other callers see every skill.

## Nuances and limits

- A rule can match any field, but the effect is always the whole folder. To deny a single file, use [resources/list.post](resources-list-post.md).
- Skills nested under a denied skill are denied with it.
- A template that is allowed cannot override a skill deny.
- Skill names are not unique. A rule on `skill.name` that matches two skills denies both folders. Match `skill.uri` to target one.
- A rule that fails to evaluate hides the skill. A plain read of `skill.name` fails on a skill whose frontmatter has no name.
- Denying a single file inside a skill is allowed, but can break the skill, because its manifest and `SKILL.md` still reference the file.
- Skill names never change. A `resources/read.post.result` rewrite of the skill's own root `SKILL.md` that changes `name` is rejected, and the whole skill keeps the server's bytes.
- `skills/list` is sorted by slug, then by skill URI, whatever order the server sends.

## Related pages

- [Hooks and rules overview](overview.md)
- [resources/list.post for single files](resources-list-post.md)
- [resources/read.post.result for rewriting skill files](resources-read-post-result.md)
