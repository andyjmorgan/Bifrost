# Bifrost

One MCP connection to all of your MCP servers.

Bifrost is an MCP gateway. You register upstream MCP servers with their credentials, and agents connect to a single front door that fans out to all of them, injects the right credentials, caches what it learns, and audits every hop.

```
agent ──► https://bifrost.example/mcp ──┬──► obsidian   (OAuth, CIMD)
          (one API key or OIDC token)   ├──► telegram   (X-Api-Key)
                                        ├──► websearch  (no auth)
                                        └──► …          (Basic, Bearer, …)
```

## What it does

- **Front door** on the Model Context Protocol, 2026-07-28 only. `/mcp` multiplexes every registered server (tool names are `slug__tool`); `/{slug}/mcp` is one server, one-to-one. Downstream it speaks whatever version each server negotiates, from 2024-11-05 up.
- **Credential registry** with three kinds: OAuth 2.1 (discovery from `WWW-Authenticate`, protected-resource and authorization-server metadata, client registration by Client ID Metadata Document first and Dynamic Client Registration as fallback, PKCE, refresh), HTTP Basic, and a static header such as `X-Api-Key` or `Authorization: Bearer`. Secrets are sealed with AES-256-GCM in Postgres.
- **Credential middleware**: an `http.RoundTripper` chain on the upstream client adds the credential, refreshes OAuth tokens near expiry or on a 401, and marks a credential broken only when the token endpoint definitively rejects it. A background worker refreshes tokens that will expire before its next pass.
- **Search mode**: append `?search=true` and the front door offers four small tools instead of every upstream tool: `tool_search` (BM25 over names, descriptions and schemas, with the connected servers described up front), `list_tools`, `get_tool_definition`, `execute_tool`, plus `refresh_services`. An agent's context stops scaling with the number of servers behind the gateway.
- **Cache in Postgres**, not Redis: each server's tools and initialize result live in a TTL table, swept by pg_cron and the worker, hidden at read time by a row-level-security policy. Concurrent misses for one server share a single upstream fetch per process, no connection is held while the upstream is in flight, and a write only lands if the server has not been edited since the fetch started. Edits and credential changes refetch and overwrite the row instead of deleting it; disabling a server or a credential going pending or broken clears it. Ping, the Explorer and `refresh_services` bypass and rewrite the cache.
- **Audit everything**: inbound and upstream hops with full JSON-RPC bodies, auth headers redacted, 30-day retention. The UI shows each exchange with JSON and SSE viewers, and each request as a trace: a flame chart of every upstream connection under the inbound span.
- **Progress**: fan-outs report MCP progress notifications per server stage when the client sends a progress token.
- **Web UI** for servers, an Explorer that lists and calls tools through the real front door as you, API keys, and the audit log. Sign-in is OIDC with PKCE.

## Routes

| Route | Purpose |
|---|---|
| `POST /mcp` | all servers, tools named `slug__tool` |
| `POST /{slug}/mcp` | one server, unprefixed tool names |
| `…?search=true` | search mode on either route |
| `GET /.well-known/oauth-protected-resource` | RFC 9728 metadata for the front door |
| `GET /.well-known/mcp-client-metadata.json` | the gateway's own CIMD document for upstream registration |
| `GET /oauth/callback` | consent callback for upstream OAuth |
| `/api/v1/*` | REST API: servers, credentials, API keys, audit, e2e |
| `/` | the SPA |

Front-door callers authenticate with an OIDC bearer token (audience and realm-role checked) or a gateway API key. API keys carry scopes: `api:read`, `api:edit`, `mcp:execute`, `mcp:edit`.

## Running it

One container: the Go binary serves the API, the front door, the worker and the built SPA.

```sh
./build.sh                       # builds web/, the Go binary and the image; tags with the short SHA
MCPGW_DSN='postgres://…' MCPGW_MASTER_KEY="$(head -c 32 /dev/urandom | base64 -w0)" ./gateway
./gateway create-key -name ops   # bootstrap an API key (prints it once)
```

Postgres needs pg_cron for the cache sweep in production; without it the worker's sweep is the only reaper, which is fine for development. A Dockerfile for `postgres:17` plus `postgresql-17-cron` is small enough to live next to your manifests.

### Configuration

| Variable | Default | Meaning |
|---|---|---|
| `MCPGW_LISTEN_ADDR` | `:8080` | |
| `MCPGW_DSN` | | Postgres connection string |
| `MCPGW_MASTER_KEY` | | base64, 32 bytes; seals credentials and API key plaintext |
| `MCPGW_PUBLIC_BASE_URL` | | external origin; used for CIMD, callbacks and audiences |
| `MCPGW_OIDC_ISSUER` | unset = dev mode | OIDC issuer for the UI and MCP bearer tokens |
| `MCPGW_OIDC_WEB_CLIENT_ID` | `mcp-gateway-web` | SPA client id |
| `MCPGW_OIDC_REQUIRED_ROLE` | `mcp-gateway-operator` | realm role required to sign in |
| `MCPGW_OWNER_ID` | `owner` | single-tenant owner every OIDC login maps to |
| `MCPGW_TOOLS_CACHE_TTL` | `5m` | tools and initialize cache lifetime; `0` disables |
| `MCPGW_REFRESH_INTERVAL` | `4h` | worker cadence |
| `MCPGW_REFRESH_LEEWAY` | `10m` | refresh tokens expiring within this window (capped at half the token lifetime) |
| `MCPGW_AUDIT_RETENTION` | `720h` | |
| `MCPGW_UPSTREAM_TIMEOUT` | `60s` | |

Dev mode (no `MCPGW_OIDC_ISSUER`) treats every request as the owner, so the UI and API work without a login.

## Registering servers

```sh
curl -X POST $BASE/api/v1/servers -H "Authorization: Bearer $KEY" -H 'Content-Type: application/json' -d '{
  "name": "Web Search", "url": "https://search.example/mcp",
  "description": "Search the web. Use it to find pages when you do not have a URL.",
  "auth": {"kind": "header", "headerName": "X-Api-Key", "headerValue": "…"}
}'
```

The slug is derived from the name (lowercase `a-z0-9`, 12 characters, collisions bump the tail) and never changes. For `auth.kind: oauth` the response carries an `authorizeUrl` to open in a browser; the gateway handles the callback. Write descriptions as capabilities, because search mode shows them to the model.

## Policies

A policy is a list of rules. Every tool starts visible, or hidden when the policy is inverted, which makes it an allow-list. Rules run top to bottom; a rule whose `when` is true applies its `then` assignments before the next rule looks at the tool.

```json
{
  "name": "guard",
  "rules": [
    {"name": "hide destructive", "when": "tool.annotations.destructiveHint && !tool.annotations.readOnlyHint", "then": {"visible": "false"}},
    {"name": "confirm sends", "when": "tool.baseName.matches(\"^send_\")", "then": {"confirm": "true", "confirmMessage": "\"Send via \" + server.slug + \"?\""}},
    {"name": "mark reads", "when": "tool.baseName.matches(\"^(get|list|read)_\")", "then": {"annotations.readOnlyHint": "true"}}
  ]
}
```

Rules see `tool` (name as `slug__name` on every route, baseName as the upstream sends it, title, description, annotations with defaults, annotationsRaw, inputSchema, outputSchema, icons, meta, visible, confirm), `server` (slug, name, description, instructions, protocolVersion, url) and `route`. Assignable fields: `visible`, `confirm`, `confirmMessage`, `name`, `title`, `description` and the four annotation hints. Values are CEL expressions, so a literal is `true` or `"text"` and a template is `tool.description + " (read-only)"`.

```sh
curl -X PUT $BASE/api/v1/bindings/mux -H "Authorization: Bearer $KEY" -d '{"policyIds":["…"]}'       # global route
curl -X PUT $BASE/api/v1/bindings/telegram -H "Authorization: Bearer $KEY" -d '{"policyIds":["…"]}'  # one server
```

## Design notes

- Connections are opened per call. There is no session cache yet; the init cache removes the discovery probe and the tools cache removes most listing traffic, which covers the common case.
- The front door accepts only 2026-07-28. Clients on older SDKs are rejected with a clear error.
- Single tenant by design. Multi-tenancy would mean removing the owner mapping and scoping the cache and audit by owner, both of which are already keyed that way.
- No Redis, no message bus, no distributed locks. Postgres does the cache and the queue-shaped work; in-process goroutines do the async writes and collapse duplicate refills. Ten replicas facing a cold key cost ten upstream listings, not one per request, and the conditional write makes the duplicates harmless.

## Layout

```
cmd/gateway          main, create-key
internal/gateway     front door: mux, direct, search mode, progress
internal/upstream    connector, credential transport, refresh
internal/oauthclient discovery, CIMD/DCR, PKCE, token exchange
internal/auth        OIDC, API keys, scopes, RFC 9728 challenge
internal/audit       recorder, inbound middleware, upstream transport
internal/cache       async cache writer
internal/toolsearch  BM25
internal/store       Postgres
internal/db          pool and embedded migrations
web/                 React, Vite, Tailwind 4
```

## License

MIT.
