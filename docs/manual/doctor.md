# Check an endpoint with doctor

Doctor publishes probe data in a random disposable room. Use an endpoint that you have permission to test.

1. Run `wingman doctor --help`.

   The help describes the five-second deadline and the additional cleanup allowance of at most one second.

2. Run the authenticated DNS check:

   ```sh
   wingman doctor --signaling dns --addr HOST:PORT --zone ZONE --dns-tsig-key-file KEY_FILE
   ```

   A successful check exits 0. The review records:

   ```text
   doctor transport=dns result=PASS
   dns_udp: PASS (0ms)
   dns_tcp: PASS (0ms)
   auth: PASS (0ms)
   publication: PASS (0ms)
   retrieval: PASS (0ms)
   ```

3. Add `--json` to obtain a structured result.

   The result contains `schema_version: 1`, `transport: dns`, and `success: true`. Each check contains its name, success, duration, and error code.

4. If a check fails, read its suggested correction.

   An unavailable endpoint returns a nonzero exit status. A silent endpoint reaches the deadline and reports `deadline_exceeded`.

> **Known limitation:** A malformed address currently reports network probe failures. Use `HOST:PORT`, such as `127.0.0.1:5353`. See [R1, issue #120](https://github.com/Crank-Git/wingman/issues/120).

For HTTPS, use the signaling URL and token file from the [HTTPS guide](https.md). The separate metrics listener does not provide signaling.
