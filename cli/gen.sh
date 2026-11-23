#!/usr/bin/env bash
# Regenerate the typed API client (internal/vaultapi/vaultapi.gen.go) from the keyring's
# OpenAPI document. That document is the wire contract and lives with the server, not here;
# pass its path, or leave it to the default, where the keyring module keeps it:
#
#   ./gen.sh [path/to/openapi.json]      (default: ../internal/keyring/api/openapi.json)
#
# The client is committed. Re-run this whenever the contract changes and commit the result.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")"

OAPI_CODEGEN_VERSION="${OAPI_CODEGEN_VERSION:-v2.4.1}"
SPEC="${1:-../internal/keyring/api/openapi.json}"
[[ -f "$SPEC" ]] || { echo "gen.sh: no OpenAPI document at $SPEC" >&2; exit 1; }

go run "github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@${OAPI_CODEGEN_VERSION}" \
  -config oapi-codegen.yaml "$SPEC"
echo "regenerated internal/vaultapi/vaultapi.gen.go from $SPEC" >&2
