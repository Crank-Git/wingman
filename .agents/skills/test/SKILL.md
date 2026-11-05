---
name: test
description: Run Wingman verification and report results.
---

# Test Wingman

1. Read the test strategy in docs/specs/spec.md.
2. Read docs/release.md for tool prerequisites.
3. Run bash scripts/verify.sh.
4. Run the relevant integration smoke path for transport or peer changes.
5. Report command results and any skipped prerequisites.

The verification command includes format, complexity, vet, coverage, race, and CLI build checks.
