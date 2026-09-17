.PHONY: build build-access-plugin build-proxmox-provider build-proxmox-qualification build-proxmox-probe-agent build-recovery web test run release image compose-config clean

GO ?= go
BUN ?= bun
DOCKER ?= docker
VERSION ?= $(shell git describe --always --dirty)
VCS_REF ?= $(shell git rev-parse HEAD)
WEB_VERSION ?= dev
RELEASE_GOOS ?= $(shell $(GO) env GOOS)
RELEASE_GOARCH ?= $(shell $(GO) env GOARCH)
IMAGE ?= labcentral:$(VERSION)
PROXMOX_SOURCE_TREE_DIGEST ?= $(shell $(GO) run ./tools/source-digest)

build: web
	mkdir -p bin
	$(GO) build -trimpath -o bin/lab-provider-docker ./cmd/lab-provider-docker
	$(GO) build -trimpath -o bin/lab-access-novnc ./cmd/lab-access-novnc
	$(GO) build -trimpath -ldflags "-X main.version=$$(git describe --always --dirty)" -o bin/labd ./cmd/labd
	sha256sum bin/lab-provider-docker | cut -d' ' -f1 > bin/lab-provider-docker.sha256
	sha256sum bin/lab-access-novnc | cut -d' ' -f1 > bin/lab-access-novnc.sha256

build-proxmox-provider:
	mkdir -p bin
	$(GO) build -trimpath -buildvcs=false -ldflags "-X main.sourceTreeDigest=$(PROXMOX_SOURCE_TREE_DIGEST)" -o bin/lab-provider-proxmox ./cmd/lab-provider-proxmox
	sha256sum bin/lab-provider-proxmox | cut -d' ' -f1 > bin/lab-provider-proxmox.sha256

build-access-plugin:
	mkdir -p bin
	$(GO) build -trimpath -o bin/lab-access-novnc ./cmd/lab-access-novnc
	sha256sum bin/lab-access-novnc | cut -d' ' -f1 > bin/lab-access-novnc.sha256

build-proxmox-qualification:
	mkdir -p bin
	$(GO) build -trimpath -o bin/lab-proxmox-qualification ./tools/proxmox-qualification

build-proxmox-probe-agent:
	mkdir -p bin
	$(GO) build -trimpath -o bin/lab-proxmox-probe-agent ./tools/proxmox-probe-agent

build-recovery:
	mkdir -p bin
	$(GO) build -trimpath -o bin/labcentral-recovery ./tools/recovery

web:
	cd web && LABCENTRAL_WEB_VERSION="$(WEB_VERSION)" $(BUN) install --frozen-lockfile && LABCENTRAL_WEB_VERSION="$(WEB_VERSION)" $(BUN) run check && LABCENTRAL_WEB_VERSION="$(WEB_VERSION)" $(BUN) run build

test: web
	$(GO) test ./...
	cd web && $(BUN) run test:cache && $(BUN) run test:browser
	$(GO) build -trimpath -o bin/extension-foundations/labd ./cmd/labd
	cd web && $(BUN) run test:extensions

run: build
	./bin/labd --auth-mode=none

release:
	$(GO) run ./tools/release -bun "$(BUN)" -version "$(VERSION)" -goos "$(RELEASE_GOOS)" -goarch "$(RELEASE_GOARCH)"

image:
	$(DOCKER) build --build-arg VERSION="$(VERSION)" --build-arg VCS_REF="$(VCS_REF)" --build-arg PROXMOX_SOURCE_TREE_DIGEST="$(PROXMOX_SOURCE_TREE_DIGEST)" --tag "$(IMAGE)" .

compose-config:
	LABCENTRAL_VERSION="$(VERSION)" LABCENTRAL_VCS_REF="$(VCS_REF)" $(DOCKER) compose config --quiet

clean:
	$(GO) clean
