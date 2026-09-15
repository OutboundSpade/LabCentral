# Run LabCentral locally

Prerequisites are Go 1.27.1, Bun 1.3 or newer, and a local Docker Engine exposing API 1.44 or newer. Build both binaries, pin the provider executable checksum, and start the loopback-only server with:

```sh
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


Next: [using the interface](README.md), [operator runbooks](../operations/README.md), or [contributing](../development/README.md).
