# resources/filter

`resources/filter` decides which resources are visible and readable. It runs on listed resources and on every URI a client reads, listed or not.

## When it runs

`resources/filter` runs in two places:

- On `resources/list`, on each listed upstream resource, after [server/config](server-config.md) and before [resources/modify](resources-modify.md).
- On `resources/read`, on the requested URI, whether or not a listing contains it. It also runs on each child of a directory read, on each content item in a read result, on each skill file before rewriting, and on each candidate during [fallback resolution](overview.md#fallback-resolution) on the mux route.

All of these places use one [visibility check](overview.md#resources-and-templates), so a listing and a read can never disagree. Every filter that covers the URI applies: [skills/filter](skills-filter.md) for a containing skill folder, `resources/filter` on the URI, and [templates/filter](templates-filter.md) for each template that covers it. Any deny wins.

## Variables

| Variable | Type | Description |
| --- | --- | --- |
| `uri` | string | The resource URI, as the upstream knows it. On the mux route, it has no slug prefix |
| `name` | string | The resource name, or `''` |
| `title` | string | The resource title, or `''` |
| `description` | string | The resource description, or `''` |
| `mimeType` | string | The MIME type, or `''` |
| `annotations` | map | The resource annotations, or `{}` |
| `meta` | map | The resource's `_meta`, or `{}` |
| `size` | int | The size in bytes, or `0` |
| `icons` | list | The resource's icons, or `[]` |
| `upstream` | map | The resource as the upstream sent it |
| `server`, `entitlement`, `caller`, `client`, `route`, `now` | Various | The [common variables](overview.md#common-variables) |

When a read names a URI that no listing contains, such as a file reached through a template, only `uri` has a value. Every other field is empty.

## Fields

| Field | Type | Effect |
| --- | --- | --- |
| `decision` | `'deny'` | Hide the resource and refuse reads of the URI. Stops evaluation |
| `reason` | string | Recorded in audit only |

Every filter rule must set `decision`.

## What the client sees

- `resources/list` omits the resource.
- `resources/read` returns `-32602`, `not found: <uri>`. The upstream is not called, so the reply says nothing about whether the resource exists.
- A directory read drops denied children, whatever their MIME type, and whether or not they are marked as directories.
- A read result drops content items for other URIs that the caller cannot read, and content items whose URI is not in canonical form.

If an enforce rule fails to evaluate, the resource is hidden and reads are refused. A listed resource that [resources/modify](resources-modify.md) fails on, or that collides with another resource, is also unreadable.

## Nuances and limits

- Rules always see URIs without the mux prefix, so the same rule works on both routes.
- Because the filter runs on every read URI, it can deny a single file that is reachable only through a template or a skill.
- A URI the server does not list has no name, MIME type or other fields, only `uri`. A rule that reads one of those fields fails on such a URI, and a rule that fails refuses the read. So a deny written on `name` also stops every unlisted read on that server. Put a `uri` condition first to avoid that, for example `uri.startsWith('docs://') && name.startsWith('internal')`.
- A read is allowed unless a rule denies it, whether or not the server lists the URI. To close a folder, deny its prefix: `uri.startsWith('file:///secret/')`.
- A directory read checks every child with the same check as a read, including `resources/filter`. A read of a denied directory URI itself is refused.
- Resource URIs cannot be rewritten by any rule.

## Examples

### Hide one resource

```json
{
  "name": "Hide changelog",
  "hook": "resources/filter",
  "when": "uri == 'file:///CHANGELOG.md'",
  "then": { "decision": "'deny'" }
}
```

`resources/list` shows only `file:///README.md`. A read of `file:///CHANGELOG.md` returns `-32602`, `not found: file:///CHANGELOG.md`.

### Deny files reached through a template

```json
{
  "name": "No env files",
  "hook": "resources/filter",
  "when": "uri.endsWith('.env') || uri.contains('/.git/')",
  "then": { "decision": "'deny'" }
}
```

With the template `file:///repo/{+path}`, a read of `file:///repo/config/prod.env` returns `not found`. A read of `file:///repo/src/main.go` reaches the upstream.

### Hide large binary resources

```json
{
  "name": "No large binaries",
  "hook": "resources/filter",
  "when": "!mimeType.startsWith('text/') && size > 1048576",
  "then": { "decision": "'deny'" }
}
```

A 50 MB `application/zip` resource is hidden. A 2 MB `text/markdown` resource stays.

## Related pages

- [Hooks and rules overview](overview.md)
- [templates/filter](templates-filter.md) and [skills/filter](skills-filter.md), the other filters on a read
- [resources/read for decisions with a reason](resources-read.md)
