# Go and Pion

Date: 2026-11-04

Status: accepted

## Context
Reconstructed from go.mod and internal/peer on 2026-11-04. The original rationale is unknown.

## Decision
Retain Go with Pion WebRTC and TURN.

## Consequences
The CLI needs no separate application runtime. Library upgrades require network regression tests.
