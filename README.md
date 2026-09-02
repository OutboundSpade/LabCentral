# LabCentral

LabCentral is a planned modular client-server platform for declarative virtual labs, with cybersecurity classrooms as its primary production use case. Its design-only Phase 0 gate is complete and implementation may now begin through `design/checklist.yaml`; production application code has intentionally not been scaffolded yet.

Start with [`PROJECT_STATE.md`](PROJECT_STATE.md), then follow [`AGENTS.md`](AGENTS.md). The architecture baseline is in [`docs/design/architecture.md`](docs/design/architecture.md), accepted decisions are in [`docs/adr/`](docs/adr/), and machine-readable progress is in [`design/state.yaml`](design/state.yaml) and [`design/checklist.yaml`](design/checklist.yaml).

The implementation-ready design includes three complete examples under [`examples/`](examples/), an executable Lab authoring schema and canonical fixtures under [`schemas/`](schemas/), an accepted plugin v1 descriptor baseline under [`api/plugin/v1/`](api/plugin/v1/), and machine-readable conformance catalogs under [`design/contracts/`](design/contracts/).

Run the design gate with Python 3, Bun, and Buf installed. Python dependencies are pinned in `tools/requirements-phase0.txt`:

```sh
python3 -m pip install -r tools/requirements-phase0.txt
python3 tools/verify_phase0.py
```

Set `BUFBIN` or `BUN` to an explicit executable path when they are not on `PATH`. This verifies design contracts only; it does not contact providers or execute lab workloads.
