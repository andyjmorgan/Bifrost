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

- **Front door** on the Model Context Protocol, 2026-07-28 by default. Older clients can be let in with `MCPGW_LEGACY_PROTOCOL_VERSIONS` (comma separated, for example `2025-11-25,2025-06-18,2025-03-26`); without it an older client's `initialize` is answered and its next request is refused with a 400, so the handshake looks fine and nothing works. `/mcp` multiplexes every server the caller holds (tool names are `slug__tool`); `/{slug}/mcp` is one server, unprefixed. Downstream it speaks whatever version each server negotiates, from 2024-11-05 up.
- **Servers own their access**: each server has its slug, allow lists for tools, prompts, resources and templates, rules on 19 hooks, and grants to users. The same lists decide what is listed and what can be called, so a refused call looks like a call to something that does not exist. A caller holds the union of their servers on `/mcp`, narrowed with `?slugs=`.
- **Credentials in the keyring**: a server authenticates upstream with OAuth 2.1 (discovery from `WWW-Authenticate`, protected-resource and authorization-server metadata, client registration by Client ID Metadata Document first and Dynamic Client Registration as fallback, PKCE, the `resource` parameter, refresh), HTTP Basic, a static header such as `X-Api-Key` or `Authorization: Bearer`, or a credential lent from a person's own keyring. Every one of them is held in the keyring, sealed with its envelope cipher; the gateway stores a reference and no secret. See `docs/keyring-credentials.md`.
- **Credential middleware**: an `http.RoundTripper` chain on the upstream client asks the keyring for the credential once for each forwarded request. The keyring refreshes an OAuth token near expiry or after a 401, and marks the account broken only when the token endpoint definitively rejects the refresh. There is no background refresh.
- **Search mode**: append `?search=true` and the front door offers four small tools instead of every upstream tool: `tool_search` (BM25 over names, descriptions and schemas, with the connected servers described up front), `list_tools`, `get_tool_definition`, `execute_tool`, plus `refresh_services`. An agent's context stops scaling with the number of servers behind the gateway. The gateway's own tools declare an `outputSchema` and return the same JSON as `structuredContent` and as the text block, as the spec asks.
- **Cache in Postgres**, not Redis: each server's tools and initialize result live in a TTL table, swept by pg_cron and the worker, hidden at read time by a row-level-security policy. Concurrent misses for one server share a single upstream fetch per process, no connection is held while the upstream is in flight, and a write only lands if the server has not been edited since the fetch started. Edits and credential changes refetch and overwrite the row instead of deleting it; disabling a server or a credential going pending or broken clears it. Ping, the Explorer and `refresh_services` bypass and rewrite the cache.
- **Audit everything**: inbound and upstream hops with full JSON-RPC bodies, auth headers redacted, 30-day retention. The UI shows each exchange with JSON and SSE viewers, and each request as a trace: a flame chart of every upstream connection under the inbound span.
- **Progress**: fan-outs report MCP progress notifications per server stage when the client sends a progress token.
- **One tenant, no roles**: everyone the identity provider lets in can run the gateway: servers, their rules and grants, every API key and the whole audit trail. What a person reaches through the MCP routes is decided by grants alone, one user on one server. A key acts as the user who created it and holds what they hold.
- **Web UI** for servers, an Explorer that lists and calls tools through the real front door as you, API keys, and the audit log. Sign-in is OIDC with PKCE.

## Routes

| Route | Purpose |
|---|---|
| `POST /mcp` | every server the caller holds, tools named `slug__tool` |
| `POST /{slug}/mcp` | one server, unprefixed tool names |
| `…?search=true` | search mode on either route |
| `GET /.well-known/oauth-protected-resource` | RFC 9728 metadata for the front door |
| `GET /.well-known/mcp-client-metadata.json` | the gateway's own CIMD document for upstream registration |
| `GET /oauth/callback` | consent callback for upstream OAuth |
| `/api/v1/*` | REST API: servers with their rules and grants, the portal, users, API keys, audit, e2e |
| `/keyring/api/*` | the keyring's API, when the keyring is on (see [Keyring](#keyring)) |
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
| `MCPGW_MASTER_KEY` | | base64, 32 bytes; seals API key plaintext, and opens the credentials the gateway sealed before they moved to the keyring (`gateway migrate-credentials`) |
| `MCPGW_PUBLIC_BASE_URL` | | external origin; used for CIMD, callbacks and audiences |
| `MCPGW_OIDC_ISSUER` | unset = dev mode | OIDC issuer for the UI and MCP bearer tokens |
| `MCPGW_OIDC_WEB_CLIENT_ID` | `mcp-gateway-web` | SPA client id |
| `MCPGW_OIDC_REQUIRED_ROLE` | `mcp-gateway-operator` | realm role required to sign in |
| `MCPGW_TOOLS_CACHE_TTL` | `5m` | tools and initialize cache lifetime; `0` disables |
| `MCPGW_REFRESH_INTERVAL` | `4h` | worker cadence: the cache sweep and the audit retention purge. The keyring refreshes OAuth tokens, not the worker |
| `MCPGW_REFRESH_LEEWAY` | `10m` | no longer used; still read, so a deployment that sets it keeps starting |
| `MCPGW_AUDIT_RETENTION` | `720h` | |
| `MCPGW_UPSTREAM_TIMEOUT` | `60s` | |
| `MCPGW_RULE_HEADERS` | empty | HTTP request headers rules may read as `request.headers`, comma separated. Headers that carry credentials are never shown |

Dev mode (no `MCPGW_OIDC_ISSUER`) treats every request as the user `dev` (`MCPGW_DEV_OWNER`), so the UI and API work without a login. An `X-Dev-User: <name>` header makes the request come from another local user, which is enough to try grants with two people.

## Registering servers

```sh
curl -X POST $BASE/api/v1/servers -H "Authorization: Bearer $KEY" -H 'Content-Type: application/json' -d '{
  "name": "Web Search", "url": "https://search.example/mcp",
  "description": "Search the web. Use it to find pages when you do not have a URL.",
  "auth": {"kind": "header", "headerName": "X-Api-Key", "headerValue": "…"}
}'
```

A new server offers everything it has and is held by the person who added it. Its slug is the one given, or one derived from the name (lowercase `a-z0-9-`, 24 characters, collisions bump the tail). Slugs never change. For `auth.kind: oauth` the response carries an `authorizeUrl` to open in a browser; the gateway handles the callback. Write descriptions as capabilities, because search mode shows them to the model.

`auth.kind` is one of `none`, `oauth`, `basic`, `header` and, when the keyring is enabled, `keyring_oauth` and `keyring_secret`: a credential from the caller's own keyring, which the gateway fetches for each request and never stores. See [Servers that use a keyring credential](#servers-that-use-a-keyring-credential).

## Access and rules

A server's allow lists say what of its catalog exists for callers. A list present means only what it names; a list absent means everything of that kind. Resource entries are URI patterns (`*` within a segment, `**` as a prefix). Its grants say who holds it: one user per grant.

```sh
curl -X PATCH $BASE/api/v1/servers/$ID -H "Authorization: Bearer $KEY" -H 'Content-Type: application/json' -d '{
  "selection": {"tools": ["search_pages", "get_page"], "resources": ["confluence://spaces/**"]}
}'
curl -X PUT $BASE/api/v1/servers/$ID/grants -H "Authorization: Bearer $KEY" -H 'Content-Type: application/json' -d '{
  "grants": [{"userId": "<keycloak subject>"}, {"userId": "<another subject>"}]
}'
```

After the allow lists come the server's rules, written as text, one block per hook, and sent as `hooks` on the same `PATCH`. A rule can hide an item, refuse a request, ask the user to confirm, or change what its hook lets it change. A group of callers that needs different rules gets them from a rule that reads `request.caller`. Rules that do not compile fail the server closed. The rule language is in [docs/rules/hooks/overview.md](docs/rules/hooks/overview.md), and the engine's handover page is [docs/rules/handover.md](docs/rules/handover.md).

```text
when tool.name.startsWith('delete_') && !(request.caller.?email.orValue('') in ['sre-lead@example.com'])
  deny "Deleting is restricted."
```

`/mcp` is the union of every server the caller holds; `/mcp?slugs=a,b` narrows it to named slugs, and a slug the caller does not hold is an error. What a server has been seen to offer, with the definition it sent, is recorded under `/servers/{id}/capabilities` as governance data for the editor and the server page; it never filters.

## Keyring

The keyring is the DonkeyWork Vault server, embedded: stored secrets, OAuth accounts and their provider apps, `dwv_` access keys and their audit trail. It keeps Vault's Postgres schema (`vault`), its `DWV1` envelope format and its KEKs, so rows written by Vault are read as they are, with no re-encryption. Every row keeps its `user_id` and `tenant_id`.

It is off by default. It turns on when its KEKs are configured, and with no keyring variable set the gateway runs exactly as it does without the module: no route, no schema, no log line.

### Where it answers

One handler, mounted twice. It is checked before the gateway's own router, because both define `/api/v1/api-keys`, `/api/v1/audit` and `/api/v1/me`, with different meanings.

| Request | Served by |
|---|---|
| `/keyring/api/…`, `/keyring/healthz`, any host | the keyring, prefix removed: `/keyring/api/v1/api-keys`, `/keyring/api/config`, `/keyring/api/oauth/callback` |
| `/api/…`, `/healthz` on a legacy host | the keyring at Vault's original paths. Nothing under `/api` on a legacy host reaches the gateway |
| any other `GET` or `HEAD` on a legacy host | `302` to the gateway's UI |
| anything else | the gateway |

A legacy host is one listed in `MCPGW_KEYRING_LEGACY_HOSTS`, matched against the request's `Host` header. An entry is `host` (any port), `host:port`, or `:port` (any host on that port, for a NodePort).

API calls on a legacy host are served, never redirected: an HTTP client drops the `Authorization` header when a redirect changes host, so every existing `dwvault` binary would fail. Only page loads are redirected, by this map, with the query string carried over. The root and anything not listed go to `/secrets`.

| Vault page | Gateway UI |
|---|---|
| `/credentials` | `/secrets` |
| `/providers` | `/providers` |
| `/oauthconnect` | `/connect` |
| `/profile` | `/profile` |

The keyring serves no UI of its own. Its pages are part of the gateway's SPA.

### OAuth flows have two addresses

- The **redirect URI** sent to a provider is `MCPGW_KEYRING_PUBLIC_BASE_URL` + `/api/oauth/callback`. It must be the address registered with the provider's app. It defaults to `<MCPGW_PUBLIC_BASE_URL>/keyring`; a deployment that took over a Vault sets it to the old Vault origin and keeps that host in `MCPGW_KEYRING_LEGACY_HOSTS`, so no provider app has to change.
- The **page the browser ends on** after the callback is `MCPGW_KEYRING_UI_BASE_URL` + `/connect`, with `?connected=<provider>` or `?oauth_error=<reason>`. It defaults to `MCPGW_PUBLIC_BASE_URL`.

Neither depends on the other, or on the host the callback arrived on.

### Who may call it

The keyring keeps Vault's authentication and Vault's scopes (`vault:read`, `vault:readwrite`, `vault:audit`). The gateway's `mcpg_` keys and its role check do not apply to it.

| Caller | Presents | Scopes |
|---|---|---|
| a script or service | a `dwv_` access key, as `X-Api-Key` or `Authorization: Bearer` | the key's own |
| the CLI | an OIDC token whose `azp` is the CLI client and whose `aud` includes the keyring's web client | read, readwrite |
| a browser | an OIDC token whose `azp` is the keyring's web client, or the gateway's (`MCPGW_OIDC_WEB_CLIENT_ID`) | read, readwrite, audit |

The gateway's client is accepted only when the keyring and the gateway verify against the same issuer, which is the default: the keyring's authority falls back to `MCPGW_OIDC_ISSUER`. Tokens are checked for signature, issuer and expiry; the client is read from `azp`. Rows are owned by the token's `sub`, which must be a UUID, so one person is the same owner through any client.

### Servers that use a keyring credential

A server can authenticate upstream with a keyring credential instead of one the gateway stores. The gateway keeps the reference and asks the keyring, in process, for each forwarded request.

```sh
# a connected OAuth account, sent as a bearer token and refreshed by the keyring
{"kind": "keyring_oauth", "keyringProvider": "x", "keyringAccount": "alice_dev"}
# a stored secret, sent in the header it describes (or as HTTP Basic with its username)
{"kind": "keyring_secret", "keyringSecretName": "atlas-api"}
```

- The reference belongs to the person who set it and resolves only in their keyring. They must be signed in: an `mcpg_` API key cannot set one, and only the owner can change the URL of a server that uses one.
- A 401 from the server makes the keyring refresh the OAuth token once, and the request is retried once.
- Nothing is cached beyond one forwarded request, so a changed or deleted keyring credential takes effect on the next one.
- Each fetch is in the keyring's audit trail with transport `gateway` and the server as its source. The servers API returns the names (`keyringOwner`, `keyringProvider`, `keyringAccount`, `keyringSecretName`), never a value.
- With the keyring off the API refuses both kinds, and a server that holds one fails to connect with an error that says so.

The OAuth accounts page also lists the OAuth connections the gateway holds for its MCP servers, tagged `MCP server`. Those are read from the servers API and cannot be revealed.

The gateway's own credential store is still there. [docs/keyring-credentials.md](docs/keyring-credentials.md) has the detail and what moving it onto the keyring would take.

### Configuration

Every setting is read as `MCPGW_KEYRING_<NAME>` first and `VAULT_<NAME>` second, so the variables a Vault deployment carries can be given to the gateway unchanged.

| Variable | Default | Meaning |
|---|---|---|
| `MCPGW_KEYRING_CRYPTO_KEKS` | unset = keyring off | `id=base64,…`, each key 32 bytes |
| `MCPGW_KEYRING_CRYPTO_ACTIVE_KEK_ID` | | the KEK that wraps new data keys; must be set with the KEKs |
| `MCPGW_KEYRING_DSN` | `MCPGW_DSN` | database holding schema `vault`. The keyring opens its own pool |
| `MCPGW_KEYRING_RUN_MIGRATIONS` | `true` | apply the keyring's baseline at startup |
| `MCPGW_KEYRING_PUBLIC_BASE_URL` | `<MCPGW_PUBLIC_BASE_URL>/keyring` | base of the OAuth redirect URI |
| `MCPGW_KEYRING_UI_BASE_URL` | `MCPGW_PUBLIC_BASE_URL` | where browsers are sent |
| `MCPGW_KEYRING_LEGACY_HOSTS` | empty | hosts served at Vault's original paths, comma separated |
| `MCPGW_KEYRING_OIDC_AUTHORITY` | `MCPGW_OIDC_ISSUER` | issuer of the tokens the keyring accepts; empty leaves access keys only |
| `MCPGW_KEYRING_OIDC_INTERNAL_AUTHORITY` | | in-cluster address for discovery, when it differs |
| `MCPGW_KEYRING_OIDC_WEB_CLIENT_ID` | `MCPGW_OIDC_WEB_CLIENT_ID` | the keyring's web client. Also read from `…_OIDC_CLIENT_ID` and `…_OIDC_AUDIENCE` |
| `MCPGW_KEYRING_OIDC_CLI_CLIENT_ID` | `donkeywork-vault-cli` | the CLI's client |
| `MCPGW_KEYRING_OIDC_WEB_SCOPES`, `…_OIDC_CLI_SCOPES`, `…_OIDC_SCOPES` | | scopes advertised to clients in `/api/config` |
| `MCPGW_KEYRING_OIDC_REQUIRE_HTTPS` | `true` | refuse an authority that is not https |
| `MCPGW_KEYRING_TRUSTED_PROXIES` | k3s pod and service ranges, loopback | peers whose `X-Forwarded-For` is believed for the audit trail |
| `MCPGW_KEYRING_AUDIT_RETENTION_DAYS` | `180` | |
| `MCPGW_KEYRING_AUDIT_CHANNEL_CAPACITY`, `…_AUDIT_BATCH_SIZE`, `…_AUDIT_FLUSH_MS`, `…_AUDIT_SWEEP_HOURS`, `…_AUDIT_RETENTION_BATCH` | `8192`, `100`, `500`, `12`, `5000` | audit writer and sweeper tuning |

A keyring with only one of the two KEK variables set, or with a UI base on a legacy host, stops the gateway at startup with an error that names the variable.

### Database

The keyring's migrations are its own: one idempotent baseline, recorded in `vault.schema_migrations`, applied under an advisory lock. They never touch the gateway's numbered migrations or its `schema_migrations` table, and both can live in one database.

### Telemetry

Service spans and the `vault.*` counters are kept as calls to the OpenTelemetry API. They are no-ops until the process installs a provider, and the keyring installs none. Vault's OTLP exporters, its log bridge and the `otelhttp` and `otelpgx` wrappers were left behind.

### Tests

`go test ./internal/keyring/...` runs everything that needs no database, including the gateway's credential middleware against the real keyring services on the in-memory store. `MCPGW_TEST_DSN` adds the gateway's own Postgres tests (`internal/store`, `internal/db`, `internal/httpapi`): they create and delete their own rows and a scratch database named `mig_…`. `VAULT_TEST_DSN` adds the Postgres suite; it drops and recreates schema `vault` and a database named `vault_migrate_test`, so point it at a throwaway server only. The wire contract is `internal/keyring/api/openapi.json`, and a test pins the route table to it.

Taking over a running Vault is described in [docs/keyring-cutover.md](docs/keyring-cutover.md).

## Design notes

- Connections are opened per call. There is no session cache yet; the init cache removes the discovery probe and the tools cache removes most listing traffic, which covers the common case.
- The front door accepts only 2026-07-28. Clients on older SDKs are rejected with a clear error.
- One tenant. There are no organisations or roles: the identity provider decides who may sign in, and grants decide what each person reaches.
- No Redis, no message bus, no distributed locks. Postgres does the cache and the queue-shaped work; in-process goroutines do the async writes and collapse duplicate refills. Ten replicas facing a cold key cost ten upstream listings, not one per request, and the conditional write makes the duplicates harmless.

## Layout

```
cmd/gateway          main, create-key, migrate-credentials
internal/gateway     front door: mux, direct, search mode, progress
internal/upstream    connector, credential transport (asks the keyring)
internal/credmigrate the one-off move of the gateway's old sealed credentials into the keyring
internal/refreshguard the lock that keeps token refreshes off while credentials are moved
internal/oauthclient discovery, CIMD/DCR, PKCE, token exchange (used by the keyring)
internal/auth        OIDC, API keys, scopes, RFC 9728 challenge
internal/audit       recorder, inbound middleware, upstream transport
internal/cache       async cache writer
internal/toolsearch  BM25
internal/store       Postgres
internal/db          pool and embedded migrations
internal/keyring     the embedded Vault server: config, the two mounts, and its packages
                     (crypto, store, db, service, manifests, audit, httpapi)
internal/keyringref  the one interface the gateway reaches the keyring through
web/                 React, Vite, Tailwind 4
cli/                 the bifrost credential CLI, its own Go module (cli/README.md)
```

## License

MIT.
