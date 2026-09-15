# Develop LabCentral

For product usage, start with the [user guide](../users/README.md). Plugin authors
can use the [public SDK guide](../plugins/README.md) without learning core internals.

## Build from a fresh checkout

Use the Go toolchain declared in `go.mod`, Bun 1.3.14 or newer, and Python 3 for
design verification. Browser tests use Playwright's Chromium, Firefox and WebKit.

```sh
make build
cd web
bunx playwright install --with-deps chromium firefox webkit
cd ..
make test
```

`make build`, `make test` and `make release` build the static UI before Go embeds
it. Before a direct `go test ./...` or `go build ./cmd/labd`, run `make web` once.
A fresh checkout intentionally contains no `web/build`, `bin` or `dist` output.
Never force-add these directories. Generated OpenAPI, TypeScript DTOs and protocol
fixtures **are** checked contracts and remain in Git.

## Useful checks

```sh
# After web assets have been built:
go test -race ./...
go vet ./...

# API changes: regenerate both checked contracts.
cd web
bun run generate:api
bun run check
bun run test:cache
bun run build
bun run test:browser
cd ..

python3 -m pip install -r tools/requirements-phase0.txt
python3 tools/verify_phase0.py
```

The design verifier also requires Bun and Buf; `BUN` and `BUFBIN` select explicit
executables. CI pins these tools. It checks schemas, protocol compatibility,
examples, stable IDs, links and task consistency without contacting providers.

Set `LABCENTRAL_TEST_POSTGRES_DSN` to an **isolated test database** to exercise
PostgreSQL parity; without it those tests skip. See the
[testing strategy](../design/testing.md) for coverage and integration boundaries.
Normal tests use fakes and do not prove live provider qualification.

## Code map

| Path | Responsibility |
| --- | --- |
| `internal/domain` | Closed portable types and invariants |
| `internal/application` | Application ports and transactional commands |
| `internal/controlplane` | Orchestration and durable operations |
| `internal/adapter` | SQL, filesystem, secrets and protocol adapters |
| `internal/pluginhost` | Verified process supervision and recovery |
| `api/http` | Huma handlers, DTOs and canonical OpenAPI |
| `web/src/lib` | Persistent workspace shell, focused views and API/cache adapter |
| `providers`, `access`, `pkg/plugin-sdk-go` | Plugin implementations and public SDK |
| `tools`, `deploy` | Verification, packaging and operator assets |

Keep dependencies inward. Use generated frontend DTOs instead of hand-copying
HTTP shapes. Administration components call the same canonical API as clients.
Navigation labels and route scope live in `web/src/lib/navigation.ts`.

## Design and change process

Read [current state](../../PROJECT_STATE.md), [state](../../design/state.yaml)
and [tasks](../../design/checklist.yaml), then relevant accepted ADRs and feature
design. Add the task and dependency order before implementation. Preserve stable
IDs and accepted decisions. An ADR is authoritative for a decision; schemas and
protocol define machine contracts; design explains them; state reports progress.

Use the [design reference](design-reference.md) to find a subject. Update affected
design and task/state records with each change. Move long dated progress logs to
[history](../history/) and keep current state short. Agent-specific requirements
live in [AGENTS.md](../../AGENTS.md).

## Releases and testbeds

`make release VERSION=...` requires a clean source tree. The release tool itself
rebuilds UI assets from the frozen lockfile before embedding them, including when
called directly with `go run ./tools/release`. It generates a native
archive, checksums, manifests, provenance and SBOM in `dist`. See
[release verification](../operations/release-verification.md). Build assets are
release outputs, not source files.

PVE1 is a disposable private testbed; consult the
[classroom deployment record](../operations/classroom-testbed.md) before using it.
Preserve user labs and exact provider qualification. Local tests or a core/UI
update do not renew a provider's qualification or complete production gates.
