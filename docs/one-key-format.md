# One key format: what unifying `mcpg_` and `dwv_` would take

Not done. Bifrost issues two kinds of key, from two tables, checked by two pieces of code. This page says how they differ and what one format would take. Nothing here has been built.

## What exists

| | Gateway key | Keyring access key |
|---|---|---|
| Prefix | `mcpg_` | `dwv_` |
| Body | 32 random bytes, base32, lower case | 32 random bytes, base64url |
| Table | `api_keys` (gateway schema) | `vault.access_keys` |
| Lookup | SHA-256 of the secret, unique | SHA-256 of the secret, unique |
| The secret itself | also sealed under `MCPGW_MASTER_KEY`, so the Explorer can call the front door as the key | not kept |
| Acts as | its creator (`created_by`, the identity provider's subject) | its owner (`user_id`, the same subject, as a UUID) and tenant |
| Scopes | `api:read`, `api:edit`, `mcp:execute`, `mcp:edit` | `vault:read`, `vault:readwrite`, `vault:audit` |
| Off switch | revoked, for good (`revoked_at`) | disabled and re-enabled (`enabled`), or deleted |
| Presented as | `Authorization: Bearer` | `X-Api-Key`, or `Authorization: Bearer` |
| Checked by | `internal/auth` | `internal/keyring/httpapi` and `service.AccessKeyService` |
| Audited in | the gateway's audit trail (`api_key_id`, prefix) | the keyring's (`access_key_id`, prefix, name) |
| Who holds one today | Sandbox (one stored key, in the keyring), agents, scripts | the `bifrost` CLI and every skill and cron job that reads a credential |

Two rules were built on the keys being different, and one format has to keep both:

- A gateway key cannot point a server at a credential in a person's keyring. It carries no right to read the keyring.
- A keyring key cannot call an MCP server or edit one.

## What one format would take

1. **One table and one check.** A single `keys` table: id, owner (and tenant), name, prefix, SHA-256 hash, scopes, enabled, revoked, created, last used. One function resolves a presented secret to a caller with scopes. The gateway's authenticator and the keyring's middleware both call it. The keyring is optional and the gateway is not, so the table belongs to the gateway and the keyring reads it through an interface, as `keyringref` does in the other direction.
2. **One scope vocabulary.** The seven scopes are disjoint, so the union works as it is: a key lists what it may do on either side. The two rules above then stop being about which kind of key it is and become scope checks. Setting a lent credential needs `vault:read` on the caller's own keyring, and the existing "signed in as yourself" rule for lent credentials has to be decided again on purpose: either a key with `vault:read` may now set one, or the rule stays "a person, not a key".
3. **One prefix for new keys, three accepted.** New keys get one prefix. `mcpg_` and `dwv_` keys keep working: both are looked up by hash, so accepting them is a matter of the prefix check. They cannot be converted in place, because only the hash of a `dwv_` key is stored. Old keys therefore stay valid until their owners replace them, and the prefix check keeps all three for as long as that takes.
4. **Moving the rows.** Copy `api_keys` and `vault.access_keys` into the new table, hash for hash. Collisions are not possible across the two (different prefixes are part of what is hashed). `revoked_at` maps to revoked; `enabled = false` maps to disabled. A one-off command, with a dry run, like `migrate-credentials`.
5. **The sealed copy of gateway keys.** The Explorer calls the front door as a key, which is why the gateway seals the secret. Either that stays (only for keys that carry `mcp:execute`, and it is the last thing `MCPGW_MASTER_KEY` seals), or the Explorer stops needing the secret: it could mint a short-lived internal token for the key instead. The second removes the master key from the picture and is the better end state.
6. **Both headers.** Accept `X-Api-Key` and `Authorization: Bearer` everywhere. Every existing caller keeps working.
7. **Audit.** Both trails already record the key's id and prefix. With one table the id means the same thing in both, which is also a step towards tracing a request across them.
8. **The UI and the CLI.** One "Keys" page instead of "API keys" and "Access keys", with the scopes grouped. `bifrost auth` and the stored `bifrost-api-key` need no change until their keys are replaced.

## What would have to be decided first

- Whether a key may hold scopes on both sides at once. It is the natural result and it is also a key that can read every secret and call every server. A cap ("a key is for the gateway or for the keyring") would keep today's blast radius and most of the benefit.
- The lent-credential rule in point 2.
- Whether disabled-and-re-enabled survives. The keyring has it; the gateway's revoke is final.
- The tenant. Keyring keys carry one and gateway keys do not. One tenant today, so the column is carried and not enforced, as elsewhere.

## Order

1. The shared table and resolver, with both old tables still written and read (dual read, single write to the new one).
2. Move the rows; switch both checks to the resolver.
3. New prefix for new keys; one Keys page.
4. Drop the old tables a release later.
