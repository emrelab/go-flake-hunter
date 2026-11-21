# Contributing

## Setup

```sh
git clone https://github.com/emrelab/go-flake-hunter
cd go-flake-hunter
make all          # fmt + vet + test + build
```

Go 1.24+ is required to build. There are no dependencies; the absence of a
`go.sum` file is deliberate. CI runs the full race suite on Linux, macOS and
Windows with Go 1.24 and 1.26. Go 1.25 and 1.27 also run the full suite on
Linux. Native cancellation and console regressions run repeatedly without the
Go test result cache. The `synctest-bubble` fixture requires Go 1.26 and is
compiled in the Go 1.26 jobs; Go 1.24 builds every other fixture.

## Golden rules

1. **Zero external dependencies.** The standard library is enough. A PR that
   adds a require line to `go.mod` is rejected (rationale: `go install` stays
   one step, review burden stays zero).
2. **`domain` imports no internal package.** If this direction breaks, the
   architecture collapses.
3. **`go test` is only invoked inside `internal/gotest`.**
4. **New types go into `internal/domain`.** Do not define a `TestID`-like type
   in another package; follow the table in `docs/source-of-truth.md`.
5. **Generated code is never edited by hand** (files headed "DO NOT EDIT").

## Testing

```sh
make test          # full suite: -race, real `go test` subprocesses (~10 min)
make test-short    # unit tests only (seconds) — use this while iterating
make test-repeat   # -count=10: does our own tool leak its own state?
make fixtures      # build every fixture module separately
make lint          # golangci-lint, the same gate CI runs
make vuln          # govulncheck: stdlib + toolchain CVEs
make cover
```

The logic in `internal/probe` and `internal/search` is tested with the
**`FakeRunner`**; anything that needs a real `go test` lives under
`testdata/fixtures`. Never let a test depend on a real flake happening — that
would make our own CI flaky.

## Adding a fixture

The easiest contribution. Write a mini module with its own `go.mod` under
`testdata/fixtures/<name>/`:

```sh
mkdir testdata/fixtures/my-flake && cd $_
printf 'module fixture/myflake\n\ngo 1.21\n' > go.mod
# write one polluter + one victim
cd - && make fixtures
```

Document the expected classification in the fixture's own `README.md` and
assert it in the fixture matrix table of the PR.

## Commits

- One topic, one commit.
- Message: `package: what was done` (e.g. `gotest: mark dropped tests correctly in JSON stream`).
- Do not open a PR before `make all` is green.

## Releasing

A tag is the release. Pushing `v*` builds the five binaries and publishes them
(`.github/workflows/release.yml`); nothing else is needed, and nothing is cut by
hand.

```sh
git checkout main && git pull --ff-only
git tag -a v0.4.0 -F /tmp/notes.md      # -a is required, see below
git push origin v0.4.0
```

**The tag must be annotated, and its message is the release page.** The workflow
publishes with `gh release create --notes-from-tag`, so the tag body is what a
reader sees on the release — the one place a version can explain *why* it is
breaking. `--generate-notes` is deliberately not used: it lists merged PR titles
and drops the tag message (v0.3.0 shipped with only a compare link because of
it). A lightweight tag makes the release page empty, and no local gate can see
that — the tag is only publishable once, so it is checked here, by hand.

Write the notes for someone deciding whether to upgrade, not for the git log:

- what changed, in one paragraph;
- **BREAKING** — what stops working, and what an existing user must do;
- anything a user must run again (a rescan, a regenerated baseline).

Then keep `CHANGELOG.md` in step: the version's section is the same content in
Keep a Changelog shape, and `[Unreleased]` starts empty again.

## Where to look

| Question | File |
|---|---|
| Overall design | `docs/ARCHITECTURE.md` |
| Where is this concept defined | `docs/source-of-truth.md` |
