# Wingman

Wingman exchanges WebRTC SDP through DNS or HTTPS. It then carries text through WebRTC.
The inherited baseline has known defects. Built feature records describe observed behavior, not production readiness.

## Specification
Read docs/specs/spec.md. Read the feature file for the active issue.
Keep planned behavior distinct from existing behavior.

## Layout and stack
Go 1.25.0; Pion WebRTC, Pion TURN, and miekg/dns.
cmd/wingman owns the CLI. internal/ owns transport, peer, ICE, TURN, and metrics packages.
Tests live beside their source. scripts/ owns verification tools.

## Commands
- Build: go build ./cmd/wingman
- Run: go run ./cmd/wingman serve
- Test and coverage: bash scripts/coverage.sh
- Race check: go test -race ./...
- Format inspection: gofmt -l cmd internal
- Spec render: python3 docs/specs/render-spec.py

Set GOCACHE to a writable temporary directory when the normal cache is restricted.
The race check currently fails in StubTransport. Foundation repairs that defect before it becomes a required gate.

## Conventions
Use Go package names and colocated tests. Preserve error causes.
Use concise imperative commit subjects. Follow existing fix:, refactor:, and chore: prefixes when applicable.
Read docs/rules/quality.md before source or test changes.
Read docs/rules/ste.md before prose, comments, test names, issues, or PR text.
Read docs/rules/external-apis.md before external interface changes.

## Branch and release model
Merge batch PRs into dev. Promote dev to main only after explicit operator approval.
Keep specifications, issues, and PRs public. Do not commit credential values.

## Filing gate
A finding earns a tracker issue in five cases: behavior, user-visible output, a guard that guards nothing, a blocked epic, or a maintainer ruling.
A wrong product description under docs/specs/ earns a spec-update issue.
Repair every other finding in the change set that found it. File no separate issue.
If a repair changes behavior, stop that repair and file a behavior finding.

Route repairs as follows:
- During issue work: the issue PR, within files it already touches.
- During integration review: a documentation commit on the integration branch before the CI-trigger commit.
- With nothing open: a documentation-only PR into dev. Do not create a tracker issue for that repair.

A changelog entry records a decision at a batch gate.
Do not require a changelog row, spec edit, or accuracy pass per change set or batch member.
A batch that follows the approved plan needs no changelog entry.
