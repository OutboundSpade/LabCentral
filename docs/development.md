# Developing LabCentral

Contribution rules for people and coding agents are in [AGENTS.md](../AGENTS.md), and
the design is in [ARCHITECTURE.md](../ARCHITECTURE.md).

## Build and test

```sh
make build                 # bin/labcentral and bin/labcentral-provider
make check                 # go vet and tests, including the consumer example
go test -race ./...
npm --prefix web ci && npm --prefix web run check && npm --prefix web run build
```

The CLI also cross-compiles for Windows and macOS (`GOOS=windows` or `darwin`),
which is enough for validating templates; running labs is supported from Linux.

## Generated files

Regenerate these after changing their sources; tests fail when they are stale.

```sh
go run ./tools/schema      # schemas/*.json from the Go types
PATH="$(go env GOPATH)/bin:$PATH" go run ./tools/protocol # pinned protoc and Go plugins
go run ./tools/webapi      # web/src/lib/types.ts from the hosted API types
LABCENTRAL_REGENERATE_V2_FIXTURE=1 go test ./hosted -run TestRetainedV2FixturePublicProvenance
```

The last one rebuilds `hosted/testdata/retained-v2.json.gz` from
`templates/ccdc-service-recovery`, so run it after changing that template.

## Optional checks

These use separately installed tools:

```sh
LABCENTRAL_TEST_PACKER="$(command -v packer)" go test ./build
LABCENTRAL_PLAYWRIGHT_MODULE=/path/to/playwright/index.mjs go test -race ./access ./hosted
LABCENTRAL_LOAD_TEST=1 go test ./hosted -run '^TestConcurrencyLoad$' -v   # local load model

# Browser scripts for the web UI (start `npx vite --host 127.0.0.1 --port 5173` in web/):
LABCENTRAL_PLAYWRIGHT_MODULE=… LABCENTRAL_CHROMIUM=… node web/tests/template-explorer.mjs
```

None of these replace testing against real AWS or Proxmox accounts.

## Failure injection

Normal and release builds ignore `LABCENTRAL_FAULT`. Qualification binaries must be
built explicitly with `-tags faultinject`, for both the CLI and provider:

```sh
go test -race -tags faultinject ./...
go build -tags faultinject -o /private/test-bin/labcentral ./cmd/labcentral
go build -tags faultinject -o /private/test-bin/labcentral-provider ./cmd/labcentral-provider
```

Use only an explicitly configured disposable environment. Select one occurrence with
JSON in `LABCENTRAL_FAULT`, for example:

```json
{"operation":"aws/network.create","step":"CreateSubnet","fault":"response-loss","nth":1,"marker":"/private/run/subnet.hit"}
```

Operations are `PLATFORM/ACTION`, using provider actions such as `image.build`,
`network.create`, `gateway.create`, `machine.create`, `resource.destroy`, `access.open`
and `access.close`. AWS steps use SDK operation names; Proxmox steps use normalized
methods and paths such as `POST/qemu/{id}/clone`. Packer steps have a `packer.` prefix;
`.received` selects the point after a native reply but before the provider receipt.
`ssh.exec` selects guest steps. Library boundaries also include `provider.result`,
`publish`, `reserve.vmid`, `reserve.vlan` and `reserve.commit`; `qualify` and
`build.cleanup` select builder phases. Reservation selectors use the library
`deploy` or `image.build` operation.

Faults are `kill-before`, `kill-during`, `kill-after`, `response-loss`,
`reject-authorization`, `reject-validation`, `throttle`, `server-error`,
`pause-before` and `pause-after`. Response loss executes the real call before dropping
its result. A during-send kill uses HTTP request acceptance or SSH exec acceptance;
the native outcome remains unknown. A pause writes the private marker and resumes
when `MARKER.continue` exists, allowing an external harness to alter ownership or
kill the controller. Markers are exclusive and stop the same selector poisoning
recovery. `LABCENTRAL_FAULT_TRACE` optionally writes native step/status diagnostics to
a private local file. Never put selectors, credentials, native inventory or evidence
from a real installation in tracked files.

The lifecycle matrices restart from persisted library and fake native inventory.
Platform matrices select each native mutation occurrence from a successful trace,
assert owned cleanup and preservation of foreign inventory, and distinguish empty
unknown discovery from confirmed absence. Subprocess tests use actual SIGKILL and
independent clients. These checks establish local behavior; real-platform evidence
must be recorded separately.

## Continuous integration and releases

`.github/workflows/ci.yml` runs vet, default and fault-tag race tests, the consumer example, CLI
cross-compilation and the web check and build on every push to `main` and every pull
request.

To release, tag a commit on `main` and push the tag:

```sh
git tag -a v2.0.0 -m "LabCentral v2.0.0"
git push origin v2.0.0
```

`.github/workflows/release.yml` tests the tag, builds the CLI for Linux (with the
provider), Windows and macOS with the version stamped in, records build provenance
attestations, and publishes a GitHub release with `SHA256SUMS`. The hosted server is
not part of the release.

## Using the library

[examples/consumer](../examples/consumer/main.go) is a separate Go module that drives
the public API; its `replace` directive points at this checkout.
