#!/usr/bin/env bash
set -euo pipefail

script_dir="$(cd "$(dirname "$0")" && pwd)"
fixture="$(mktemp -d "${TMPDIR:-/tmp}/wingman-coverage-test.XXXXXX")"
trap 'rm -rf "$fixture"' EXIT
mkdir -p "$fixture/scripts" "$fixture/bin"
cp "$script_dir/coverage.sh" "$fixture/scripts/coverage.sh"
printf 'module fixture\n' > "$fixture/go.mod"
printf 'fixture checksum\n' > "$fixture/go.sum"
cp "$fixture/go.mod" "$fixture/expected.mod"
cp "$fixture/go.sum" "$fixture/expected.sum"

cat > "$fixture/bin/go" <<'GO'
#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$*" >> "$CALL_LOG"
case "$1" in
  mod)
    printf 'tidy changes the module\n' >> go.mod
    printf 'tidy changes the sums\n' >> go.sum
    ;;
  vet) exit "${VET_STATUS:-0}" ;;
  test) exit "${TEST_STATUS:-0}" ;;
  tool) printf 'total: (statements) %s%%\n' "$COVERAGE" ;;
  *) exit 2 ;;
esac
GO
chmod +x "$fixture/bin/go"
export PATH="$fixture/bin:$PATH"
export CALL_LOG="$fixture/calls"

run_case() {
  local coverage="$1" expected_status="$2" vet_status="$3" test_status="$4"
  local status=0
  : > "$CALL_LOG"
  COVERAGE="$coverage" VET_STATUS="$vet_status" TEST_STATUS="$test_status" \
    bash "$fixture/scripts/coverage.sh" > "$fixture/output" 2>&1 || status=$?
  if [[ "$status" -ne "$expected_status" ]]; then
    cat "$fixture/output" >&2
    echo "coverage $coverage: expected status $expected_status, got $status" >&2
    exit 1
  fi
  if ! cmp -s "$fixture/go.mod" "$fixture/expected.mod" || \
     ! cmp -s "$fixture/go.sum" "$fixture/expected.sum"; then
    echo 'coverage changes module files' >&2
    exit 1
  fi
  if grep -q '^mod ' "$CALL_LOG"; then
    echo 'coverage invokes a module command' >&2
    exit 1
  fi
}

run_case 95.0 0 0 0
grep -Fx 'vet ./...' "$CALL_LOG"
grep -Fx 'test ./... -coverprofile=/tmp/wingman-cover.out' "$CALL_LOG"
grep -Fx 'tool cover -func=/tmp/wingman-cover.out' "$CALL_LOG"
run_case 94.9 1 0 0
grep -Fx 'coverage 94.9%: below the 95% gate' "$fixture/output"
run_case 100.0 0 0 0
run_case 100.0 7 7 0
run_case 100.0 8 0 8
echo 'coverage preserves module files and enforces the gate'
