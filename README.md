# LabCentral

LabCentral is a modular client-server platform for declarative virtual labs, with cybersecurity classrooms as its primary production use case. Its design-only Phase 0, architecture-foundation Phase 0B, and thin Docker laptop vertical slice are complete. The repository now includes the typed Go core, strict renderer, durable SQLite operations, a separately supervised Docker provider, the Huma/OpenAPI API, a content-addressed filesystem artifact adapter, and an embedded static SvelteKit/DaisyUI interface.

Start with [`PROJECT_STATE.md`](PROJECT_STATE.md), then follow [`AGENTS.md`](AGENTS.md). The architecture baseline is in [`docs/design/architecture.md`](docs/design/architecture.md), accepted decisions are in [`docs/adr/`](docs/adr/), and machine-readable progress is in [`design/state.yaml`](design/state.yaml) and [`design/checklist.yaml`](design/checklist.yaml).

The implementation-ready design includes three complete examples under [`examples/`](examples/), an executable Lab authoring schema and canonical fixtures under [`schemas/`](schemas/), an accepted plugin v1 descriptor baseline under [`api/plugin/v1/`](api/plugin/v1/), and machine-readable conformance catalogs under [`design/contracts/`](design/contracts/).

## Run the Docker laptop slice

Prerequisites are Go 1.27.1, Bun 1.3 or newer, and a local Docker Engine exposing API 1.44 or newer. Build both binaries, pin the provider executable checksum, and start the loopback-only server with:

```sh
make build
make run
```

Open <http://127.0.0.1:8080/>. LabCentral stores local state beneath `./labcentral-data`, launches `lab-provider-docker` as a separate process over a private Unix socket, and refuses a non-loopback no-auth listener unless the exact unsafe flag is supplied.

Example A intentionally requires a checked acknowledgement. Ordinary Docker containers are not a hostile-workload boundary, and its loopback-published web port currently uses a normal user-defined bridge. The requested denied-egress intent is therefore degraded and is not claimed as enforced. Use this slice only on a trusted development laptop with trusted images.

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
