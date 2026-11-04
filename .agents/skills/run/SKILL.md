---
name: run
description: Start Wingman locally for manual checks.
---

# Run Wingman

1. Read docs/specs/spec.md for configuration.
2. Run go run ./cmd/wingman serve in one terminal.
3. Copy the printed host and port.
4. Run go run ./cmd/wingman offer --addr HOST:PORT --room demo in another terminal.
5. Run go run ./cmd/wingman answer --addr HOST:PORT --room demo in a third terminal.
6. Send a line after the channel opens.
7. Stop each process with Ctrl-C.
