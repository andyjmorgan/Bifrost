# Servers that use a keyring credential

An MCP server can authenticate upstream with a credential held in the keyring instead of one the gateway stores itself. This page says how that works, and what it would take to move the gateway's own credentials onto the keyring, which has not been done.

## What exists

Two credential kinds, beside `none`, `oauth`, `basic` and `header`:

| Kind | The reference | What is sent |
|---|---|---|
| `keyring_oauth` | provider and account of a connected OAuth account | `Authorization: Bearer <access token>`. The keyring refreshes the token as it does for the CLI |
| `keyring_secret` | the name of a stored secret | the header the secret describes: its header and prefix, or `Authorization: Basic` with its username |

The gateway stores the reference and no secret. `keyring_oauth` is what reaches a server that wants a provider token and offers no MCP OAuth discovery, such as X's.

A secret must describe a header. `http_basic` needs its username. Any other kind must be a `header_api_key` (which defaults to `Authorization`) or name a header. An SSH login, a connection string or an opaque value is refused when the reference is set, and again when it is used.

### Ownership

Keyring rows belong to a user, so a reference records its owner (`credentials.keyring_owner`): the person who set it. Every lookup runs in that user's keyring and nowhere else. A credential somebody else holds under the same name is not found.

Three rules follow from the owner being a person:

- Only a signed-in person can set a keyring credential, and only from their own keyring. A gateway API key (`mcpg_`) cannot: it carries no right to read the keyring, and pointing a server at a keyring credential makes the gateway send that credential to the server's URL.
- Only the owner can change the URL of a server that uses their keyring credential, for the same reason. Anyone may replace the credential, which makes them the owner of the new reference.
- Everyone the server is granted to calls it with the owner's credential. That is the same as for the gateway's own credentials, which are also per server.

The gateway's user id is the identity provider's subject, and so is the keyring's, which requires it to be a UUID. A dev-mode owner such as `dev` has no keyring.

### The one interface

`internal/keyringref` holds the interface and nothing else:

```go
type Keyring interface {
    Check(ctx context.Context, ref Ref) error
    OAuthToken(ctx context.Context, ref Ref, use Use, rejected string) (Token, error)
    SecretHeader(ctx context.Context, ref Ref, use Use) (Header, error)
}
```

`keyring.Source` implements it over the keyring's own services, in process. `internal/upstream` and `internal/httpapi` are handed it at start and import nothing else of the keyring. With the keyring off they are handed nil.

### Each request

The credential middleware (`internal/upstream/keyring.go`) asks the keyring once for each upstream session, which is one forwarded request: the initialize, the call and the close share what was fetched. Nothing is kept past the session, and an access token is dropped at its own expiry even within it. A changed or deleted keyring credential takes effect on the next request.

A 401 from the server with a `keyring_oauth` credential asks the keyring to replace the rejected token, and the request is sent once more. The keyring refreshes whatever the expiry says, unless the stored token is already a different one, in which case a sibling refreshed it first and that one is used. If the account has no refresh token the 401 stands.

### What is written down

- The keyring's audit trail gets one entry for each fetch, under the owner, with transport `gateway` and the server in `method` (`mcp-server <slug> (<id>)`). A fetch that finds nothing is recorded as a failure.
- The gateway's audit payloads mask the credential header. A secret names its own header, so the name is passed to the audit transport with the request.
- The servers API returns the reference (`keyringOwner`, `keyringProvider`, `keyringAccount`, `keyringSecretName`) and never a value. Logs carry names only.

### With the keyring off

The API refuses the two kinds with `the keyring is not enabled on this gateway`. Responses for other kinds are unchanged: the reference fields are omitted when empty. A server that already holds a keyring reference fails to connect with an error that says the keyring is off; nothing is sent upstream without the credential.

## Moving the gateway's own credentials onto the keyring

Not done. The gateway still has its own store for `oauth`, `basic` and `header` credentials (`credentials.secret_blob`, sealed with `MCPGW_MASTER_KEY`), its own OAuth client (`internal/oauthclient`) and its own refresh worker. This is what one store would take.

### Static credentials: small

`basic` and `header` rows map directly onto keyring secrets (`http_basic` with the username; `header_api_key` with the header name and an empty prefix).

1. Decide the owner. A gateway credential belongs to a server, not a person. Either each one goes to the keyring of whoever is recorded as the server's first grant holder, or the keyring gains a service owner (see below).
2. For each row: open `secret_blob` with the gateway's sealer, store the value as a keyring secret named after the server's slug, switch the row to `keyring_secret`, clear `secret_blob`. This has to run in the gateway process, which holds both keys. It is a one-off command like `create-key`, not a SQL migration.
3. Stop accepting `basic` and `header` in the API, or keep accepting them and write to the keyring behind the scenes.

Rollback is the reverse while the old blobs are kept; clear them in a later release.

### OAuth credentials: the real work

A gateway OAuth credential and a keyring OAuth account are different things, and the keyring cannot hold the gateway's today.

| | Gateway (`internal/oauthclient`) | Keyring |
|---|---|---|
| Authorization server | discovered from the MCP server (RFC 9728, then RFC 8414) | a provider manifest someone added |
| Client | registered per server: CIMD, falling back to DCR | an OAuth app whose id and secret a person entered |
| Token request | carries `resource` (RFC 8707) and the scopes | no `resource` |
| Refresh | sends `resource` and scope; a definitive rejection marks the credential broken | no `resource`; failure is an error to the caller |
| Token type | stored, and honoured when sending | not stored; the gateway sends it as Bearer |
| Belongs to | a server | a user |

To move them the keyring needs:

1. **A provider that is discovered, not written.** A manifest kind created from an MCP server's metadata, holding the endpoints, the registration kind and the resource. One per server.
2. **Registration.** CIMD and DCR, so the client id (and the DCR secret) is produced by the flow instead of typed in. `internal/oauthclient` already does this and could become the keyring's implementation of it.
3. **`resource` on the authorization request, the code exchange and the refresh.** Without it a token is not bound to the MCP server and a compliant server refuses it.
4. **The callback.** The gateway's redirect URI is `<base>/oauth/callback`; the keyring's is `<keyring base>/api/oauth/callback`. Every existing CIMD or DCR registration names the gateway's. Either the keyring serves the gateway's path for these providers, or every server is re-registered and re-consented.
5. **A broken state.** The gateway marks a credential broken on `invalid_grant` so the UI can ask for re-authorization, and drops the server's cached tools. The keyring has no such state on a token.
6. **An owner that is not a person,** or an agreed person. Server tokens are shared by everyone the server is granted to.
7. **Background refresh.** The gateway's worker renews tokens ahead of expiry; the keyring refreshes on demand. On demand is enough once the middleware asks the keyring, so the worker's refresh pass could go.

Then the data: for each `oauth` row, write a provider, an app config (client id, DCR secret) and a token row (access and refresh token, expiry, scopes) in the keyring, re-encrypted from the gateway's sealer to the keyring's envelope, and switch the row to `keyring_oauth`. Refresh tokens rotate, so a server must not refresh on both sides during the move: do it with the gateway stopped, or one server at a time under its refresh lock.

After that `internal/oauthclient` is the keyring's, `credentials` keeps only references, `MCPGW_MASTER_KEY` seals nothing but API keys, and the OAuth accounts page lists one kind of row.

### A service owner

Both moves want an owner that is not a person. The keyring's rows carry `user_id` and `tenant_id` and nothing checks that the user is human, so a fixed, documented UUID for "the gateway" would work with no schema change. What it needs is a decision on who may read those rows through the keyring's API: nobody (the gateway alone, in process) is the answer that keeps today's promise that an MCP server's token is never handed out.

### Order

1. Static credentials, with a service owner. Small, reversible.
2. Keyring support for discovered providers, registration and `resource`, with no data moved: new OAuth servers go to the keyring, old ones stay.
3. Move the existing OAuth rows.
4. Remove the gateway's credential store, its refresh worker and the sealed columns.
