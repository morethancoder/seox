#!/usr/bin/env bash
# Build ./seox, or `go install` it with the arg "install".
set -euo pipefail
cd "$(dirname "$0")/.."
source scripts/_lib.sh
version="${VERSION:-$(git describe --tags --always --dirty 2>/dev/null || echo dev)}"
ldflags="-s -w -X github.com/morethancoder/seox/internal/cli.Version=${version}"
if [ "${1:-}" = "install" ]; then
  go install -ldflags "$ldflags" ./cmd/seox
  ok "installed seox ${version} into $(go env GOBIN || true)$( [ -z "$(go env GOBIN)" ] && echo "$(go env GOPATH)/bin")"
else
  go build -ldflags "$ldflags" -o seox ./cmd/seox
  ok "built ./seox ${version}"
fi
