# Container HTTP server

Requires a Docker environment that advertises native containers and host port
publication. Catalogue-only on PVE1: its Proxmox environment cannot launch this
example. No Docker environment is provisioned there.

This trusted teaching example starts Python’s HTTP server on an isolated internal
network and publishes its port. Use the returned web URL from a host that can
reach the Docker machine. Process isolation is unsuitable for arbitrary hostile
code. The image digest is shared with the verified SQLite web example.
