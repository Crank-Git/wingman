# Review evidence and limits

This ledger records project-review `2026-11-04-inherited-dev` against revision `84d825e399169d59a1eec37ac48ff46524bde59d`.

| Route | Count |
|---|---:|
| Findings gathered | 2 |
| Issues filed | 2 |
| Documentation repairs routed | 0 |
| Findings recorded only | 0 |
| Confirmed code audit findings | 0 |

Both filed findings have low severity. This review changes documentation and tests only.

## R1: malformed doctor address

[Issue #120](https://github.com/Crank-Git/wingman/issues/120) tracks address validation and recovery advice.

Command: `wingman doctor --addr not-an-endpoint`.
Exit status: 1. Exact excerpt:

```text
dns_udp: FAIL (0ms) [probe_failed] Check the DNS endpoint and permit UDP traffic to its port.
dns_tcp: FAIL (0ms) [probe_failed] Check the DNS endpoint and permit TCP traffic to its port.
wingman: doctor: a requested check failed
```

The JSON result reports `success=false` with `probe_failed` checks. It does not identify the required `HOST:PORT` format. The [doctor page](doctor.md) gives the current workaround.

## R2: DynUpdate recovery advice

[Issue #121](https://github.com/Crank-Git/wingman/issues/121) tracks safe configuration recovery advice.

A valid fixture key with `--zone wrong.test` exits 1. A wrong secret produces the same error. Exact excerpt:

```text
wingman: peer: publish offer: DynUpdate publish exchange: dns: bad authentication
```

The BIND log independently records `not authoritative for update zone (NOTAUTH)` for the wrong-zone attempt. This server evidence establishes the configuration error. It does not authenticate the response for the client. No packet capture establishes the precise response format. Authentication checks must remain required. The [DynUpdate page](dynupdate.md) lists configuration checks.

## Observed flows

| Flow | Result | Evidence retained here |
|---|---|---|
| First run | Complete | Two demo runs; both receipt directions; host selected pairs. |
| Doctor | Complete with R1 | Signed DNS success; unavailable, silent, missing-key, and wrong-key failures. |
| HTTPS | Complete in Linux | Verified TLS doctor and both CLI receipts; wrong token and untrusted certificate fail. |
| DynUpdate | Complete with R2 | Real BIND CLI exchange, repeated cleanup, and nine passing acceptance checks. |
| Relay | Complete | Actual namespace transfer; both selected pairs report relay; cleanup completes. |
| Operations | Partial | Health, readiness after startup, metrics, authentication, and clean shutdown pass. |

The BIND fixture uses `ubuntu/bind9:9.20-26.04_stable@sha256:8f62529754757e8cb7bbf27f43a1d280f9c5f92c2d30704565242058d8d24872` on `linux/amd64`.
It reports `BIND 9.20.24-1ubuntu0.4-Ubuntu (Stable Release)`.
The existing acceptance harness verifies record preservation and consumed records after its own CLI exchange. The extra operator session verifies receipts and cleanup output.

## Limits

- CLI screenshots are unavailable. The flow pages retain exact terminal excerpts. Three screenshots show actual HTTP endpoint responses.
- Native macOS private-certificate trust blocks the supplementary HTTPS doctor check. An isolated Linux fixture verifies the positive path with `SSL_CERT_FILE=/fixture/tls.crt`.
- The Linux HTTPS fixture needs a private dummy interface for WebRTC host candidates. The initial loopback-only attempt proves no transfer.
- The review does not observe the shutdown readiness 503 transition.
- The HTTPS walkthrough does not stress the retained-byte quota or race replacement against consumption.
- Local checks do not prove public networks, arbitrary NAT topologies, or another authoritative DNS implementation.
- The review uses an existing checkout and cache. It does not prove dependency downloads from a fresh offline machine.
- Demo output contains no detected raw SDP markers or secret fields. Removed credentials prevent an exact-value comparison after cleanup.

Raw credential files, raw SDP, full BIND logs, and fixture binaries do not belong in this manual.

## Repeat the smoke checks

Run these commands from the repository root with Go 1.25.0 and Python 3:

```sh
go test ./cmd/wingman -run '^TestCLIExec$' -count=1 -v
python3 scripts/test_demo.py
```

The CLI suite builds a binary and owns its disposable services. Its authenticated operations test starts the actual `serve` process. It runs signed `doctor --json` against that process and checks health, readiness, and write/poll counter changes. It uses Wingman's custom DNS service, not BIND. No shared server or public endpoint participates.

The existing demo tests cover the two-peer exchange and cleanup. The existing BIND and namespace fixtures retain their separate opt-in checks. See [the release guide](../release.md#local-verification) for the complete verification command and tool prerequisites.
