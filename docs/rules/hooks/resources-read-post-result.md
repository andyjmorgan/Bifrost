# resources/read.post.result

`resources/read.post.result` changes the contents of a read result, drops entries from it, or blocks the read. It runs once over the whole response. Without rules in this hook, contents pass through unchanged.

## When it runs

`resources/read.post.result` runs in two places:

- On each `resources/read` result, once, over every entry the server returned. It runs after the gateway drops entries the caller cannot read, entries whose URI is not in [canonical form](overview.md#uri-form), and entries that are not objects with a string `uri`. It runs before the mux URI prefix.
- On `skills/list` and `skills/get`, once for each text file in each visible skill's manifest, as a read that returned that file alone. This builds the skill's [rewrite plan](#skill-files).

It does not run on directory reads, or when the server replies with `input_required`.

## What a rule can read

| Branch | Notable fields |
| --- | --- |
| `response.result` | `response.result.contents`, the list of entries, as earlier rules left it. Each entry has `uri`, and `mimeType`, `text`, `blob`, `_meta` and any other key where the server sent them. `response.result.resultType` and `response.result._meta` are there when the server sent them |
| `response.upstream` | The response as the server produced it: `response.upstream.result`, with the same fields. Its `contents` holds the entries this caller may read, and never changes |
| `resource` | The resource the read named. Each entry of `contents` has its own `uri` |
| `resource.upstream` | That resource as the server declares it |
| `request.params` | `request.params.uri`, the URI the read named |
| `request.upstream` | The read as the server received it: `request.upstream.params.uri` |
| `request` | Also `request.method`, `request.caller`, `request.client`, `request.headers`, `request.route`, `request.flags`, `request.time` |
| `server` | `server.slug` and what the gateway knows about the server |
| `entitlement` | `entitlement.id`, `entitlement.slug`, `entitlement.name` |

A read can return several entries. An entry is text when it has `text` and binary when it has `blob`. There is no joined text of the response. Work on the list with CEL's list macros, and read the optional fields of an entry with the [optional forms](overview.md#optional-values):

| Read | Use |
| --- | --- |
| The text of an entry, or `''` for a binary entry | `c.?text.orValue('')` |
| Whether an entry is binary | `has(c.blob)` |
| An entry's MIME type | `c.?mimeType.orValue('')` |
| A rebuilt entry that keeps an optional field | `{'uri': c.uri, ?'mimeType': c.?mimeType, 'text': ...}` |

For a URI that no listing contains, only `resource.uri` and `resource.upstream.uri` are present.

## What a rule can set

| Path | Type | Effect | Stops evaluation |
| --- | --- | --- | --- |
| `response.result.contents` | list | Replace the list of entries. A rule may change an entry or drop one | No |

A rule here can deny. It cannot ask the user to confirm.

| Key | Value | Effect | Stops evaluation |
| --- | --- | --- | --- |
| `decision` | `'deny'` | Block the whole read | Yes |
| `reason` | string | The error message the client receives with a deny | No |

A rule may not add an entry for a URI the read did not return. The entries are already the ones this caller may read, and nothing else may join them. An entry a rule leaves must keep its `uri` exactly as the server wrote it, and its `text`, `blob` and `mimeType` must be strings.

`slugUris(text)` is available in this hook.

## What the client sees

| Outcome | Result |
| --- | --- |
| Changed | The read result with the new list of entries |
| Denied | `-32602` with `reason` as the message. Without a reason, the message is `the read result was refused by policy` |
| A rule fails to evaluate | `-32602`, `the read result was refused by policy` |
| A rule adds an entry for a URI the read did not return | `-32602`, `the read result could not be checked` |
| Shaped result over the size limit | `-32013`, `result exceeds gateway size limit after shaping` |

### Skill files

For skills, the gateway builds one rewrite plan for each skill. It runs these rules over each text file of the skill ahead of time. The same plan drives the `skills/list` and `skills/get` entry and every read of the skill's files, so the manifest always matches what a read returns:

- It recomputes `digest` (`sha256:<hex>`) and `size` for each changed file, including a file that was empty.
- It copies the rewritten frontmatter of the skill's own root `SKILL.md` into the entry's `frontmatter`. A nested `SKILL.md` in a subfolder is rewritten like any other file.
- The plan is all or nothing. If any file is denied, fails to evaluate, or the rewrite is rejected, every file of the skill keeps the server's bytes, and the listing reports why.
- The gateway rejects a rewrite of the root `SKILL.md` that changes the skill's `name`, or that leaves invalid frontmatter. Valid frontmatter starts with a `---` line, ends with a line that is exactly `---`, and has `name` and `description` as strings.
- A rule that drops a skill file, or leaves it with no text, fails the rewrite.
- Dynamic skills, whose manifest is `"dynamic"`, are never rewritten, at list time or read time. Denies still apply.
- Rules that read `request` are not applied to skill files, because listing and reading would produce different bytes. That includes `request.caller`, `request.client`, `request.time` and `request.route`. The skill is served unchanged, and the listing reports why.
- Files the caller cannot read are never fetched or rewritten.

On a read, `SKILL.md` and each file that a visible skill's manifest lists keep the bytes the manifest describes, whatever a rule sets at read time. This holds for a file in a nested skill's folder that an outer skill's manifest lists. When a rule's change is set aside this way, the gateway records an issue. A file that more than one skill answers for is never rewritten, so no manifest promises bytes a read does not return. A second entry in one read for a file a skill vouches for is dropped. A rule can still block the read or drop the entry. A file under the skill's folder that the manifest does not list is an ordinary entry, and a rule's change to it stands. For a dynamic skill, which has no manifest, every file under the folder is served as the server sent it.

`response.upstream` always holds the server's own bytes, also for a skill file that the plan rewrote.

## Examples

### Replace text

```json
{
  "name": "Wiki wording",
  "hook": "resources/read.post.result",
  "when": "true",
  "then": {
    "response.result.contents": "response.result.contents.map(c, has(c.text) ? {'uri': c.uri, ?'mimeType': c.?mimeType, 'text': regex.replace(c.text, 'Confluence', 'the wiki')} : c)"
  }
}
```

A read of `file:///guide.md` that returns `Search Confluence first.` reaches the client as `Search the wiki first.`, with its `uri` and `mimeType` unchanged. A binary entry keeps its bytes.

The rule also applies to skills. For a skill whose `SKILL.md` frontmatter says `description: Use Confluence.`, `skills/get` returns `frontmatter.description` as `Use the wiki.`. The manifest entry for `SKILL.md` gets a new `sha256` digest and the new size, 51 bytes.

### Prefix this server's URIs on the mux route

```json
{
  "name": "Prefix own URIs",
  "hook": "resources/read.post.result",
  "when": "true",
  "then": {
    "response.result.contents": "response.result.contents.map(c, has(c.text) ? {'uri': c.uri, ?'mimeType': c.?mimeType, 'text': slugUris(c.text)} : c)"
  }
}
```

On the mux route, with `file:///faq.md` listed by slug `docs`, the text `See file:///faq.md and https://example.com/x.` becomes `See docs+file:///faq.md and https://example.com/x.` On the direct route, the text is unchanged.

### Block a read by its contents

```json
{
  "name": "Withhold keys",
  "hook": "resources/read.post.result",
  "when": "response.result.contents.exists(c, has(c.text) && (c.text.contains('BEGIN PRIVATE KEY')))",
  "then": { "decision": "'deny'", "reason": "'This file contains a private key and was withheld.'" }
}
```

A read of a file containing a PEM private key returns `{"code": -32602, "message": "This file contains a private key and was withheld."}`.

### Drop binary entries

```json
{
  "name": "Drop binary entries",
  "hook": "resources/read.post.result",
  "when": "response.result.contents.exists(c, has(c.blob))",
  "then": { "response.result.contents": "response.result.contents.filter(c, !has(c.blob))" }
}
```

A read that returns a text entry and an image entry reaches the client with the text entry only. `response.upstream.result.contents` still holds both, so a later rule can see what was dropped.

## Nuances and limits

- The hook runs once over the whole response. It does not run once for each entry, and there is no hook for a single entry.
- Contents belong to the server. Without a rule here, URIs inside text reach the client without the mux prefix.
- `slugUris(text)` rewrites only URIs that belong to this server: its listed resources, URIs under its templates' literal prefixes, and URIs under its skill folders. Other URIs, such as web links, are unchanged. On the direct route, it changes nothing, so the same rule works on both routes.
- A deny blocks the whole read, whatever entry made the rule match.
- `resource` describes the resource the read named. To test an entry's own MIME type, read it from the entry, as `c.?mimeType.orValue('')`.
- `response.upstream` does not show entries the gateway already withheld, such as entries for URIs the caller cannot read.
- A rule that rebuilds an entry must keep its `uri`. An entry with another URI counts as added, and the read fails.
- Read contents are never cached, so rules run on every read.

## Related pages

- [Hooks and rules overview](overview.md)
- [Mux URIs and fallback resolution](overview.md#mux-uris)
- [skills/list.post for skill visibility](skills-list-post.md)
- [resources/read.pre for read decisions](resources-read-pre.md)
