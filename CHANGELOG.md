# Changelog

All notable changes to this project are documented here.
The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/);
this project adheres to [Semantic Versioning](https://semver.org/).

## [Unreleased]

### Security
- Validate capsule IDs and generated calls. Repro, compile and replay resolve
  recorded package names against the checkout. Witness creation is exclusive,
  and cleanup checks ownership before removing a file.

### Outcome and CI changes
- Exit 7 indicates package failure or incomplete execution. A test that fails
  in isolation is reported as broken and fails `--fail-on-flake` without a capsule.
- JSON run reports add `packages`. Partial results are inconclusive across
  report formats, with a SARIF note for the investigated target.
- Reproduction commands run replay with the actual capsule. Capsules record
  short mode, build tags and the target Go toolchain.
- Load the default baseline automatically and merge updates under a lock.
  Generated capsules and temporary witnesses are excluded from dirty provenance.
- Scaffold gates fail on partial reproduction. CI uploads visible capsules,
  and release builds test the tagged SHA before producing attestations and SBOMs.

### Fixed
- The release page is the annotated tag message again. `release.yml` published
  with `gh release create --generate-notes`, which lists merged PR titles and
  discards the tag body: v0.3.0's page carried nothing but a changelog link,
  while the paragraph explaining *why* the version is breaking sat unread on
  the tag itself. Publishing now uses `--notes-from-tag`, and `CONTRIBUTING.md`
  says the tag must be annotated.
- `docs/CI-REPRO.md` — the copy-paste CI recipe — pinned `checkout@v4`,
  `setup-go@v5` and `upload/download-artifact@v4` while every workflow in this
  repository ran `@v7`/`@v8`: a reader copying it installed action versions the
  project had already moved past. The recipe now matches the repo.
- The README banner claimed "257 tests" long after the suite had 209 (+2 fuzz
  targets). The number is now the toolchain's own count.

### Added
- Three more `scripts/docs-drift.sh` gates, so the three defects above fail in
  CI instead of on a reader: the publish step must take notes from the tag, an
  action pinned in the docs must carry the same major as the workflows, and the
  README's test count must equal `go test -list`'s.

### Changed
- `.github/dependabot.yml` groups routine (minor/patch) Actions bumps into one
  weekly PR; a major still arrives on its own, because that is the pull request
  that needs reading.

## [0.3.0] — 2026-11-13

### Changed
- The project is named `go-flake-hunter` everywhere: module path, module
  directory (`cmd/go-flake-hunter`), binary, `uses:` ref, workflow scaffold,
  capsule directory (`.go-flake-hunter`), capsule id prefix (`gfh_`), build tag
  (`goflakehunter` — a hyphen is not valid in a build constraint), SARIF tool
  name and every doc. Three spellings existed before (a repository called
  flakesmith, an Action listed as go-flake-hunter, and prose using both), which
  meant a reader could not tell whether they were the same tool.
- **Breaking, and why:** capsule ids are derived from the schema id, so
  `SchemaVersion` moves to `go-flake-hunter/v2`. Capsules already on disk keep
  their `gfh_`-era ids and stay readable (`domain.LegacySchema` accepts the
  pre-rename `flakesmith/v1` on read), but a *new* hunt over the same defect
  computes a new id. A pre-rename baseline (`flakesmith/baseline/v1` and
  `/v2`) therefore triggers the existing explicit-rescan path instead of
  silently comparing two id spaces.
- The GitHub repository is renamed to `go-flake-hunter` to match the module
  path: a module path that no longer names the repository cannot be fetched.
  GitHub redirects the old URL, so a workflow still pinned to
  `emrelab/flakesmith@v0.2.1` keeps resolving; the next tag is cut from the new
  name.
- The documented install line moves to `@v0.3.0`, and it is not cosmetic:
  `go install github.com/emrelab/go-flake-hunter/cmd/go-flake-hunter@v0.2.1`
  fails with "module ... found, but does not contain package", because the
  module in that tag is still named `flakesmith`. A release is the only way the
  new path becomes installable.
- `scripts/docs-drift.sh` also gates the one-name invariant now: the module
  path, `action.yml`'s `name:`, the `cmd/` directory and the README's `go
  install`/`uses:` lines must agree, so a rename that misses one of them fails
  in CI instead of on a user's first install.

### Added
- `scripts/make-demo-gif.py` and `make demo`: the README's GIF is rendered from
  the built binary's real output against `testdata/fixtures/order-basic` instead
  of being a hand-recorded picture. The previous one had gone stale in three
  ways (title bar, capsule prefix, build tag) and nobody could regenerate it.
- Third real-world case, and the first where the witness is the contribution
  rather than a re-validation: `Jamf-Concepts/jamf-cli` @ `977b704`, issue
  [#370](https://github.com/Jamf-Concepts/jamf-cli/issues/370). The failing
  `-shuffle=10` log (38 MB, victim at index 1873 of 3146) went through
  `repro --from-json` and came out as one polluter,
  `TestResolveAuth_URLNormalization` → `TestCheckAPIMatchRefusesAPlatformCommandOnAnInstanceProfile`,
  3/3 deterministic. `compile --verify` reported RED; the fix is proposed in
  [#474](https://github.com/Jamf-Concepts/jamf-cli/pull/474). Evidence and the
  attribution note: `docs/real-world/jamf-cli/`.

## [0.2.1] — 2026-11-11

### Added
- `scripts/docs-drift.sh` and its `docs` CI job: the README is a claim about the
  binary, so the gate fails when a doc says no release exists, when the Action
  example pins a branch instead of a real tag, or when the sample `hunt` output
  drifts from what `testdata/fixtures/order-basic` actually prints (ids, run
  counts and Wilson bounds normalized). The job checks out with
  `fetch-depth: 0`, because a shallow clone cannot see the tags it verifies.
- A GitHub Pages site at https://emrelab.github.io/go-flake-hunter/ built from
  `docs/` by `.github/workflows/pages.yml` (build on PRs, deploy only from
  `main`). Docs pages need explicit front matter to be rendered rather than
  copied, and the fences carrying Go templates or Actions expressions are
  wrapped in `{% raw %}` — Liquid runs before markdown and would blank them.
- Two pinned real-world reproductions (#1): gosimple/slug#83 and
  router-for-me/CLIProxyAPI#7356, each verified through replay, RED regression,
  upstream fix and GREEN regression. `scripts/verify-real-world.sh` repeats the
  lifecycle; `docs/real-world/` carries the evidence. Two dependency-free
  reductions keep the isolation patterns in the integration suite.
- `witness.func` on the capsule: the name the hunt actually installed the witness
  under (`TestFlakeWitness`, or `TestFlakeWitness_2` when the package already
  defines the first). `replay` and the printed reproduce command now read it
  instead of hardcoding the constant. Capsules without the field keep working
  (they fall back to the fixed name). Not part of the capsule id — it is
  provenance, not identity (ARCHITECTURE §9.1).
- `PkgFacts.TestSynctest` (issue #22): the facts scan now names tests that use
  the `testing/synctest` bubble API (`Test`/`Run`/`Omega`/`Wait`/`Go`, all
  spellings across the previews and the Go 1.26 graduation). A bubble's clock
  is virtual, so a flagged test whose verdict still varies is a real code bug —
  the "time/network/DB dependent" excuse cannot cover it. Naming only: the tool
  never synthesizes bubbles around someone else's test. Fixture:
  `synctest-bubble` (needs Go ≥ 1.26).
- `scripts/auto/land.sh`: the independent verdict for a direct push. Waits for
  the check runs on a `main` commit, calls green only when every run concluded
  `SUCCESS`/`NEUTRAL`, and on red reverts, pushes the revert and exits 1. Exit 2
  is inconclusive (no check run, or none concluded in time) and never reverts —
  undoing work that may have been fine is worse than a late human look.
- `scripts/auto/digest.sh` / `make auto-digest`: the human's half of the
  autonomous loop in one screen — open `auto/*` PRs with age, CI verdict,
  mergeability and diffstat; `main`'s own CI verdict and recent reverts;
  `auto:blocked` issues; and the cron's state with the two mismatches worth
  naming (paused with work queued, armed on an empty queue). Read-only:
  `gh pr list`, `gh issue list`, `gh api` and one `jq` read, no writes.
  `docs/AUTO.md` §11 says why a run cannot report this itself.
- `docs/CI-REPRO.md`: the CI glue for a red `go test` log — capture the `-json`
  log as an artifact, `repro --from-json` it, post one upserted PR comment with
  the witness. Comments only on exit 0: exit 3/4 means no witness, and silence is
  the honest answer. The workflow is a template (not wired into `.github/`), and
  the page says which half was verified locally.
- `scripts/test-cost.sh` / `make cost`: wall time, per-package time and the
  slowest tests. An operator tool for this exact tuning, not a gate — a
  wall-time assertion is the flakiness issue #12 removed.
- `TestDefaultPassesTheConfigThrough`: `probe.Default` owns the probe list, so
  the test reads `Repeat.Count` and `Order.Seeds` back off the registered probes
  — a transposed `Config` field would otherwise hunt with the wrong `-count` and
  no order assertion would notice.
- `TestHuntRecordsCapsuleProvenance`: the capsule's `repo.commit` and
  `repo.dirty` are asserted against a repository the test owns, in both the
  struct and the JSON a CI consumer reads.
- `TestScanRecordsGlobalsWrittenThroughEveryShape`: all five assignment shapes
  the AST scanner must see through (`x =`, `x.f =`, `x[i] =`, `*p =`, `(x) =`).

### Changed
- The GitHub Action's Marketplace listing name no longer differs from the
  repository name. It had to back then: Marketplace rejects a listing name that
  matches a GitHub user or organization, and `github.com/FlakeSmith` is an
  unrelated account, so the v0.2.0 listing appeared under a second name. The
  rename in this release collapses the two into one.
- The README describes the release that exists. It claimed "no release has been
  cut yet" and "the only tag is `v0.1.0`" for a day after `v0.2.0` shipped with
  five binaries; the install section now carries the real download and `go
  install` lines, and the Action example pins `@v0.2.0` instead of the mutable
  `main`. The stat line no longer calls both real-world cases "fixed": one fix
  is merged upstream, the other is an open proposal applied locally.
- `auto:safe` no longer opens a PR: after `verify.sh --tier safe` it pushes
  `auto/issue-N:main` (explicit refspec, never forced) and `land.sh` takes the
  CI verdict, reverting on red. A PR paid for CI before touching `main`; this
  pays after, which is the whole trade — mechanical work is cheap to revert,
  and the review tier keeps the PR because a design decision is not.
  `docs/AUTO.md` §7 states both tiers, §8.4 the new stop condition.
- The CLI's integration suite is split into six test-only packages —
  `internal/cli/{hunt,huntci,huntevidence,compile,replay,surface}` — over a
  shared `internal/clitest` harness. `go test` runs tests inside a package
  serially but packages in parallel, and every CLI test spawns real `go test`
  subprocesses (a single `hunt` on `order-basic` spawns 33 `go` processes), so
  the single package sat on one core for its whole 185s. Same assertions, same
  count: the `-json` test-name set (subtests included) is 239 before and 239
  after, with no additions. Local wall time 185s → 50-55s; `-race` 184s → 56s.
- PR repeat sweep narrower and honest about it: `-count=3` now covers the unit
  and probe packages, and the integration layer runs once per OS per PR. A
  repeat exists to surface state that survives between runs in one process;
  there it multiplies subprocess spawns instead (18m35s on windows-latest at
  `-count=3`). No CI failure to date came from a repeat-only leak in that layer,
  and the nightly `-count=10` sweep on all three OSes still covers it.

### Fixed
- Baseline matching now uses commit-independent defect fingerprints (baseline
  schema v2), keeping known flakes known after unrelated commits (#32).
  Legacy v1 baselines require an explicit full rescan with `--write-baseline`;
  corrupt files are refused rather than silently replaced. Baseline files are
  excluded from capsule discovery, so default compile/replay still select evidence.
- Compiling multiple victims from one source file preserves every regression
  in separate victim-named files (#33). Existing legacy generated files are
  reused for their own victim, and repeated compilation remains idempotent.
- The parallel probe collected sibling candidates by name only and never
  filtered them by package clause: a package with tests in both `foo` and
  `foo_test` handed a cross-clause name to the witness template, the generated
  file failed to compile, and the probe died with
  `domain.ErrInternal: witness did not compile` — which `ddmin` propagated out
  of the whole hunt. Candidates are now split with the same `splitByClause` the
  order probe uses; the witness is built from the victim's clause only, and a
  cross-clause polluter is reported as `ErrUnsupportedTest` naming both tests and
  both packages. `crossClauseError` takes the witness kind, so the message says
  *parallel* when the parallel probe is the one refusing. New fixture
  `parallel-clause` (`t.Parallel()` with the polluter in the other clause) plus
  three probe-level tests: cross-clause-only, same-clause-does-not-explain, and
  same-clause-covers (the last one guards against over-refusing). The comment
  that claimed the other clause was "a separate process" is corrected: two
  clauses of one directory link into one test binary. Issue #13.

- `replay` reported an uncompilable package as "never reproduced — the flake may
  already be fixed (stale capsule)" and exited 3. `RunResult.BuildFailed` was
  never consulted, so a missing witness made every test absent from the JSON
  stream, which reads as `StatusSkip`, which reads as "not failed". A build error
  is now exit 6 with "package does not build — the witness could not be run", and
  the verdict switch is skipped entirely when a round ended in an error.
- `replay` and the printed `reproduce` command hardcoded `TestFlakeWitness`. In a
  package that defines that name itself (`TestFlakeWitness redeclared`) the hunt
  correctly picked `TestFlakeWitness_2` for its predicate, but replay installed
  the colliding file anyway: build failure, `0/N reproduced`, "the flake may
  already be fixed", exit 3 — a real flake reported as fixed. The name now
  travels in the capsule (`witness.func`) and is used by `replay`, the printed
  `reproduce` command and `ConfirmRace`. `compile`'s generated test keeps its own
  `TestFlakeRegression_*` name, where no such collision exists.
- `hunt --format sarif` wrote an unparseable document when the budget ran out:
  the partial path printed the human summary to stdout and only then the SARIF
  body, so the file started with prose. The partial capsule is now collected for
  `sarif` exactly as for `json`, and the human summary goes to stderr.
- `--sarif FILE` (side file, any `--format`) dropped partial capsules: the file
  said `results: []` while `--format json` reported the same capsule. Same fix as
  above — one collection path for every machine-readable surface.
- `capsule.Store.Read` accepted any `kind` string and let the failure surface
  three layers down as "internal error: unknown flake class". It now validates
  the class and returns a usage error naming the bad value; partial capsules stay
  exempt (they carry no class by construction).
- Capsule provenance asked git four times per capsule (`rev-parse` and `status`,
  twice: once on the witness path and again inside `baseCapsule`). One memoized
  read per package now, with a 10s deadline — a wedged git can no longer hang a
  hunt at the point where the capsule is written.
- `domain.Witness.Cleaners` was a schema field nothing ever wrote or read; removed.
- `facts.PkgFacts.Clause`'s doc comment claimed two clauses are two test binaries.
  They are one binary (cmd/go links the `_test` package into the main test
  binary); what the clause boundary blocks is the witness *form*, not the
  pollution — the same correction the parallel probe's refusal message got in #13.
- `release.yml`'s publish job ran `gh release create` with no checkout, so gh
  could not resolve the repository ("failed to run git: fatal: not a git
  repository") and the first v0.2.0 tag push died at the last step with all
  five platform builds green. The command now passes `--repo
  "$GITHUB_REPOSITORY"` explicitly — the job has no working tree by design.
- The README's GitHub Actions snippet pinned `emrelab/go-flake-hunter@v0.2.0`, a tag
  that was never cut (`git tag` has only `v0.1.0`, `gh release list` is empty),
  so a copy-pasted workflow could not resolve the action. The example now pins
  `main` — the one ref that is both current and real — with a note to pin a `v*`
  release tag once one is published; `docs/ARCHITECTURE.md`'s status line and the
  install paragraph claimed the same nonexistent release and now say what is
  true. Issue #18.

- `docs/ARCHITECTURE.md` §14 no longer bans "cross-package order" outright: the
  `--polluter-pkg` two-stage engine ships, so the fence now excludes what is
  actually out of reach — in-memory state across two `go test` processes — and
  the note that two *clauses* of one directory link into one binary.
- The autonomous loop's review tier could feed itself: a run opened a PR and
  dropped `auto:wip`, which left the issue open, still `auto:review`, and the
  very next `next.sh` call picked it again — one design re-proposed up to
  `MAX_ISSUES` times per tick. The review tier now also adds `auto:pr`, and the
  picker excludes it; a human merges (or closes) the PR and clears the label to
  put the issue back in the queue. `docs/AUTO.md` §0/§2/§7 and
  `CONTRIBUTING.md` state the third resting state; `next.sh --selftest` covers
  both shapes.
- `scripts/auto/run.sh` claimed "there is no cron on purpose" while
  `docs/AUTO.md` §10 documents the scheduled drain; the comment now names the
  one-shot path and the cron job separately.
- `parallel-race` fixture no longer times out into a false "no overlap": the
  barrier is a channel closed by the second arriver (the first wakes in
  microseconds) instead of a 5ms poll of a counter for a fixed 2s. The 30s
  fallback deadline is skipped when the run's `-test.run` cannot select a
  sibling, so the tool's solo/verification passes never pay it. The victim is
  fixed by name (`TestParallelBeta`), so arrival order no longer decides which
  test fails — the witness verification that reported "2/3" on loaded windows
  (issue #12) is now deterministic. `hunt` on this fixture: 56s → 6s, every
  witness run 2.1s → instant.
- New `TestParallelRaceFixtureContract` asserts that instrument directly: the
  serial run passes, the overlapped run fails on the victim only, the victim
  alone passes, the fallback deadline is never paid when a sibling was
  selected, and a solo run costs no more than the serial baseline (the guard
  that fails on the old polling design: 2.27s vs 0.25s).
- CI timeouts back to measured values: the race step 20m → 15m and the
  `-count=3` sweep 40m → 30m. Both bumps existed only to absorb the
  `parallel-race` window; with it gone the windows job runs the race step in
  4m06s (was 9m20s) and the sweep in 13m02s (was 24m04s).
- Pre-commit gate: a commit touching `.github/workflows/` widens the gate to
  the full suite again. The pattern's `$` anchor made `^\.github/workflows/$`
  match only a directory name, which is never a staged file, so CI-only
  commits — exactly the ones the gate exists for — ran build+vet alone.
- `make cover` collapsed from 78% to 51% after the split: `go test
  -coverprofile` collects a package's coverage only from tests physically
  inside it, and the CLI's tests now live next door. `-coverpkg=./...` restores
  it (82.5%, identical to the pre-split number measured the same way).
- Two tests hunted the live `testdata` fixture instead of a copy
  (`TestHuntEndToEnd`, `TestHuntBudgetWritesPartialCapsule`), which made them
  depend on the surrounding working tree — their git state leaked into the
  capsule they produced. They now use a copy like every other test; the
  coverage that accident provided is replaced by the explicit provenance test
  above.

## [0.2.0] — 2026-11-06

### Changed
- `go.mod` floor lowered from 1.26 to 1.24 (measured: `t.Chdir` is the newest
  API in use); CI now proves the floor on a real 1.24 toolchain (`oldest` job).
- CI: per-PR repeat sweep is now `-count=3` on three OSes; the full
  `-count=10` sweep moved to a nightly workflow (`nightly.yml`).
- CI: new `self-hunt` job runs `go-flake-hunter hunt` on go-flake-hunter's own repo.
- The `parallel-race` fixture's fixed 40ms barrier replaced by an adaptive
  wait, so the fixture no longer loses its overlap window under load.
- GitHub Action: the JSON flake counter pins the capsule id prefix
  (`"id": "gfh_`), so a future field named `id` cannot inflate the count.

### Added
- SECURITY.md, dependabot (github-actions), issue/PR templates, CHANGELOG.
- Release workflow (`release.yml`): a `v*` tag publishes CGO-free static
  binaries (linux/darwin amd64+arm64, windows amd64) with a `SHA256SUMS`
  file as a GitHub Release; `workflow_dispatch` builds the matrix without
  publishing.
- GitHub Action: at a tag ref the action now downloads the matching release
  binary, verifies its SHA-256 and its version stamp, and only then skips
  the build; on any miss (branch/SHA ref, missing asset, mismatch, no `gh`)
  it falls back to the unchanged source build. Auth uses the caller's
  `github.token`, overridable via the new `github-token` input.
- Verification contract (#8): `compile --verify` separates `verified`,
  `green-not-reproduced` and `skipped-missing-env`, records the verdict in
  the capsule's `verifyStatus`, and exits 3 unless the artifact actually
  reproduced the red — a skipped run can no longer read as a green
  verification in CI.
- Env detection: `os.Getenv`/`os.LookupEnv` reads in the package's
  non-test sources are collected as app-level evidence and probed after the
  test-local keys — a test that consumes an env var through application
  code is now reachable.
- `repro --from-json FILE|-` (#5): takes a failed `go test -json` log,
  folds subtests onto their parents, and reproduces the suspect list —
  CI's existing failure output is now a direct input.
- SARIF 2.1.0 (#4): `hunt --format sarif`, a `--sarif FILE` side file that
  pairs with any format, and victim file+line pinned from the AST scan into
  the capsule. The action gained an opt-in `upload-sarif` step (code
  scanning annotations never redden the job by themselves).
- Cross-binary order witness (#6): `hunt --polluter-pkg PKG` finds pollution
  that survives the process boundary (files, ports), records the two-stage
  `go test && go test` reproduction, replays it deterministically, and
  refuses compile-time inlining honestly. In-memory globals across binaries
  remain out of scope by physics, not by omission.
- Fuzz targets for the JSON stream parser and the capsule store, ddmin
  property tests (shrink preserves the predicate, stays a subset, is
  idempotent and 1-wise-minimal), and benchmarks for the parser, ddmin and
  the scanner at 25/100/400-test scale.
- `hunt/repro --short`: run the whole scan with `go test -short`. Operator
  choice on the runner, not a spec property — for repos whose integration
  tests honor `testing.Short()`, so a shuffle scan does not re-run them per
  seed. The PR self-hunt uses it; nightly added a full unshorted sweep.

### Fixed
- CI self-hunt: the budget guard was dead code — GitHub's default shell is
  `bash -e`, so a non-zero hunt exit killed the step before `rc` was read
  (surfaced as self-hunt @ d92e895). Guard rewritten (`|| rc=$?`) and the
  PR sweep now runs `--short`, with the unshorted sweep moved to nightly.

## [0.1.0] — 2026-11-04

### Added
- First public release: `hunt`, `repro`, `compile`, `replay`, `explain`,
  `init`; four probe classes (repeat, order, parallel, env); capsules;
  GitHub Action; fixtures for every flake class; zero external dependencies.

[Unreleased]: https://github.com/emrelab/go-flake-hunter/compare/v0.2.1...HEAD
[0.2.1]: https://github.com/emrelab/go-flake-hunter/releases/tag/v0.2.1
[0.2.0]: https://github.com/emrelab/go-flake-hunter/releases/tag/v0.2.0
[0.1.0]: https://github.com/emrelab/go-flake-hunter/releases/tag/v0.1.0
