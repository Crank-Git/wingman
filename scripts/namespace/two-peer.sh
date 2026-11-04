#!/usr/bin/env bash
umask 077
scenario_work="$(mktemp -d /tmp/namespace-peer.XXXXXX)"
printf '%s\n' '{"name":"fixture.example.","algorithm":"hmac-sha256.","secret":"bmFtZXNwYWNlLWZpeHR1cmU="}' >"$scenario_work/key.json"

wait_notice() {
  local pid="$1" log="$2" notice="$3"
  for attempt in {1..600}; do
    if grep -Fq "$notice" "$log"; then return; fi
    if ! kill -0 "$pid" 2>/dev/null; then
      echo "two-peer: child exits before $notice" >&2
      cat "$log" >&2
      return 1
    fi
    sleep 0.05
  done
  echo "two-peer: timeout waiting for $notice" >&2
  return 1
}

join_child() {
  local pid="$1" log="$2"
  for attempt in {1..200}; do
    if ! kill -0 "$pid" 2>/dev/null; then
      if wait "$pid"; then return; fi
      echo 'two-peer: child fails during shutdown' >&2
      cat "$log" >&2
      return 1
    fi
    sleep 0.05
  done
  echo 'two-peer: child shutdown times out' >&2
  return 1
}

server_args=()
peer_args=()
if [[ $1 == relay ]]; then
  # The server route remains reachable for both TURN allocations and relayed packets.
  ip -n peer-a route add prohibit 10.203.0.3/32
  ip -n peer-b route add prohibit 10.203.0.2/32
  # TURN control replies need the peer route; relay sockets must not reach host candidates.
  # The minimal image has no protocol name database.
  ip -n server rule add priority 100 ipproto 17 sport 3478 lookup main
  ip -n server rule add priority 101 ipproto 17 sport 5353 lookup main
  ip -n server rule add priority 102 ipproto 6 sport 5353 lookup main
  ip -n server rule add priority 110 to 10.203.0.2/32 prohibit
  ip -n server rule add priority 111 to 10.203.0.3/32 prohibit
  printf '%s\n' 'namespace-turn-password' >"$scenario_work/turn-pass"
  server_args=(--turn-server 10.203.0.1:3478 --turn-user fixture --turn-pass-file "$scenario_work/turn-pass")
  peer_args=(--turn turn:10.203.0.1:3478 --user fixture --pass-file "$scenario_work/turn-pass")
fi

ip netns exec server /bin-fixture/wingman serve "${server_args[@]}" --listen 10.203.0.1:5353 --zone example.com --dns-tsig-key-file "$scenario_work/key.json" >"$scenario_work/server.log" 2>&1 &
server_pid=$!
wait_notice "$server_pid" "$scenario_work/server.log" 'server listening on 10.203.0.1:5353'
mkfifo "$scenario_work/offer.in" "$scenario_work/answer.in"
# Each peer must inherit only its read side so that closing the writer delivers EOF.
exec 3<>"$scenario_work/offer.in" 4<>"$scenario_work/answer.in"
ip netns exec peer-a /bin-fixture/wingman offer --addr 10.203.0.1:5353 --zone example.com --room namespace-cli --dns-tsig-key-file "$scenario_work/key.json" --handshake-timeout 20s "${peer_args[@]}" <"$scenario_work/offer.in" 3>&- 4>&- >"$scenario_work/offer.log" 2>&1 &
offer_pid=$!
ip netns exec peer-b /bin-fixture/wingman answer --addr 10.203.0.1:5353 --zone example.com --room namespace-cli --dns-tsig-key-file "$scenario_work/key.json" --handshake-timeout 20s "${peer_args[@]}" <"$scenario_work/answer.in" 3>&- 4>&- >"$scenario_work/answer.log" 2>&1 &
answer_pid=$!
wait_notice "$offer_pid" "$scenario_work/offer.log" 'offer: connected'
wait_notice "$answer_pid" "$scenario_work/answer.log" 'answer: connected'
printf '%s\n' 'namespace message from peer-a' >&3
printf '%s\n' 'namespace message from peer-b' >&4
wait_notice "$answer_pid" "$scenario_work/answer.log" 'answer: received "namespace message from peer-a"'
wait_notice "$offer_pid" "$scenario_work/offer.log" 'offer: received "namespace message from peer-b"'
if [[ $1 == relay ]]; then
  wait_notice "$offer_pid" "$scenario_work/offer.log" 'selected pair: local=relay remote=relay'
  wait_notice "$answer_pid" "$scenario_work/answer.log" 'selected pair: local=relay remote=relay'
  echo 'namespace relay: both peers receive known text; selected pair: local=relay remote=relay'
fi
exec 3>&- 4>&-
join_child "$offer_pid" "$scenario_work/offer.log"
offer_pid=
join_child "$answer_pid" "$scenario_work/answer.log"
answer_pid=
kill -TERM "$server_pid"
join_child "$server_pid" "$scenario_work/server.log"
server_pid=
echo 'namespace two-peer: both CLI peers connect, receive text, and exit after EOF; server exits after SIGTERM'
