# LabCentral

LabCentral is a modular client-server platform for declarative virtual labs, with cybersecurity classrooms as its primary production use case. Its design-only Phase 0 and architecture-foundation Phase 0B gates are complete. The typed Go domain/application layer, strict deterministic renderer, durable job prototype, and reusable fake-provider contract suite are implemented; runtime services and real providers remain gated by `design/checklist.yaml`.

Start with [`PROJECT_STATE.md`](PROJECT_STATE.md), then follow [`AGENTS.md`](AGENTS.md). The architecture baseline is in [`docs/design/architecture.md`](docs/design/architecture.md), accepted decisions are in [`docs/adr/`](docs/adr/), and machine-readable progress is in [`design/state.yaml`](design/state.yaml) and [`design/checklist.yaml`](design/checklist.yaml).

The implementation-ready design includes three complete examples under [`examples/`](examples/), an executable Lab authoring schema and canonical fixtures under [`schemas/`](schemas/), an accepted plugin v1 descriptor baseline under [`api/plugin/v1/`](api/plugin/v1/), and machine-readable conformance catalogs under [`design/contracts/`](design/contracts/).

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

The default suite runs job/outbox conformance on SQLite. Run the same eight invariants against an isolated PostgreSQL database with:

```sh
LABCENTRAL_TEST_POSTGRES_DSN='postgres://user:password@127.0.0.1:5432/labcentral_test?sslmode=disable' \
  go test -count=1 -run TestPostgreSQLJobConformance ./internal/adapter/jobsdb
```
