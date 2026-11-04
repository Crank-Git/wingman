#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
if [[ $# != 2 || $1 != --scenario || ( $2 != dns && $2 != readiness && $2 != two-peer && $2 != relay && $2 != all ) ]]; then
  echo 'usage: bash scripts/namespace-smoke.sh --scenario dns|readiness|two-peer|relay|all' >&2
  exit 1
fi
skip() { echo "namespace skip: $*" >&2; exit 77; }
command -v docker >/dev/null || skip 'Docker is unavailable'
command -v go >/dev/null || skip 'Go is unavailable'
arch="$(docker info --format '{{.Architecture}}')" || skip 'Docker daemon is unavailable'
case "$arch" in
  aarch64|arm64) goarch=arm64 ;;
  x86_64|amd64) goarch=amd64 ;;
  *) skip "unsupported Docker architecture: $arch" ;;
esac
[[ "$(docker info --format '{{.OSType}}')" == linux ]] || skip 'a Linux Docker daemon is required'
work="$(mktemp -d "${TMPDIR:-/tmp}/wingman-namespace.XXXXXX")"
name="wingman-namespace-$$"
cleanup() {
  docker rm -f "$name" >/dev/null 2>&1 || true
  rm -rf "$work"
  echo 'namespace host cleanup: complete'
}
trap cleanup EXIT
export BUILDX_CONFIG="${BUILDX_CONFIG:-$work/buildx}"
CGO_ENABLED=0 GOOS=linux GOARCH="$goarch" go build -o "$work/wingman" ./cmd/wingman
CGO_ENABLED=0 GOOS=linux GOARCH="$goarch" go build -tags namespace -o "$work/probe" ./scripts/namespace/probe.go
if [[ $2 == readiness || $2 == all ]]; then
  CGO_ENABLED=0 GOOS=linux GOARCH="$goarch" go build -tags namespace -o "$work/readiness-probe" ./scripts/namespace/readiness_probe.go
fi
image=wingman-namespace:local
docker build -t "$image" scripts/namespace
docker image inspect "$image" --format 'fixture image: {{.Id}} architecture: {{.Architecture}}'
echo 'base digest: ubuntu:noble-20261024.1@sha256:fe81560990fae86dc4743202b2cb4d3df98d0bcf949173cecef15ce1127ef852 (resolved by build)'
caps=(--cap-add NET_ADMIN --cap-add SYS_ADMIN)
if docker run --rm --network=none "${caps[@]}" --entrypoint bash "$image" -c 'ip netns add probe && ip netns exec probe ip link set lo up && ip netns del probe'; then
  echo 'capability probe: NET_ADMIN+SYS_ADMIN passes'
else
  echo 'capability probe: NET_ADMIN+SYS_ADMIN fails; test isolated privileged container' >&2
  caps=(--privileged)
  if ! docker run --rm --network=none "${caps[@]}" --entrypoint bash "$image" -c 'ip netns add probe && ip netns exec probe ip link set lo up && ip netns del probe'; then
    skip 'namespace capability is unavailable; privileged probe fails'
  fi
  echo 'capability probe: privileged passes'
fi
scenarios=("$2")
if [[ $2 == all ]]; then scenarios=(dns readiness two-peer relay); fi
for scenario in "${scenarios[@]}"; do
  echo "namespace scenario: $scenario"
  if docker run --rm --name "$name" --network=none "${caps[@]}" \
    --mount "type=bind,src=$PWD,dst=/repo,readonly" \
    --mount "type=bind,src=$work,dst=/bin-fixture,readonly" "$image" "$scenario"; then
    echo "namespace scenario: $scenario passes"
  else
    echo "namespace scenario: $scenario fails" >&2
    exit 1
  fi
done
