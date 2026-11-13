#!/usr/bin/env bash
# Check the tools seox needs to build, then ask seox itself.
set -euo pipefail
cd "$(dirname "$0")/.."
source scripts/_lib.sh
require_cli go "brew install go"
go run ./cmd/seox doctor --human
