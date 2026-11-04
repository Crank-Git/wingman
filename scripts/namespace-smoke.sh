#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
if [[ $# != 2 || $1 != --scenario || $2 != dns ]]; then
  echo 'usage: bash scripts/namespace-smoke.sh --scenario dns' >&2
  exit 1
fi
arch="$(docker info --format '{{.Architecture}}')"
case "$arch" in
  aarch64|arm64) goarch=arm64 ;;
  x86_64|amd64) goarch=amd64 ;;
  *) echo "unsupported Docker architecture: $arch" >&2; exit 1 ;;
esac
[[ "$(docker info --format '{{.OSType}}')" == linux ]] || { echo 'namespace smoke requires a Linux Docker daemon' >&2; exit 1; }
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
    echo 'namespace capability unavailable: privileged probe fails' >&2
    exit 1
  fi
  echo 'capability probe: privileged passes'
fi
docker run --rm --name "$name" --network=none "${caps[@]}" \
  --mount "type=bind,src=$PWD,dst=/repo,readonly" \
  --mount "type=bind,src=$work,dst=/bin-fixture,readonly" "$image" "$2"
