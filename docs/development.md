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

## Continuous integration and releases

`.github/workflows/ci.yml` runs vet, race tests, the consumer example, CLI
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
