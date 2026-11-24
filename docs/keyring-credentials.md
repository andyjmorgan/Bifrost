# Server credentials live in the keyring

Every credential an MCP server authenticates upstream with is held in the keyring. The gateway stores a reference and no secret. This page says how that works, how the credentials the gateway used to store itself are moved, and what is left to do.

## The kinds

A server's credential is one of these. The API's `kind` is what the operator set; `store` says where the secret is.

| API kind | Stored as | Whose keyring | What is sent |
|---|---|---|---|
| `none` | nothing | | nothing |
| `basic` | `keyring_secret` | the gateway's own space | `Authorization: Basic` with the secret's username |
| `header` | `keyring_secret` | the gateway's own space | the header the secret names |
| `oauth` | `keyring_oauth` | the gateway's own space | the token of the server's own OAuth connection, under its token type |
| `keyring_secret` | `keyring_secret` | a person's | the header the secret describes |
| `keyring_oauth` | `keyring_oauth` | a person's | `Authorization: Bearer` with the account's token |

The first three are credentials the server owns. Somebody typed them in, or gave consent, for this server. The last two are lent: a person pointed the server at something in their own keyring.

### The gateway's own space

Keyring rows belong to a user. A credential that belongs to a server belongs to no person, so its owner is a fixed service user, `keyringref.GatewayOwner` (`b1f405c7-0000-4000-8000-000000000001`), with the nil tenant. Three things follow.

- Nobody reads that space through the keyring's API. A token or access key that resolves to the gateway's id is refused with 403, whatever it says. The gateway reaches the space in process and nothing else does, which keeps the promise that an MCP server's token is never handed out.
- The rules for a server's own credential are the ones the gateway had when it stored the credential itself: an API key with `mcp:edit` may set one, and any operator may re-point the server. The rules for a lent credential are unchanged: only a signed-in person sets one, from their own keyring, and only they may change the URL of a server that uses it.
- The name is `mcp-<slug>`: the name of the secret, or the key of the OAuth provider with the account `default`. A slug never changes.

Deleting a server, or giving it another credential, deletes what the gateway held for it. A lent credential is never deleted by the gateway.

### With the keyring off

The keyring is off unless its KEKs are configured. A gateway without it can register servers with no credential and nothing else: `basic`, `header`, `oauth` and the two lent kinds are refused with `the keyring is not enabled on this gateway`. A server that already holds a reference fails to connect with an error that says the keyring is off. Nothing is sent upstream without its credential.

## OAuth for an MCP server

The keyring runs the MCP authorization flow. The protocol is `internal/oauthclient`, which the keyring uses as it is.

1. Discovery starts at the server: its `WWW-Authenticate` challenge, the protected resource metadata it points to (RFC 9728), and the authorization server metadata that names (RFC 8414, then the OIDC document).
2. The client is registered by the flow. Where the authorization server takes a Client ID Metadata Document the client id is the gateway's `/.well-known/mcp-client-metadata.json`; otherwise the keyring registers dynamically (RFC 7591) and keeps the client secret.
3. The authorization request carries PKCE and the `resource` (RFC 8707). So do the code exchange and every refresh.
4. The redirect URI is the gateway's `/oauth/callback`. The gateway's handler asks the keyring whether the state is a server's pending consent, and the keyring redeems the code.

The gateway's identity to upstream servers is the client metadata URL and the callback path. Both are built from `MCPGW_PUBLIC_BASE_URL` alone and neither has moved, so no registration made before this change needs consent again.

What is stored, all under the gateway's id:

- a provider `mcp-<slug>` whose manifest carries an `mcp` block: the server URL, issuer, resource, registration kind and endpoint, and the redirect URI. Only the flow writes that block; the provider API neither returns nor accepts it.
- the provider's app credentials: the client id and, for a dynamic registration, the client secret.
- one account, `default`: the access and refresh token, scopes, expiry and token type.

### Refresh and the broken state

The keyring refreshes on demand, inside the request that needs the token, when the token is within a minute of expiry or the server has just refused it with a 401. There is no background refresh.

A refresh the token endpoint definitively refuses (a 4xx such as `invalid_grant`) marks the account broken (`oauth_tokens.broken_at`, `broken_reason`). A broken account is not refreshed again and not handed out. The gateway stores the state on its reference, which drops the server's cached tools, and the server's page asks for authorization again. A network error, a timeout or a 5xx is transient: that request fails and the next one tries again.

## Each request

The credential middleware (`internal/upstream/keyring.go`) asks the keyring once for each upstream session, which is one forwarded request. Nothing is kept past the session, and an access token is dropped at its own expiry even within it. A changed or deleted credential takes effect on the next request.

## What is written down

- The keyring's audit trail gets a row for every fetch, under the credential's owner, with transport `gateway` and the server in `method` (`mcp-server <slug> (<id>)`). A change the gateway makes for somebody (storing a server's password, deleting the server) also names them: `… by <user id>`.
- Deletes and changes are audited like reads: `CredentialRemoved` and `CredentialChanged` for secrets, access keys, providers and their app credentials; `TokenRemoved` for an account. No row carries a value.
- A fetch made for a forwarded request carries that request's id (`audit_log.request_id`). See "Tracing a credential's use" below.
- The gateway's audit payloads mask the credential header.
- The servers API returns names and never a value. Logs carry names only. For a secret lent from a person's keyring, everybody sees whose it is and its name; the user name it logs in with and the header it travels in are shown to its owner only.

### Tracing a credential's use

The gateway's audit trail and the keyring's are two tables and stay two. The request id is on both.

- From a gateway audit event: `GET /api/v1/audit/{id}/keyring` lists the keyring rows written for that event's request, whoever owns the credential. `heldByGateway` marks a credential the server owns. Those rows are in no person's keyring trail, so this is where they are seen.
- From a keyring row: `requestId` is on the row, the keyring's audit API filters on `?requestId=`, and `GET /api/v1/audit?requestId=` lists the gateway's events of that request.

Every request to the gateway's API has a request id: the caller's `X-Request-Id` when it is a plain token (1 to 64 ASCII letters, digits and `- _ . :`), and otherwise one made on arrival.

## Moving the credentials the gateway stored itself

Before this, the gateway sealed `basic`, `header` and `oauth` credentials in `credentials.secret_blob` under `MCPGW_MASTER_KEY`. `gateway migrate-credentials` moves them. It runs in a process that holds both the master key and the keyring's KEKs, with the same environment as the server:

```
gateway migrate-credentials -kind static|oauth [-dry-run] [-rollback]
        [-owner gateway|<user id>] [-server slug,slug] [-include-new] [-ignore-other-sessions]
```

For each server it copies the credential into the keyring, reads the copy back and compares it with the original, and only then switches the gateway's row to the reference and clears the sealed blob. A run can stop anywhere: a server is either wholly moved or wholly not, and the next run carries on. The report is one line for each server, names only. The exit status is 0, 1 when a credential could not be moved, or 2 for a usage or setup error.

- `-dry-run` prints what would be done and changes nothing.
- `-owner` is the keyring user the moved credentials belong to: the gateway's own space, or a person's id. In a person's keyring an unrelated secret or provider that already has the server's name is never overwritten; that server is reported as failed.
- `-rollback` moves back the rows this command moved (`credentials.migrated_from`), resealing the keyring's current value, which is the only right one once a token has been refreshed. `-include-new` also takes back credentials the gateway stored in its own space from the start.

A rollback takes back only what the command wrote. A row is the command's when its owner is the owner the run was given, its keyring name is `mcp-<slug>`, and it was moved (or, with `-include-new`, is held by the gateway). A server that was moved and later pointed at a secret in somebody's own keyring is skipped, with the reason on its line: that secret is theirs, and the command neither reads it out nor deletes it. When the run was given a person as owner, the rollback restores the gateway's row and leaves the secret in that person's keyring.

### A credential that changes during the run

Each server's row is read again at its turn, and the switch is made only if the row is still exactly that row (same kind, same `updated_at`). If somebody replaced the credential, a consent landed, or the server was deleted, the server's line says `changed during the run; run again`, nothing is switched, and the keyring is tidied: a copy nothing refers to is removed, and if the row now refers to the keyring entry the copy overwrote, what the keyring held before the copy is put back.

One case is detected but not repaired: a gateway storing a new credential under the server's name in the few milliseconds between the run reading the keyring and writing its copy. The run cannot tell its copy from the new value. It does not switch, and says to set the server's credential again. Do not edit server credentials while the command runs.

### OAuth rows and token refresh

Refresh tokens rotate. A token refreshed while its row was being copied would leave the copy with a retired token, and the server would need consent again. So the command holds off every refresh while it runs.

`internal/refreshguard` is a Postgres advisory lock on the gateway's database. Every refresh holds it shared for as long as it talks to the token endpoint and stores the result. The command holds it exclusive: it waits for refreshes in flight and keeps new ones from starting. A refresh that waited reads its row again before doing anything.

The lock lives in one database session. If that session is lost during a run (the database restarts, the connection is cut), the lock goes with it. The run asks the session before every server and before every switch, and stops with an error when it does not answer; the server in hand is left as it was, and the next run carries on.

A gateway from before the guard takes no lock. The command reads `pg_stat_activity` and refuses to move OAuth rows while a session that does not name itself as this version of Bifrost is connected to the database. `-ignore-other-sessions` vouches for a `psql` or a backup.

A connection awaiting consent moves as a registration without tokens and stays pending. A broken one stays broken on both sides.

### Running it on a live gateway

The rows can be moved in either of two ways. Both were rehearsed against a throwaway database with the real binaries.

- **Two deploys, no downtime.** Deploy the build at the commit "Servers: existing OAuth connections move into the keyring". It still serves the gateway's own store and holds the refresh guard. Run the command for `static` and then `oauth` while it serves. Then deploy the head.
- **One deploy.** Deploy the head. Servers whose credential has not been moved fail until the command has run, which takes seconds. Nothing refreshes an unmoved row in the meantime, because the head has no code that could.

Either way, the command must not run beside a gateway older than this branch; it refuses to for OAuth rows.

To go back to an image older than this branch, run `-rollback` for `oauth` first, and for `static` if the image predates keyring credentials altogether. An older image over moved rows still sends Basic and header credentials, and sends an OAuth token until it expires, but it cannot refresh a server's connection: its keyring knows nothing of the `resource` parameter or of a client without a secret.

### What this release no longer does

The gateway no longer reads or writes its own sealed credentials. The refresh worker's token pass, the gateway's own OAuth start and callback, and the sealing of Basic and header values are gone. `internal/credmigrate` is the only code that touches the old columns.

The columns themselves stay for one release, unused: `credentials.secret_blob`, `basic_username`, `header_name`, the `oauth_*` columns and `last_refreshed_at`, and the table `oauth_states`. A later migration drops them, together with `credentials.migrated_from` and the rollback mode.

A server whose credential has not been moved does not authenticate with this release. A request through it fails with `upstream.ErrNotMigrated`, which names the command, and nothing is sent upstream. The API shows it with `store: "gateway"`, state `broken` and the reason. `POST /servers/{id}/oauth/start` on it is refused. The gateway logs the slugs once at start. Replacing the credential in the UI works and discards the sealed one.

`MCPGW_REFRESH_INTERVAL` now only paces the cache sweep and the audit purge. `MCPGW_REFRESH_LEEWAY` is read and ignored.

## What is still in two

- **Key formats.** The gateway's `mcpg_` keys and the keyring's `dwv_` keys are separate tables, scopes and checks. `docs/one-key-format.md` says what one format would take.
- **Audit trails.** Two tables, joined by the request id.
- **The old columns.** Unused, until a later migration drops them.
