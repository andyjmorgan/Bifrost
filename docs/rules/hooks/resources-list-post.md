# resources/list.post

`resources/list.post` decides which resources are visible and readable, and changes the title, description, annotations and metadata of a listed resource. It cannot change the URI or the name. Its deny rules also run on every URI a client reads, listed or not.

## When it runs

`resources/list.post` runs in two places:

- On `resources/list`, on each resource the server lists, after [server.settings](server-settings.md). On the mux route, the gateway adds the slug prefix to the URI after this hook.
- On `resources/read`, on the requested URI, whether or not a listing contains it. It also runs on each child of a directory read, on each entry of a read result, on each skill file before rewriting, and on each candidate during [fallback resolution](overview.md#fallback-resolution) on the mux route.

All of these places use one [visibility check](overview.md#resources-and-templates), so a listing and a read can never disagree. Every list hook that covers the URI applies: [skills/list.post](skills-list-post.md) for a containing skill folder, `resources/list.post` on the URI, and [resources/templates/list.post](resources-templates-list-post.md) for each template that covers it. Any deny wins.

### URIs that no listing contains

A read can name a URI that no listing contains, such as a file reached through a template or a link in a tool result. The hook still runs on that URI, with these differences:

- Only the rules that deny run. Nothing is changed.
- Only `resource.uri` is present. `resource.upstream` holds the same one field, `resource.upstream.uri`. Every other field is absent.
- A deny rule that reads an absent field with a plain `.` fails, and a rule that fails refuses the read.

## Order of rules

The rules run on each resource from top to bottom, in the order you write them. A rule can do one of two things:

- hide the resource, by setting `decision: 'deny'`
- change the resource, by setting any of the [writable paths](#what-a-rule-can-set)

Changes chain. A later rule reads the resource as earlier rules left it, so a hide rule written after a change to `resource.title` sees the new title. `resource.upstream` always holds the resource as the server declares it.

The first deny hides the resource and stops the enforce rules. Audit rules still run, so the audit record is complete.

Writing hide rules first is cheaper, because the gateway never changes a hidden resource. It is not required.

A rule that fails to evaluate also hides the resource, and the listing reports the problem to administrators. The hidden resource is also unreadable: a read returns `-32602`, `not found: <uri>`, and fallback resolution does not offer it. A listed resource that collides with another resource is unreadable in the same way.

## What a rule can read

| Branch | Notable fields |
| --- | --- |
| `resource` | The resource as earlier rules left it: `resource.uri`, in [canonical spelling](overview.md#uri-form), `resource.name`, `resource.title`, `resource.description`, `resource.mimeType`, `resource.size`, `resource.annotations`, `resource.icons`, `resource._meta` |
| `resource.upstream` | The resource as the server declares it. It never changes |
| `request` | `request.caller`, `request.client`, `request.headers`, `request.route`, `request.flags`, `request.time`. `request.params` holds only `_meta` here |
| `server` | `server.id`, `server.slug` and what the gateway knows about the server |

`resource.uri` is the URI as the server knows it. On the mux route, it has no slug prefix. It never changes.

Only `resource.uri` is always present. A listed resource also always has `resource.name`. Read the other fields with the [optional forms](overview.md#optional-values):

| Read | Use |
| --- | --- |
| A MIME type, or `''` | `resource.?mimeType.orValue('')` |
| A size, or zero | `resource.?size.orValue(0)` |
| A title, falling back to the name, then the URI | `resource.?title.orValue(resource.?name.orValue(resource.uri))` |
| An annotation | `resource.?annotations.?priority.orValue(0.0)` |
| Whether there is a description | `has(resource.description)` |

## What a rule can set

| Path | Type | Effect |
| --- | --- | --- |
| `resource.title` | string | Replace the title |
| `resource.description` | string | Replace the description |
| `resource.annotations` | map | Replace all annotations |
| `resource.annotations.<key>` | any | Set one annotation. `audience` is a list, `priority` a number and `lastModified` a string |
| `resource._meta.<key>` | any | Set a `_meta` key. `null` removes the key |

A rule here can deny. It cannot ask the user to confirm.

| Key | Value | Effect |
| --- | --- | --- |
| `decision` | `'deny'` | Hide the resource and refuse reads of the URI. Stops the enforce rules |
| `reason` | string | Recorded in audit only |

Only `decision` stops evaluation. Every other path chains.

A rule cannot set `resource.uri`, `resource.name`, `resource.mimeType` or `resource.size`. A rule that sets one is refused when saved, for example with `resource.uri cannot be changed on resources/list.post; a rule here can set fields of resource`.

## What the client sees

When a rule hides a resource:

- `resources/list` omits the resource.
- `resources/read` returns `-32602`, `not found: <uri>`. The server is not called, so the reply says nothing about whether the resource exists.
- A directory read drops denied children, whatever their MIME type, and whether or not they are marked as directories.
- A read result drops entries for other URIs that the caller cannot read, and entries whose URI is not in canonical form.
- A rule that sets both `decision` and other paths hides the resource. Its other values are discarded.

When a rule changes a resource:

- `resources/list` shows the changed resource with its original URI. On the mux route, the URI carries the slug prefix.
- Text changes affect listings only. They do not change read results. To change contents, use [resources/read.post.result](resources-read-post-result.md).

## Examples

### Hide one resource

```json
{
  "name": "Hide changelog",
  "hook": "resources/list.post",
  "when": "resource.uri == 'file:///CHANGELOG.md'",
  "then": { "decision": "'deny'" }
}
```

`resources/list` shows only `file:///README.md`. A read of `file:///CHANGELOG.md` returns `-32602`, `not found: file:///CHANGELOG.md`.

### Deny files reached through a template

```json
{
  "name": "No env files",
  "hook": "resources/list.post",
  "when": "resource.uri.endsWith('.env') || resource.uri.contains('/.git/')",
  "then": { "decision": "'deny'" }
}
```

With the template `file:///repo/{+path}`, a read of `file:///repo/config/prod.env` returns `not found`. A read of `file:///repo/src/main.go` reaches the server.

### Hide large binary resources

```json
{
  "name": "No large binaries",
  "hook": "resources/list.post",
  "when": "!resource.?mimeType.orValue('').startsWith('text/') && resource.?size.orValue(0) > 1048576",
  "then": { "decision": "'deny'" }
}
```

A 50 MB `application/zip` resource is hidden. A 2 MB `text/markdown` resource stays. A resource that declares no size stays, and so does a read of a URI that no listing contains.

### Prefix titles for one space

```json
{
  "name": "ENG titles",
  "hook": "resources/list.post",
  "when": "resource.uri.startsWith('confluence://ENG/')",
  "then": { "resource.title": "'ENG wiki: ' + resource.?title.orValue(resource.?name.orValue(resource.uri))" }
}
```

`confluence://ENG/runbook` is listed with the title `ENG wiki: runbook`. `confluence://OPS/oncall` keeps its title.

### Set audience and priority

```json
{
  "name": "Assistant only",
  "hook": "resources/list.post",
  "when": "resource.uri.endsWith('.json')",
  "then": { "resource.annotations.audience": "['assistant']", "resource.annotations.priority": "0.2" }
}
```

`file:///schema.json` is listed with `{"audience": ["assistant"], "priority": 0.2}`.

## Nuances and limits

- Rules always see URIs without the mux prefix, so the same rule works on both routes.
- Because the deny rules run on every read URI, the hook can deny a single file that is reachable only through a template or a skill.
- A URI the server does not list has no name, MIME type or other fields, only `resource.uri`. A deny rule that reads one of those fields with a plain `.` fails on such a URI, and a rule that fails refuses the read. So a deny written on `resource.name` also stops every unlisted read on that server. Put a URI condition first and read the name safely, for example `resource.uri.startsWith('docs://') && resource.?name.orValue('').startsWith('internal')`.
- A read is allowed unless a rule denies it, whether or not the server lists the URI. To close a folder, deny its prefix: `resource.uri.startsWith('file:///secret/')`.
- A directory read checks every child with the same check as a read, including `resources/list.post`. A read of a denied directory URI itself is refused.
- URIs and names never change, and no rule can rewrite a resource URI.
- An empty string and a size of zero from the server read as absent.

## Related pages

- [Hooks and rules overview](overview.md)
- [resources/templates/list.post](resources-templates-list-post.md) and [skills/list.post](skills-list-post.md), the other list hooks on a read
- [resources/read.pre for decisions with a reason](resources-read-pre.md)
- [resources/read.post.result for changing contents](resources-read-post-result.md)
