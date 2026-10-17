# Releases

Releases use [GoReleaser](https://goreleaser.com/) v2.18.3 and the configuration
in `.goreleaser.yaml`. Install that version of its CLI to package locally;
GitHub Actions installs the pinned version through the GoReleaser action.
GoReleaser reads the version from the Git tag (`vMAJOR.MINOR.PATCH`). The tag
must match the version committed in `cmd/server/cli.go`, so binaries and installs
built from a source archive report the same version. A pre-release hook checks
this match and gathers the Go and module license notices for the release notes with the `notices`
command of [go-release-actions](https://github.com/edspc/go-release-actions),
which the workflow also uses to raise the version; both are pinned by commit.

Run the **Release** workflow manually on the branch to release and choose
`bump`, the part of the version to increase: `patch` (the default), `minor` or
`major`. The parts after it reset to zero, so `minor` turns `X.Y.Z` into
`X.(Y+1).0`. The workflow computes the version from the one committed in
`cmd/server/cli.go`, updates the executable version and CLI documentation
examples, tests the result, commits the version, and atomically pushes the
commit and annotated
`v<version>` tag. GoReleaser then builds and publishes the GitHub Release,
including its changelog and assets.
Existing tags or releases are refused. The workflow needs permission to push to
the selected branch; branch protection can reject the atomic push without
creating a tag. If GoReleaser fails after the push, the commit and tag remain:
recover the release from that existing tag instead of dispatching the workflow
again.

For local release packaging, check out a clean commit tagged with its executable
version, then run `make release`. This validates the release and creates the
binaries in platform-specific subdirectories of `dist/` without publishing.
`make release-snapshot` also works from an untagged or dirty checkout, using a
`-SNAPSHOT` version derived from the latest
tag; it skips the source-version check and never publishes. The executable in a
snapshot still reports its committed source version.

Both commands build four executables with `CGO_ENABLED=0` and `-trimpath`.
Locally, each executable keeps the name `bitbucket-custom-mcp`, for example at
`dist/bitbucket-custom-mcp_linux_amd64_v1/bitbucket-custom-mcp`. For exact paths
and their corresponding release asset names, read the `path` and `name` fields
of entries with `extra.Format` set to `binary` in `dist/artifacts.json`.

The GitHub Release asset names are:

- `bitbucket-custom-mcp-darwin-arm64`
- `bitbucket-custom-mcp-darwin-amd64`
- `bitbucket-custom-mcp-linux-arm64`
- `bitbucket-custom-mcp-linux-amd64`

Only these binaries are uploaded as release assets. Project, Go and module license
notices are included in the release notes. GoReleaser does not upload source
archives or a checksum file; GitHub still provides its automatic source downloads.
Private repositories require authenticated access to release assets.

Binary names are stable across versions so the installation command can use
`releases/latest/download/<binary>` directly. The release tag and executable
version still identify the version.
