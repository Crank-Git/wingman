# Deploy Wingman

Wingman uses DNS or HTTPS to exchange SDP. WebRTC then carries text directly or through TURN.
This guide uses one server and two peers. The peers can share a host or run on separate hosts.

## Build and local demo

Install Go 1.25.0. Run from the repository root:

```sh
go build -o ./wingman ./cmd/wingman
./wingman help
./wingman serve --local-demo
```

The server prints its actual DNS address. Replace `PORT` below with that port.
Run each peer in a separate terminal:

```sh
./wingman offer --addr 127.0.0.1:PORT --zone example.com --room demo
./wingman answer --addr 127.0.0.1:PORT --zone example.com --room demo
```

Wait for **both** peers to print `connected`. Type a different message in each terminal.
Each opposite terminal prints `received` and the message.
Press Ctrl-C in each terminal to cancel. Input EOF also ends a peer.
Use a fresh room for each exchange. Concurrent publishers for the same role and room are unsupported.
The explicit `--local-demo` mode permits unauthenticated DNS only on literal loopback addresses.

## Restricted credentials

Generate disposable credentials on a trusted host. This command requires Python 3:

```sh
umask 077
export WINGMAN_FILES="$(mktemp -d)"
python3 - <<'PY'
import base64
import json
import os
from pathlib import Path
import secrets

root = Path(os.environ["WINGMAN_FILES"])
key = {"name": "deployment.example.", "algorithm": "hmac-sha256.",
       "secret": base64.b64encode(secrets.token_bytes(32)).decode("ascii")}
(root / "tsig.json").write_text(json.dumps(key) + "\n")
(root / "https-token").write_text(secrets.token_urlsafe(32) + "\n")
(root / "turn-password").write_text(secrets.token_urlsafe(32) + "\n")
PY
```

Transfer the required files through a trusted channel to each host before use.
Set mode `0600` on each copy. Set mode `0700` on its directory.
The server and both DNS peers need the same TSIG file contents.
HTTPS peers need the server's token. TURN peers need the server's password.
Never place credential values in command arguments or public logs.
Readers reject symbolic links and files larger than 64 KiB.
They require regular, owner-readable files with no group or other permissions.
Token and password files contain one nonempty token. Internal whitespace and control characters are invalid.
TSIG JSON contains a lowercase key name with a final dot, an HMAC-SHA256 algorithm, and a base64 secret.
TSIG authenticates DNS messages. It does not encrypt SDP.

## Authenticated DNS across hosts

Choose an IP address assigned to the server. Replace `192.0.2.10` below with that address.
The example address is reserved for documentation. It does not identify a deployed service.
Set `WINGMAN_FILES` to the credential directory on each host.

On the server:

```sh
./wingman serve --listen 192.0.2.10:5353 --zone example.com \
  --dns-tsig-key-file "$WINGMAN_FILES/tsig.json"
```

On peer A:

```sh
./wingman offer --addr 192.0.2.10:5353 --zone example.com --room deployment-1 \
  --dns-tsig-key-file "$WINGMAN_FILES/tsig.json" --handshake-timeout 30s
```

On peer B:

```sh
./wingman answer --addr 192.0.2.10:5353 --zone example.com --room deployment-1 \
  --dns-tsig-key-file "$WINGMAN_FILES/tsig.json" --handshake-timeout 30s
```

Wait for both `connected` notices before you type messages.
Both peers use the same room, zone, and credential file contents.
Queries outside the configured zone receive `REFUSED` before storage access.
Remote DNS listeners require credentials. Unsigned requests, invalid signatures, and wrong keys fail authentication.
An occupied listener port fails startup. Use the actual addresses printed after successful binds.
DNS requires direct UDP **and** TCP reachability to the configured listener.
TCP carries replies that exceed the UDP limit. Recursive resolver compatibility is not guaranteed.
This setup needs no public DNS delegation because peers query the server address directly.

## Optional HTTPS fallback

Obtain a TLS certificate and private key for the server address or hostname.
The certificate must contain the name or IP address that peers use.
Store the files as `tls-cert.pem` and `tls-key.pem` in the credential directory.
Keep the private key restricted. Configure the certificate chain your clients trust.
Restart the server with the additional TLS listener:

```sh
./wingman serve --listen 192.0.2.10:5353 --zone example.com \
  --dns-tsig-key-file "$WINGMAN_FILES/tsig.json" \
  --https-listen 192.0.2.10:8443 \
  --tls-cert "$WINGMAN_FILES/tls-cert.pem" --tls-key "$WINGMAN_FILES/tls-key.pem" \
  --signaling-secret-file "$WINGMAN_FILES/https-token" \
  --metrics-addr 127.0.0.1:9090
```

On both peers, choose HTTPS when direct DNS is unavailable:

```sh
./wingman offer --signaling https --signaling-url https://192.0.2.10:8443 \
  --signaling-secret-file "$WINGMAN_FILES/https-token" --room deployment-2
./wingman answer --signaling https --signaling-url https://192.0.2.10:8443 \
  --signaling-secret-file "$WINGMAN_FILES/https-token" --room deployment-2
```

Run each command separately. Wait for both connections, then exchange messages.
Go uses system certificate roots. For a private CA on Unix, set `SSL_CERT_FILE` to its PEM file on each peer.
Do not disable certificate verification. The fixture below generates and trusts a disposable certificate for its TLS test.
Both peers must select the same transport. HTTPS does not automatically retry a failed DNS handshake.
The CLI adds `/signaling` to the base URL. TLS startup requires a certificate, key, and restricted token file.

The TLS listener also exposes these endpoints without the signaling token. Restrict access through your network policy.

| Endpoint | Meaning |
|---|---|
| `/healthz` | Returns 200 after startup. |
| `/readyz` | Returns 200 when listeners are ready; returns 503 during shutdown before listener closure. |

The optional `--metrics-addr` exposes `/metrics` on a separate HTTP listener.
It reports shared counters, including DNS writes, polls, rejections, and retained bytes.
The example binds it to loopback. It is independent from TLS and also serves `/healthz` and `/readyz`.

## TURN fallback and firewall ports

Direct WebRTC needs a reachable candidate pair between the peers.
For networks that prevent direct connections, run the bundled TURN service with the authenticated DNS server:

```sh
./wingman serve --listen 192.0.2.10:5353 --zone example.com \
  --dns-tsig-key-file "$WINGMAN_FILES/tsig.json" \
  --turn-server 192.0.2.10:3478 --turn-external 192.0.2.10 \
  --turn-user deployment --turn-pass-file "$WINGMAN_FILES/turn-password"
```

Set `--turn-external` to the address peers can reach. A wildcard bind requires this setting.
Add the same TURN settings to **both** peer commands:

```sh
./wingman offer --addr 192.0.2.10:5353 --zone example.com --room deployment-3 \
  --dns-tsig-key-file "$WINGMAN_FILES/tsig.json" \
  --turn turn:192.0.2.10:3478 --user deployment --pass-file "$WINGMAN_FILES/turn-password" --metrics
./wingman answer --addr 192.0.2.10:5353 --zone example.com --room deployment-3 \
  --dns-tsig-key-file "$WINGMAN_FILES/tsig.json" \
  --turn turn:192.0.2.10:3478 --user deployment --pass-file "$WINGMAN_FILES/turn-password" --metrics
```

Run each peer separately. Wait for both connections, then exchange messages.
Pion gathers relay candidates and can use TURN when direct connectivity fails.
The `--metrics` output reports gathered candidate types. A relay candidate alone does not prove that traffic uses the relay.
The CLI has no forced-relay option. The namespace suite tests direct WebRTC; it does not prove a forced TURN path.

Review these ports before deployment:

| Traffic | Example | Required access |
|---|---|---|
| DNS | UDP and TCP 5353 | Both peers to the server. |
| HTTPS | TCP 8443 | Both peers to the optional TLS listener. |
| TURN/STUN | UDP 3478 | Both peers to the bundled listener. |
| TURN relay | OS-assigned UDP ports | Peers to allocated relay sockets on the server. |
| Direct WebRTC | ICE-selected peer UDP ports | Between peers for direct connectivity. |
| Separate metrics | TCP 9090 on loopback | Local monitoring in this example. |

The bundled TURN server uses Pion TURN v5.1.3 with `RelayAddressGeneratorStatic`.
For ordinary allocations, Pion requests port zero. The operating system assigns the UDP relay port.
The current CLI has no relay port-range setting.
Opening only the configured TURN listener port can leave relay sockets unreachable.
Your firewall and address translation must permit allocated relay ports at the advertised external address.
If your policy requires a fixed relay range, use a separate TURN service that supports that policy.
This guide does not change host firewall rules or relay allocation behavior.

## Cancellation and credential removal

Ctrl-C cancels a peer or server. SIGTERM also stops the server.
The server closes its DNS, TLS, metrics, and TURN resources during shutdown.
1. Stop each peer.
2. Stop the server.
3. Remove disposable credential copies on every host after the test:

```sh
rm -rf -- "$WINGMAN_FILES"
unset WINGMAN_FILES
```

## Reproduce the isolated acceptance test

Install Go, Docker, and access to a Linux Docker daemon. Run from the repository root:

```sh
bash scripts/namespace-smoke.sh --scenario all
```

The wrapper builds Linux binaries for the daemon architecture and one pinned fixture image.
It runs `dns`, `readiness`, and `two-peer` sequentially in separate disposable containers.
Select any one scenario with `--scenario`.
Runtime uses `--network=none` and read-only repository and binary mounts.
The wrapper never mounts the Docker socket or uses host networking.
Image construction can access package repositories. Scenario runtime needs no public service.
Each container creates a private bridge with these namespaces:

| Namespace | Address | Role |
|---|---|---|
| `server` | `10.203.0.1` | DNS on port 5353; optional TLS on port 8443. |
| `peer-a` | `10.203.0.2` | Offer CLI or protocol probe. |
| `peer-b` | `10.203.0.3` | Answer CLI or protocol probe. |

The capability probe first tests `NET_ADMIN` and `SYS_ADMIN`.
If it fails, the wrapper tests a privileged disposable container. A failed second probe fails acceptance.
The fixture never changes host namespaces or firewall rules.
DNS tests signed UDP/TCP and rejects unsigned, wrong-key, and wrong-zone requests.
Readiness tests trusted TLS probes, authenticated signaling, and server shutdown.
The two-peer scenario starts actual `serve`, `offer`, and `answer` processes with a restricted TSIG file.
Both FIFO inputs stay open until both peers connect and receive each other's unique message.
The scenario then sends EOF, joins both peers, sends SIGTERM to the server, and joins it.
Cleanup removes the bridge, namespaces, container, and temporary binaries, including after a failure.

Output records the base digest, fixture image ID, architecture, capability result, scenario results, and cleanup result.
`BUILDX_CONFIG` can select a writable Buildx directory. The default is temporary.
Set `GOCACHE` to a writable temporary directory if the normal Go cache is restricted.
CI runs this aggregate command after full verification. Missing capabilities fail the job; they do not skip acceptance.

Interface references: [Docker container run](https://docs.docker.com/reference/cli/docker/container/run/),
[Pion TURN v5.1.3](https://pkg.go.dev/github.com/pion/turn/v5@v5.1.3),
and [Go 1.25 certificate roots](https://pkg.go.dev/crypto/x509@go1.25.0#SystemCertPool).
