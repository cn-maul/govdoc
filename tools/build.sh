#!/usr/bin/env bash
# Build and verify GovDoc.
#
# GOWORK=off is not optional here. A sibling project (rosetta) keeps a go.work
# one directory up that does not list this module, and Go then refuses to
# resolve ./... at all:
#
#     pattern ./...: directory prefix . does not contain modules listed in
#     go.work or their selected dependencies
#
# That failure looks like a broken build rather than a workspace-membership
# problem, so it is pinned here instead of being rediscovered.
set -euo pipefail
export GOWORK=off

cd "$(dirname "$0")/.."

echo "== gofmt =="
out=$(gofmt -l internal/ cmd/ tools/ embed.go)
if [ -n "$out" ]; then
  echo "需要格式化:"; echo "$out"; exit 1
fi
echo "clean"

echo "== build =="
go build ./...

echo "== vet =="
go vet ./...

echo "== test =="
go test -count=1 -timeout 300s ./...