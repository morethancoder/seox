#!/usr/bin/env bash
# Remove the built binary, dist/ and Go's test cache for this module; print what was freed.
set -euo pipefail
cd "$(dirname "$0")/.."
source scripts/_lib.sh
paths=(seox dist coverage.out)
total=0
for p in "${paths[@]}"; do
  [ -e "$p" ] || continue
  kb=$(du -sk "$p" 2>/dev/null | awk '{print $1}')
  rm -rf "$p"
  ok "removed $p (${kb} KB)"
  total=$((total + kb))
done
go clean -testcache && ok "cleared go test cache"
say "freed ${total} KB"
