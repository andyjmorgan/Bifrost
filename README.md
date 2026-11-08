# mcp-gateway

A single-tenant MCP gateway. You register downstream MCP servers (each with its own credential), and the
gateway exposes them to MCP clients through one front door that speaks only the 2026-07-28 protocol:

- `POST /mcp` multiplexes every registered server. Tools are named `<slug>__<tool>`; a `tools/call` opens
  only that server's connection.
- `POST /{slug}/mcp` is a 1:1 pass-through to one server.

Downstream, the gateway speaks whatever version the server offers (2024-11-05 through 2026-07-28, HTTP
only, never stdio). Nothing is cached yet: every request opens the upstream connection on demand.

## Credentials

Each server carries one credential of kind `none`, `basic`, `header` (any header name and value, which
covers `X-Api-Key` and static bearer tokens) or `oauth`. Secret material is sealed with AES-256-GCM
under `MCPGW_MASTER_KEY` and bound to the server id.

The credential is applied by an `http.RoundTripper` middleware on the downstream client
(`internal/upstream`). For OAuth it refreshes an expiring token inline before the call, retries once
after a 401 following a refresh, and marks the credential `broken` when the refresh fails. A worker
(`internal/worker`) also refreshes anything due to expire before its next 4-hourly pass.

Adding an OAuth server runs discovery (`WWW-Authenticate` → protected resource metadata → authorization
server metadata), registers the gateway as a client (CIMD using `/.well-known/mcp-client-metadata.json`
when the server advertises it, otherwise RFC 7591 dynamic registration), requests the scopes the server
asked for, and hands the UI a consent URL. The callback at `/oauth/callback` stores the token set and
scopes on the credential.

## Slugs

A slug is the server's name lowercased and reduced to `[a-z0-9]`, at most 12 characters, unique per
owner and immutable. On collision the tail is overwritten with a counter: `atlassian`, `atlassia1`,
`atlassi11`. Renaming the server never changes it.

## Front-door auth

- Keycloak (`Agents` realm) access tokens whose `aud` contains the SPA client id or the gateway's
  `/mcp` URL and that carry the realm role `mcp-gateway-operator`.
- Gateway API keys (`mcpg_…`) with scopes `api:read`, `api:edit` (manage keys), `mcp:edit` (manage
  servers and credentials), `mcp:execute` (call `/mcp`, `/{slug}/mcp`, ping, e2e).

Unauthenticated MCP requests get a 401 with an RFC 9728 `resource_metadata` pointer so CIMD/DCR
clients can onboard themselves.

## Audit

Every inbound and upstream HTTP exchange is recorded (`internal/audit`): actor, key, slug, route,
JSON-RPC method and tool, status, outcome, bytes, duration, plus redacted headers and bodies. Upstream
events link to their inbound parent through `requestId`. Retention is 30 days.

## Run it

```bash
docker run -d --name mcpgw-pg -e POSTGRES_PASSWORD=dev -e POSTGRES_USER=mcpgw -e POSTGRES_DB=mcpgw -p 15432:5432 postgres:17
export MCPGW_DSN='postgres://mcpgw:dev@127.0.0.1:15432/mcpgw?sslmode=disable'
export MCPGW_MASTER_KEY=$(head -c 32 /dev/urandom | base64 -w0)
go run ./cmd/gateway            # dev mode: no MCPGW_OIDC_ISSUER → every request is owner "dev"
cd web && npm install && npm run dev   # SPA on :5173, proxied to :8080
```

| Env | Meaning |
|---|---|
| `MCPGW_LISTEN_ADDR` | default `:8080` |
| `MCPGW_DSN` | Postgres DSN (required) |
| `MCPGW_MASTER_KEY` | base64 of 32 random bytes (required) |
| `MCPGW_PUBLIC_BASE_URL` | external base URL; builds MCP URLs, the CIMD document and the OAuth callback |
| `MCPGW_WEBROOT` | directory with the built SPA |
| `MCPGW_OIDC_ISSUER` | Keycloak realm issuer; unset = dev mode |
| `MCPGW_OIDC_WEB_CLIENT_ID` | SPA client id (default `mcp-gateway-web`) |
| `MCPGW_OIDC_REQUIRED_ROLE` | role gate (default `mcp-gateway-operator`) |
| `MCPGW_OIDC_SCOPE` | scope advertised to MCP clients (default `mcp:gateway`) |
| `MCPGW_REFRESH_INTERVAL` / `MCPGW_REFRESH_LEEWAY` | worker cadence (4h) and early-refresh window (10m) |
| `MCPGW_AUDIT_RETENTION` | default 720h |
| `MCPGW_UPSTREAM_TIMEOUT` | per-request upstream timeout (60s) |

`./build.sh` builds and pushes the image to the lab registry. Deployment manifests live in the
DonkeyWork-Lab repo under `clusters/attic/applications/mcp-gateway/`.

## Layout

| Package | Role |
|---|---|
| `cmd/gateway` | wiring |
| `internal/config` | env config |
| `internal/db` | pgx pool + embedded migrations |
| `internal/store` | records and the Postgres store |
| `internal/crypto` | AES-GCM sealing |
| `internal/slug` | slug derivation |
| `internal/auth` | callers, API keys, Keycloak OIDC, front-door middleware, PRM |
| `internal/oauthclient` | discovery, CIMD/DCR registration, PKCE, exchange, refresh |
| `internal/upstream` | credential + audit RoundTrippers, MCP client sessions |
| `internal/gateway` | the MCP front door (mux and direct) |
| `internal/audit` | recorder, inbound middleware, upstream transport |
| `internal/httpapi` | REST API, OAuth callback, e2e driver, SPA |
| `internal/worker` | refresh and retention |
| `web` | React SPA |
