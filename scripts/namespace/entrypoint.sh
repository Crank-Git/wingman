#!/usr/bin/env bash
set -euo pipefail
server_pid=
offer_pid=
answer_pid=
cleanup() {
  local status=$?
  trap - EXIT
  local pid
  for pid in "$offer_pid" "$answer_pid" "$server_pid"; do
    if [[ -n "$pid" ]]; then
      kill -TERM "$pid" 2>/dev/null || true
      # A failed scenario must not leave a child that blocks namespace removal.
      for attempt in {1..100}; do
        kill -0 "$pid" 2>/dev/null || break
        sleep 0.05
      done
      kill -KILL "$pid" 2>/dev/null || true
      wait "$pid" 2>/dev/null || true
    fi
  done
  local ns
  for ns in server peer-a peer-b; do
    if ip netns list | awk '{print $1}' | grep -qx "$ns"; then
      ip netns del "$ns" || status=1
    fi
  done
  ip link del fixture-br || status=1
  if ip link show fixture-br >/dev/null 2>&1; then status=1; fi
  if [[ -n "$(ip netns list)" ]]; then
    echo 'namespace cleanup: namespaces remain' >&2
    status=1
  fi
  echo "namespace cleanup: exit $status"
  exit "$status"
}
trap cleanup EXIT
ip link add fixture-br type bridge
ip link set fixture-br up
index=1
for ns in server peer-a peer-b; do
  ip netns add "$ns"
  ip link add "host$index" type veth peer name eth0 netns "$ns"
  ip link set "host$index" master fixture-br
  ip link set "host$index" up
  ip -n "$ns" addr add "10.203.0.$index/24" dev eth0
  ip -n "$ns" link set eth0 up
  ip -n "$ns" link set lo up
  index=$((index + 1))
done
case "${1:-}" in
  dns) source /repo/scripts/namespace/dns.sh ;;
  readiness) source /repo/scripts/namespace/readiness.sh ;;
  two-peer) source /repo/scripts/namespace/two-peer.sh ;;
  *) echo 'unknown namespace scenario' >&2; exit 1 ;;
esac
