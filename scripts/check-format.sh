#!/usr/bin/env bash
set -euo pipefail

if ! command -v gofmt >/dev/null 2>&1; then
  echo 'format: missing gofmt; install the Go toolchain' >&2
  exit 1
fi

if ! files=$(gofmt -l cmd internal); then
  echo 'format: gofmt failed' >&2
  exit 1
fi
if [[ -n "$files" ]]; then
  printf 'format: run gofmt on these files:\n%s\n' "$files" >&2
  exit 1
fi
echo 'format: passes'
