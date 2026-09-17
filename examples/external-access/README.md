# Independent access enforcement fixture

Configure ID `org.example.access`, kind `fixture`, access protocol 1.2, endpoint schema `fixture.endpoint`, and explicit `org.example.echo`/`org.example.network` target bindings. Pass a protected `--state` journal and a loopback `--listen` endpoint for the host WebSocket proxy.

The fixture stores only session issuance markers; launch/network credentials and callback leases stay in memory. WebSocket and downloaded TCP echo access are authorized through core callbacks. The serving process enforces the current lease deadline, synchronously closes sockets on revocation and fails closed during a core outage. Restart has no active sockets or credentials. It is a conformance fixture, not WireGuard, a VPN or an SSH server. See [author examples](../../docs/plugins/extensions.md).
