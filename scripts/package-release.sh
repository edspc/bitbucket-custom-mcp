#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."

# The source archive and executable must describe the same committed tree.
if [[ -n "$(git status --porcelain --untracked-files=normal)" ]]; then
  echo "Commit all source changes before packaging a release." >&2
  exit 1
fi
mkdir -p .scratch dist
release_tmp=$(mktemp -d .scratch/release.XXXXXX)
trap 'rm -rf "$release_tmp"' EXIT
go build -mod=readonly -trimpath -o "$release_tmp/server" ./cmd/server
release_version=$("$release_tmp/server" --version)
release_version=${release_version#bitbucket-custom-mcp }
if [[ ! "$release_version" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
  echo "Expected a stable semantic version from --version." >&2
  exit 1
fi
if [[ -n "${RELEASE_TAG:-}" && "$RELEASE_TAG" != "v$release_version" ]]; then
  echo "The release tag must match the executable version." >&2
  exit 1
fi

release_dir="dist/v$release_version"
mkdir -p "$release_dir"
archives=()
source_archive="bitbucket-custom-mcp-$release_version-source.tar.gz"
git archive --format=tar --prefix="bitbucket-custom-mcp-$release_version/" HEAD |
  gzip -n > "$release_dir/$source_archive"
archives+=("$source_archive")

# Binary archives redistribute Go and module code, so ship their license notices too.
notices="$release_tmp/THIRD_PARTY_LICENSES"
mkdir -p "$notices/go"
cp "$(go env GOROOT)/LICENSE" "$notices/go/"
for release_os in darwin linux; do
  for release_arch in arm64 amd64; do
    CGO_ENABLED=0 GOOS="$release_os" GOARCH="$release_arch" \
      go list -mod=readonly -deps -f '{{with .Module}}{{if not .Main}}{{.Path}}@{{.Version}} {{.Dir}}{{end}}{{end}}' ./cmd/server
  done
done | LC_ALL=C sort -u > "$release_tmp/modules"
while read -r module_name module_dir; do
  [[ -n "$module_name" ]] || continue
  if [[ -z "$module_dir" ]]; then
    echo "No module directory found for $module_name." >&2
    exit 1
  fi
  found=false
  mkdir -p "$notices/$module_name"
  for notice in "$module_dir"/LICENSE* "$module_dir"/COPYING* "$module_dir"/NOTICE* "$module_dir"/AUTHORS* "$module_dir"/PATENTS*; do
    [[ -f "$notice" ]] || continue
    cp "$notice" "$notices/$module_name/"
    found=true
  done
  if [[ "$found" != true ]]; then
    echo "No license notices found for $module_name." >&2
    exit 1
  fi
done < "$release_tmp/modules"

for release_os in darwin linux; do
  for release_arch in arm64 amd64; do
    stage="$release_tmp/$release_os-$release_arch"
    mkdir -p "$stage"
    CGO_ENABLED=0 GOOS="$release_os" GOARCH="$release_arch" \
      go build -mod=readonly -trimpath -o "$stage/bitbucket-custom-mcp" ./cmd/server
    cp README.md LICENSE "$stage/"
    cp -R "$notices" "$stage/"
    archive="bitbucket-custom-mcp-$release_version-$release_os-$release_arch.tar.gz"
    tar -czf "$release_dir/$archive" -C "$stage" bitbucket-custom-mcp README.md LICENSE THIRD_PARTY_LICENSES
    archives+=("$archive")
  done
done
(cd "$release_dir" && shasum -a 256 "${archives[@]}" > checksums.txt)
echo "Release archives and checksums: $release_dir"
