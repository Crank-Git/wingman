#!/usr/bin/env bash
umask 077
printf '%s\n' '{"name":"fixture.example.","algorithm":"hmac-sha256.","secret":"bmFtZXNwYWNlLWZpeHR1cmU="}' >/tmp/key.json

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

ip netns exec server /bin-fixture/wingman serve --listen 10.203.0.1:5353 --zone example.com --dns-tsig-key-file /tmp/key.json >/tmp/server.log 2>&1 &
server_pid=$!
wait_notice "$server_pid" /tmp/server.log 'server listening on 10.203.0.1:5353'
mkfifo /tmp/offer.in /tmp/answer.in
# Each peer must inherit only its read side so that closing the writer delivers EOF.
exec 3<>/tmp/offer.in 4<>/tmp/answer.in
ip netns exec peer-a /bin-fixture/wingman offer --addr 10.203.0.1:5353 --zone example.com --room namespace-cli --dns-tsig-key-file /tmp/key.json --handshake-timeout 20s </tmp/offer.in 3>&- 4>&- >/tmp/offer.log 2>&1 &
offer_pid=$!
ip netns exec peer-b /bin-fixture/wingman answer --addr 10.203.0.1:5353 --zone example.com --room namespace-cli --dns-tsig-key-file /tmp/key.json --handshake-timeout 20s </tmp/answer.in 3>&- 4>&- >/tmp/answer.log 2>&1 &
answer_pid=$!
wait_notice "$offer_pid" /tmp/offer.log 'offer: connected'
wait_notice "$answer_pid" /tmp/answer.log 'answer: connected'
printf '%s\n' 'namespace message from peer-a' >&3
printf '%s\n' 'namespace message from peer-b' >&4
wait_notice "$answer_pid" /tmp/answer.log 'answer: received "namespace message from peer-a"'
wait_notice "$offer_pid" /tmp/offer.log 'offer: received "namespace message from peer-b"'
exec 3>&- 4>&-
join_child "$offer_pid" /tmp/offer.log
offer_pid=
join_child "$answer_pid" /tmp/answer.log
answer_pid=
kill -TERM "$server_pid"
join_child "$server_pid" /tmp/server.log
server_pid=
echo 'namespace two-peer: both CLI peers connect, receive text, and exit after EOF; server exits after SIGTERM'
