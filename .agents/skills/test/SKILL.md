---
name: test
description: Run Wingman verification and report results.
---

# Test Wingman

1. Read the test strategy in docs/specs/spec.md.
2. Run bash scripts/coverage.sh.
3. Run go test -race ./... for concurrency changes.
4. Run the relevant integration smoke path for transport or peer changes.
5. Report command results and any skipped prerequisites.

The inherited race suite fails in StubTransport. Do not report that failure as a pass.
After Foundation lands, use scripts/verify.sh as the primary command.
