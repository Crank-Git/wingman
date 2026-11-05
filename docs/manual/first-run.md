# First local exchange

You need Go 1.25.0 and Python 3. Run commands from the repository root.

1. Run `go version`.
2. Run `python3 --version`.
3. Run `bash scripts/demo.sh --help`.

   The help describes a disposable authenticated DNS server and a 30-second exchange deadline.

4. Run `bash scripts/demo.sh`.

   The script builds Wingman and starts two peers. Each peer receives the other peer's text.

   ```text
   selected pair: local=host remote=host
   offer: connected
   offer: received "demo message from answer"
   ```

   ```text
   selected pair: local=host remote=host
   answer: connected
   answer: received "demo message from offer"
   demo: both peers receive known text; temporary credentials and children are removed
   ```

5. Check the command's exit status.

   Status 0 means the demo completes. The script removes its temporary credentials and stops its children.

If you already have a binary, run `bash scripts/demo.sh --binary /path/to/wingman`.
Use `--timeout 60` if you need a longer deadline.

The review completes two demo runs. Both runs report host candidates in the selected pair. The [relay guide](relay.md) covers TURN separately.
