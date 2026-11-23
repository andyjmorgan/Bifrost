# Keyring cutover

A draft runbook for moving DonkeyWork Vault's traffic and data onto the gateway's keyring. Nothing here has been run against the lab. The dump, restore, startup and rollback sequence was rehearsed against a throwaway Postgres 18 with two databases and two unprivileged roles, as in the lab; the Kubernetes and Cloudflare steps were not.

Never paste a KEK, a DSN with its password, or a key into a terminal that logs, a commit or a chat. Every command below moves secrets from one store to another without printing them.

## What changes

| | Before | After |
|---|---|---|
| `vault.donkeywork.dev` | cloudflared to `vault.donkeywork-vault` | cloudflared to `gateway.mcp-gateway` |
| API calls to that host | Vault | the keyring, same paths, same keys |
| Page loads on that host | Vault's SPA | `302` to `bifrost.donkeywork.dev` |
| Schema `vault` | database `donkeywork_vault` | database `mcp_gateway`, same server |
| OAuth redirect URI | `https://vault.donkeywork.dev/api/oauth/callback` | unchanged |
| KEK | Secret in `donkeywork-vault` | a copy in `mcp-gateway`, same id and bytes |
| Vault deployment | serving | scaled to zero, kept for rollback |

Facts read from the lab repository on 2026-11-23 (`/mnt/lab/k3s/clusters/office/applications`): both deployments are on the office cluster, and both databases are on the same PostgreSQL 18 server on easternkingdoms, owned by the roles `donkeywork_vault` and `mcp_gateway`. Confirm each before starting.

## Before the window

1. The gateway image in use contains the keyring. With no keyring variable set it is inert, so this can be rolled out days ahead. Check that the log has no `keyring enabled` line and that `/keyring/api/config` is a 404.
2. Fetch anything you will need from the Vault while it is still up: the cutover takes it off the air for a few minutes, and the database administrator's DSN is itself stored in it. The steps below use local peer authentication on easternkingdoms and need nothing from the Vault.
3. Check who else reaches Vault, beyond the public host:
   - the `vault-lan` NodePort (30820) in `donkeywork-vault/vault.yaml`. If anything still uses it, see [The LAN NodePort](#the-lan-nodeport).
   - in-cluster callers of `vault.donkeywork-vault.svc.cluster.local`. On 2026-11-23 the only one in the lab repository was cloudflared.
4. Check the tenant claim, which decides whether a Keycloak change is needed (see [Keycloak](#keycloak)):

   ```sql
   select tenant_id, count(*) from vault.audit_log group by 1;
   ```

## Variables to add to the gateway

In `mcp-gateway/gateway.yaml`, after the existing `MCPGW_*` entries. Names only; the KEK comes from a Secret.

| Variable | Value |
|---|---|
| `KEYRING_KEK_V1` | `secretKeyRef` to the copied KEK Secret (step 1 below) |
| `MCPGW_KEYRING_CRYPTO_ACTIVE_KEK_ID` | the id Vault uses today: the value of its `VAULT_CRYPTO_ACTIVE_KEK_ID` |
| `MCPGW_KEYRING_CRYPTO_KEKS` | `<that id>=$(KEYRING_KEK_V1)`. Kubernetes expands `$(…)` only for variables defined earlier in the list |
| `MCPGW_KEYRING_PUBLIC_BASE_URL` | Vault's `VAULT_PUBLIC_BASE_URL`: the origin the provider apps have registered |
| `MCPGW_KEYRING_LEGACY_HOSTS` | `vault.donkeywork.dev`, plus `:30820` if the NodePort moves too |
| `MCPGW_KEYRING_OIDC_AUDIENCE` | Vault's `VAULT_OIDC_AUDIENCE` |
| `MCPGW_KEYRING_OIDC_WEB_CLIENT_ID` | Vault's `VAULT_OIDC_WEB_CLIENT_ID` |
| `MCPGW_KEYRING_OIDC_CLI_CLIENT_ID` | Vault's `VAULT_OIDC_CLI_CLIENT_ID` |
| `MCPGW_KEYRING_OIDC_WEB_SCOPES` | Vault's `VAULT_OIDC_WEB_SCOPES` |
| `MCPGW_KEYRING_OIDC_CLI_SCOPES` | Vault's `VAULT_OIDC_CLI_SCOPES` |

Left unset on purpose:

- `MCPGW_KEYRING_DSN`: defaults to `MCPGW_DSN`, which is where the schema is restored.
- `MCPGW_KEYRING_OIDC_AUTHORITY`: defaults to `MCPGW_OIDC_ISSUER`. Vault and the gateway use the same realm. They must stay the same string, or the gateway's SPA client is not accepted.
- `MCPGW_KEYRING_UI_BASE_URL`: defaults to `MCPGW_PUBLIC_BASE_URL`.

The gateway also reads each of these under Vault's own name (`VAULT_CRYPTO_KEKS` and so on), so Vault's `env` block can be copied as it is. Do not copy `VAULT_DSN`: it would point the keyring at Vault's database (see [Variant](#variant-share-vaults-database-first)). `VAULT_LISTEN_ADDR`, `VAULT_WEBROOT`, `VAULT_ENVIRONMENT`, `VAULT_OTLP_INSECURE` and `OTEL_EXPORTER_OTLP_ENDPOINT` are not read.

## Cutover

Each numbered step needs the owner's go-ahead. Run `kubectl` with `--context=office`.

### 1. Copy the KEK Secret into the gateway's namespace

The key must be the same bytes under the same id. This pipes the Secret across without showing it:

```sh
kubectl --context=office -n donkeywork-vault get secret donkeywork-vault-secrets -o json \
  | jq '{apiVersion, kind, type, data, metadata: {name: "gateway-keyring-secrets", namespace: "mcp-gateway"}}' \
  | kubectl --context=office apply -f -
```

The gateway reads key `kek-v1` of `gateway-keyring-secrets` as `KEYRING_KEK_V1`.

### 2. Stop Vault

```sh
kubectl --context=office -n donkeywork-vault scale deploy/vault --replicas=0
```

From here until step 6, Vault's host answers with an error from the tunnel. Stopping first matters: Vault refreshes OAuth tokens on read, some providers rotate the refresh token when it is used, and a token refreshed after the dump would leave the restored copy holding a dead one.

### 3. Dump schema `vault` and restore it into the gateway's database

On easternkingdoms, as the OS `postgres` user (peer authentication, no password):

```sh
DUMP=/var/tmp/vault-schema-$(date +%Y%m%dT%H%M%S).dump
sudo -u postgres pg_dump --dbname=donkeywork_vault --schema=vault \
  --no-owner --no-privileges --format=custom --file="$DUMP"
sudo -u postgres pg_restore --list "$DUMP" | grep -c 'TABLE DATA'    # expect 8
sudo -u postgres pg_restore --dbname=mcp_gateway --role=mcp_gateway \
  --no-owner --no-privileges --single-transaction --exit-on-error "$DUMP"
```

- `--role=mcp_gateway` makes the gateway's role the owner of everything restored. It needs `CREATE` on the database; as the database's owner it has it.
- `--single-transaction` leaves nothing behind if the restore fails.
- The dump includes `vault.schema_migrations`, so the keyring finds its baseline already applied and changes nothing at startup.
- The restore must come before the keyring first starts on this database. If the keyring ran first, it created empty tables and the restore stops with "already exists". Then drop the empty schema (`drop schema vault cascade`, after checking it holds no rows you want) and restore again.
- Keep the dump file until the cutover is settled. It holds ciphertext, not plaintext, but treat it as sensitive and delete it afterwards.

Compare row counts between the two databases:

```sql
select 'access_keys', count(*) from vault.access_keys union all
select 'api_keys', count(*) from vault.api_keys union all
select 'oauth_provider_configs', count(*) from vault.oauth_provider_configs union all
select 'oauth_tokens', count(*) from vault.oauth_tokens union all
select 'provider_manifests', count(*) from vault.provider_manifests union all
select 'audit_log', count(*) from vault.audit_log;
```

### 4. Turn the keyring on

Edit `mcp-gateway/gateway.yaml` in the lab repository with the variables above, apply it and wait:

```sh
kubectl --context=office apply -f clusters/office/applications/mcp-gateway/gateway.yaml
kubectl --context=office -n mcp-gateway rollout status deploy/gateway
kubectl --context=office -n mcp-gateway logs deploy/gateway | grep '"keyring enabled"'
```

The log line lists the legacy hosts. A wrong KEK id, a KEK that is not 32 bytes, or a half-set pair stops the gateway at startup with an error naming the variable.

### 5. Check through the gateway's own host, before moving any traffic

```sh
curl -fsS https://bifrost.donkeywork.dev/keyring/api/config | jq .cliClientId
curl -fsS https://bifrost.donkeywork.dev/keyring/api/v1/api-keys -H "X-Api-Key: $(cat "$KEYFILE")" | jq length
```

`$KEYFILE` is a file holding a `dwv_` access key; do not type the key. Then reveal one known secret and compare it with what you expect. If it decrypts, the KEK is right, and it is right for every row.

### 6. Move the Vault host

In `cloudflared/configmap.yaml`, change the service of `vault.donkeywork.dev`:

```yaml
      - hostname: vault.donkeywork.dev
        service: http://gateway.mcp-gateway.svc.cluster.local:8080
```

```sh
kubectl --context=office apply -f clusters/office/applications/cloudflared/configmap.yaml
kubectl --context=office -n networking rollout restart deploy/cloudflared
kubectl --context=office -n networking rollout status deploy/cloudflared
```

cloudflared must pass the original `Host` header, which is its default: the keyring picks the legacy mount by that header. Do not set `httpHostHeader` on this route.

### 7. Commit and push the lab repository

## Verification

| Check | Expect |
|---|---|
| `curl -sI https://vault.donkeywork.dev/` | `302`, `Location: https://bifrost.donkeywork.dev/secrets`, `Cache-Control: no-store` |
| `curl -sI 'https://vault.donkeywork.dev/oauthconnect?connected=x'` | `302` to `https://bifrost.donkeywork.dev/connect?connected=x` |
| `curl -s https://vault.donkeywork.dev/api/config` | Vault's config document, `200`, no redirect |
| `dwvault credentials list` on a machine that was never touched | the same list as before |
| `dwvault credentials get <a known name>` | the known value |
| `dwvault oauth get <provider>` | a token; one within a minute of expiry comes back refreshed |
| a new OAuth connection started from the gateway's UI | the provider shows the Vault redirect URI, and the browser ends on `bifrost.donkeywork.dev/connect?connected=<provider>` |
| `https://bifrost.donkeywork.dev/api/v1/api-keys` with a gateway key | the gateway's `mcpg_` keys, as before |
| the gateway's UI, signed in | the Secrets page lists the same secrets as `dwvault` does for that person |
| Sandbox's Vault connection | still resolves |
| `select count(*) from vault.audit_log where created_at > now() - interval '10 minutes'` in `mcp_gateway` | growing |

## Keycloak

No Keycloak change is needed for sign-in. The keyring accepts a token of the realm whose `azp` is the gateway's SPA client, and rows follow the token's `sub`, which is the same for one person whichever client they signed in with.

One thing to check. The keyring reads an optional `tenant_id` claim. Secrets, OAuth accounts, providers and access keys are scoped by `user_id` alone, but the audit list is scoped by `user_id` and `tenant_id`. If the query in "Before the window" shows a tenant other than `00000000-0000-0000-0000-000000000000`, Vault's clients carry a `tenant_id` mapper, and the gateway's SPA client needs the same mapper or its audit page will show only rows written without a tenant. If every row has the zero tenant, there is nothing to do.

## The LAN NodePort

`vault-lan` exposes Vault on node port 30820, and a Service cannot select pods in another namespace. To keep that address working, add a NodePort Service in `mcp-gateway` that selects the gateway's pods on port 8080 with `nodePort: 30820`, after deleting `vault-lan` (a node port can be held by one Service). Callers arrive with `Host: <node address>:30820`, which the entry `:30820` in `MCPGW_KEYRING_LEGACY_HOSTS` matches on any node. Do not list the gateway's own port there.

## Rollback

Routing back is quick. The data needs a decision.

1. In `cloudflared/configmap.yaml`, point `vault.donkeywork.dev` back at `http://vault.donkeywork-vault.svc.cluster.local:8080`, apply, restart cloudflared.
2. `kubectl --context=office -n donkeywork-vault scale deploy/vault --replicas=1`, then `rollout status`.
3. Remove the keyring variables from `gateway.yaml` and apply. The gateway is as it was; schema `vault` stays in `mcp_gateway`, unused.
4. Data. Vault's database is as it was at step 2 of the cutover. Anything written through the keyring since then (new secrets, new OAuth connections, and refreshed tokens, which matter for providers that rotate refresh tokens) is only in `mcp_gateway`. Either accept the loss, which is safe only within minutes of the cutover, or copy the schema back while both servers are stopped:

   ```sh
   sudo -u postgres pg_dump --dbname=mcp_gateway --schema=vault \
     --no-owner --no-privileges --format=custom --file="$DUMP.back"
   sudo -u postgres pg_restore --dbname=donkeywork_vault --role=donkeywork_vault \
     --clean --if-exists --no-owner --no-privileges --single-transaction --exit-on-error "$DUMP.back"
   ```

   Vault's server reading rows the keyring wrote, with the same KEK, was rehearsed. `--clean` against the live Vault database was not.

The redirect is a `302` with `Cache-Control: no-store`, so browsers do not remember it once the host points at Vault again.

## Variant: share Vault's database first

The keyring opens its own connection pool, so it does not have to live in the gateway's database. Setting `MCPGW_KEYRING_DSN` to Vault's DSN (from a copy of `vault-postgres-creds` in `mcp-gateway`) makes step 3 unnecessary on the day: stop Vault, turn the keyring on, move the host. Rollback is then routing alone, with no data to reconcile. The schema can be moved into `mcp_gateway` later, in its own window, by the same dump and restore and by removing the variable. Never run Vault and the keyring against the same database at the same time: each refreshes OAuth tokens and would invalidate the other's.

## Afterwards

- Delete the dump files.
- When the old deployment is retired: remove the `donkeywork-vault` namespace's Deployment and Services, drop the `donkeywork_vault` database after a final backup, and delete the KEK Secret there. The KEK itself must outlive Vault: it is the only thing that reads the stored secrets.
