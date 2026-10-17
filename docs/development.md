# Development

Run the commands below from the repository root.

## Stack

- Go
- [modelcontextprotocol/go-sdk](https://github.com/modelcontextprotocol/go-sdk)
- `log/slog` to stderr — stdout carries the MCP protocol

## Running commands

| Task    | Command                                                    |
|---------|------------------------------------------------------------|
| Run     | `go run ./cmd/server`                                      |
| Build   | `make build` — binary in `bin/`                            |
| Install | `make install` — copies it to `~/.local/bin` (override with `BINDIR=…`) |
| Lint    | `make lint` (`golangci-lint run ./...`)                    |
| Tests   | `make test` (`go test -race ./...`)                        |
| Smoke test | `make smoke` — builds and starts the executable, initializes MCP and lists tools without contacting Bitbucket |
| Release binaries | `make release` — packages the clean tagged commit with GoReleaser into `dist/` |
| Snapshot binaries | `make release-snapshot` — builds local binaries without publishing |
| Snyk    | `make snyk` (`snyk test`, `snyk code test`)                |
| OSV     | `make osv` (OSV-Scanner on `go.mod` and the built server)  |

`make lint` needs [golangci-lint](https://golangci-lint.run/) built with the Go
of `go.mod` or later, which a build with an older Go refuses:
`go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.14.0`,
with `$(go env GOPATH)/bin` on `PATH`.

`make snyk` needs the [Snyk CLI](https://docs.snyk.io/snyk-cli), for example
the [standalone binary](https://docs.snyk.io/snyk-cli/install-or-update-the-snyk-cli),
signed in with `snyk auth` or given a token in
`SNYK_TOKEN`; Snyk Code must be enabled in the Snyk organization.

`make osv` needs [OSV-Scanner](https://google.github.io/osv-scanner/):
`go install github.com/google/osv-scanner/v2/cmd/osv-scanner@v2.6.0`, with
`$(go env GOPATH)/bin` on `PATH`. It scans `go.mod` for the module versions
and the server built by `make build` for the Go standard library it was
built with, which OSV-Scanner does not take from `go.mod`. Every known
vulnerability fails it, whether the code reaches it or not.

GitHub Actions run on pushes to `main` and on pull requests: `make lint` and
`make test` as separate jobs of the CI workflow (`.github/workflows/ci.yml`).
Tests, including the executable smoke test, run on Linux, macOS Apple Silicon
and macOS Intel. Both security scans run
in the Security workflow (`.github/workflows/security.yml`):
the `snyk` job runs `make snyk` with the standalone Snyk CLI, and the
`osv-scanner` job runs the scans of `make osv` with the scanner image of the
official [OSV-Scanner action](https://github.com/google/osv-scanner-action).
Actions are pinned by commit, the image by digest and the Snyk CLI by its
SHA-256, and checkouts keep no credentials. The Snyk job reads the token from the
repository secret `SNYK_TOKEN`.

Pull requests that change `.github/workflows/release.yml`, `.goreleaser.yaml`,
README, `docs/` or the check itself also run the Release check workflow
(`.github/workflows/release-check.yml`). Without committing or publishing, it
builds snapshot binaries with GoReleaser, which runs the release hook, checks
the release assets and license notices with the pinned `check-binaries` action,
and raises the version with the pinned bump action. Both checks and the collection
of license notices run in Go through go-release-actions.
It first checks that its GoReleaser and bump steps match those of the Release
workflow, and that the hook uses the same go-release-actions commit.

## Change workflow

See [.claude/rules/lifecycle.md](../.claude/rules/lifecycle.md).
