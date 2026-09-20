# Run LabCentral locally

The recommended path uses Docker Engine and Docker Compose with a supplied,
verified LabCentral image. No Go, Bun or Make installation is required.

```sh
export LABCENTRAL_IMAGE='your-registry/labcentral@sha256:YOUR_VERIFIED_DIGEST'
docker compose up -d --no-build
docker compose ps
```

For an offline image, verify the supplied archive checksum, run `docker load
--input labcentral-image.tar`, and set `LABCENTRAL_IMAGE` to its loaded image ID.
Use your actual verified image reference; the example above is a placeholder.
Signed public images are published only through the protected release workflow.

Open <http://127.0.0.1:8080/>. Template Library and Teams are included, with isolated
feature assets at `http://localhost:8082`, a separate cookie hostname. `LABCENTRAL_FEATURE_SET=with-classroom` also
installs Classroom, whose launch workflows need authenticated instructor access.
`LABCENTRAL_PORT`, `LABCENTRAL_ASSET_PORT`, `DOCKER_SOCKET_PATH` and
`LABCENTRAL_DATA_VOLUME` select the ports, Docker socket and persistent volume.
Stop with `docker compose down`; state remains in the named volume. Use
`--volumes` only when intentionally deleting that state.

If no release image is available, build one using Docker alone from this checkout:

```sh
docker compose -f compose.yaml -f compose.build.yaml up -d --build
```

This optional path compiles the UI, feature packages and executables inside pinned
build stages. Source/native development instructions live in the
[contributor guide](../development/README.md). Shared or remote installations use
[authenticated container deployment](../operations/container-deployment.md).

Example A intentionally requires a checked acknowledgement. Ordinary Docker containers are not a hostile-workload boundary, and its loopback-published web port currently uses a normal user-defined bridge. The requested denied-egress intent is therefore degraded and is not claimed as enforced. Use this slice only on a trusted development laptop with trusted images.

Compose uses host networking so the no-auth listener remains host-loopback-only and `labd` can probe provider-published loopback ports. Host networking is native on Linux; Docker Desktop requires version 4.34 or newer with host networking enabled. The selected Docker socket is mounted read-only into the LabCentral container, but that mount flag does not restrict Docker Engine API operations: socket access is effectively host-root control and the image therefore runs as root. Never expose this configuration to untrusted users or mount the socket into lab guests.


Next: [using the interface](README.md), [operator runbooks](../operations/README.md), or [contributing](../development/README.md).
