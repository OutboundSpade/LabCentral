# Container HTTP server

Requires a Docker environment that advertises native containers and host port
publication. For Proxmox VM isolation, use the
[dedicated-VM example](../proxmox-containers/lab.yaml); host port publication
is outside that runtime’s supported subset.

This trusted teaching example starts Python’s HTTP server on an isolated internal
network and publishes its port. Use the returned web URL from a host that can
reach the Docker machine. Process isolation is unsuitable for arbitrary hostile
code. The image digest is shared with the verified SQLite web example.
