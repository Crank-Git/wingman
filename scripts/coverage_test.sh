#!/usr/bin/env bash
set -euo pipefail

script_dir="$(cd "$(dirname "$0")" && pwd)"
fixture="$(mktemp -d "${TMPDIR:-/tmp}/wingman-coverage-test.XXXXXX")"
trap 'rm -rf "$fixture"' EXIT
mkdir -p "$fixture/scripts" "$fixture/bin" "$fixture/tmp"
export TMPDIR="$fixture/tmp"
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
  test)
    profile="${3#-coverprofile=}"
    printf '%s\n' "$profile" > "$CALL_LOG.profile"
    if [[ -n "${BARRIER:-}" ]]; then
      touch "$BARRIER/$INVOCATION"
      for ((attempt=0; attempt<500; attempt++)); do
        [[ -f "$BARRIER/first" && -f "$BARRIER/second" ]] && break
        sleep 0.01
      done
      [[ -f "$BARRIER/first" && -f "$BARRIER/second" ]] || exit 9
    fi
    # Keep a pre-patch run from changing another worker's shared profile.
    if [[ "$profile" == "$TMPDIR/"* ]]; then
      printf '%s\n' "$COVERAGE" > "$profile"
    fi
    exit "${TEST_STATUS:-0}"
    ;;
  tool)
    profile="${3#-func=}"
    [[ "$profile" == "$(cat "$CALL_LOG.profile")" ]] || exit 10
    [[ "$profile" == "$TMPDIR/"* ]] || exit 11
    printf 'total: (statements) %s%%\n' "$(cat "$profile")"
    exit "${COVER_STATUS:-0}"
    ;;
  *) exit 2 ;;
esac
GO
chmod +x "$fixture/bin/go"
export PATH="$fixture/bin:$PATH"
export CALL_LOG="$fixture/calls"

mkdir "$fixture/barrier"
BARRIER="$fixture/barrier" INVOCATION=first COVERAGE=95.0 CALL_LOG="$fixture/first" \
  bash "$fixture/scripts/coverage.sh" > "$fixture/first.output" 2>&1 &
first_pid=$!
BARRIER="$fixture/barrier" INVOCATION=second COVERAGE=94.9 CALL_LOG="$fixture/second" \
  bash "$fixture/scripts/coverage.sh" > "$fixture/second.output" 2>&1 &
second_pid=$!
first_status=0
second_status=0
wait "$first_pid" || first_status=$?
wait "$second_pid" || second_status=$?
if [[ "$(cat "$fixture/first.profile")" == "$(cat "$fixture/second.profile")" ]]; then
  echo 'concurrent coverage commands use the same profile' >&2
  exit 1
fi
if [[ "$first_status" -ne 0 || "$second_status" -ne 1 ]] || \
   ! grep -Fxq 'coverage 95.0%: passes (>= 95% gate)' "$fixture/first.output" || \
   ! grep -Fxq 'coverage 94.9%: below the 95% gate' "$fixture/second.output"; then
  cat "$fixture/first.output" "$fixture/second.output" >&2
  echo 'each coverage calculation must read its own profile' >&2
  exit 1
fi
for invocation in first second; do
  if [[ -e "$(cat "$fixture/$invocation.profile")" ]]; then
    echo 'concurrent coverage leaves a temporary profile' >&2
    exit 1
  fi
done

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
  if [[ -n "$(find "$TMPDIR" -type f -print)" ]]; then
    echo 'coverage leaves a temporary profile' >&2
    exit 1
  fi
}

run_case 95.0 0 0 0
grep -Fx 'vet ./...' "$CALL_LOG"
profile="$(cat "$CALL_LOG.profile")"
grep -Fx "test ./... -coverprofile=$profile" "$CALL_LOG"
grep -Fx "tool cover -func=$profile" "$CALL_LOG"
run_case 94.9 1 0 0
grep -Fx 'coverage 94.9%: below the 95% gate' "$fixture/output"
run_case 100.0 0 0 0
run_case 100.0 7 7 0
run_case 100.0 8 0 8
COVER_STATUS=6 run_case 100.0 6 0 0
echo 'coverage isolates profiles, removes profiles, preserves module files, and enforces the gate'
