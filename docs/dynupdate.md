# DynUpdate configuration

The CLI accepts `--signaling dynupdate` for the planned RFC 2136 transport.
Issue #41 supplies configuration validation. Issues #42 and #43 supply Publish and Poll.
Until those issues complete, these operations return an error.

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
Set the transport fields and key fields before concurrent use.
The configuration slice does not establish protocol or BIND interoperability.
