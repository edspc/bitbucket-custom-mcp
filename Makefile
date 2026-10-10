BINARY := bitbucket-custom-mcp
BINDIR ?= $(HOME)/.local/bin

.PHONY: build install lint test snyk clean

build:
	go build -o bin/$(BINARY) ./cmd/server

install: build
	mkdir -p "$(BINDIR)"
	install -m 0755 bin/$(BINARY) "$(BINDIR)/$(BINARY)"

lint:
	golangci-lint run ./...

test:
	go test -race ./...

snyk:
	snyk test
	snyk code test

clean:
	rm -rf bin
