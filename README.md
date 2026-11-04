# Wingman

**WebRTC / NAT-traversal signaling over DNS.**

DNS is often the last protocol a network lets out. Wingman uses it *only* for
the signaling handshake — publishing an SDP offer and its answer under a
rendezvous name — then switches to a direct P2P connection. Once ICE completes,
DNS is out of the data path.

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
- **`DynUpdate`** (optional): an RFC 2136 dynamic-update backend for environments
  that run a real authoritative server accepting `DNSUPDATE` messages.
- **HTTPS** (optional): a `sig.Transport` backed by an ordinary HTTPS
  `http.Handler` for environments where unsolicited DNS queries are dropped by a
  firewall or ISP. The same room/kind addressing applies, but the whole SDP is
  carried in a single request body, so no chunking is needed.

All three are pluggable behind the same interface. The CLI selects one with
`--signaling dns` (default) or `--signaling https --signaling-url https://…`.

### Polling

Peers poll the authoritative server for not-yet-published names. To keep polling
latency low the negative-caching (SOA minimum) TTL is kept at 0–1 s, and peers
query the authoritative server directly to avoid recursive-resolver negative
caching. See `internal/sig`.

### TURN / STUN (NAT traversal)

Host candidates only reach a direct P2P connection when the peers are on the
same LAN. Behind symmetric NATs the peers need a relay. Wingman configures a
STUN/TURN server through the pion WebRTC engine, which gathers server-reflected
(`srflx`) and relayed (`relay`) candidates automatically.

```
wingman offer --addr 127.0.0.1:PORT --room demo \
  --turn turn:stun.example.com:3478 --user bob --pass s3cret
```

`--turn` is repeatable to list several servers; `--user`/`--pass` supply the
long-term TURN credential. STUN URIs need no credential. The URIs are validated
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
