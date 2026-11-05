#!/usr/bin/env bash
umask 077
secret='namespace-readiness-secret'
printf '%s\n' "$secret" >/tmp/signaling-secret
printf '%s\n' '{"name":"fixture.example.","algorithm":"hmac-sha256.","secret":"bmFtZXNwYWNlLWZpeHR1cmU="}' >/tmp/key.json
/bin-fixture/readiness-probe cert /tmp/tls-cert.pem /tmp/tls-key.pem
ip netns exec server /bin-fixture/wingman serve \
  --listen 10.203.0.1:5353 --zone example.com --dns-tsig-key-file /tmp/key.json \
  --https-listen 10.203.0.1:8443 --tls-cert /tmp/tls-cert.pem --tls-key /tmp/tls-key.pem \
  --signaling-secret-file /tmp/signaling-secret >/tmp/server.log 2>&1 &
server_pid=$!
ready=false
for attempt in {1..100}; do
  if grep -q 'HTTPS listening on https://10.203.0.1:8443' /tmp/server.log; then ready=true; break; fi
  if ! kill -0 "$server_pid" 2>/dev/null; then cat /tmp/server.log >&2; exit 1; fi
  sleep 0.05
done
if [[ "$ready" != true ]]; then echo 'HTTPS listener startup times out' >&2; exit 1; fi
for network in udp tcp; do
  ip netns exec peer-a /bin-fixture/probe "$network" offer
  ip netns exec peer-b /bin-fixture/probe "$network" answer
  ip netns exec peer-a /bin-fixture/probe "$network" confirm
done
base=https://10.203.0.1:8443
ip netns exec peer-a /bin-fixture/readiness-probe status "$base" /tmp/tls-cert.pem /healthz 200
ip netns exec peer-a /bin-fixture/readiness-probe status "$base" /tmp/tls-cert.pem /readyz 200
ip netns exec peer-a /bin-fixture/readiness-probe roundtrip "$base" /tmp/tls-cert.pem "$secret"
kill -TERM "$server_pid"
wait "$server_pid"
server_pid=
echo 'namespace readiness: trusted HTTPS probes and signaling roundtrip pass; server shuts down cleanly'
