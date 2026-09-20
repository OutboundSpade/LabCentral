# syntax=docker/dockerfile:1.12@sha256:93bfd3b68c109427185cd78b4779fc82b484b0b7618e36d0f104d4d801e66d25

FROM oven/bun:1.3.14-debian@sha256:9dba1a1b43ce28c9d7931bfc4eb00feb63b0114720a0277a8f939ae4dfc9db6f AS web-build
ARG VERSION=dev
ENV LABCENTRAL_WEB_VERSION=$VERSION
WORKDIR /src/web
COPY web/package.json web/bun.lock ./
RUN --mount=type=cache,target=/root/.bun/install/cache bun install --frozen-lockfile
COPY web/src ./src
COPY web/svelte.config.js web/tsconfig.json web/vite.config.ts ./
COPY tools/normalize_web_build.mjs tools/sync_theme.mjs /src/tools/
COPY pkg/plugin-sdk-browser /src/pkg/plugin-sdk-browser
COPY features /src/features
RUN if ! command -v node >/dev/null; then ln -s /usr/local/bin/bun /usr/local/bin/node; fi
RUN bun run check && bun run build && \
    bun /src/features/package.mjs /out/features /opt/labcentral/features && \
    chmod -R a+rX /out/features

FROM golang:1.27.1-bookworm@sha256:648f440f42a0958804efb24df176f806f9d353b41f1c0627f666428e40310f6b AS go-build
ARG TARGETOS=linux
ARG TARGETARCH=amd64
ARG VERSION=dev
ARG PROXMOX_SOURCE_TREE_DIGEST=unavailable
ENV GOTOOLCHAIN=local
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .
COPY --from=web-build /src/web/build ./web/build
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    mkdir -p -m 0700 /out/data && \
    CGO_ENABLED=0 GOOS="$TARGETOS" GOARCH="$TARGETARCH" \
    go build -trimpath -buildvcs=false -ldflags "-buildid= -s -w -X=main.version=$VERSION" -o /out/labd ./cmd/labd && \
    CGO_ENABLED=0 GOOS="$TARGETOS" GOARCH="$TARGETARCH" \
    go build -trimpath -buildvcs=false -ldflags "-buildid= -s -w" -o /out/lab-provider-docker ./cmd/lab-provider-docker && \
    CGO_ENABLED=0 GOOS="$TARGETOS" GOARCH="$TARGETARCH" \
    go build -trimpath -buildvcs=false -ldflags "-buildid= -s -w -X=main.sourceTreeDigest=$PROXMOX_SOURCE_TREE_DIGEST" -o /out/lab-provider-proxmox ./cmd/lab-provider-proxmox && \
    CGO_ENABLED=0 GOOS="$TARGETOS" GOARCH="$TARGETARCH" \
    go build -trimpath -buildvcs=false -ldflags "-buildid= -s -w" -o /out/lab-access-novnc ./cmd/lab-access-novnc && \
    for component in lab-source-github lab-builder-vm lab-container-vm; do \
      CGO_ENABLED=0 GOOS="$TARGETOS" GOARCH="$TARGETARCH" \
      go build -trimpath -buildvcs=false -ldflags "-buildid= -s -w" -o "/out/$component" "./cmd/$component" || exit 1; \
    done && \
    CGO_ENABLED=0 GOOS="$TARGETOS" GOARCH="$TARGETARCH" \
    go build -trimpath -buildvcs=false -ldflags "-buildid= -s -w" -o /out/labcentral-recovery ./tools/recovery && \
    sha256sum /out/lab-provider-docker | cut -d' ' -f1 > /out/lab-provider-docker.sha256 && \
    sha256sum /out/lab-provider-proxmox | cut -d' ' -f1 > /out/lab-provider-proxmox.sha256 && \
    printf '%s\n' "$PROXMOX_SOURCE_TREE_DIGEST" > /out/lab-provider-proxmox.source.sha256 && \
    sha256sum /out/lab-access-novnc | cut -d' ' -f1 > /out/lab-access-novnc.sha256

FROM scratch
ARG VERSION=dev
ARG VCS_REF=unknown
LABEL org.opencontainers.image.title="LabCentral" \
      org.opencontainers.image.description="Declarative isolated-lab control plane and official plugins" \
      org.opencontainers.image.source="https://github.com/OutboundSpade/LabCentral" \
      org.opencontainers.image.version="$VERSION" \
      org.opencontainers.image.revision="$VCS_REF"
COPY --from=go-build --chmod=0755 /out/labd /usr/local/bin/labd
COPY --from=go-build --chmod=0755 /out/lab-source-github /out/lab-builder-vm /out/lab-container-vm /out/labcentral-recovery /usr/local/bin/
COPY --from=web-build /out/features /opt/labcentral/features
COPY --from=go-build --chmod=0755 /out/lab-provider-docker /usr/local/bin/lab-provider-docker
COPY --from=go-build /out/lab-provider-docker.sha256 /usr/local/bin/lab-provider-docker.sha256
COPY --from=go-build --chmod=0755 /out/lab-provider-proxmox /usr/local/bin/lab-provider-proxmox
COPY --from=go-build /out/lab-provider-proxmox.sha256 /usr/local/bin/lab-provider-proxmox.sha256
COPY --from=go-build /out/lab-provider-proxmox.source.sha256 /usr/local/bin/lab-provider-proxmox.source.sha256
COPY --from=go-build --chmod=0755 /out/lab-access-novnc /usr/local/bin/lab-access-novnc
COPY --from=go-build /out/lab-access-novnc.sha256 /usr/local/bin/lab-access-novnc.sha256
COPY --from=go-build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=go-build /out/data /var/lib/labcentral
VOLUME ["/var/lib/labcentral"]
EXPOSE 8080 8443 9090
USER 0:0
HEALTHCHECK --interval=10s --timeout=4s --start-period=10s --retries=3 CMD ["/usr/local/bin/labd", "healthcheck"]
ENTRYPOINT ["/usr/local/bin/labd"]
CMD ["--listen", "127.0.0.1:8080", "--observability-listen", "127.0.0.1:9090", "--data-dir", "/var/lib/labcentral", "--auth-mode", "none", "--installation", "/opt/labcentral/features/standard.installation.json", "--extension-asset-listen", "127.0.0.1:8082", "--extension-asset-origin", "http://localhost:8082"]
