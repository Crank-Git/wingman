#!/usr/bin/env bash
# Enforces the project's 95% statement-coverage gate across all packages.
set -euo pipefail

cd "$(dirname "$0")/.."

go vet ./...

profile="$(mktemp "${TMPDIR:-/tmp}/wingman-cover.XXXXXX")"
trap 'rm -f "$profile"' EXIT

go test ./... -coverprofile="$profile"

total="$(go tool cover -func="$profile" | awk '/^total:/{print $3}')"
pct="${total%\%}"

if awk -v p="$pct" 'BEGIN {exit !(p + 0 >= 95)}'; then
  echo "coverage ${total}: passes (>= 95% gate)"
  exit 0
fi

echo "coverage ${total}: below the 95% gate" >&2
exit 1
