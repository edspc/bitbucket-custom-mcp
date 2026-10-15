BINARY := bitbucket-custom-mcp
BINDIR ?= $(HOME)/.local/bin

.PHONY: build install lint test smoke release release-snapshot snyk osv clean

build:
	go build -trimpath -o bin/$(BINARY) ./cmd/server

install: build
	mkdir -p "$(BINDIR)"
	install -m 0755 bin/$(BINARY) "$(BINDIR)/$(BINARY)"

lint:
	golangci-lint run ./...

test:
	go test -race ./...

smoke:
	go test -count=1 ./cmd/server -run '^TestInstalledBinary$$'

release:
	goreleaser release --clean --skip=publish

release-snapshot:
	goreleaser release --snapshot --clean

snyk:
	snyk test
	snyk code test

# The sources tell the module versions; only the built server tells the Go standard library it
# was built with. Every known vulnerability counts, reachable or not, and both scans report even
# when the first one fails.
osv: build
	status=0; \
	osv-scanner scan source --recursive --no-call-analysis=go . || status=$$?; \
	osv-scanner scan source --experimental-plugins go/binary bin/$(BINARY) || status=$$?; \
	exit $$status

clean:
	rm -rf bin
