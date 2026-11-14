#!/usr/bin/env bash
# Fails when README/docs claims diverge from the binary and the tag list.
# Why this exists: the README said "no release has been cut yet" for a day after
# v0.2.0 shipped, and its sample hunt output lost the Wilson line. Both were
# invisible to `go test` and to every reviewer who did not run the tool.
set -uo pipefail

root=$(cd "$(dirname "$0")/.." && pwd)
cd "$root"
README=${README:-README.md}
fail=0
err() { echo "docs-drift: $*" >&2; fail=1; }

# 1. Claims that a release does not exist.
if grep -rn "cut yet\|has been cut\|not been published" --include='*.md' . \
   | grep -v CHANGELOG.md | grep -v 'docs-drift'; then
  err "a doc still claims no release exists"
fi

# 2. One name. The project once carried three spellings at the same time
#    (repository, Marketplace listing, prose), and the install line and the
#    `uses:` example disagreed with each other. The module path is the anchor:
#    the Action's `name:`, the `cmd/` directory and the install line all derive
#    from it, so a rename that misses one of them fails here instead of on a
#    user's first `go install`.
module=$(awk '/^module /{print $2; exit}' go.mod)
if [ -z "$module" ]; then
  err "go.mod has no module line"
else
  base=${module##*/}
  action_name=$(awk '/^name: /{print $2; exit}' action.yml)
  if [ "$action_name" != "$base" ]; then
    err "action.yml is named '$action_name' but the module is '$module'"
  fi
  [ -d "cmd/$base" ] || err "cmd/$base does not exist (module basename is '$base')"
  if ! grep -q "go install $module/cmd/$base@" "$README"; then
    err "$README has no 'go install $module/cmd/$base@<tag>' line"
  fi
  repo=${module#github.com/}
  if ! grep -qE "uses: *$repo@" "$README"; then
    err "$README has no 'uses: $repo@<ref>' example"
  fi
fi

# 3. The action example must pin a tag that actually resolves.
# README only: it is the canonical example, and prose elsewhere may quote the
# ref shape (`emrelab/go-flake-hunter@<ref>`) without claiming anything is pinned.
ref=$(grep -hoE 'uses: *emrelab/go-flake-hunter@[^ #]+' "$README" \
      | head -1 | sed 's/.*@//')
if [ -z "$ref" ]; then
  err "no 'uses: emrelab/go-flake-hunter@<ref>' example found in $README"
elif [ "$ref" = "main" ] || [ "$ref" = "master" ]; then
  err "the action example pins the mutable branch '$ref'"
elif ! git rev-parse -q --verify "refs/tags/$ref" >/dev/null; then
  err "the action example pins '$ref', which is not a tag in this clone"
fi

# 4. The sample hunt output must be the output the binary produces.
[ -x ./go-flake-hunter ] || { echo "docs-drift: build first (make build)" >&2; exit 2; }
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

cp -R testdata/fixtures/order-basic "$tmp/order-basic"
fixture="$tmp/order-basic"
( cd "$fixture" && "$root/go-flake-hunter" hunt ./... ) \
  > "$tmp/actual.raw" 2>&1
# Only the confirmed block; the run also reports the tests it cleared.
sed -n '/^flake found: TestVictim/,/status .*: confirmed/p' "$tmp/actual.raw" \
  > "$tmp/actual"

# The documented block: between the first fence pair after "Real output".
awk '
  /^Real output/ { seen = 1; next }
  seen && /^```/ { if (inb) exit; inb = 1; next }
  inb { print }
' "$README" > "$tmp/doc"

if [ ! -s "$tmp/actual" ] || [ ! -s "$tmp/doc" ]; then
  err "could not extract the sample output (doc block or fixture run empty)"
else
  norm() {
    sed "s|$fixture/||g; s|--pkg '$fixture'|--pkg '.'|g" "$1" | sed -E \
      -e 's/gfh_[0-9a-f]{12}/gfh_ID/g' \
      -e 's/^[[:space:]]+//' -e 's/[[:space:]]+: /: /' \
      -e 's/^run: .*/run: N\/N/' \
      -e 's/^Wilson 95%: .*/Wilson 95%: X/' \
      -e '/^$/d'
  }
  if ! diff -u <(norm "$tmp/doc") <(norm "$tmp/actual") > "$tmp/diff"; then
    err "the README sample output differs from the binary's"
    sed -n '1,40p' "$tmp/diff" >&2
  fi
fi

# 5. The tag is the release page. release.yml once published with
#    --generate-notes, which lists merged PR titles and throws away the
#    annotated tag message: v0.3.0's release page said nothing but "Full
#    Changelog". The notes a user reads must be the ones a maintainer wrote on
#    the tag, so the publish step is spelled out here.
if grep -v '^[[:space:]]*#' .github/workflows/release.yml | grep -q -- '--generate-notes'; then
  err "release.yml generates notes from the PR list; the tag message must be the release page"
fi
if ! grep -q -- '--notes-from-tag' .github/workflows/release.yml; then
  err "release.yml does not publish with --notes-from-tag"
fi

# 6. Action versions in a copy-paste recipe are a claim about this repo's own
#    pins. docs/CI-REPRO.md taught upload-artifact@v4 while every workflow ran
#    v7: the recipe a reader copies installs a version the project itself moved
#    past, and no test notices. Any action this repo pins must be pinned
#    identically wherever the docs show it.
repo_pins=$(grep -rhoE 'uses: [A-Za-z0-9._/-]+@v[0-9][0-9.]*' \
              .github/workflows/*.yml action.yml | sed 's/.*uses: //' | sort -u)
doc_pins=$(grep -rhoE 'uses: [A-Za-z0-9._/-]+@v[0-9][0-9.]*' \
             --include='*.md' README.md docs | sed 's/.*uses: //' | sort -u)
for pin in $doc_pins; do
  action=${pin%@*}
  # This repo's own action is not a pin to agree with: prose quotes an old ref
  # on purpose to explain the source-build fallback.
  case "$action" in "$repo"|"$repo"/*) continue ;; esac
  want=$(printf '%s\n' "$repo_pins" | awk -F@ -v a="$action" '$1 == a { print $2; exit }')
  [ -z "$want" ] && continue   # not an action this repo runs: a third-party example
  [ "$pin" = "$action@$want" ] \
    || err "docs pin $pin but this repo runs $action@$want"
done

# 7. The banner's test count is a claim about the suite, and it drifted: it
#    read "257 tests" long after there were 209, because nobody re-counts a
#    number by hand. Count what the toolchain sees instead.
listout="$tmp/tests.list"
if ! go test -list '.*' ./... > "$listout" 2> "$tmp/tests.err"; then
  err "go test -list failed, so the README's test count cannot be verified"
else
  ntest=$(grep -cE '^Test' "$listout")
  nfuzz=$(grep -cE '^Fuzz' "$listout")
  if ! grep -qE "^${ntest} tests \+ ${nfuzz} fuzz targets" README.md; then
    err "README must say '${ntest} tests + ${nfuzz} fuzz targets' (from go test -list)"
  fi
fi

# Architecture, documented exit codes and release support must match the tree.
for pkg in internal/*/; do
  grep -Fq "${pkg#internal/}" docs/ARCHITECTURE.md || err "architecture is missing $pkg"
done
for code in $(awk '/^[[:space:]]*Exit[A-Za-z]+[[:space:]]*=/ {print $3}' internal/domain/errors.go); do
  cell=$(printf '| `%s` |' "$code")
  grep -Fq "$cell" README.md || err "README is missing exit $code"
  grep -Fq "$cell" docs/ARCHITECTURE.md || err "architecture is missing exit $code"
done
series=$(git tag --list 'v[0-9]*' --sort=-version:refname | sed -n '1s/^v\([0-9]*\.[0-9]*\)\..*/\1.x/p')
if [ -z "$series" ] || ! grep -Eq "^\| $series[[:space:]]+\| ✅" SECURITY.md; then
  err "SECURITY.md must list the current release series as supported"
fi
if grep -E 'go-flake-hunter explain .*\*\.json' README.md; then
  err "explain requires one capsule"
fi
grep -Eq 'pull/474\).*merged' docs/real-world/README.md || err "Jamf #474 merged status is missing"
scripts/release-check.sh || err "release workflow is missing a required gate"

if [ "$fail" -ne 0 ]; then
  echo "docs-drift: FAILED — update the docs or the fixture, not this script" >&2
  exit 1
fi
echo "docs-drift: ok"
