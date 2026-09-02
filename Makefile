.PHONY: build build-proxmox-provider web test run release image compose-config clean

GO ?= go
BUN ?= bun
DOCKER ?= docker
VERSION ?= $(shell git describe --always --dirty)
VCS_REF ?= $(shell git rev-parse HEAD)
WEB_VERSION ?= dev
RELEASE_GOOS ?= $(shell $(GO) env GOOS)
RELEASE_GOARCH ?= $(shell $(GO) env GOARCH)
IMAGE ?= labcentral:$(VERSION)

build: web
	mkdir -p bin
	$(GO) build -trimpath -o bin/lab-provider-docker ./cmd/lab-provider-docker
	$(GO) build -trimpath -ldflags "-X main.version=$$(git describe --always --dirty)" -o bin/labd ./cmd/labd
	sha256sum bin/lab-provider-docker | cut -d' ' -f1 > bin/lab-provider-docker.sha256

build-proxmox-provider:
	mkdir -p bin
	$(GO) build -trimpath -o bin/lab-provider-proxmox ./cmd/lab-provider-proxmox

web:
	cd web && LABCENTRAL_WEB_VERSION="$(WEB_VERSION)" $(BUN) install --frozen-lockfile && LABCENTRAL_WEB_VERSION="$(WEB_VERSION)" $(BUN) run check && LABCENTRAL_WEB_VERSION="$(WEB_VERSION)" $(BUN) run build

test:
	$(GO) test ./...
	cd web && $(BUN) run check && $(BUN) run build

run: build
	./bin/labd

release:
	$(GO) run ./tools/release -version "$(VERSION)" -goos "$(RELEASE_GOOS)" -goarch "$(RELEASE_GOARCH)"

image:
	$(DOCKER) build --build-arg VERSION="$(VERSION)" --build-arg VCS_REF="$(VCS_REF)" --tag "$(IMAGE)" .

compose-config:
	LABCENTRAL_VERSION="$(VERSION)" LABCENTRAL_VCS_REF="$(VCS_REF)" $(DOCKER) compose config --quiet

clean:
	$(GO) clean
