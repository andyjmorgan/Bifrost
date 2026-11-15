# resources/result

`resources/result` changes the text of read results and skill files, or withholds them. It is opt-in: without rules in this hook, content passes through unchanged.

## When it runs

`resources/result` runs in two places:

- On each `resources/read` result, once for each content item, text or binary (`blob`). It runs after the gateway drops content items the caller cannot read, and before the mux URI prefix.
- On `skills/list` and `skills/get`, once for each text file in each visible skill's manifest, as part of the skill's [rewrite plan](#skill-files).

It does not run on directory reads, or when the upstream replies with `input_required`.

## Variables

| Variable | Type | Description |
| --- | --- | --- |
| `result` | map | `text`, the text of this content item (`''` for a binary item), and `contents`, a one-item list with the item's `uri`, `mimeType` and, for a text item, `text`. Later rules see earlier changes |
| `args` | map | `{uri: <uri>}`, the content item's URI as the upstream knows it |
| `uri`, `name`, `title`, `description`, `mimeType`, `annotations`, `meta`, `size`, `icons` | Various | The listed resource for the content item's URI, or only `uri` when no listing contains it |
| `upstream` | map | The resource as the upstream sent it |
| `server`, `entitlement`, `caller`, `client`, `route`, `now` | Various | The [common variables](overview.md#common-variables) |

## Fields

| Field | Type | Effect | Stops evaluation |
| --- | --- | --- | --- |
| `decision` | `'deny'` | Withhold the whole result | Yes |
| `reason` | string | The error message the client receives with a deny | No |
| `text` | string | Replace the text of this content item. Ignored for a binary item, which keeps its bytes | No |

`slugUris(text)` is available in this hook.

## What the client sees

| Outcome | Result |
| --- | --- |
| Changed | The read result with the new text. `uri`, `mimeType` and other keys stay |
| Denied | `-32602` with `reason` as the message. Without a reason, the message is `the read result was refused by policy` |
| A rule fails to evaluate | `-32602`, `the read result was refused by policy` |
| Shaped result over the size limit | `-32013`, `result exceeds gateway size limit after shaping` |

### Skill files

For skills, the gateway builds one rewrite plan per skill. The same plan drives the `skills/list` and `skills/get` entry and every read of the skill's files, so the manifest always matches what a read returns:

- It recomputes `digest` (`sha256:<hex>`) and `size` for each changed file, including a file that was empty.
- It copies the rewritten frontmatter of the skill's own root `SKILL.md` into the entry's `frontmatter`. A nested `SKILL.md` in a subfolder is rewritten like any other file.
- The plan is all or nothing. If any file is denied, fails to evaluate, or the rewrite is rejected, every file of the skill keeps its upstream bytes, and the listing reports why.
- The gateway rejects a rewrite of the root `SKILL.md` that changes the skill's `name`, or that leaves invalid frontmatter. Valid frontmatter starts with a `---` line, ends with a line that is exactly `---`, and has `name` and `description` as strings.
- Dynamic skills, whose manifest is `"dynamic"`, are never rewritten, at list time or read time. Denies still apply.
- Rules that read `caller`, `client` or `now` are not applied to skill files, because listing and reading would produce different bytes. The skill is served unchanged, and the listing reports why. Rules that read `route` are applied.
- Files the caller cannot read are never fetched or rewritten.
- A read of a skill file still runs the rules for their decisions. A deny withholds the file even when the plan serves upstream bytes.

## Nuances and limits

- Content belongs to the upstream. Without a rule here, URIs inside text reach the client without the mux prefix.
- `slugUris(text)` rewrites only URIs that belong to this upstream: its listed resources, URIs under its templates' literal prefixes, and URIs under its skill folders. Other URIs, such as web links, are unchanged. On the direct route, it changes nothing, so the same rule works on both routes.
- A deny on any content item, text or binary, withholds the whole result.
- `mimeType` and the other top-level item variables describe the listed resource. To test the content item's own MIME type, read `result.contents[0].mimeType`.
- Read content is never cached, so rules run on every read.

## Examples

### Replace text

```json
{
  "name": "Wiki wording",
  "hook": "resources/result",
  "when": "true",
  "then": { "text": "regex.replace(result.text, 'Confluence', 'the wiki')" }
}
```

A read of `file:///guide.md` that returns `Search Confluence first.` reaches the client as `Search the wiki first.`, with its `uri` and `mimeType` unchanged. A `blob` item keeps its bytes.

### Prefix this server's URIs on the mux route

```json
{
  "name": "Prefix own URIs",
  "hook": "resources/result",
  "when": "true",
  "then": { "text": "slugUris(result.text)" }
}
```

On the mux route, with `file:///faq.md` listed by slug `docs`, the text `See file:///faq.md and https://example.com/x.` becomes `See docs+file:///faq.md and https://example.com/x.` On the direct route, the text is unchanged.

### Withhold content by pattern

```json
{
  "name": "Withhold keys",
  "hook": "resources/result",
  "when": "result.text.contains('BEGIN PRIVATE KEY')",
  "then": { "decision": "'deny'", "reason": "'This file contains a private key and was withheld.'" }
}
```

A read of a file containing a PEM private key returns `{"code": -32602, "message": "This file contains a private key and was withheld."}`.

### Rewrite a skill file

The "Wiki wording" rule above also applies to skills. For a skill whose `SKILL.md` frontmatter says `description: Use Confluence.`, `skills/get` returns `frontmatter.description` as `Use the wiki.`, and the manifest entry for `SKILL.md` gets a new `sha256` digest and the new size, 51 bytes.

## Related pages

- [Hooks and rules overview](overview.md)
- [Mux URIs and fallback resolution](overview.md#mux-uris)
- [skills/filter for skill visibility](skills-filter.md)
- [resources/read for read decisions](resources-read.md)
