#!/usr/bin/env bash
# Validate the release job ordering and required metadata.
set -euo pipefail
cd "$(dirname "$0")/.."
workflow=.github/workflows/release.yml
grep -q '^  test:$' "$workflow"
grep -q '^    needs: test$' "$workflow"
grep -Eq "GO_RELEASE_VERSION: '[0-9]+\.[0-9]+\.[0-9]+'" "$workflow"
grep -q 'run: go test -race -count=1 ./...' "$workflow"
grep -q 'uses: actions/attest@v4' "$workflow"
grep -q 'sbom-path:' "$workflow"
grep -q '## Rollback' SECURITY.md
targets=$(awk '/^            goos:/ { os=$2 } /^            goarch:/ { print os "-" $2 }' "$workflow" | sort)
test "$targets" = "$(printf '%s\n' darwin-amd64 darwin-arm64 linux-amd64 linux-arm64 windows-amd64)"
echo 'release-check: ok'
