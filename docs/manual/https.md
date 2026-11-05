# Exchange text through HTTPS

Prepare a certificate for the signaling address. Configure a certificate chain that each peer trusts. Store the token in a restricted file with mode `0600`.

1. Start your local server:

   ```sh
   wingman serve --listen 127.0.0.1:53530 --local-demo --zone example.test \
     --https-listen 127.0.0.1:8443 --tls-cert CERT_FILE --tls-key KEY_FILE \
     --signaling-secret-file TOKEN_FILE
   ```

   The server prints its DNS and HTTPS addresses. The HTTPS endpoint requires the token even with `--local-demo`.

2. Check the HTTPS endpoint:

   ```sh
   wingman doctor --signaling https --signaling-url https://127.0.0.1:8443 \
     --signaling-secret-file TOKEN_FILE --json
   ```

   The `tls`, `auth`, `publication`, and `retrieval` checks report success.

3. Start the offerer in one terminal:

   ```sh
   wingman offer --signaling https --signaling-url https://127.0.0.1:8443 \
     --signaling-secret-file TOKEN_FILE --room https-knowntext --handshake-timeout 8s
   ```

4. Start the answerer in another terminal:

   ```sh
   wingman answer --signaling https --signaling-url https://127.0.0.1:8443 \
     --signaling-secret-file TOKEN_FILE --room https-knowntext --handshake-timeout 8s
   ```

5. Wait for both terminals to show `connected`.
6. Type `https review text from offer` in the offerer terminal.
7. Type `https review text from answer` in the answerer terminal.

   The review records both receipts:

   ```text
   offer: received "https review text from answer"
   answer: received "https review text from offer"
   ```

8. Press Ctrl-C in each peer terminal after both receipts appear.
9. Press Ctrl-C in the server terminal.

> **Known limitation:** The review's native macOS Go client does not trust the private certificate through `SSL_CERT_FILE`. Configure platform certificate trust before use.

The positive review runs in an isolated Linux container. It sets `SSL_CERT_FILE=/fixture/tls.crt` on each client and verifies TLS. A private dummy interface supplies local WebRTC host candidates. A loopback-only attempt reaches the handshake deadline and provides no successful exchange evidence.

An incorrect token returns `401 Unauthorized`. An untrusted certificate fails the TLS check. Remote plain HTTP fails before signaling. The review also confirms request size and room validation, plus one successful consumer among two concurrent requests. It does not stress the storage quota or the replacement race.
