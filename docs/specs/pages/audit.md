---
id: audit
title: Repository audit and evidence
---

# Repository audit and evidence

## Inventory
| Source | Claims | Date |
|---|---|---|
| README.md | Protocol, transports, metrics, branch model | 2026-11-04 |
| go.mod and go.sum | Go 1.25.0 and pinned library versions | Current tree |
| .github/workflows/ci.yml | CI for main and dev | Current tree |
| scripts/coverage.sh | Vet, tests, aggregate coverage | Current tree |
| Git history | Twelve commits from framing through TURN and format repairs | 2026-11-03 to 2026-11-04 |
| GitHub API | Public visibility, dev default, unprotected branches | 2026-11-04 |

No prior AGENTS.md, CLAUDE.md, docs/, architecture decisions, schemas, release tags, or environment template exists.
GitHub returned no open issues, open pull requests, or releases during this audit.
The worktree starts on main. Main and origin/dev point to the same commit, e93a2dd.

## Confirmed
- Framing uses base32 labels and a final CRC32 trailer: internal/sig/chunk.go:42-83.
- DNS writes and polls carry message fragments: internal/dns/transport.go:49-165.
- UDP truncation triggers a TCP retry: internal/dns/client.go and tcp_fallback_test.go.
- Peer sessions exchange full SDP before the data channel opens: internal/peer/session.go:244-317.
- TURN configuration reaches Pion: internal/peer/session.go:365-369.
- HTTPS provides the same transport contract: internal/https/https.go.
- CI runs vet and the 95% aggregate coverage gate: scripts/coverage.sh.

## Contradicted
| Claim | Measured behavior | Resolution |
|---|---|---|
| README provides three backends | No DynUpdate implementation exists | Epic 5 implements it |
| README specifies an SOA negative-cache policy | Server has no SOA response path | Epic 4 documents direct-query scope |
| README calls main protected | GitHub reports protected=false | Foundation defines the operator protection procedure |
| serve --zone configures the served zone | The value only changes printed text | Epic 4 enforces the setting |
| Metrics zero value is usable | RecordCandidate writes a nil map | Epic 3 initializes the map |
| TURN comments describe short-term credentials | AuthHandler uses username, realm, and password key derivation | Correct comments with relevant work |

## Undocumented
| Observation | Evidence | Destination |
|---|---|---|
| DNS zone omitted from key | internal/dns/server.go:101,136 | Epic 1 |
| DNS storage has no expiry or capacity | internal/dns/server.go:19-24,103-108 | Epic 1 |
| Query type and class do not guard writes | internal/dns/server.go:77-88 | Epic 1 |
| Poll trusts unrelated fragment identity | internal/dns/transport.go:150-165 | Epic 1 |
| Resolver nil response can panic | internal/dns/transport.go:75-80,135-139 | Epic 1 |
| HTTPS body read has no limit | internal/https/https.go:263-272 | Epic 2 |
| GET reads before removal under separate locks | internal/https/https.go:276-290 | Epic 2 |
| URL can use HTTP with a secret | cmd/wingman/main.go:296-302; internal/https/https.go:69-83 | Epic 2 |
| Session has no public Close | internal/peer/session.go:92-244 | Epic 3 |
| Invalid role waits instead of rejecting | internal/peer/session.go:212-220 | Epic 3 |
| Metrics pointer locking differs between read and write | internal/peer/session.go:132-136,203-210 | Epic 3 |
| Zero-value metrics map can panic | internal/metrics/metrics.go:15-35,67-70 | Epic 3 |
| Server scrape collector receives no updates | cmd/wingman/main.go:163-168 | Epic 4 |
| DNS server binds only loopback | internal/dns/server.go:35-62 | Epic 4 |
| HTTPS handler has no bundled TLS listener | cmd/wingman/main.go:83-94 | Epic 4 |
| Candidate utility reports port zero | internal/ice/gather.go:76-83 | Preserve distinction from Pion |
| CandidatePriority can underflow for invalid components | internal/ice/candidate.go:108-112 | Epic 3 |
| StubTransport has a demonstrated map race | internal/sig/transport.go:31-37 | Foundation |

No TODO, FIXME, HACK, unimplemented stub, or placeholder content beyond the advertised absent backend became a separate finding.
A finding describes code behavior. The author model does not determine its severity.

## Validation
| Command | Result |
|---|---|
| GOCACHE=/tmp/wingman-inherit-go-cache bash scripts/coverage.sh | Pass, 96.3% aggregate coverage |
| GOCACHE=/tmp/wingman-inherit-go-cache go test -race ./... | Fail, peer tests report StubTransport map races |
| Focused go test -race ./internal/peer | Fail, reproduces the same shared transport race |

The audit ran on the installed Go 1.27.3 toolchain. The module declares Go 1.25.0.
The standard test command does not enable the race detector.
Coverage demonstrates executed statements; it does not establish missing security or lifecycle requirements.
The remaining findings come from source inspection unless a test result explicitly states otherwise.

## Foundation audit
| Item | Existing state | Planned gap |
|---|---|---|
| Go project scaffold | Present | None |
| Unit and integration tests | Present | Add defect regressions with each repair |
| CI | Present | Race, format, complexity, toolchain pin |
| Coverage | Present | Remove mutation from verification |
| Branches | dev and main exist | Release guide and operator protection steps |
| Deploy target | No hosted target detected | Local release guide now; remote service guide in Epic 4 |
| Demo data | Rooms are ephemeral | No database seed needed |
| Smoke path | Local DNS peers exist | Script and relay checks in Epic 6 |

## Review round
The user chooses public visibility and dev-and-live releases.
The user prioritizes fixes before additions.
The user requests implementation of the advertised dynamic DNS backend.
The draft interprets that request as RFC 2136 DynUpdate.
