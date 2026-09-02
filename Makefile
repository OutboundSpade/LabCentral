.PHONY: build web test run clean

GO ?= go
BUN ?= bun

build: web
	mkdir -p bin
	$(GO) build -trimpath -o bin/lab-provider-docker ./cmd/lab-provider-docker
	$(GO) build -trimpath -ldflags "-X main.version=$$(git describe --always --dirty)" -o bin/labd ./cmd/labd
	sha256sum bin/lab-provider-docker | cut -d' ' -f1 > bin/lab-provider-docker.sha256

web:
	cd web && $(BUN) install --frozen-lockfile && $(BUN) run check && $(BUN) run build

test:
	$(GO) test ./...
	cd web && $(BUN) run check && $(BUN) run build

run: build
	./bin/labd

clean:
	$(GO) clean
