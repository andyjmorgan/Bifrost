#!/usr/bin/env bash
# Cross-compile the bifrost CLI into cli/dist/ with checksums.
#
#   ./build.sh                     # linux + darwin, amd64 + arm64
#   VERSION=v0.3.0 ./build.sh      # stamp an explicit version
#   TARGETS="linux/amd64" ./build.sh
#
# Output, the same files a release carries (see .github/workflows/cli-release.yml):
#
#   dist/bifrost-<os>-<arch>   the binaries
#   dist/SHA256SUMS            their checksums, as install.sh and `bifrost update` verify them
#
# The version comes from git. CLI releases are tagged cli-vX.Y.Z (the gateway shares this
# repository, so a bare vX.Y.Z is never the CLI's): at exactly such a tag with a clean tree
# the version is vX.Y.Z, otherwise it is a `git describe` string. Only an exact vX.Y.Z
# counts as a release; `bifrost update` treats anything else as a build from source and
# leaves it alone unless forced.
#
# macOS binaries are not signed or notarized here (the release workflow does that when its
# signing secrets are set). Go ad-hoc signs darwin/arm64 at link time, which is enough to
# run a binary fetched with curl; a browser download is quarantined.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")"

TARGETS="${TARGETS:-linux/amd64 linux/arm64 darwin/amd64 darwin/arm64}"

git_version() {
  local v
  if v=$(git describe --tags --match 'cli-v[0-9]*' --dirty 2>/dev/null); then
    printf '%s\n' "${v#cli-}"
    return
  fi
  # No cli-v* tag reachable yet: name the commit instead.
  v=$(git rev-parse --short HEAD 2>/dev/null) || { printf 'dev\n'; return; }
  if ! git diff --quiet HEAD -- 2>/dev/null; then v="$v-dirty"; fi
  printf 'dev-%s\n' "$v"
}

VERSION="${VERSION:-$(git_version)}"
if [[ "$VERSION" == *[[:space:]\'\"]* ]]; then
  echo "build.sh: VERSION must not contain spaces or quotes: $VERSION" >&2
  exit 1
fi

rm -rf dist
mkdir -p dist
for t in $TARGETS; do
  os="${t%/*}"
  arch="${t#*/}"
  echo "building bifrost-$os-$arch ($VERSION)" >&2
  GOOS="$os" GOARCH="$arch" CGO_ENABLED=0 \
    go build -trimpath -ldflags "-s -w -X main.version=$VERSION" \
    -o "dist/bifrost-$os-$arch" .
done

(
  cd dist
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum bifrost-* >SHA256SUMS
  else
    shasum -a 256 bifrost-* >SHA256SUMS
  fi
)

echo "built into $(pwd)/dist:" >&2
ls -1 dist >&2
if [[ ! "$VERSION" =~ ^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$ ]]; then
  echo "note: $VERSION is not a vX.Y.Z release version; \`bifrost update\` will treat this as a build from source" >&2
fi
