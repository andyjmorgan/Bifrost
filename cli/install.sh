#!/bin/sh
# bifrost installer — the Bifrost credential CLI (formerly dwvault).
#
#   curl -fsSL https://raw.githubusercontent.com/andyjmorgan/Bifrost/main/cli/install.sh | sh
#
# Downloads the right prebuilt `bifrost` binary for your OS/arch from the newest CLI
# release on GitHub, verifies it against the published SHA256SUMS, and installs it.
#
# CLI releases are tagged cli-vX.Y.Z. The repository also holds the gateway, so the
# repository-wide "latest release" is never used: the newest CLI release is the highest
# cli-v tag among the published releases.
#
# Environment overrides (flags win):
#   BIFROST_VERSION       release to install, e.g. v0.3.0 (default: latest)   --version V
#   BIFROST_BIN_DIR       install directory (default: ~/.local/bin, or        --bin-dir DIR
#                         /usr/local/bin if writable and on PATH)
#   BIFROST_REPO          owner/repo to pull from                             --repo OWNER/NAME
#                         (default: andyjmorgan/Bifrost)
#   BIFROST_LINK_DWVAULT  set to 1 to also link `dwvault` to the binary       --link-dwvault
#   BIFROST_NO_VERIFY     set to 1 to skip checksum verification (not recommended)
#   BIFROST_GITHUB_API    GitHub REST API root (default: https://api.github.com)
#   BIFROST_GITHUB_URL    GitHub web root for downloads (default: https://github.com)
set -eu

REPO="${BIFROST_REPO:-andyjmorgan/Bifrost}"
VERSION="${BIFROST_VERSION:-latest}"
BINDIR="${BIFROST_BIN_DIR:-}"
LINK_DWVAULT="${BIFROST_LINK_DWVAULT:-0}"
API="${BIFROST_GITHUB_API:-https://api.github.com}"
GITHUB="${BIFROST_GITHUB_URL:-https://github.com}"

say()  { printf '%s\n' "$*" >&2; }
err()  { printf 'install.sh: %s\n' "$*" >&2; exit 1; }
have() { command -v "$1" >/dev/null 2>&1; }

# Printed from here rather than read back out of $0, which is "sh" under `curl | sh`.
usage() {
  cat >&2 <<'EOF'
Usage: install.sh [--version V] [--bin-dir DIR] [--repo OWNER/NAME] [--link-dwvault]

  --version V        release to install, e.g. v0.3.0 (or BIFROST_VERSION; default: latest)
  --bin-dir DIR      install directory (or BIFROST_BIN_DIR; default: ~/.local/bin,
                     or /usr/local/bin if writable and on PATH)
  --repo OWNER/NAME  GitHub repository to pull from (or BIFROST_REPO;
                     default: andyjmorgan/Bifrost)
  --link-dwvault     also link `dwvault` to the installed binary (or BIFROST_LINK_DWVAULT=1);
                     an existing dwvault binary is left alone

BIFROST_NO_VERIFY=1 skips checksum verification (not recommended).
EOF
}

while [ $# -gt 0 ]; do
  case "$1" in
    --version)      [ $# -ge 2 ] || err "--version needs a value"; VERSION="$2"; shift 2 ;;
    --version=*)    VERSION="${1#--version=}"; shift ;;
    --bin-dir)      [ $# -ge 2 ] || err "--bin-dir needs a value"; BINDIR="$2"; shift 2 ;;
    --bin-dir=*)    BINDIR="${1#--bin-dir=}"; shift ;;
    --repo)         [ $# -ge 2 ] || err "--repo needs a value"; REPO="$2"; shift 2 ;;
    --repo=*)       REPO="${1#--repo=}"; shift ;;
    --link-dwvault) LINK_DWVAULT=1; shift ;;
    -h|--help)      usage; exit 0 ;;
    *)              err "unknown argument '$1' (see --help)" ;;
  esac
done

# --- release source --------------------------------------------------------
# The repository becomes part of a URL, so anything but a plain owner/name is refused.
printf '%s\n' "$REPO" | grep -Eq '^[A-Za-z0-9][A-Za-z0-9-]*/[A-Za-z0-9._-]+$' \
  || err "'$REPO' is not a GitHub owner/name"
# The binary and the checksums that vouch for it come from the same release, so TLS is what
# ties them to the host. Plain http is accepted for a loopback host only, for local testing.
API="${API%/}"
GITHUB="${GITHUB%/}"
for u in "$API" "$GITHUB"; do
  case "$u" in
    https://?*) ;;
    http://localhost|http://localhost[:/]*|http://127.0.0.1|http://127.0.0.1[:/]*) ;;
    *) err "'$u' must use https" ;;
  esac
done

# --- detect platform -------------------------------------------------------
os=$(uname -s | tr '[:upper:]' '[:lower:]')
case "$os" in
  linux)  os=linux  ;;
  darwin) os=darwin ;;
  *) err "unsupported OS '$os' (prebuilt binaries: linux, darwin). Build from source: https://github.com/$REPO/tree/main/cli" ;;
esac

arch=$(uname -m)
case "$arch" in
  x86_64|amd64)  arch=amd64 ;;
  aarch64|arm64) arch=arm64 ;;
  *) err "unsupported architecture '$arch' (prebuilt binaries: amd64, arm64)" ;;
esac

asset="bifrost-$os-$arch"

# --- pick a download tool --------------------------------------------------
if have curl; then
  dl() { curl -fsSL -o "$1" "$2"; }
elif have wget; then
  dl() { wget -qO "$1" "$2"; }
else
  err "need curl or wget to download"
fi

tmp=$(mktemp -d 2>/dev/null || mktemp -d -t bifrost)
trap 'rm -rf "$tmp"' EXIT INT TERM

# --- resolve the version ---------------------------------------------------
strict='^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$'
if [ "$VERSION" = "latest" ]; then
  dl "$tmp/releases.json" "$API/repos/$REPO/releases?per_page=100" \
    || err "could not list the releases of $REPO (is the repository public, and are you online?)"
  # Highest cli-vX.Y.Z among the listed tags, compared numerically. A tag with any suffix
  # (cli-v1.0.0-rc1) does not match, and drafts are not listed to anonymous callers.
  VERSION=$(grep -o '"tag_name"[[:space:]]*:[[:space:]]*"cli-v[0-9][0-9.]*"' "$tmp/releases.json" \
    | sed 's/.*"cli-\(v[0-9.]*\)"$/\1/' \
    | grep -E "$strict" \
    | sort -t. -k1.2,1n -k2,2n -k3,3n \
    | tail -n 1 || true)
  if [ -z "$VERSION" ]; then
    # Not an error: the repository simply has no CLI release yet. Say so and stop cleanly.
    say "No bifrost release has been published yet at $GITHUB/$REPO/releases"
    say "(CLI releases are tagged cli-vX.Y.Z). Nothing was installed."
    say ""
    say "To build from source instead:"
    say "  git clone $GITHUB/$REPO.git && cd $(basename "$REPO")/cli && ./build.sh"
    exit 0
  fi
else
  VERSION="${VERSION#cli-}"
fi
# The version becomes a path segment, so only a strict one passes.
printf '%s\n' "$VERSION" | grep -Eq "$strict" \
  || err "'$VERSION' is not a release version (want vMAJOR.MINOR.PATCH)"
src="$GITHUB/$REPO/releases/download/cli-$VERSION"

# --- choose install dir ----------------------------------------------------
if [ -z "$BINDIR" ]; then
  case ":$PATH:" in
    *:/usr/local/bin:*) [ -w /usr/local/bin ] && BINDIR=/usr/local/bin ;;
  esac
  [ -z "$BINDIR" ] && BINDIR="$HOME/.local/bin"
fi

# --- download --------------------------------------------------------------
say "Downloading $asset ($VERSION)…"
dl "$tmp/$asset" "$src/$asset" || err "download failed: $src/$asset"

# --- verify checksum -------------------------------------------------------
# Verification fails closed: a binary that cannot be checked is not installed.
if [ "${BIFROST_NO_VERIFY:-0}" = "1" ]; then
  say "warning: BIFROST_NO_VERIFY=1, installing without checksum verification"
else
  dl "$tmp/SHA256SUMS" "$src/SHA256SUMS" \
    || err "could not fetch $src/SHA256SUMS; refusing to install an unverified binary (BIFROST_NO_VERIFY=1 overrides)"
  # `sha256sum` writes "<sum>  <name>", or "<sum> *<name>" in binary mode.
  want=$(awk -v n="$asset" '$2 == n || $2 == "*" n { print $1; exit }' "$tmp/SHA256SUMS")
  [ -n "$want" ] || err "$asset is not listed in SHA256SUMS; refusing to install an unverified binary"
  if have sha256sum; then got=$(sha256sum "$tmp/$asset" | awk '{print $1}')
  elif have shasum;  then got=$(shasum -a 256 "$tmp/$asset" | awk '{print $1}')
  else err "no sha256 tool found (sha256sum or shasum); refusing to install an unverified binary (BIFROST_NO_VERIFY=1 overrides)"
  fi
  [ "$got" = "$want" ] || err "checksum mismatch for $asset (expected $want, got $got)"
  say "Checksum OK."
fi

# --- install ---------------------------------------------------------------
chmod +x "$tmp/$asset"
mkdir -p "$BINDIR" 2>/dev/null || true
SUDO=""
if [ ! -w "$BINDIR" ]; then
  have sudo || err "cannot write to $BINDIR; set BIFROST_BIN_DIR to a writable directory"
  say "Installing to $BINDIR (needs sudo)…"
  SUDO=sudo
  $SUDO mkdir -p "$BINDIR"
fi
$SUDO mv "$tmp/$asset" "$BINDIR/bifrost"
say "Installed bifrost $VERSION to $BINDIR/bifrost"

# --- optional compatibility name -------------------------------------------
# Run as `dwvault`, the binary keeps that CLI's address and stored login, so scripts written
# for it carry on. A real dwvault binary is never replaced: only a missing name or an
# existing symlink is (re)pointed.
if [ "$LINK_DWVAULT" = "1" ]; then
  link="$BINDIR/dwvault"
  if [ -L "$link" ] || [ ! -e "$link" ]; then
    $SUDO ln -sfn bifrost "$link"
    say "Linked $link -> bifrost"
  else
    say "warning: $link is an existing dwvault binary; it was left as it is."
    say "         To switch it to bifrost: mv '$link' '$link.old' && ln -s bifrost '$link'"
  fi
fi

# --- PATH hint -------------------------------------------------------------
case ":$PATH:" in
  *":$BINDIR:"*) : ;;
  *) say ""
     say "  $BINDIR is not on your PATH. Add it, e.g.:"
     say "    echo 'export PATH=\"$BINDIR:\$PATH\"' >> ~/.profile && . ~/.profile"
     ;;
esac

say ""
say "Done. Next steps:"
say "  bifrost --version"
say "  bifrost auth status           # an existing dwvault login is picked up; otherwise: bifrost auth login"
say "  bifrost credentials list      # see your credentials"
