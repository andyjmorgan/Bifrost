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

- **Front door** on the Model Context Protocol, 2026-07-28 only. `/mcp` multiplexes every entitlement the caller holds (tool names are `slug__tool`); `/{slug}/mcp` is one entitlement, unprefixed. Downstream it speaks whatever version each server negotiates, from 2024-11-05 up.
- **Entitlements** are the only way to a server. Each is a named view of one server under its own slug with allow lists for tools, prompts, resources and templates. The same lists decide what is listed and what can be called, so a refused call looks like a call to something that does not exist. A server can carry several entitlements (`atlassian` and `confluence-read` on one credential) and can be marked exclusive, in which case a caller holding two of its entitlements gets neither. Grants give a user or a role an entitlement; a caller holds the union on `/mcp`, narrowed with `?slugs=`.
- **Credential registry** with three kinds: OAuth 2.1 (discovery from `WWW-Authenticate`, protected-resource and authorization-server metadata, client registration by Client ID Metadata Document first and Dynamic Client Registration as fallback, PKCE, refresh), HTTP Basic, and a static header such as `X-Api-Key` or `Authorization: Bearer`. Secrets are sealed with AES-256-GCM in Postgres.
- **Credential middleware**: an `http.RoundTripper` chain on the upstream client adds the credential, refreshes OAuth tokens near expiry or on a 401, and marks a credential broken only when the token endpoint definitively rejects it. A background worker refreshes tokens that will expire before its next pass.
- **Search mode**: append `?search=true` and the front door offers four small tools instead of every upstream tool: `tool_search` (BM25 over names, descriptions and schemas, with the connected servers described up front), `list_tools`, `get_tool_definition`, `execute_tool`, plus `refresh_services`. An agent's context stops scaling with the number of servers behind the gateway. The gateway's own tools declare an `outputSchema` and return the same JSON as `structuredContent` and as the text block, as the spec asks.
- **Cache in Postgres**, not Redis: each server's tools and initialize result live in a TTL table, swept by pg_cron and the worker, hidden at read time by a row-level-security policy. Concurrent misses for one server share a single upstream fetch per process, no connection is held while the upstream is in flight, and a write only lands if the server has not been edited since the fetch started. Edits and credential changes refetch and overwrite the row instead of deleting it; disabling a server or a credential going pending or broken clears it. Ping, the Explorer and `refresh_services` bypass and rewrite the cache.
- **Audit everything**: inbound and upstream hops with full JSON-RPC bodies, auth headers redacted, 30-day retention. The UI shows each exchange with JSON and SSE viewers, and each request as a trace: a flame chart of every upstream connection under the inbound span.
- **Progress**: fan-outs report MCP progress notifications per server stage when the client sends a progress token.
- **Orgs**: every user gets a personal org at first sign-in and is its admin. An admin mints an invite (a magic link, single use, seven days); a signed-in user who opens it joins that org and it becomes their active org, their personal org staying behind until they leave. Admins manage servers, entitlements, members and invites; members see the entitlements they hold, use the Explorer on them and hold their own API keys, and see only their own audit events. A key acts as the user who created it, inherits their role, and dies when they leave the org.
- **Web UI** for servers, an Explorer that lists and calls tools through the real front door as you, API keys, members and invites, and the audit log. Sign-in is OIDC with PKCE.

## Routes

| Route | Purpose |
|---|---|
| `POST /mcp` | every entitlement the caller holds, tools named `slug__tool` |
| `POST /{slug}/mcp` | one entitlement, unprefixed tool names |
| `…?search=true` | search mode on either route |
| `GET /.well-known/oauth-protected-resource` | RFC 9728 metadata for the front door |
| `GET /.well-known/mcp-client-metadata.json` | the gateway's own CIMD document for upstream registration |
| `GET /oauth/callback` | consent callback for upstream OAuth |
| `/api/v1/*` | REST API: servers, entitlements, ceiling, org, API keys, audit, e2e |
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
| `MCPGW_OWNER_ID` | `owner` | the pre-org owner id whose records were migrated into the first org |
| `MCPGW_ADMIN_EMAIL` | unset | the human who claims that migrated org as their personal org at first sign-in |
| `MCPGW_TOOLS_CACHE_TTL` | `5m` | tools and initialize cache lifetime; `0` disables |
| `MCPGW_REFRESH_INTERVAL` | `4h` | worker cadence |
| `MCPGW_REFRESH_LEEWAY` | `10m` | refresh tokens expiring within this window (capped at half the token lifetime) |
| `MCPGW_AUDIT_RETENTION` | `720h` | |
| `MCPGW_UPSTREAM_TIMEOUT` | `60s` | |

Dev mode (no `MCPGW_OIDC_ISSUER`) treats every request as the user `dev`, admin of its own org, so the UI and API work without a login. An `X-Dev-User: <name>` header makes the request come from another local user, which is enough to exercise invites, joining and leaving.

## Registering servers

```sh
curl -X POST $BASE/api/v1/servers -H "Authorization: Bearer $KEY" -H 'Content-Type: application/json' -d '{
  "name": "Web Search", "url": "https://search.example/mcp",
  "description": "Search the web. Use it to find pages when you do not have a URL.",
  "auth": {"kind": "header", "headerName": "X-Api-Key", "headerValue": "…"}
}'
```

A new server gets a default entitlement, everything it offers, held by the org's admins, under a slug derived from the name (lowercase `a-z0-9-`, 24 characters, collisions bump the tail). Slugs never change. For `auth.kind: oauth` the response carries an `authorizeUrl` to open in a browser; the gateway handles the callback. Write descriptions as capabilities, because search mode shows them to the model.

## Entitlements

An entitlement is a named view of one server under its own slug, with allow lists per kind and grants. A list present means only what it names; a list absent means everything of that kind. Resource entries are URI patterns (`*` within a segment, `**` as a prefix). The same lists decide what is listed and what can be called.

```sh
curl -X POST $BASE/api/v1/servers/$ID/entitlements -H "Authorization: Bearer $KEY" -H 'Content-Type: application/json' -d '{
  "name": "Confluence read", "slug": "confluence-read",
  "selection": {"tools": ["search_pages", "get_page"], "resources": ["confluence://spaces/**"]}
}'
curl -X PUT $BASE/api/v1/entitlements/$EID/grants -H "Authorization: Bearer $KEY" -H 'Content-Type: application/json' -d '{
  "grants": [{"subjectType": "role", "subjectId": "member"}, {"subjectType": "user", "subjectId": "<keycloak subject>"}]
}'
```

`/mcp` is the union of every entitlement the caller holds; `/mcp?slugs=a,b` narrows it to named slugs, and a slug the caller does not hold is an error. A server marked exclusive refuses a caller holding two of its entitlements. What a server has been seen to offer is recorded under `/servers/{id}/capabilities` as governance data for the editor and the server page; it never filters. Advanced CEL rules will run after the allow lists, in order, and may enable or disable; `policies` on the entitlement is reserved for them.

## Design notes

- Connections are opened per call. There is no session cache yet; the init cache removes the discovery probe and the tools cache removes most listing traffic, which covers the common case.
- The front door accepts only 2026-07-28. Clients on older SDKs are rejected with a clear error.
- Tenancy is the org. Every record is keyed by org id; a user's active org is a column on the user, so switching orgs is a write, not a session. A user belongs to at most a handful of orgs, so no org switcher state lives in the token.
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
