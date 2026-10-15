#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."

# Source-build installs and release binaries must report the same version as the tag.
if [[ "${2:-false}" != true ]]; then
  release_version=$(go run ./cmd/server --version)
  release_version=${release_version#bitbucket-custom-mcp }
  if [[ "$release_version" != "$1" ]]; then
    echo "The release tag must match the executable version." >&2
    exit 1
  fi
fi
mkdir -p .scratch
# Binary archives redistribute Go and module code, so ship their license notices too.
notices=".scratch/THIRD_PARTY_LICENSES"
rm -rf "$notices"
mkdir -p "$notices/go"
cp "$(go env GOROOT)/LICENSE" "$notices/go/"
for release_os in darwin linux; do
  for release_arch in arm64 amd64; do
    CGO_ENABLED=0 GOOS="$release_os" GOARCH="$release_arch" \
      go list -mod=readonly -deps -f '{{with .Module}}{{if not .Main}}{{.Path}}@{{.Version}} {{.Dir}}{{end}}{{end}}' ./cmd/server
  done
done | LC_ALL=C sort -u > ".scratch/release-modules"
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
done < ".scratch/release-modules"

rm .scratch/release-modules
