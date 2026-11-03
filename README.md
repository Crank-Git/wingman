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

### Transport (two backends)

The `sig.Transport` interface has exactly two methods, `Publish` and `Poll`.

- **`WriteOverQuery`** (default): encodes writes as DNS queries that the
  authoritative server interprets as writes. This matches the rendezvous-name
  scheme above and keeps DNS purely a request/response channel.
- **`DynUpdate`** (optional): an RFC 2136 dynamic-update backend for environments
  that run a real authoritative server accepting `DNSUPDATE` messages.

Both are pluggable behind the same interface; `WriteOverQuery` is the primary
implementation.

### Polling

Peers poll the authoritative server for not-yet-published names. To keep polling
latency low the negative-caching (SOA minimum) TTL is kept at 0–1 s, and peers
query the authoritative server directly to avoid recursive-resolver negative
caching. See `internal/sig`.

## Layout

```
internal/sig/    signaling core: framing, chunking, naming, transport interface
cmd/wingman/     peer CLI (builds on the sig package)
scripts/         coverage gate (>= 95%)
```

## Development

- **Language:** Go (pure, no CGO).
- **Tests:** TDD throughout. CI enforces a **95% statement-coverage gate**
  (`scripts/coverage.sh`) plus `go vet`.
- **Branches:** `dev` is the active integration branch; features branch off
  `dev` and merge back via PR. `main` is the protected release branch.

```bash
bash scripts/coverage.sh   # vet + test + 95% coverage gate
```
