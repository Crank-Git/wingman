# External interface rules

Read these rules before code uses an external library, service, or CLI.

Read the pinned documentation before the first call. Record the version and source in the PR.
Verify request fields, response fields, errors, limits, timeouts, and authentication.
If an interface contradicts the spec, report the conflict before a behavior change.
Treat third-party examples as examples, rather than proof of this project's required behavior.

Pinned interfaces:
- miekg/dns v1.1.73: https://pkg.go.dev/github.com/miekg/dns@v1.1.73
- Pion WebRTC v4.2.26: https://pkg.go.dev/github.com/pion/webrtc/v4@v4.2.26
- Pion TURN v5.1.3: https://pkg.go.dev/github.com/pion/turn/v5@v5.1.3
- DNS UPDATE: https://www.rfc-editor.org/rfc/rfc2136
- TSIG: https://www.rfc-editor.org/rfc/rfc8945

Verify BIND image version and update-policy documentation before the integration fixture.
No infrastructure or marketplace change follows from documentation access.
