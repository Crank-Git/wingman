# Wingman

**WebRTC / NAT-traversal signaling over DNS.**

DNS is often the last protocol a network lets out. Wingman uses it *only* for
the signaling handshake — publishing an SDP offer and its answer under a
rendezvous name — then switches to a direct P2P connection. Once ICE completes,
DNS is out of the data path.

Read the [deployment guide](docs/deployment.md) for the local demo, authenticated two-peer setup,
TLS, cancellation, firewall ports, TURN fallback, and isolated Linux acceptance test.

```
Peer A  ──offer──►  <room>.<chunk>.offer.sig.example   (DNS)
Peer B  ──answer──► <room>.<chunk>.answer.sig.example  (DNS)
Peer A ◄────────── direct P2P / TURN ──────────────────┘  (no DNS)
```

## How it works

1. **Peer A** creates a WebRTC offer, fragments the SDP, and publishes each
   fragment as a DNS query under `<room>.<seq>-<total>.<payload>.offer.sig.<zone>`.
2. **Peer B** polls for the offer, reassembles it, builds an answer, and
   publishes the answer the same way.
3. **ICE** completes directly (or via TURN). Signaling DNS is now unused.

### DNS server configuration

For an unauthenticated local demo, run `go run ./cmd/wingman serve --local-demo`.
The default listener uses an ephemeral loopback port. The command prints the actual address after the bind succeeds.
`--local-demo` accepts only a literal loopback IP address.

For an authenticated listener, run:

```sh
wingman serve --listen 192.0.2.10:5353 --zone example.com --dns-tsig-key-file /path/to/key.json
```

Use an address assigned to the server. The listener accepts a literal IPv4 or IPv6 address and a numeric port.
UDP and TCP use the same port. Both ports must be reachable directly; this transport does not support recursive DNS resolvers.
The configured zone controls both writes and polls. Zone comparisons ignore letter case and one trailing root dot.
Names for other zones, including subzones, receive `REFUSED` before storage access.

Remote listeners require a valid TSIG key. Peers use the same `--dns-tsig-key-file` setting for signed queries.
The restricted JSON file contains `name`, `algorithm`, and `secret`.
Use a lowercase fully qualified key name, the `hmac-sha256` algorithm, and a nonempty base64 secret.
The file must permit owner reads and deny group and other access. TSIG authenticates DNS messages; it does not encrypt SDP.

The library exposes `Server.ListenAddr`, `Server.Zone`, and `Server.TSIGKey`.
Set these fields before `Start`. Keep them immutable while the server runs.
An empty library zone preserves unrestricted library behavior. The CLI always supplies a zone.

Run the isolated Linux acceptance scenario with:

```sh
bash scripts/namespace-smoke.sh --scenario all
```

The scenario requires Go and a Linux Docker daemon. It builds Linux binaries for the daemon architecture.
It creates a server namespace and two peer namespaces inside a disposable container.
The runtime uses `--network=none` with read-only repository and binary mounts.
The fixture first probes `NET_ADMIN` and `SYS_ADMIN`. If required, it probes a privileged disposable container.
A failed capability probe fails the scenario. The fixture removes its namespaces and container after the run.
`BUILDX_CONFIG` can select an existing writable Buildx directory. By default, the wrapper uses a temporary directory.

### SDP chunking

A real SDP offer is a few KB. DNS labels are limited to 63 octets and use a
DNS-safe base32 alphabet, so the SDP is split into fragments that each fit in a
single label. Each fragment carries its sequence/total, and the final fragment
carries a CRC32 trailer so reassembly can detect corruption. See
`internal/sig/chunk.go`.

### Transport (three backends)

The `sig.Transport` interface has exactly two methods, `Publish` and `Poll`.

- **`WriteOverQuery`** (default): encodes writes as DNS queries that the
  authoritative server interprets as writes. This matches the rendezvous-name
  scheme above and keeps DNS purely a request/response channel.
- **`DynUpdate`** (in progress): an authenticated RFC 2136 backend with Publish, Poll, and scoped cleanup.
  The real BIND fixture remains pending in issue #45. See [DynUpdate configuration](docs/dynupdate.md).
  TTL one controls cache retention; it does not expire authoritative records.
  Use fresh random rooms. After an interrupted exchange, run
  `wingman cleanup --addr HOST:PORT --zone ZONE --room ROOM --dns-tsig-key-file PATH`.
- **HTTPS** (optional): a `sig.Transport` backed by an ordinary HTTPS
  `http.Handler` for environments where unsolicited DNS queries are dropped by a
  firewall or ISP. The same room/kind addressing applies, but the whole SDP is
  carried in a single request body, so no chunking is needed.

All three are pluggable behind the same interface. The CLI selects one with
`--signaling dns` (default), `--signaling dynupdate`, or `--signaling https --signaling-url https://…`.

To enable the bundled TLS listener, supply `--https-listen`, `--tls-cert`, and `--tls-key` to `serve`.
HTTPS stays disabled without `--https-listen`. The certificate and private key use PEM files.
Supply `--signaling-secret-file /path/to/token` to the server and both HTTPS peers.
The file must be a regular owner-readable file with no group or other permissions, such as mode `0600`.
The file holds one nonempty token. Wingman trims surrounding whitespace and rejects internal whitespace and control characters.
Peers also accept `--signaling-secret` for compatibility. Use only one nonempty secret source.
Without a secret file, the server requires `--local-demo` and a literal loopback IP listen address.
The TLS listener uses TLS 1.2 or newer and the system certificate trust policy on clients.
Its header, read, write, and idle timeouts are 5, 15, 15, and 60 seconds.
The TLS listener is independent from `--metrics-addr`.
Both HTTP listeners expose `/healthz` and `/readyz` after all requested listeners bind.
`/healthz` returns 200 after startup. `/readyz` returns 503 when shutdown starts.
For a DNS-only server, set `--metrics-addr 127.0.0.1:9090` to expose these endpoints over HTTP.
Wingman reports actual listener addresses after all requested binds succeed.
If a bind fails, Wingman releases earlier listeners and exits with an error.

The HTTPS transport requires an absolute HTTPS service URL without URL userinfo.
It rejects cross-origin redirects when `--signaling-secret` supplies credentials.
For local development, `--signaling-allow-loopback-http` permits HTTP to loopback addresses or `localhost` only.
The Go transport exposes the same option as `https.Transport.AllowLoopbackHTTP`.
For example, use `--signaling https --signaling-url http://127.0.0.1:8080 --signaling-allow-loopback-http` with a local handler.

The HTTPS handler consumes each message before it writes the response.
Concurrent GET requests for one message return one 200 response; the other requests return 404.
A failed response write still consumes the message.
If a peer loses the response, start a new room exchange.

### Polling

Peers poll the authoritative server for not-yet-published names. To keep polling
latency low the negative-caching (SOA minimum) TTL is kept at 0–1 s, and peers
query the authoritative server directly to avoid recursive-resolver negative
caching. See `internal/sig`.

### Handshake timeout

The `offer` and `answer` commands limit the handshake to 30 seconds.
Use `--handshake-timeout 90s` for a slower network.
The option accepts a positive Go duration, such as `500ms`, `30s`, or `1m30s`.
If the deadline expires, the command closes the Session and reports the cause.
After the handshake succeeds, chat continues until normal exit.

### TURN / STUN (NAT traversal)

Host candidates only reach a direct P2P connection when the peers are on the
same LAN. Behind symmetric NATs the peers need a relay. Wingman configures a
STUN/TURN server through the pion WebRTC engine, which gathers server-reflected
(`srflx`) and relayed (`relay`) candidates automatically.

```
wingman offer --addr 127.0.0.1:PORT --room demo \
  --turn turn:stun.example.com:3478 --user bob --pass-file /path/to/turn-password
```

`--turn` is repeatable to list several servers. `--user` and `--pass-file` supply the long-term TURN credential.
Use `serve --turn-pass-file /path/to/turn-password` for a self-hosted TURN server.
The password file must be a regular owner-readable file with no group or other permissions (for example, mode `0600`).
The file contains one nonempty token. The command removes surrounding whitespace and rejects internal whitespace and control characters.
The direct `--pass` and `--turn-pass` flags remain available.
If both password sources are nonempty, the command rejects the configuration.
STUN URIs need no credential. The URIs are validated
(`internal/turn`) and turned into the pion ICE-server slice that the peer wires
into its `PeerConnection`.

### Metrics

Set `--metrics` on `offer`/`answer` to print Prometheus-format counters for the
run: connection attempts/successes/failures, the gathered-candidate
distribution (`host`, `srflx`, `relay`), handshake latency, and the failure
count. See `internal/metrics`.

## Layout

```
internal/sig/    signaling core: framing, chunking, naming, transport interface
internal/dns/    DNS authoritative server and WriteOverQuery transport
internal/ice/    ICE candidate model, gathering, ranking, connection state
internal/peer/   WebRTC offer/answer/ICE handshake over a sig.Transport
internal/https/  HTTPS signaling transport + http.Handler fallback
internal/turn/   TURN/STUN server config parsing and validation
internal/metrics connection metrics (Prometheus text output)
cmd/wingman/     peer CLI (builds on the sig and peer packages)
scripts/         coverage gate (>= 95%)
```

## Development

- **Tools:** Go 1.25.0, Python 3, and gocyclo v0.6.0.
  The race detector requires a C compiler and CGO support.
- **Checks:** CI checks format, complexity, vet, race, the CLI build, and
  aggregate statement coverage of at least **95%**.
- **Branches:** `dev` is the active integration branch; features branch off
  `dev` and merge back via PR. `main` is the release branch.
  Each promotion to `main` requires explicit operator approval.

```bash
bash scripts/verify.sh
```

Read the [release guide](docs/release.md) for tool setup, required CI checks,
operator branch protection steps, and approved promotion to `main`.
