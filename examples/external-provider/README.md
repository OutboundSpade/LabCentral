# External provider fixture

This is a separate Go module importing only the public SDK and plugin protocol. It proves a separately built executable can register two targets through an installation file without adding a provider branch to `labd`.

Build from this directory with `go build -mod=readonly -o /absolute/protected/plugin-root/external-provider .`, remove group/world write permissions, and calculate its SHA-256 for a v1 manifest. Use ID `org.example.provider`, version `0.1.0`, category `provider`, provider kind `example`, and protocol major 1/minor range 0–1. Configure targets using the [installation guide](../../docs/operations/plugin-installation.md).

This fixture advertises `workload.vm` as unsupported and performs no infrastructure operations. A real provider must implement and test validation, planning, durable invocation recovery, owned apply/destroy, scoped credentials and truthful discovery for its supported existing resource types. `cmd/labd/installation_test.go` builds this module and exercises the production loader with two target bindings. The local `replace` directive is only for repository tests; independently published modules pin a released SDK revision.
