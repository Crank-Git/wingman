# Go quality rules

Read these rules before changes to cmd/, internal/, scripts/, or tests.

- Keep Go source formatted with gofmt.
- Return errors with the failed operation. Preserve causes with %w.
- Handle every new error result unless the code states why loss is acceptable.
- Guard every shared mutable field with one documented synchronization rule.
- Make the owner close every connection, listener, timer, and background goroutine.
- Bound every request body and every retained collection reachable from network input.
- Keep credentials and raw SDP out of default logs.
- Use a regression test for each repaired behavior.
- Keep new functions at cyclomatic complexity 20 or below.
- Use the existing package boundary until a second caller needs an abstraction.
- Add configuration only when a caller needs a different value.
- Remove dead code and commented-out code in changed regions.
- State the reason in comments. Remove comments that restate the code.
- Avoid recovery that logs an error and continues with corrupt state.
- Make failed scripts exit nonzero with a cause on stderr.
- Keep tests independent from public internet services.
- Apply format, complexity, vet, race, and coverage checks once Foundation provides them.

Reviewers assign pass or fail per applicable rule. Existing violations outside the changed scope do not authorize unrelated refactors.
