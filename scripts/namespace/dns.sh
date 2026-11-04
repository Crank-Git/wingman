#!/usr/bin/env bash
umask 077
printf '%s\n' '{"name":"fixture.example.","algorithm":"hmac-sha256.","secret":"bmFtZXNwYWNlLWZpeHR1cmU="}' >/tmp/key.json
ip netns exec server /bin-fixture/wingman serve --listen 10.203.0.1:5353 --zone example.com --dns-tsig-key-file /tmp/key.json >/tmp/server.log 2>&1 &
server_pid=$!
ready=false
for attempt in {1..100}; do
  if grep -q 'server listening on 10.203.0.1:5353' /tmp/server.log; then ready=true; break; fi
  if ! kill -0 "$server_pid" 2>/dev/null; then cat /tmp/server.log >&2; exit 1; fi
  sleep 0.05
done
if [[ "$ready" != true ]]; then echo 'DNS listener startup times out' >&2; exit 1; fi
for network in udp tcp; do
  ip netns exec peer-a /bin-fixture/probe "$network" offer
  ip netns exec peer-b /bin-fixture/probe "$network" answer
  ip netns exec peer-a /bin-fixture/probe "$network" confirm
done
echo 'namespace DNS: two peers exchange signed UDP/TCP messages; negative requests fail'
