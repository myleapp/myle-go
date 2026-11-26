#!/usr/bin/env bash
# The whole check of the Go SDK, from any directory: gofmt, go vet, the tests, staticcheck (pinned).
# Every Go cache stays under sdk/go/.tools/ (gitignored); at most 2 parallel build jobs. Exits non-zero on failure.
set -euo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")"
STATICCHECK_VERSION=v0.8.2 # staticcheck 2026.2.2

export GOPATH="$PWD/.tools/gopath"
export GOMODCACHE="$PWD/.tools/gopath/pkg/mod"
export GOCACHE="$PWD/.tools/gocache"
export GOFLAGS="-p=2"
export GOTELEMETRY=off
mkdir -p "$GOPATH" "$GOCACHE"

echo "go: $(go version)"
unformatted=$(gofmt -l -- *.go)
if [ -n "$unformatted" ]; then
  echo "gofmt: not formatted:" >&2
  echo "$unformatted" >&2
  exit 1
fi
go vet ./...
go test -count=1 ./...
go run "honnef.co/go/tools/cmd/staticcheck@${STATICCHECK_VERSION}" ./...
echo "sdk/go: gofmt, vet, tests and staticcheck passed"
