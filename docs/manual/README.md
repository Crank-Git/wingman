# Wingman user manual

Wingman exchanges Session Description Protocol (SDP) data through DNS or HTTPS. WebRTC then carries text between peers, directly or through TURN. You use Wingman through its command-line interface (CLI). This manual records the reviewed behavior of revision `84d825e399169d59a1eec37ac48ff46524bde59d` on 2026-11-04.

Start with [First local exchange](first-run.md).

| Task | Guide |
|---|---|
| Exchange text locally | [First local exchange](first-run.md) |
| Check an endpoint | [Doctor](doctor.md) |
| Exchange text through HTTPS | [HTTPS](https.md) |
| Use an authoritative DNS server | [DynUpdate](dynupdate.md) |
| Prove a TURN transfer | [Relay](relay.md) |
| Check service health and metrics | [Operations](operations.md) |
| Inspect review evidence and limits | [Review ledger](review-ledger.md) |

Commands use `wingman` from your executable search path. Replace uppercase placeholders with your configured values. Use the [deployment guide](../deployment.md#restricted-credentials) to prepare restricted credential files. EOF means end of file. HTTP means Hypertext Transfer Protocol. HTTPS means HTTP over TLS. UDP means User Datagram Protocol. TCP means Transmission Control Protocol. DNS means Domain Name System. TSIG means DNS transaction signature. TLS means Transport Layer Security. TURN means Traversal Using Relays around NAT. See the [project Terms](../specs/spec.md#terms) for other protocol terms.

The CLI has no graphical interface. Exact terminal excerpts replace screenshots for CLI steps. The operations page includes three actual endpoint screenshots.
