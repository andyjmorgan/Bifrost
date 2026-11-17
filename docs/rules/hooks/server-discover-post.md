# server/discover.post

`server/discover.post` changes how a server describes itself to the client: its title, description and instructions.

## When it runs

`server/discover.post` runs after [server.settings](server-settings.md) whenever the gateway shows server details:

- discover on the direct route
- mux instruction excerpts in discover
- the `get_server_instructions` tool on the mux route
- the search roster, when `settings.searchRoster` is on

The gateway reads instructions from the server live, so a change at the server shows at once.

## What a rule can read

| Branch | Notable fields |
| --- | --- |
| `server` | The server as earlier rules left it: `server.title`, `server.description` and `server.instructions`. Also `server.slug`, `server.name`, `server.protocolVersion` and `server.url` |
| `server.upstream` | What the server says about itself: `server.upstream.title`, `server.upstream.description`, `server.upstream.instructions`. It never changes |
| `request` | `request.caller`, `request.client`, `request.headers`, `request.route`, `request.flags`, `request.time`. `request.method` is `server/discover`, and `request.params` holds only `_meta` |
| `entitlement` | `entitlement.id`, `entitlement.slug`, `entitlement.name` |

Title, description and instructions are absent when the server did not give them. Read them with the [optional forms](overview.md#optional-values): `server.?instructions.orValue('')`, or guard with `has(server.instructions)`.

## What a rule can set

| Path | Type | Effect |
| --- | --- | --- |
| `server.title` | string | The title on the direct route, and in search ranking when `settings.searchRoster` is on |
| `server.description` | string | The server description. Search mode uses it to rank tools when `settings.searchRoster` is on |
| `server.instructions` | string | The instructions on the direct route, in mux excerpts, through `get_server_instructions` and in the search roster |

A rule here cannot deny and cannot ask the user to confirm. Nothing stops evaluation.

Changes chain: a later rule reads the value an earlier rule set through `server.title`, `server.description` and `server.instructions`. `server.upstream` keeps what the server said.

## What the client sees

- Direct route: discover returns the changed title in `_meta["io.modelcontextprotocol/serverInfo"].title`, and the changed instructions if `settings.instructions` is on.
- Mux route: discover shows a slug-labelled excerpt of each server's changed instructions, within the excerpt budget. Every excerpt line starts with `> `. `get_server_instructions` returns the full changed text, unquoted.
- Search mode: when `settings.searchRoster` is on, ranking uses the changed title and description.

If an enforce rule fails to evaluate, the title, description and instructions are empty.

## Examples

### Remove a section from the instructions

```json
{
  "name": "Drop Confluence section",
  "hook": "server/discover.post",
  "when": "has(server.instructions)",
  "then": { "server.instructions": "regex.replace(server.instructions, '(?s)## Confluence.*', '')" }
}
```

The server's instructions `Use search first.\n## Confluence\nConfluence details.` become `Use search first.\n`. The `(?s)` flag lets `.` match line breaks. A server with no instructions is left alone.

### Retitle the server

```json
{
  "name": "Retitle",
  "hook": "server/discover.post",
  "when": "true",
  "then": { "server.title": "'Jira (read only)'" }
}
```

On the direct route, discover returns `_meta["io.modelcontextprotocol/serverInfo"]` as `{"name": "bifrost", "version": "<gateway version>", "title": "Jira (read only)"}`.

### Rewrite, then append a note

```json
[
  {
    "name": "Drop Confluence section",
    "hook": "server/discover.post",
    "when": "has(server.instructions)",
    "then": { "server.instructions": "regex.replace(server.instructions, '(?s)## Confluence.*', '')" }
  },
  {
    "name": "Ticket note",
    "hook": "server/discover.post",
    "when": "true",
    "then": { "server.instructions": "server.?instructions.orValue('') + 'All writes need a ticket ID.'" }
  }
]
```

The second rule reads the output of the first. On the mux route, `get_server_instructions` for this server returns `Use search first.\nAll writes need a ticket ID.`. The discover excerpt shows the same text under `## wiki`, quoted: `## wiki\n> Use search first.\n> All writes need a ticket ID.`

For a server with no instructions, the first rule does not match and the second rule sets the instructions to the note alone.

## Nuances and limits

- To build on earlier rules, read `server.instructions`. To start from the server's own text every time, read `server.upstream.instructions`.
- A plain read of `server.instructions` fails when the server gave none. The rule fails, and title, description and instructions are all empty.
- `server.upstream` exists only on this hook. On other hooks, `server.title`, `server.description` and `server.instructions` hold what the server said, and no rule can set them.
- A server's instructions name its own tools. After a rename, or with the mux name prefix, they can name tools that the client cannot see.
- Mux excerpts are limited to 600 characters for each server and 4,000 in total by default. A cut excerpt ends with a mark that points to `get_server_instructions`.
- The `> ` quoting stops a server's text from starting a heading, such as `## eng`, that poses as another server's section. It separates the sections visually. It does not make the text trustworthy: a model still reads whatever a server writes.

## Related pages

- [Hooks and rules overview](overview.md)
- [server.settings switches](server-settings.md)
