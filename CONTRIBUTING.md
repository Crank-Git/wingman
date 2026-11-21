# Contributing to Wingman

Read [AGENTS.md](AGENTS.md) first. It holds the commands, the conventions, and the rules for prose.

## Tools

- Go 1.25.0, Python 3, and gocyclo v0.6.0.
- The race detector requires a C compiler and CGO support.

## Verification

Run the full verification from the repository root:

```bash
bash scripts/verify.sh
```

CI checks format, complexity, vet, race, the CLI build, and aggregate statement coverage of at least **95%**.
CI also runs the namespace acceptance scenario and the BIND acceptance check.

## Branches

- `dev` is the active integration branch. Features branch off `dev` and merge back through a PR.
- `main` is the release branch. Each promotion to `main` requires explicit operator approval.

Read the [release guide](docs/release.md) for tool setup, required CI checks, operator branch protection steps, and approved promotion to `main`.

## Layout

```
internal/sig/    signaling core: framing, chunking, naming, transport interface
internal/dns/    DNS authoritative server and WriteOverQuery transport
internal/ice/    ICE candidate model, gathering, ranking, connection state
internal/peer/   WebRTC offer/answer/ICE handshake over a sig.Transport
internal/https/  HTTPS signaling transport + http.Handler fallback
internal/turn/   TURN/STUN server config parsing and validation
internal/turnserver/  self-hosted TURN/STUN server (pion/turn)
internal/credentials/ restricted credential file reader
internal/metrics connection metrics (Prometheus text output)
cmd/wingman/     peer CLI (builds on the sig and peer packages)
scripts/         coverage gate (>= 95%)
```
