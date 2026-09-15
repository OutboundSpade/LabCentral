# LabCentral

Create isolated virtual labs from reusable templates. Students launch their own
environments and open browser consoles; instructors launch classroom batches;
administrators manage workspaces, people, provider access and limits.

LabCentral runs as a Go control plane with a built-in web interface, SQLite or
PostgreSQL, and separate infrastructure and access plugins. Docker supports
trusted local development; Proxmox supports qualified VM environments.

## Get started

- **Use LabCentral:** [user guide](docs/users/README.md) and [local setup](docs/users/local-setup.md).
- **Operate a deployment:** [operator guides](docs/operations/README.md).
- **Build a plugin:** [plugin author guide](docs/plugins/README.md).
- **Develop LabCentral:** [contributor guide](docs/development/README.md).
- **Work as an agent:** [agent instructions](AGENTS.md), then [current state](PROJECT_STATE.md).

See the [documentation index](docs/README.md) for contracts and design references.

## Run locally

With the Go version in `go.mod`, Bun and Docker installed:

```sh
make run
```

Open [LabCentral](http://127.0.0.1:8080/). This builds the UI and binaries before
starting a loopback-only, unauthenticated development instance. Use trusted images
only: ordinary Docker containers do not isolate arbitrary hostile workloads.
See [local setup](docs/users/local-setup.md) for prerequisites and Compose.

## Project status

Classroom workflows, authentication, persistence and plugin integration are
implemented. Production load/soak, failover/recovery and release approval gates
remain open. Exact-target Proxmox qualification is required for ordinary writes;
a historical testbed result does not qualify another build. See
[current state](PROJECT_STATE.md) and [compatibility](docs/compatibility.md).
