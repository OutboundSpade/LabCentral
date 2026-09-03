# LabCentral

LabCentral is a modular client-server platform for declarative virtual labs, with cybersecurity classrooms as its primary production use case. Its design-only Phase 0, architecture-foundation Phase 0B, and packaged Docker laptop vertical slice are complete. The repository now includes the typed Go core, strict renderer, durable SQLite operations, a separately supervised Docker provider, the Huma/OpenAPI API, a content-addressed filesystem artifact adapter, an embedded static SvelteKit/DaisyUI interface, and native/Docker/Compose laptop distributions.

Phase 2's single-operator Proxmox deployment is complete. Qualified release candidate `0.2.0-rc5` provides fixed-node Linux QEMU full-clone execution behind a production mutation gate, a separately supervised provider, deterministic Linux/amd64 packaging, and a hardened loopback-only systemd service. Proxmox writes require signed evidence for the exact target configuration, live mutation inventory, PVE build, provider binary/source tree, reviewed topology, permissions, T3 case matrix, and cleanup record; configuration alone cannot enable them. The native release includes the Proxmox provider, offline review utility, configuration examples, and the complete [deployment runbook](docs/operations/proxmox-deployment.md).

Start with [`PROJECT_STATE.md`](PROJECT_STATE.md), then follow [`AGENTS.md`](AGENTS.md). The architecture baseline is in [`docs/design/architecture.md`](docs/design/architecture.md), accepted decisions are in [`docs/adr/`](docs/adr/), and machine-readable progress is in [`design/state.yaml`](design/state.yaml) and [`design/checklist.yaml`](design/checklist.yaml).

The implementation-ready design includes three complete examples under [`examples/`](examples/), an executable Lab authoring schema and canonical fixtures under [`schemas/`](schemas/), an accepted plugin v1 descriptor baseline under [`api/plugin/v1/`](api/plugin/v1/), and machine-readable conformance catalogs under [`design/contracts/`](design/contracts/).

## Run the Docker laptop slice

Prerequisites are Go 1.27.1, Bun 1.3 or newer, and a local Docker Engine exposing API 1.44 or newer. Build both binaries, pin the provider executable checksum, and start the loopback-only server with:

```sh
make build
make run
```

Open <http://127.0.0.1:8080/>. LabCentral stores local state beneath `./labcentral-data`, launches `lab-provider-docker` as a separate process over a private Unix socket, and refuses a non-loopback no-auth listener unless the exact unsafe flag is supplied.

Build a deterministic native archive for the host platform with:

```sh
make release VERSION=0.1.0
sha256sum -c dist/labcentral-0.1.0-$(go env GOOS)-$(go env GOARCH).tar.gz.sha256
```

The archive includes `labd`, both official providers, the Proxmox qualification utility, provider checksums/manifests, the Proxmox source-tree binding, deployment assets, an archive-wide checksum file, a CycloneDX SBOM, and source/build provenance. Releases require a clean Git worktree so the embedded source binding describes the built provider. Set `SOURCE_DATE_EPOCH` to reproduce a prior build timestamp; otherwise the tool uses the current Git commit time. Provenance is descriptive metadata, not a signature or SLSA attestation.

For the supported Linux Docker path, start the minimal image with Compose:

```sh
LABCENTRAL_VERSION=0.1.0 LABCENTRAL_VCS_REF=$(git rev-parse HEAD) docker compose up -d --build
docker compose ps
```

Open <http://127.0.0.1:8080/>. `LABCENTRAL_PORT`, `DOCKER_SOCKET_PATH`, `LABCENTRAL_IMAGE`, and `LABCENTRAL_DATA_VOLUME` select the loopback port, Engine socket, image, and persistent volume. Stop with `docker compose down`; add `--volumes` only when you intend to delete local LabCentral state.

Example A intentionally requires a checked acknowledgement. Ordinary Docker containers are not a hostile-workload boundary, and its loopback-published web port currently uses a normal user-defined bridge. The requested denied-egress intent is therefore degraded and is not claimed as enforced. Use this slice only on a trusted development laptop with trusted images.

Compose uses host networking so the no-auth listener remains host-loopback-only and `labd` can probe provider-published loopback ports. Host networking is native on Linux; Docker Desktop requires version 4.34 or newer with host networking enabled. The selected Docker socket is mounted read-only into the LabCentral container, but that mount flag does not restrict Docker Engine API operations: socket access is effectively host-root control and the image therefore runs as root. Never expose this configuration to untrusted users or mount the socket into lab guests.

Run the design gate with Python 3, Bun, and Buf installed. Python dependencies are pinned in `tools/requirements-phase0.txt`:

```sh
python3 -m pip install -r tools/requirements-phase0.txt
python3 tools/verify_phase0.py
```

Set `BUFBIN` or `BUN` to an explicit executable path when they are not on `PATH`. This verifies design contracts only; it does not contact providers or execute lab workloads.

Verify the Go foundation and renderer with the toolchain declared by `go.mod`:

```sh
go test ./...
go test -race ./...
go vet ./...
```

The default suite also covers the local HTTP/control-plane/provider slice using in-process fakes; it does not contact the real Docker Engine. The filesystem adapter tests content measurement, atomic content-addressed commit, immutable permissions, and symlink refusal. Run the same eight job invariants against an isolated PostgreSQL database with:

```sh
LABCENTRAL_TEST_POSTGRES_DSN='postgres://user:password@127.0.0.1:5432/labcentral_test?sslmode=disable' \
  go test -count=1 -run TestPostgreSQLJobConformance ./internal/adapter/jobsdb
```
