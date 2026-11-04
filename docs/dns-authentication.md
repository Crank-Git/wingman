# DNS authentication

Use `--dns-tsig-key-file PATH` with `serve`, `offer`, and `answer` to authenticate direct DNS transactions.
TSIG authenticates messages. It does not encrypt SDP.
The server retains its loopback listener default.

The JSON file contains one key with these fields:

| Field | Value |
|---|---|
| `name` | A lowercase fully qualified DNS name with a final dot, such as `wingman.example.` |
| `algorithm` | `hmac-sha256.` |
| `secret` | A nonempty base64 shared secret |

Create the file through a secure local process. Share the same key with the server and both peers.
Set its Unix permissions to `0400` or `0600`.
The file must be regular and owner-readable. Group and other permission bits cause rejection.
The reader rejects files larger than 64 KiB.

```sh
wingman serve --dns-tsig-key-file /secure/wingman-key.json
wingman offer --addr 127.0.0.1:PORT --room demo --dns-tsig-key-file /secure/wingman-key.json
wingman answer --addr 127.0.0.1:PORT --room demo --dns-tsig-key-file /secure/wingman-key.json
```

Replace `PORT` with the server's printed port.
The client signs writes and polls. It requires authenticated replies, including TCP retries after truncated UDP replies.
The server rejects unsigned requests and invalid signatures before storage access.
The signature allows a clock difference of 300 seconds. Synchronize the server and peer clocks.

`dns.LoadTSIGKey` validates the file before listener or transport setup.
Library callers set `Server.TSIGKey` and `Dialer.TSIGKey` before use.
Keep the key immutable during concurrent use.
`credentials.ReadRestrictedFile` supplies the shared restricted-file reader for other credential formats.

The implementation uses [miekg/dns v1.1.73](https://pkg.go.dev/github.com/miekg/dns@v1.1.73) and [RFC 8945](https://www.rfc-editor.org/rfc/rfc8945).
