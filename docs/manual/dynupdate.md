# Exchange text with DynUpdate

DynUpdate uses an authoritative DNS server that accepts authenticated updates. It differs from Wingman's custom DNS query transport. Configure the zone, endpoint, key, and update policy before use. See the [DynUpdate guide](../dynupdate.md).

Use a fresh room for each exchange. A one-second record TTL controls cache retention; it does not delete authoritative records.

1. Check your endpoint:

   ```sh
   wingman doctor --signaling dynupdate --addr HOST:PORT --zone ZONE --dns-tsig-key-file KEY_FILE --json
   ```

   A successful report includes `dns_tcp`, `auth`, `publication`, `retrieval`, and `cleanup`.

2. Start the offerer in one terminal:

   ```sh
   wingman offer --signaling dynupdate --addr HOST:PORT --zone ZONE \
     --dns-tsig-key-file KEY_FILE --room ROOM --handshake-timeout 20s
   ```

3. Start the answerer in another terminal:

   ```sh
   wingman answer --signaling dynupdate --addr HOST:PORT --zone ZONE \
     --dns-tsig-key-file KEY_FILE --room ROOM --handshake-timeout 20s
   ```

4. Wait for both terminals to show `connected`.
5. Type `review message from offer` in the offerer terminal.
6. Type `review message from answer` in the answerer terminal.

   The review records:

   ```text
   answer: received "review message from offer"
   offer: received "review message from answer"
   ```

7. Send input EOF to each peer after both receipts appear.
8. Clean the room with the same configuration:

   ```sh
   wingman cleanup --addr HOST:PORT --zone ZONE --room ROOM --dns-tsig-key-file KEY_FILE
   ```

   The command reports `DynUpdate cleanup complete.` Repeated cleanup also succeeds.

If an exchange stops early, run the cleanup command before you discard its configuration.

A wrong zone can produce the same authentication error as a wrong key. The error includes these configuration checks:

- Check the configured zone.
- Check the authoritative endpoint.
- Check the key identity and key file.
- Check the server update policy.

The advice does not identify the remote cause. The client requires valid signatures before it trusts a response. See [issue #121](https://github.com/Crank-Git/wingman/issues/121).

The review completes a real BIND exchange and cleanup. Its nine acceptance checks also verify replacement, shuffled records, unauthorized requests, bounds, cancellation, and preservation of unrelated records. The separate CLI session confirms both receipts and repeated cleanup. This evidence covers a disposable local BIND server, not public DNS.
