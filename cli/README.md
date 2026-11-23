# bifrost CLI

`bifrost` is the command-line client for the credentials Bifrost holds: stored secrets, OAuth access tokens and the access keys that guard them. It is for shell scripts, local development, CI jobs and agents that need a credential without keeping it in a file.

It replaces `dwvault`, the DonkeyWork Vault CLI. The commands, flags and output are the same. Only the name, the default address and the storage names changed, and an existing `dwvault` login carries over without logging in again. See [Moving from dwvault](#moving-from-dwvault).

This directory is its own Go module (`github.com/andyjmorgan/Bifrost/cli`). It shares no `go.mod` with the gateway.

## Install

Linux and macOS, amd64 and arm64:

```bash
curl -fsSL https://raw.githubusercontent.com/andyjmorgan/Bifrost/main/cli/install.sh | sh
```

The installer finds the newest CLI release on GitHub, downloads the binary for your platform, checks it against the release's `SHA256SUMS` and installs it. It refuses to install a binary it cannot verify. If no release has been published yet it says so and installs nothing.

| Variable | Flag | Default | Purpose |
|---|---|---|---|
| `BIFROST_VERSION` | `--version` | `latest` | Install a specific release, for example `v0.3.0`. |
| `BIFROST_BIN_DIR` | `--bin-dir` | `~/.local/bin`, or `/usr/local/bin` if writable and on `PATH` | Install directory. |
| `BIFROST_REPO` | `--repo` | `andyjmorgan/Bifrost` | GitHub repository to download from. |
| `BIFROST_LINK_DWVAULT` | `--link-dwvault` | unset | Set `1` to also create a `dwvault` symlink. An existing `dwvault` binary is left alone. |
| `BIFROST_NO_VERIFY` | | unset | Set `1` to skip checksum verification. Not recommended. |

To build from source instead:

```bash
cd cli
./build.sh                                  # all four platforms into dist/, with SHA256SUMS
cp dist/bifrost-linux-amd64 ~/.local/bin/bifrost
```

Check the install:

```bash
bifrost version
```

## Login

Sign in once per server. The CLI stores the credential and uses it for every command:

```bash
bifrost auth login
```

In an interactive terminal this offers two methods:

1. OAuth device login, the default. The CLI prints an activation URL from the identity provider. Open it in any browser and approve the request. The CLI stores the access and refresh tokens and refreshes them as needed.
2. Paste an access key. Paste a `dwv_...` key created in the web app. Use this for automation, or where a browser login is impractical.

Scripts and non-interactive shells must choose:

```bash
bifrost auth login --oauth     # OAuth device login, no selector
bifrost auth login --api-key   # read a dwv_... key from the prompt or stdin
```

The CLI validates the credential before saving it. The secret goes in the OS keyring when one is available, otherwise in a `0600` file. Non-secret details go in `hosts.json`. See [Where things are stored](#where-things-are-stored).

- Already logged in to a server? Pass `--force` to replace the stored credential.
- `VAULT_API_KEY`, when set, takes precedence over any stored login. `auth login` validates it and never stores it.
- An OAuth device login acts with `vault:readwrite`. Mint a scoped access key when a caller should have less.

```bash
bifrost auth status    # server, method, where the credential came from, account
bifrost auth logout    # forget the stored credential
```

## Server address

The API is the Vault API, unchanged, in two places:

- on the Bifrost host under the `/keyring` prefix: `https://bifrost.donkeywork.dev/keyring/api/v1/...`
- on the original Vault host at the root: `https://vault.donkeywork.dev/api/v1/...`

The address is a base URL. Requests go to `<base>/api/...`, so a base with a path prefix and one without both work, with or without a trailing slash.

The CLI takes the address from the first of these that is set:

| Order | Source | |
|---|---|---|
| 1 | `--addr` | |
| 2 | `BIFROST_ADDR` | |
| 3 | `VAULT_ADDR` | Still honoured, so an existing `VAULT_ADDR=https://vault.donkeywork.dev` keeps working. |
| 4 | default | `https://bifrost.donkeywork.dev/keyring` |

An address with no scheme is taken as `http://`. Give a full `https://...` URL for TLS.

The stored login is kept per base URL, so `https://bifrost.donkeywork.dev/keyring` and `https://vault.donkeywork.dev` each have their own entry.

Other global options:

| Flag | Env | Meaning |
|---|---|---|
| `--api-key` | `VAULT_API_KEY` | Access key for this process. Overrides the stored login. |

## Usage

Credentials:

```bash
bifrost credentials list                 # name, description, base URL, kind
bifrost credentials shape grafana-prod   # how to use it, without revealing the secret
bifrost credentials get grafana-prod     # the secret, to stdout
bifrost credentials header grafana-prod  # a ready "Name: value" header line
bifrost credentials delete grafana-prod
```

```bash
curl -H "$(bifrost credentials header grafana-prod)" https://grafana.example.com/api/health
```

Store one, keeping the secret off the command line:

```bash
some-source | bifrost credentials create grafana-prod --secret-stdin \
  --kind header_api_key --header Authorization --prefix "Bearer " \
  --description "Grafana production API" --base-url https://grafana.example.com
```

`create` is an upsert. Kinds are `opaque` (the default), `header_api_key`, `http_basic`, `username_password`, `ssh` and `connection_string`; `bifrost credentials --help` describes each.

OAuth tokens:

```bash
bifrost oauth list
bifrost oauth get microsoft
bifrost oauth get microsoft --account alice@example.com
```

Access keys:

```bash
bifrost keys list
bifrost keys create agent-prod --scope vault:read     # the secret is printed once, to stdout
bifrost keys disable <id>
bifrost keys enable <id>
bifrost keys delete <id>
```

The agent skill, for Claude and other agents that shell out to the CLI:

```bash
bifrost skill            # print the bundled SKILL.md
bifrost skill install    # write it to ~/.claude/skills/credential-manager/SKILL.md
```

### Secret handling

`bifrost` reserves stdout for the secret or token you asked for. Prompts, status, help and errors go to stderr. Nothing secret is ever written to stderr or a log.

Prefer:

```bash
curl -H "$(bifrost credentials header grafana-prod)" "$URL"
```

Avoid:

```bash
echo "$(bifrost credentials get grafana-prod)"
curl -v -H "$(bifrost credentials header grafana-prod)" "$URL"
```

Keep secrets out of URLs, shell history, committed files, build logs and verbose HTTP traces.

## The dwvault name

Started as `dwvault`, for example through a symlink, the binary behaves as the DonkeyWork Vault CLI did, so existing scripts, skills and cron jobs keep working:

```bash
ln -s bifrost ~/.local/bin/dwvault
```

| | as `bifrost` | as `dwvault` |
|---|---|---|
| Default address | `https://bifrost.donkeywork.dev/keyring` | `https://vault.donkeywork.dev` |
| Address variables | `BIFROST_ADDR`, then `VAULT_ADDR` | `VAULT_ADDR` |
| Keyring service | `bifrost` | `dwvault` |
| Config directory | `~/.config/bifrost` | `~/.config/dwvault` |
| Reads the other name's login | yes, `dwvault`'s, read-only | no |
| `skill` prints | the `bifrost` skill | the `dwvault` skill |
| `update` | updates from GitHub releases | says it does not update itself, and changes nothing |

The name is taken from `argv[0]`, and only the exact name `dwvault` selects it. Messages and help use whichever name was typed.

`dwvault` never updates itself. A release is a `bifrost` binary, and fetching one under the old name would replace what that name points at with something its user did not ask for. `dwvault update` and `dwvault --update` say so and make no request. Run `bifrost update`; a `dwvault` symlink follows it.

## Moving from dwvault

Nothing needs doing to keep working, and nobody has to log in again.

1. Install `bifrost` next to `dwvault`.
2. Run any command, for example `bifrost auth status`. `bifrost` has no login of its own yet, so it looks for the one `dwvault` stored, copies it into its own store, and says so once on stderr. From then on it uses its own copy.
3. When you are ready, replace the old binary with a symlink so old scripts run the new code:

   ```bash
   mv ~/.local/bin/dwvault ~/.local/bin/dwvault.old
   ln -s bifrost ~/.local/bin/dwvault
   ```

   `install.sh --link-dwvault` does the same for a name that is free or already a symlink, and leaves a real `dwvault` binary alone.
4. Later, change scripts and skills from `dwvault` to `bifrost` at your own pace. `bifrost skill install --force` writes the new skill text.

### Where things are stored

| | `dwvault` (read, never changed) | `bifrost` |
|---|---|---|
| Secret, OS keyring | service `dwvault`, account = base URL | service `bifrost`, account = base URL |
| Secret, file fallback | `~/.config/dwvault/credentials.json` (`0600`) | `~/.config/bifrost/credentials.json` (`0600`) |
| Non-secret host records | `~/.config/dwvault/hosts.json` | `~/.config/bifrost/hosts.json` |
| Update-check cache | `~/.config/dwvault/update.json` | `~/.config/bifrost/update.json` |

`~/.config` is `$XDG_CONFIG_HOME` when that is set, and the platform's equivalent on macOS. The keyring is the macOS Keychain or the Linux Secret Service.

### How the carry-over works

- Lookup order is `VAULT_API_KEY`, then `bifrost`'s keyring entry, then `bifrost`'s file, then `dwvault`'s keyring entry, then `dwvault`'s file.
- A login is stored per base URL. `bifrost` looks under the address it is using. For its default address it also looks under `https://vault.donkeywork.dev`, because the two are the same server. A login for the Vault host is never offered to any other address.
- The copy is exact. The `dwvault` entries are only read: `bifrost` never writes or deletes them, including on `bifrost auth logout`.
- `bifrost auth logout` removes `bifrost`'s copy and records that you logged out, so the `dwvault` login is not picked up again. `bifrost auth login` clears that.
- The OIDC client is unchanged. The CLI asks the server for it (`<base>/api/config`), and a carried-over OAuth login keeps the client id and issuer it was made with.

Things to know:

- Until the keyring is live on the Bifrost host, the default address answers with Bifrost's web app, and the CLI reports that a web page answered instead of the API. Use `--addr https://vault.donkeywork.dev`, or leave `VAULT_ADDR` set, in the meantime.
- After the copy, `bifrost` and `dwvault` each hold the same OAuth refresh token and refresh it separately. That is fine with Keycloak's default settings. If the realm is set to revoke a refresh token once used, the first of the two to refresh would log the other out; log in again with that one.
- `VAULT_API_KEY` is still the variable for an access key under both names. The keys are the same `dwv_...` keys.
- `VAULT_NO_UPDATE_CHECK=1` still silences the update notice. `BIFROST_NO_UPDATE_CHECK=1` does too.
- `DWVAULT_REPO`, `DWVAULT_VERSION`, `DWVAULT_BIN_DIR` and `DWVAULT_NO_VERIFY` belonged to the old installer and updater. Their `BIFROST_` equivalents replace them.

## Updating and releases

```bash
bifrost update            # upgrade in place
bifrost update --check    # report only
```

`bifrost update` downloads the newest release for your platform, verifies it against the release's `SHA256SUMS`, and replaces the running binary where it lives. A build from source (any version that is not `vX.Y.Z`) is left alone unless you pass `--force`. When no release has been published it says so and changes nothing.

Releases are GitHub releases of `andyjmorgan/Bifrost`:

- The tag is `cli-vX.Y.Z`. The gateway lives in the same repository, so the CLI never uses a bare `vX.Y.Z`, and neither `bifrost update` nor `install.sh` asks GitHub for the repository's "latest release". They list the releases and take the highest `cli-v` version, so a gateway release can never be installed as the CLI.
- The assets are `bifrost-linux-amd64`, `bifrost-linux-arm64`, `bifrost-darwin-amd64`, `bifrost-darwin-arm64` and `SHA256SUMS`.
- The binary reports the version without the prefix: `bifrost version v0.3.0`.

`BIFROST_REPO=owner/name` points both the updater and the installer at another repository. `BIFROST_GITHUB_API` changes the API root, for GitHub Enterprise or a mirror, and `BIFROST_GITHUB_URL` changes the host the installer downloads from. Both URLs must be `https`.

`.github/workflows/cli-release.yml` publishes a release when a change under `cli/` reaches `main` (the next patch version, starting at `v0.3.0`), or when a `cli-vX.Y.Z` tag is pushed (that exact version). It runs the tests and the coverage gate first.

## Development

```bash
cd cli
go build ./... && go vet ./... && go test ./...
golangci-lint run          # uses cli/.golangci.yml
./coverage.sh              # the 95% gate over hand-written code
./build.sh                 # cross-compile into dist/
./gen.sh                   # regenerate internal/vaultapi from the keyring's OpenAPI document
```

`internal/vaultapi` is generated with `oapi-codegen` from the keyring's OpenAPI document, which lives with the server (`internal/keyring/api/openapi.json`). Regenerate and commit it when the contract changes.

Layout:

| Path | |
|---|---|
| `main.go`, `auth.go`, `client.go`, `addr.go`, `update.go`, `skill.go` | commands |
| `internal/identity` | the two names and everything that follows from them |
| `internal/config`, `internal/credstore` | `hosts.json`, the keyring and file stores, the carry-over from `dwvault` |
| `internal/oauthdevice`, `internal/restclient` | device login and the hand-written API calls |
| `internal/selfupdate` | finding, verifying and applying a release |
| `internal/vaultapi` | generated API client |
| `skill/` | the agent skill, one text per name |
