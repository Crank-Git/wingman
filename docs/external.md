# External configuration

No application environment variables exist in the inherited configuration.
Use CLI help for current flags. Use docs/specs/spec.md for planned configuration.

Credential names: signaling secret, TURN username, TURN password, DNS TSIG key name, DNS TSIG secret.
Planned file references: --signaling-secret-file, --dns-tsig-key-file, --tls-cert, --tls-key.
Keep credential values outside the repository. Examples use placeholders.

No hosted endpoint, deployment account, test account, or owned domain has been supplied.
The local demo uses loopback services. New integration tests create disposable rooms and credentials.
Support and project changes use the public Crank-Git/wingman GitHub repository.
