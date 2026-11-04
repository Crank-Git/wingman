# DynUpdate configuration

The CLI accepts `--signaling dynupdate` for the planned RFC 2136 transport.
The constructor validates configuration. Publish replaces a complete TXT RRset through authenticated TCP.
Poll reconstructs authenticated TXT answers. The complete backend remains unshipped until epic #40 completes.

Both `offer` and `answer` require these flags:

- `--addr HOST:PORT`: the authoritative server address with a numeric port from 1 to 65535.
- `--zone ZONE`: an explicit authoritative zone, including when the zone is `example.com`.
- `--dns-tsig-key-file PATH`: the restricted TSIG JSON file.
- `--room NAME`: the shared room.

Hostnames, IPv4 addresses, and bracketed IPv6 addresses are valid server hosts.
Configuration validation performs no DNS lookup or network request.
The constructor uses authenticated TCP exchanges through the existing DNS dialer.
DNS and HTTPS retain the `example.com` zone default.

The TSIG file uses the existing [credential format](deployment.md).
The key requires a canonical name, HMAC-SHA256, and a nonempty base64 secret.
Errors omit the key secret.

## Go interface

`internal/dns.NewDynUpdate(addr, zone string, key *dns.TSIGKey)` returns `(*dns.DynUpdate, error)`.
Load the key with `dns.LoadTSIGKey(path)` before this call.
The constructor validates programmatic configuration before it returns a transport.
It rejects a missing key, an invalid key, an invalid address, or an invalid zone.
It converts the zone to lowercase and removes the final dot.

`DynUpdate` implements `sig.Transport`.
Its `Resolver` uses `dns.Dialer` with TCP, a five-second timeout, and the supplied TSIG key.
Its `Zone` holds the canonical zone.
The constructor retains a key copy for the default dialer and wire checks.
Set transport fields before concurrent use. An injected resolver must provide authenticated TCP exchanges.
Publish rejects a zero-value transport before network access.

## Publish

Publish uses the owner `<room>.<kind>.sig.<zone>.`.
Each TXT record contains `<seq>-<total>:<payload>` with the existing base32 bytes and final CRC trailer.
One UPDATE deletes the old TXT RRset and inserts all new records with TTL one.
TTL controls cache retention. It does not delete authoritative records.
Publish checks the signed UPDATE and expected complete TXT response against the 60 KiB wire budget.
The absolute DNS limit is 65535 bytes. The decoded message limit is 16 KiB.
DynUpdate uses TCP without EDNS0. The response model therefore has no OPT record.
Long names can reduce the effective message limit.
Size failures return `DynUpdateSizeError`. Authenticated DNS rejections return `DynUpdateRcodeError`.
Exchange failures preserve their causes. Unsigned responses and invalid signatures fail through the existing dialer.
The loopback fixture checks the protocol. Issue #45 supplies real BIND interoperability evidence.

## Poll

Poll queries the same owner through authenticated TCP without EDNS0.
It checks the signed query before the exchange and the complete received response before it reads fragments.
The response limit is 60 KiB, including all records and the actual TSIG data.
The absolute DNS limit is 65535 bytes. The decoded message limit is 16 KiB.
Poll accepts unordered TXT records with matching owner, type, class, fragment counts, and CRC.
It rejects duplicate sequences, incomplete RRsets, and malformed fragments immediately.
Atomic RRset replacement makes a nonempty incomplete answer a validation failure.
NXDOMAIN and empty successful answers retry every 250 milliseconds until the context expires.
Other DNS result codes return `DynUpdateRcodeError`. Size failures return `DynUpdateSizeError`.
Cancellation and exchange failures preserve their causes.
After successful validation, Poll attempts cleanup of only the consumed kind's TXT RRset.
If cleanup fails, Poll returns the valid message and calls the optional `OnCleanupFailure func()` once.
The callback receives no error or message. The library does not log the failure or retain mutable error state.
Set the callback before concurrent use. The callback must support concurrent calls.
The CLI prints a fixed warning that suggests explicit cleanup.
Issue #45 supplies the pinned BIND fixture.

## Cleanup

`DynUpdate.Cleanup(ctx, room)` deletes the room's offer and answer TXT RRsets in one signed TCP UPDATE.
It preserves other rooms, owner names, and record types. The `sig.Transport` interface retains only Publish and Poll.
Cleanup requires a valid transport and room. It returns authentication, network, cancellation, and typed DNS rejection errors.
Missing RRsets do not cause an error.

Use a fresh random room for each exchange. TTL one controls cache retention, not authoritative record expiry.
An interrupted exchange can leave authoritative records. Delete those records explicitly:

```sh
wingman cleanup --addr HOST:PORT --zone ZONE --room ROOM --dns-tsig-key-file PATH
```

All four flags are required. Use the same zone, server address, and key file as the interrupted exchange.
The command prints `DynUpdate cleanup complete.` on success. A failure returns a nonzero exit code.
Limit the server's update permissions to TXT records within the signaling subtree.
