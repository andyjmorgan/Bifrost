# server/discover.post

`server/discover.post` changes a server's title, description and instructions before the client sees them.

## When it runs

`server/discover.post` runs after [server.settings](server-settings.md) whenever the gateway shows server details:

- discover on the direct route
- mux instruction excerpts in discover
- the `get_server_instructions` tool on the mux route
- the search roster, when the `searchRoster` switch is on

The gateway reads instructions from the upstream live, so a change upstream shows at once.

## Variables

| Variable | Type | Description |
| --- | --- | --- |
| `title` | string | The current title |
| `description` | string | The current description |
| `instructions` | string | The current instructions |
| `upstream` | map | `title`, `description` and `instructions` as the upstream sent them |
| `server`, `entitlement`, `caller`, `client`, `route`, `flags`, `now` | Various | The [common variables](overview.md#common-variables). `server.instructions` is the upstream text and does not change |

## Fields

| Field | Type | Effect |
| --- | --- | --- |
| `title` | string | The title on the direct route, and in search ranking when `searchRoster` is on |
| `description` | string | The server description. Search mode uses it to rank tools when `searchRoster` is on |
| `instructions` | string | The instructions on the direct route, in mux excerpts, through `get_server_instructions` and in the search roster |

No field stops evaluation. Changes chain: a later rule reads the value an earlier rule set through `title`, `description` and `instructions`.

## What the client sees

- Direct route: discover returns the modified title in `_meta["io.modelcontextprotocol/serverInfo"].title`, and the modified instructions if the `instructions` switch is on.
- Mux route: discover shows a slug-labelled excerpt of each server's modified instructions, within the excerpt budget. Every excerpt line starts with `> `. `get_server_instructions` returns the full modified text, unquoted.
- Search mode: when `searchRoster` is on, ranking uses the modified title and description.

If an enforce rule fails to evaluate, the title, description and instructions are empty.

## Nuances and limits

- To build on earlier rules, read `instructions`. To start from the upstream text every time, read `upstream.instructions` or `server.instructions`.
- Upstream instructions name upstream tools. After a rename, or with the mux name prefix, they can name tools that the client cannot see.
- Mux excerpts are limited to 600 characters per server and 4,000 in total by default. A cut excerpt ends with a mark that points to `get_server_instructions`.
- The `> ` quoting stops upstream text from starting a heading, such as `## eng`, that poses as another server's section. It separates the sections visually. It does not make upstream text trustworthy: a model still reads whatever an upstream writes.

## Examples

### Remove a section from the instructions

```json
{
  "name": "Drop Confluence section",
  "hook": "server/discover.post",
  "when": "true",
  "then": { "instructions": "regex.replace(instructions, '(?s)## Confluence.*', '')" }
}
```

Upstream instructions `Use search first.\n## Confluence\nConfluence details.` become `Use search first.\n`. The `(?s)` flag lets `.` match line breaks.

### Retitle the server

```json
{
  "name": "Retitle",
  "hook": "server/discover.post",
  "when": "true",
  "then": { "title": "'Jira (read only)'" }
}
```

On the direct route, discover returns `_meta["io.modelcontextprotocol/serverInfo"]` as `{"name": "bifrost", "version": "<gateway version>", "title": "Jira (read only)"}`.

### Rewrite, then append a note

```json
[
  {
    "name": "Drop Confluence section",
    "hook": "server/discover.post",
    "when": "true",
    "then": { "instructions": "regex.replace(instructions, '(?s)## Confluence.*', '')" }
  },
  {
    "name": "Ticket note",
    "hook": "server/discover.post",
    "when": "true",
    "then": { "instructions": "instructions + 'All writes need a ticket ID.'" }
  }
]
```

The second rule reads the output of the first. On the mux route, `get_server_instructions` for this server returns `Use search first.\nAll writes need a ticket ID.`. The discover excerpt shows the same text under `## wiki`, quoted: `## wiki\n> Use search first.\n> All writes need a ticket ID.`

## Related pages

- [Hooks and rules overview](overview.md)
- [server.settings switches](server-settings.md)
