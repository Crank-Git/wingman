#!/usr/bin/env bash
set -euo pipefail

cd "$(dirname "$0")/.."

run_stage() {
  local stage="$1"
  shift
  printf 'stage %s: start\n' "$stage"
  if "$@"; then
    printf 'stage %s: passes\n' "$stage"
  else
    local status=$?
    printf 'stage %s: failed (exit %s)\n' "$stage" "$status" >&2
    exit "$status"
  fi
}

check_tools() {
  local tool
  for tool in go gofmt python3; do
    if ! command -v "$tool" >/dev/null; then
      printf 'missing %s; install Go and Python 3, then add their binaries to PATH\n' "$tool" >&2
      return 1
    fi
  done
  if ! command -v gocyclo >/dev/null; then
    echo 'missing gocyclo; run go install github.com/fzipp/gocyclo/cmd/gocyclo@v0.6.0; add its binary directory to PATH' >&2
    return 1
  fi
}

run_stage tools check_tools
if ! work="$(mktemp -d "${TMPDIR:-/tmp}/wingman-verify.XXXXXX")"; then
  echo 'stage temporary-directory: failed; cannot create a temporary directory' >&2
  exit 1
fi
trap 'rm -rf "$work"' EXIT
export GOCACHE="${GOCACHE:-$work/cache}"
run_stage cache mkdir -p "$GOCACHE"
# Read-only module mode makes dependency drift fail the verification command.
export GOFLAGS="${GOFLAGS:-} -mod=readonly"

run_stage format bash scripts/check-format.sh
run_stage complexity python3 scripts/check-complexity.py
run_stage quality-tests python3 -m unittest discover -s scripts -p test_quality.py
run_stage coverage-tests bash scripts/coverage_test.sh
run_stage verify-tests python3 -m unittest discover -s scripts -p test_verify.py
run_stage vet go vet ./...
run_stage coverage bash scripts/coverage.sh
run_stage race go test -race ./...
run_stage build go build -o "$work/wingman" ./cmd/wingman
echo 'verification: passes'
