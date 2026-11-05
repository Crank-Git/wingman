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

A malformed DNS address returns a `configuration` result before doctor sends probes. Use `HOST:PORT`, such as `127.0.0.1:5353` or `[::1]:5353`.
The host and port must be present. The port can be a number or a service name, such as `domain`.
The text result explains the format. The JSON result reports `error_code: configuration`. Both commands exit with a nonzero status.

For HTTPS, use the signaling URL and token file from the [HTTPS guide](https.md). The separate metrics listener does not provide signaling.
