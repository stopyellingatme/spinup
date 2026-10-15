GOOS ?= linux
GOARCH ?= amd64
export GOTOOLCHAIN := local
export GOWORK := off

.PHONY: build test

build:
	@mkdir -p bin
	CGO_ENABLED=0 GOOS=$(GOOS) GOARCH=$(GOARCH) go build -trimpath -ldflags='-s -w' -o bin/example_$(GOOS)_$(GOARCH) .

test:
	go test -race ./...
