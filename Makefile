.PHONY: build test race check package clean

build:
	mkdir -p bin
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -o bin/labcentral ./cmd/labcentral
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -o bin/labcentral-provider ./cmd/labcentral-provider

test:
	go test ./...

race:
	go test -race ./...

check:
	go vet ./...
	go test ./...
	cd examples/consumer && go test ./...

# Archives the exact existing binaries; does not rebuild or assert live qualification.
package:
	./tools/package.sh

clean:
	rm -rf bin dist
