# Service trust and credentials

Date: 2026-11-04

Status: accepted

## Context
Reconstructed from HTTPS Secret and TURN AuthHandler on 2026-11-04. DNS authentication is absent today.

## Decision
Record existing optional service credentials. Planned direct DNS transactions use TSIG.

## Consequences
Peers trust the configured service. DNS payloads remain visible without a separate encrypted transport.
