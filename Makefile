BINARY  := persia
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

.PHONY: build test vet fmt run once doctor docker clean

build:
	mkdir -p bin
	go build -trimpath -ldflags "-s -w -X main.version=$(VERSION)" -o bin/$(BINARY) ./cmd/persia

test:
	go test ./...

vet:
	go vet ./...

fmt:
	gofmt -w cmd internal

run:
	go run ./cmd/persia run -config config.yaml

once:
	go run ./cmd/persia once -config config.yaml

doctor:
	go run ./cmd/persia doctor -config config.yaml

docker:
	docker build --build-arg VERSION=$(VERSION) -t $(BINARY):latest .

clean:
	rm -rf bin
