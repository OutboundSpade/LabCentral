# syntax=docker/dockerfile:1.12@sha256:93bfd3b68c109427185cd78b4779fc82b484b0b7618e36d0f104d4d801e66d25

FROM oven/bun:1.3.14-debian@sha256:9dba1a1b43ce28c9d7931bfc4eb00feb63b0114720a0277a8f939ae4dfc9db6f AS web-build
ARG VERSION=dev
ENV LABCENTRAL_WEB_VERSION=$VERSION
WORKDIR /src/web
COPY web/package.json web/bun.lock ./
RUN --mount=type=cache,target=/root/.bun/install/cache bun install --frozen-lockfile
COPY web/src ./src
COPY web/svelte.config.js web/tsconfig.json web/vite.config.ts ./
COPY tools/normalize_web_build.mjs /src/tools/normalize_web_build.mjs
RUN bun run check && bun run build

FROM golang:1.27.1-bookworm@sha256:648f440f42a0958804efb24df176f806f9d353b41f1c0627f666428e40310f6b AS go-build
ARG TARGETOS=linux
ARG TARGETARCH=amd64
ARG VERSION=dev
ENV GOTOOLCHAIN=local
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .
COPY --from=web-build /src/web/build ./web/build
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS="$TARGETOS" GOARCH="$TARGETARCH" \
    go build -trimpath -buildvcs=false -ldflags "-buildid= -s -w -X=main.version=$VERSION" -o /out/labd ./cmd/labd && \
    CGO_ENABLED=0 GOOS="$TARGETOS" GOARCH="$TARGETARCH" \
    go build -trimpath -buildvcs=false -ldflags "-buildid= -s -w" -o /out/lab-provider-docker ./cmd/lab-provider-docker && \
    sha256sum /out/lab-provider-docker | cut -d' ' -f1 > /out/lab-provider-docker.sha256

FROM scratch
ARG VERSION=dev
ARG VCS_REF=unknown
LABEL org.opencontainers.image.title="LabCentral" \
      org.opencontainers.image.description="Trusted-laptop declarative Docker lab control plane" \
      org.opencontainers.image.source="https://github.com/OutboundSpade/LabCentral" \
      org.opencontainers.image.version="$VERSION" \
      org.opencontainers.image.revision="$VCS_REF"
COPY --from=go-build --chmod=0755 /out/labd /usr/local/bin/labd
COPY --from=go-build --chmod=0755 /out/lab-provider-docker /usr/local/bin/lab-provider-docker
COPY --from=go-build --chmod=0444 /out/lab-provider-docker.sha256 /usr/local/bin/lab-provider-docker.sha256
VOLUME ["/var/lib/labcentral"]
EXPOSE 8080
USER 0:0
HEALTHCHECK --interval=10s --timeout=4s --start-period=10s --retries=3 CMD ["/usr/local/bin/labd", "healthcheck"]
ENTRYPOINT ["/usr/local/bin/labd"]
CMD ["--listen", "127.0.0.1:8080", "--data-dir", "/var/lib/labcentral"]
