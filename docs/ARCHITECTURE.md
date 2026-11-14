---
layout: default
title: Architecture
---

# go-flake-hunter — Architecture

**Version:** 1.1 · **Date:** November 1, 2026 · **Last synced:** November 14, 2026
**Status:** Implemented; `v0.3.0` is the current release (prebuilt binaries for
linux/darwin/windows, `SHA256SUMS`). The tag list starts at `v0.1.0`.

This document describes not "what we will do" but **how we will build it**: package boundaries, types, algorithms, Go-specific traps, and task order. The product definition is not repeated here.

---

## 1. One-sentence architecture decision

**go-flake-hunter is not a runtime — it is an orchestrator that drives `go test`.**

It runs processes, reads `-json`, **generates a temporary test file** to change the order, shrinks the result, and writes a permanent regression test. At no stage does it patch the Go toolchain, fork a runtime, or compile to WASM.

This decision is the single thing that kept the MVP inside one developer’s reach. ReplayGo (the A− candidate) was eliminated for exactly this reason: touching the runtime means a rebase on every Go release.

### 1.1. Five design constraints

| # | Constraint | Why | If violated |
|---|---|---|---|
| K1 | **Zero external dependencies** (stdlib only in the MVP) | `go install` gives one binary; zero supply-chain surface | `go.sum` grows, review burden rises, the SLSA story weakens |
| K2 | **Never modify source code** — only add/remove *new* files | The user's `git diff` must not get dirty; trust threshold | The tool gets stamped "it changed my code" and is abandoned |
| K3 | **Runner is an interface** — the layer above doesn't know `go test` | Being testable without real flakes | Our own tests go flaky in CI, development stalls |
| K4 | **Every run is budgeted and cancellable** | Not hanging 40 minutes on a pathological search | Eats CI timeouts, users get used to `Ctrl-C` |
| K5 | **Evidence comes from observation; no "root cause" is claimed** | A wrong diagnosis kills trust in the tool | The user fixes the wrong place, the product gets blamed |

---

## 2. Layers

```
┌─────────────────────────────────────────────────────────────┐
│  cmd/go-flake-hunter         main() → cli.Execute() → os.Exit(code)  │
└───────────────────────────┬─────────────────────────────────┘
                            │
┌───────────────────────────▼─────────────────────────────────┐
│  internal/cli        argument → command → report → exit code │
│  (hunt / repro / compile / replay / explain)                 │
└───────────────────────────┬─────────────────────────────────┘
                            │
┌───────────────────────────▼─────────────────────────────────┐
│  internal/probe         hypothesis order: repeat→order→      │
│                         parallel→env                          │
│  ┌──────────────────────────────────────────────────────┐   │
│  │  internal/search   ddmin + verified minimization      │   │
│  └──────────────────────────────────────────────────────┘   │
└────┬──────────────┬──────────────┬──────────────┬───────────┘
     │              │              │              │
┌────▼─────┐ ┌──────▼──────┐ ┌─────▼──────┐ ┌────▼─────────┐
│ gotest   │ │  harness    │ │  facts     │ │  evidence    │
│ (Runner) │ │ (gen/del)   │ │  (AST)     │ │  (env/fs/…)  │
└────┬─────┘ └──────┬──────┘ └─────┬──────┘ └────┬─────────┘
     │              │              │              │
     │       ┌──────▼──────────────▼──────────────▼───────┐
     │       │  internal/capsule        permanent artifact │
     │       └──────────────────┬──────────────────────────┘
     │                          │
┌────▼──────────────────────────▼─────────────────────────────┐
│  internal/compile     capsule → *_goflakehunter_test.go        │
└─────────────────────────────────────────────────────────────┘

  Cross-cutting: internal/domain (types) · internal/policy (budget, redaction)
                 internal/report (text|json|markdown|sarif)
```

**Dependency direction is one-way:** `domain` imports nothing. `gotest`, `facts`, `harness`, `evidence` use only `domain` + stdlib. `probe` uses those. `cli` is on top. No reverse dependencies — nothing looks at `domain` except from its own `domain_test` package.

---

## 3. Directory structure

```
go-flake-hunter/
├── cmd/go-flake-hunter/     CLI entry point and signal handling
├── internal/
│   ├── capsule/            capsule IDs, store and baseline files
│   ├── cli/                commands, checkout resolution and reporting
│   ├── clitest/            integration test helpers
│   ├── compile/            persistent regression generation
│   ├── domain/             types, names, schema and exit codes
│   ├── evidence/           static evidence from package facts
│   ├── facts/              AST scans of active Go files
│   ├── gotest/             discovery, package metadata, runner and event parsing
│   ├── harness/            temporary witness rendering, install and cleanup
│   ├── policy/             budgets, directory locks and redaction
│   ├── probe/              repeat, order, parallel and environment hypotheses
│   ├── report/             text, JSON, Markdown and SARIF
│   └── search/             minimization and verification
├── testdata/               fixture modules and golden files
├── scripts/                local validation and maintenance
└── .github/workflows/      CI, nightly, release and Pages
```

`testdata/` is skipped by the go tool — the fixtures don't pollute `./...`, each one is its own module.

---

## 4. Domain types (single source of truth)

No package outside `internal/domain` defines its own `TestID`/`RunSpec`-like type.

```go
package domain

// TestID identifies a test function together with its package.
// Name is always an exported function prefixed with "Test".
type TestID struct {
	Package string // import path: "example.com/svc/internal/checkout"
	Name    string // "TestCheckout"
}

func (t TestID) String() string { return t.Package + "." + t.Name }

type Status uint8

const (
	StatusSkip Status = iota
	StatusPass
	StatusFail
)

// Outcome is a single test's result in a single run.
type Outcome struct {
	Status  Status
	Elapsed time.Duration
	Output  string // normalized; not raw output
}

// RunSpec is the full description of a "go test" invocation. Same spec + same commit = same run.
type RunSpec struct {
	Package     string        // used as the working directory
	RunRegex    string        // "" → all tests ("^TestX$" for an exact match)
	Count       int           // always >= 1; 1 also breaks the test cache
	Parallel    int           // -parallel; 0 → leave alone
	CPU         int           // -cpu; 0 → leave alone
	ShuffleSeed int64         // 0 → -shuffle off
	Race        bool          // -race
	Env         []string      // "KEY=VALUE" layer; nil → inherit
	Tags        []string      // -tags; changes the set of compiled files
	Timeout     time.Duration // 0 → DefaultTimeout (120s)
	Source      string        // identity of the generated witness file; never reaches the go command
}

// RunResult is the observation of one run.
type RunResult struct {
	Spec        RunSpec
	Outcomes    map[string]Outcome // test name → outcome (subtests not included)
	Order       []string           // order of the -json "run" events
	BuildFailed bool               // build error != test error
	BuildOutput string
	ExitCode    int
	Duration    time.Duration
	TimedOut    bool
}

// Kind is the flake class: the output of the four probes in docs/ARCHITECTURE.md §7.
type Kind string

const (
	KindRepeat   Kind = "repeat"   // fails when repeated in the same process
	KindOrder    Kind = "order"    // fails when another test runs first
	KindParallel Kind = "parallel" // fails only when run in parallel
	KindEnv      Kind = "env"      // depends on an environment variable
)

// Step is a single t.Run call inside the harness.
type Step struct {
	Label string // subtest name: "polluter/TestSeedDB"
	Test  TestID
	// WrapParallel: the witness calls t.Parallel() for tests that don't call it
	// themselves; otherwise the steps run serially and the parallel witness proves
	// nothing. A test that calls its own t.Parallel() is left alone: the second call panics.
	WrapParallel bool
}

// Witness is the minimal witness found.
type Witness struct {
	Kind      Kind
	Victim    TestID
	Polluters []TestID
	Cleaners  []TestID
	Steps     []Step // the exact order to be written into the harness
	Flags     Flags  // flags needed for reproduction
	Seed      int64  // -shuffle seed for KindOrder
	EnvDiff   []string
	Runs      int    // verified over how many runs
}

type Flags struct {
	Parallel int  `json:"parallel,omitempty"`
	Race     bool `json:"race,omitempty"`
	// Count is the -count value a repeat witness was found with ("--repeat N"). Without it
	// a capsule found with -count=50 replays with the verification count (2), goes
	// green, and the tool reports "flake fixed" — a false diagnosis that kills trust
	// in every later capsule.
	Count int `json:"count,omitempty"`
}
```

`RunSpec.Source` never reaches the go command; it only separates the result cache.
Without it, every ddmin subset asks the same spec (same package, same `-run`, same
flags), one subset's PASS is served to the next, and the search eliminates the real
polluter. `Tags` is what lets `compile --verify` run with `-tags goflakehunter`:
a spec without the build tag does not see the generated test.

The flag set a witness writes into a capsule: `Flags{Parallel: 2, Race: true, Count: 10}`
— all three flow into the reproduction command and the `compile --verify` spec; `Count`
is only filled for a repeat witness.

`domain/names.go` — every generated name lives in one place (K3 and the single-source-of-truth rule):

```go
package domain

const (
	// Temporary harness. Files starting with "_" or "." are SKIPPED by the go
	// tool — that's why the "zz_" prefix is required (verified on November 1, 2026).
	WitnessFilePrefix = "zz_goflakehunter_witness_"

	// Permanent artifact.
	PersistentFileSuffix = "_goflakehunter_test.go"
	PersistentBuildTag   = "goflakehunter"

	// Function name inside the harness; a suffix is added on collision.
	WitnessFuncName = "TestFlakeWitness"

	// Capsule directory (relative to the repo root).
	CapsuleDir = ".go-flake-hunter"
)
```

---

## 5. Runner: the single real-world touchpoint

This is the only place in the whole system that sees `go test`. K3 made concrete:

```go
package gotest

// Runner is the only interface the probe/search layers see.
// Tests replace it with FakeRunner; the layer above knows nothing about exec.
type Runner interface {
	Discover(ctx context.Context, pkg string) ([]domain.TestID, error)
	Run(ctx context.Context, spec domain.RunSpec) (*domain.RunResult, error)
}
```

### 5.1. Discover

`go test -list '.*'` gives top-level tests in source order. The harness
calls the top-level test and observes its subtest tree. `go list -json` selects
the package files for the static scan, including active build tags.

```go
func (r *Exec) Discover(ctx context.Context, pkg string) ([]domain.TestID, error) {
	cmd := exec.CommandContext(ctx, "go", "test", "-list", `.*`, ".")
	cmd.Dir = pkg
	cmd.Env = os.Environ() // GOFLAGS, GOCACHE, PATH are inherited — never a clean env
	out, err := cmd.Output()
	// "ok  \tpkg\t0.12s" lines are dropped; every remaining line is a test name
}
```

### 5.2. Run

```go
func (r *Exec) Run(ctx context.Context, spec domain.RunSpec) (*domain.RunResult, error) {
	if spec.Count < 1 {
		spec.Count = 1 // -count=1 is mandatory: the test cache hides flakes
	}
	args := []string{"test", "-json", "-count", strconv.Itoa(spec.Count)}
	if spec.RunRegex != "" {
		args = append(args, "-run", spec.RunRegex)
	}
	if spec.Parallel > 0 {
		args = append(args, "-parallel", strconv.Itoa(spec.Parallel))
	}
	if spec.CPU > 0 {
		args = append(args, "-cpu", strconv.Itoa(spec.CPU))
	}
	if spec.ShuffleSeed != 0 {
		args = append(args, "-shuffle", strconv.FormatInt(spec.ShuffleSeed, 10))
	}
	if spec.Race {
		args = append(args, "-race")
	}
	args = append(args, ".")

	cmd := exec.CommandContext(ctx, "go", args...)
	cmd.Dir = spec.Package
	cmd.Env = append(os.Environ(), spec.Env...)

	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	start := time.Now()
	err := cmd.Run()
	// *exec.ExitError is expected: a failing test is data, not an error.
	return parseJSON(stdout.Bytes(), spec, err, time.Since(start))
}
```

The runner matches the process exit status to the package terminal event
and records terminal events for each test and repeat round. A complete test
failure is data for the probe. A package that reports failure with no failed
test ends the run with `ErrPackageFailed`. Missing or inconsistent terminal
events produce `ErrIncomplete`. Both return exit 7. Compilation failures
return exit 6. Each run consumes budget and inherits the target Go environment.

### 5.3. Order reconstruction

In the `-json` stream, `Action: "run"` events give the **actual start order** of the tests (verified). But in parallel tests, the `run` event of a test calling `t.Parallel()` happens at the moment of *pause*, i.e. the order is misleading.

**That's why the order-dependency hypothesis is only searched under `-parallel=1`.** A parallel flake is a separate hypothesis (`KindParallel`); its minimization is done not over an order but over a **set**.

```go
// parseJSON → RunResult.Order: only Action=="run" + Test!="" events,
// in arrival order. Subtests ("TestA/sub") are dropped.
```

### 5.4. Output normalization

The same error arrives with a different timestamp/tmp path/pointer in two runs; the signature comparison must strip them:

```go
var normalizers = []*regexp.Regexp{
	regexp.MustCompile(`\d{4}/\d{2}/\d{2} \d{2}:\d{2}:\d{2}(\.\d+)?`), // timestamp
	regexp.MustCompile(`/var/folders/\S+`),                              // macOS tmp
	regexp.MustCompile(`/tmp/\S+`),                                      // linux tmp
	regexp.MustCompile(`0x[0-9a-f]{6,}`),                                // pointer
	regexp.MustCompile(`\b\d+(\.\d+)?(ns|µs|ms|s)\b`),                   // duration
	regexp.MustCompile(`127\.0\.0\.1:\d+`),                              // ephemeral port
}
```

---

## 6. Harness: the heart of the product

**Single generator, two consumers.** Temporary during the search, permanent at the end. That's why the search and the artifact are the *same mechanism* — the code-level counterpart of the "Go-native engine + compiled test" defense.

### 6.1. Why must we generate?

Verified fact: `go test -run '^(TestAlpha|TestZebra)$'` runs in **source order**; the alternation order in the regex changes nothing. The only way to put tests in order in Go is to write a test that calls them.

{% raw %}
```go
const witnessTmpl = `// Code generated by go-flake-hunter; DO NOT EDIT.
// Temporary witness file — deleted after the run.

package {{.Package}}

import "testing"

func {{.Func}}(t *testing.T) {
{{- range .Steps}}
	t.Run({{printf "%q" .Label}}, {{.Test}})
{{- end}}
}
`
```
{% endraw %}

Generated example (for the `order-basic` fixture):

```go
// Code generated by go-flake-hunter; DO NOT EDIT.

package flakedemo

import "testing"

func TestFlakeWitness(t *testing.T) {
	t.Run("polluter/TestSeedDB", TestSeedDB)
	t.Run("victim/TestCheckout", TestCheckout)
}
```

### 6.2. Package clause — the silent trap

A package can have two kinds of test files: `package foo` (inner) and `package foo_test` (outer). **These are two separate packages.** A test function of `foo` cannot be called from `foo_test` — `undefined: TestPolluter` (verified).

Rule:

1. Find the package clause of the file that defines the victim (`facts.TestClause`).
2. Is the clause the same for all polluters? If different → `ErrUnsupportedTest`, message: *"TestCheckout (package foo_test) cannot be called in the same test binary as TestSeedDB (package foo); the two live in different test packages."*
3. If the same, the harness is generated with that clause.

Also checked: is there a `TestMain` (if so, it runs too since the harness is in the same package — desired behavior, but stated in the report), does the name `TestFlakeWitness` already exist (if so, `TestFlakeWitness_2`).

### 6.3. File name and lifecycle

- The harness formats source before creating a `zz_goflakehunter_witness_*_test.go`
  file. Exclusive creation rejects an existing file or symlink.
- The caller holds the package lock across installation, execution and
  deferred removal. Removal checks file identity and the checksum header.
- Startup cleanup uses the same lock. It removes checksum-verified files
  and complete legacy files with the exact old template header and a single
  witness declaration. Other files stay in place.
- `compile` writes the persistent, build-tagged artifact on request.

---

## 7. Search: four probes, by cost order

Cheapest hypothesis first. If a probe returns `ErrNotReproduced`, move on to the next.

```go
type Probe interface {
	Name() string
	// Probe returns either a verified Witness or ErrNotReproduced.
	Probe(ctx context.Context, tgt Target, r gotest.Runner) (*domain.Witness, error)
}

type Target struct {
	Test   domain.TestID
	PkgDir string
	Facts  facts.PkgFacts
}
```

| Order | Probe | Cost | What it does | If found |
|---|---|---|---|---|
| 1 | `repeat` | **1 run** | `-count=10`; anything that passes the first round then fails? | `KindRepeat` |
| 2 | `order` | ~10–40 runs | passes alone, fails with all → shuffle sweep → ddmin | `KindOrder` |
| 3 | `parallel` | ~6 runs | passes with `-parallel=1`, fails with `-parallel=2,4,8` | `KindParallel` |
| 4 | `env` | ~4 runs/var | flip each variable read by `os.Getenv` to a sentinel | `KindEnv` |

### 7.1. `repeat` — the one-command flake detector

Verified: with `-count=5`, an error like "state survived into run 2" shows up in a single run. On `-count=1` it passes clean. This is the cheapest and most frequently productive of all hypotheses — hence first in line.

```go
func (p Repeat) Probe(ctx context.Context, tgt Target, r gotest.Runner) (*domain.Witness, error) {
	base, err := r.Run(ctx, domain.RunSpec{
		Package: tgt.PkgDir, RunRegex: "^" + tgt.Test.Name + "$", Count: 1,
	})
	if err != nil { return nil, err }
	if base.Outcomes[tgt.Test.Name].Status == domain.StatusFail {
		return nil, ErrNotReproduced // fails on its own too: not a flake
	}
	rep, err := r.Run(ctx, domain.RunSpec{
		Package: tgt.PkgDir, RunRegex: "^" + tgt.Test.Name + "$", Count: p.Count, // 10
	})
	if err != nil { return nil, err }
	if rep.Outcomes[tgt.Test.Name].Status != domain.StatusFail {
		return nil, ErrNotReproduced
	}
	return &domain.Witness{Kind: domain.KindRepeat, Victim: tgt.Test, Runs: 2}, nil
}
```

No minimization needed: one test, one hypothesis. The evidence reads directly ("it failed on round 2").

### 7.2. `order` — order dependency

```
1) victim alone, -parallel=1   →  if it fails: not a flake, stop
2) all tests, -parallel=1      →  if it fails: the observed order is in hand, go to 4
3) shuffle seeds [1..N]        →  take the order of the first failing seed, go to 4
4) shrink the tests BEFORE the victim with ddmin
5) re-verify the result 3 times
```

```go
func (p Order) Probe(ctx context.Context, tgt Target, r gotest.Runner) (*domain.Witness, error) {
	// 1) victim alone
	alone, err := r.Run(ctx, spec(tgt, `^`+tgt.Test.Name+`$`, 1))
	if err != nil { return nil, err }
	if alone.Outcomes[tgt.Test.Name].Status == domain.StatusFail {
		return nil, ErrNotReproduced
	}

	// 2) everything, source order, single thread
	full, err := r.Run(ctx, spec(tgt, "", 1)) // RunRegex="" → all tests
	if err != nil { return nil, err }
	if full.Outcomes[tgt.Test.Name].Status == domain.StatusFail {
		return p.minimize(ctx, tgt, r, full.Order)
	}

	// 3) shuffle sweep
	for _, seed := range p.Seeds {
		res, err := r.Run(ctx, domain.RunSpec{
			Package: tgt.PkgDir, Count: 1, Parallel: 1, ShuffleSeed: seed,
		})
		if err != nil { return nil, err }
		if res.Outcomes[tgt.Test.Name].Status == domain.StatusFail {
			return p.minimize(ctx, tgt, r, res.Order)
		}
	}
	return nil, ErrNotReproduced
}
```

### 7.3. ddmin + verification

Classic ddmin, but **a non-deterministic predicate produces false minimality**. That's why the result is re-verified N times; if it cannot be verified, `ErrNotReproducible`.

```go
type Predicate func(ctx context.Context, subset []domain.TestID) (bool, error)

// Minimize finds the smallest set satisfying the predicate "victim fails when the
// subset runs first", then re-verifies the result verify times.
func Minimize(ctx context.Context, cand []domain.TestID, pred Predicate, verify int) ([]domain.TestID, error) {
	min, err := ddmin(ctx, cand, pred)
	if err != nil { return nil, err }
	for i := 0; i < verify; i++ {
		ok, err := pred(ctx, min)
		if err != nil { return nil, err }
		if !ok { return nil, ErrNotReproducible }
	}
	return min, nil
}

// ddmin: split-try, then try the complement (classic Zeller algorithm).
func ddmin(ctx context.Context, cand []domain.TestID, pred Predicate) ([]domain.TestID, error) {
	n := 2
	for len(cand) >= 2 {
		chunks := split(cand, n)
		reduced := false

		for _, c := range chunks {             // is a single chunk enough?
			ok, err := pred(ctx, c)
			if err != nil { return nil, err }
			if ok { cand, n, reduced = c, 2, true; break }
		}
		if reduced { continue }

		for i := range chunks {                // drop this chunk, keep the rest
			comp := complement(chunks, i)
			if len(comp) == 0 { continue }
			ok, err := pred(ctx, comp)
			if err != nil { return nil, err }
			if ok { cand, reduced = comp, true; n = max(n-1, 2); break }
		}
		if reduced { continue }

		if n >= len(cand) { break }
		n = min(n*2, len(cand))
	}
	return cand, nil
}
```

### 7.4. Predicate: harness + runner

The search and the artifact use the same generator — the only difference is whether the file is permanent.

```go
func (p Order) predicate(tgt Target, r gotest.Runner) Predicate {
	return func(ctx context.Context, subset []domain.TestID) (bool, error) {
		steps := make([]domain.Step, 0, len(subset)+1)
		for _, t := range subset {
			steps = append(steps, domain.Step{Label: "polluter/" + t.Name, Test: t})
		}
		steps = append(steps, domain.Step{Label: "victim/" + tgt.Test.Name, Test: tgt.Test})

		h := harness.New(tgt.PkgDir)
		if _, err := h.Install(p.clause, steps, p.fn); err != nil { return false, err }
		defer h.Remove()

		res, err := r.Run(ctx, domain.RunSpec{
			Package: tgt.PkgDir,
			RunRegex: "^" + p.fn + "$", // only the witness we generated
			Count:   1, Parallel: 1, Race: p.Race,
		})
		if err != nil { return false, err }
		if res.BuildFailed { return false, fmt.Errorf("%w: %s", ErrBuildFailed, res.BuildOutput) }
		return res.Outcomes[tgt.Test.Name].Status == domain.StatusFail, nil
	}
}
```

Verified detail: **the victim still runs even if the polluter itself fails** — when a subtest fails, `t.Run` lets the parent continue. So ddmin gets the right answer even on polluters that blow up.

### 7.5. `parallel` and `env` (post-MVP but designing now)

`parallel`: baseline passes with `-parallel=1`, fails with `-parallel=2,4,8` → hypothesis confirmed. Minimization is over a **set, not an order**: which tests can be removed from the set while it still fails. `t.Cleanup` ordering and port collisions are the typical causes in this class.

`env`: for each variable in the `facts.EnvReads` list, run with `KEY=<synthetic>` and compare against the baseline. The value is never written into the capsule — only the **key name** and "effective/not effective".

---

## 8. Evidence collection

Evidence is a list of **observations**, not a "why" claim. Every field is optional; whatever is filled, the report shows it.

| Field | Source | Cost | When valuable |
|---|---|---|---|
| `EnvDiff` | `os.Environ()` diff of two runs (values masked) | 0 | Environment-caused flake |
| `GlobalsWritten` | AST: which package vars does the test function assign | 0 | Internal pollution (39% in real-world scans) |
| `GlobalsRead` | AST: which package vars does the victim read | 0 | Intersection = strong signal |
| `FilesTouched` | file paths in `-v` output + `t.TempDir()` patterns | 0 | External pollution |
| `PortsUsed` | `127.0.0.1:\d+` in `-v` output | 0 | Parallel port collision |
| `RaceOutput` | `-race` output (summarized) | 1 run | Real data race |
| `GoroutineTop` | first 3 lines of the stack at timeout/panic | 0 | Deadlock |

**Intersection rule:** if `GlobalsWritten ∩ GlobalsRead` is not empty, the report says "suspected cause: shared package variable `checkout.seeded` (written by TestSeedDB, read by TestCheckout)". This is **the single highest-value line** — because it tells the user where to look, without claiming a "root cause".

In the MVP, the AST walk is enough (`go/ast`); SSA (`golang.org/x/tools/go/ssa`) is optional in Phase 3, behind a build tag so as not to break K1.

---

## 9. Capsule and permanent artifact

### 9.1. Identity: content-addressed

Same commit + same victim + same witness → same ID. This is idempotency: a re-run in CI does not produce a new file.

```
id = sha256(schema | module | commit | victim.pkg | victim | kind
            | sort(polluters) | parallel | race | sort(envKeys))[:12]
```

Time, machine, and run count do not enter the identity: the same defect writes the
same file on every run — it becomes "one capsule per bug", not "one capsule per run".
`count` is deliberately excluded too — the same victim, the same polluter, the same
class is the same bug; the number of repetitions it was caught in does not change the identity.

`repo.dirty` is excluded for a related but distinct reason: **a capsule is a claim about
committed code.** The identity names the tree the evidence is about (`commit`), and the
working tree is a *property of that run*, not of the bug — it travels next to the identity
as `repo.dirty: true`, which is where a consumer that cares (a CI gate refusing evidence
taken over uncommitted patches) reads it. Mixing a tree-state digest into the ID would make
every save-edit-rerun cycle mint a new capsule for the same defect, and the file would stop
being a stable answer to "what does this commit owe us?".

The cost of that decision is real and accepted: a hunt over uncommitted changes overwrites
the capsule of the committed tree it just proved, and the overwritten file carries
`dirty: true`, so the loss is visible in the artifact rather than silent. A user who wants
the two kept apart commits or stashes before hunting — the tool does not guess which of the
two trees a capsule belongs to.

`witness.func` is excluded for the same reason, one step further out: the witness function
name (`TestFlakeWitness`, or `TestFlakeWitness_2` when the package already defines the first
one) is derived from the package's own test names, not from the defect. It changes when a
user adds a test, so hashing it would re-open the same bug as a new capsule. It still has to
travel **with** the capsule: `replay` and the printed reproduce command reinstall that file,
and a hardcoded name collides with the user's test, breaks the build, and reads as "the flake
is gone". Provenance, not identity.

```json
{
  "schema": "go-flake-hunter/v2",
  "id": "gfh_a3f19c02b7d4",
  "createdAt": "2026-11-01T00:42:11+03:00",
  "repo": {"module": "example.com/svc", "commit": "a1b2c3d", "dirty": false},
  "tool": {"version": "0.3.0", "go": "go1.26.2", "os": "darwin", "arch": "arm64"},
  "target": {"package": "example.com/svc/internal/checkout",
             "relDir": "./internal/checkout", "victim": "TestCheckout"},
  "kind": "order",
  "witness": {
    "kind": "order",
    "victim": {"package": "example.com/svc/internal/checkout", "name": "TestCheckout"},
    "polluters": [{"package": "example.com/svc/internal/checkout", "name": "TestSeedDB"}],
    "steps": [{"label": "polluter/TestSeedDB"},
              {"label": "victim/TestCheckout"}],
    "func": "TestFlakeWitness",
    "flags": {"parallel": 1},
    "seed": 1788950163029644000,
    "runs": 3
  },
  "observation": {"runs": 3, "failures": 3, "deterministic": true, "failureRate": 1},
  "evidence": {
    "globalsWritten": {"TestSeedDB": ["seeded"]},
    "globalsRead": {"TestCheckout": ["seeded"]},
    "race": null
  },
  "repro": {
    "command": "go test ./internal/checkout -tags goflakehunter -run '^TestFlakeRegression_TestCheckout$' -count=1 -parallel=1",
    "generatedTest": "checkout_TestVictim_goflakehunter_test.go"
  },
  "status": "confirmed"
}
```

The `steps[].test` field is **not written** to JSON (`json:"-"`): the name is already
inside `label`, and keeping it in two places would bloat the capsule. In a step read
back from a capsule, `Test` stays empty — code generation must use `Step.Func()`, not `Test.Name`.
`witness.func` is the name the hunt actually installed, and the only name `replay` and
`repro.command` are allowed to print (see §9.1); capsules written before it existed fall
back to `TestFlakeWitness`.
`evidence` drops empty fields with `omitempty`; `race` is only filled in a `--race`
verification. `note` (if present) carries the one-sentence warning that `status`
does not say: "budget exceeded: 1/1 runs", "not reproduced under a race run".

`status`: `confirmed` (all verification rounds reproduced), `suspected`
(some completed rounds reproduced), or `partial` (interrupted without verdict).
The legacy `probabilistic` status remains readable. Suspected order/parallel
candidates retain the measured k/n, rate and two-sided Wilson 95% interval;
confirmed remains strict N/N. Every verification bypasses result caching,
including prior passing results. Suspected findings are excluded from baseline
and flake gate, rendered as SARIF review/note, and cannot be compiled. Replay
can measure them again without changing their status. A suspected-only hunt
exits 0; a suspected-only repro exits 3.

### 9.2. Permanent test

The template is separate **per class**: reproducing the four classes requires different
code; one generic body used to stay green in three classes (the tool said "it will be
red", the test passed).

| Class | Body | Requirement to reproduce |
|---|---|---|
| `order` | `t.Run` chain (polluter → victim) | order |
| `parallel` | each step `t.Run` + `t.Parallel()` if `WrapParallel` | `-parallel=N`, slots = number of steps |
| `repeat` | the same victim `Flags.Count` times, in one process | `-count=N` |
| `env` | `t.Skip` when the variable is missing, then the victim | the variable itself (its value is not stored) |

```go
//go:build goflakehunter

// Code generated by go-flake-hunter; DO NOT EDIT.
//
// Capsule: gfh_a3f19c02b7d4
// Class: order · Polluter: TestSeedDB
// Victim: TestCheckout
//
// Reproduce:
//   go test ./internal/checkout -tags goflakehunter -run '^TestFlakeRegression_TestCheckout$' -count=1 -parallel=1

package checkout_test

import "testing"

func TestFlakeRegression_TestCheckout(t *testing.T) {
	t.Run("polluter/TestSeedDB", TestSeedDB)
	t.Run("victim/TestCheckout", TestCheckout)
}
```

The "Reproduce" line in the header is not written by hand: it comes from
`compile.CommandLine`, i.e. the class, `-race`, `-parallel`, `-count` and `-tags` truth
lives in one place. If the witness was found with `-race`, it also enters the command and
`VerifySpec`; otherwise, on a race-only flake the artifact stays green and `--verify`
wrongly says "the artifact does not reproduce".

`compile --verify` really runs the generated file **once**
(`RunSpec{Tags: [go-flake-hunter], Race: the witness's, Parallel: width}`). If it is not
red, exit code 3: the claim "the artifact does not reproduce" is measured, not
a guess.

The price of the build tag: `go test ./...` does not see this. In exchange, the default suite is neither slowed down nor turned red. If the user removes the tag, it runs on every run — both options are written in the README.

**Important:** the test will be **red** right now (the flake is still there). The artifact's job is to catch the flake, not to fix it. After the fix it turns green and guards the regression permanently. That's why when `compile` says "the test will be RED right now", `--verify` proves it.

---

## 10. Budget, cancellation, error handling

```go
type Budget struct {
	MaxRuns int           // default 1000 (policy.DefaultMaxRuns)
	MaxWall time.Duration // default 5m
	runs    int
	started time.Time
}

func New(maxRuns int, maxWall time.Duration) *Budget {
	return &Budget{MaxRuns: maxRuns, MaxWall: maxWall, started: time.Now()}
}

// Spend is called at the START of every run. The time check happens at run
// start; the last run may overshoot by its own Timeout — this overshoot is bounded
// and deliberate (cutting a run in half produces a wrong negative).
func (b *Budget) Spend(n int) error {
	if b.runs+n > b.MaxRuns {
		return fmt.Errorf("%w: %d/%d runs", ErrBudgetExceeded, b.runs+n, b.MaxRuns)
	}
	if elapsed := time.Since(b.started); elapsed > b.MaxWall {
		return fmt.Errorf("%w: %s / %s", ErrBudgetExceeded, elapsed.Round(time.Second), b.MaxWall)
	}
	b.runs += n
	return nil
}
```

Every `Runner.Run` call does one `Spend(1)`, via a `Runner` wrapper:

```go
type budgeted struct {
	inner gotest.Runner
	b     *policy.Budget
}

func (r budgeted) Run(ctx context.Context, s domain.RunSpec) (*domain.RunResult, error) {
	if err := r.b.Spend(1); err != nil { return nil, err }
	return r.inner.Run(ctx, s)
}
```

No panic when the budget is exceeded: a `status: partial` capsule is written and **which steps were tried** is reported. A partial result is worth more than an empty one.

### 10.1. Typed errors → exit codes

| Condition | Exit | Message (to the user) |
|---|---|---|
| `hunt`: no witness (clean scan) | `0` | one block: `flake not found: N tests in M packages, runs, duration` + per-probe tally (`-v` keeps the per-test `tried:` list) |
| `hunt`/`repro`: witness found, no `--fail-on-flake` | `0` | report + capsule; the gate stays open |
| `ErrFlakeFound` (`--fail-on-flake`, outside baseline) | `1` | "flake found" |
| `ErrUsage` | `2` | "usage: go-flake-hunter repro <package-dir> [TestName]" |
| `ErrNotReproduced` | `3` | `repro`: requested test could not be reproduced within the budget; `replay`: witness did not fail N/N; `compile --verify`: generated test stayed green |
| `ErrBudgetExceeded` | `4` | "budget exceeded" + note: "raise the budget: --budget N --timeout 10m" (partial capsule written) |
| `ErrUnsupportedTest` | `5` | "unsupported test form: <reason>" (unsupported witness or package combination) |
| `ErrBuildFailed` | `6` | "package failed to build: <reason>" — also `replay` when the package does not compile (a build error is never reported as "the flake is fixed") |
| `ErrPackageFailed` or `ErrIncomplete` | `7` | package failed after its tests or the selected run did not complete |
| internal error | `10` | "internal error: <id> (use --debug for detail)" |
| `context.Canceled` / SIGINT | `130` | "cancelled (SIGINT); unfinished witness files deleted" (128+SIGINT) |

The distinguishing rule: `hunt` asks "I looked around, there's nothing" — even a
found flake exits 0 while the gate is closed. `repro` asks about a single suspect;
if no answer comes, the request has failed (3). With `--baseline`, v2 stores commit-independent defect fingerprints, separate
from capsule IDs. A fingerprint includes module, victim package/name, class,
sorted polluter identities, env keys and same-/cross-process shape. Commit,
dirty status, generated name, repetition count, parallel width and race flags
are excluded. Capsule IDs and provenance retain their existing semantics.
Legacy v1 baselines are refused with regeneration guidance; explicitly running
`hunt ./... --write-baseline` replaces a v1 file using only the confirmed
findings from that scan. Scan all previously covered packages. Invalid files
are never silently overwritten.

With `--baseline`, known identities do
not turn `hunt` red; only **new** flakes produce 1 with `--fail-on-flake`.

`--format json` always produces **a single document**; it is not line-delimited JSON:

```json
{"schema": "go-flake-hunter/v2", "flakes": [ … capsule … ], "runs": 42, "maxRuns": 1000}
```

On a clean run: `"flakes": []` — not empty output, but the sentence "scanned and clean".

The single-document rule covers the **partial** path too, in every machine format: a
budget-capped `--format sarif`, and the `--sarif FILE` side file, both carry the partial
capsule (a partial result is a finding). The human summary goes to stderr there, so the
artifact on stdout stays parseable — one run, one document, whichever consumer reads it.

Without `--debug`, no stack trace is shown. A raw panic never leaks to the user (K5).

### 10.2. Concurrency

- `hunt --jobs N` bounds concurrent package scans, default `min(NumCPU, 4)`.
  Workers share the thread-safe run/time budget and cached runner; reports,
  counters and capsule lists remain worker-local and merge in package order.
  Canonical directory deduplication prevents a package from being scheduled twice.
  An error stops new dispatch while active workers finish honestly; partial
  findings survive and retain their exit code. `repro` and `--polluter-pkg`
  scans are serial. Cross-binary sequences lock both directories in canonical
  order, with context tokens preserving re-entrant ownership of both.
- **Two runs in the same package are never parallel** — they'd share the same port/temp file/DB and produce false positives against each other. A per-package `sync.Mutex` semaphore and a directory lock (`policy/dirlock`) also prevent a second go-flake-hunter in the same directory.
- The width inside the `probe/parallel` class is given via `-parallel=N`; that is the witness's own load, not the concurrency of the package scan.

---

## 11. Testing strategy: developing without real flakes

The problem: producing real flakes in our own tests is not deterministic — our own tool would go flaky in CI. The solution is **two layers**:

### 11.1. FakeRunner — logic layer (fast, deterministic)

```go
type FakeRunner struct {
	Discoveries map[string][]domain.TestID
	// Script maps a spec to a deterministic result: the "fake universe".
	Script func(domain.RunSpec) (*domain.RunResult, error)
	Runs   []domain.RunSpec // records what was called (for assertions)
}

func (f *FakeRunner) Run(ctx context.Context, s domain.RunSpec) (*domain.RunResult, error) {
	f.Runs = append(f.Runs, s)
	return f.Script(s)
}
```

Example fake universe: `TestVictim` fails only when `RunRegex` covers all tests → the whole order probe + ddmin + harness flow is tested without a real `go test`. **This is the highest-leverage testing decision in the project:** probe/ddmin/classification logic is verified in milliseconds, 100% deterministically.

### 11.2. Fixture modules — integration (real `go test`)

`testdata/fixtures/*`, each with its own `go.mod`; they contain real flake types:

| Fixture | Content | Expected class |
|---|---|---|
| `order-basic` | package var; polluter writes, victim reads | `KindOrder`, polluter = TestSeedDB |
| `parallel-race` | `t.Parallel()` + shared counter | `KindParallel` |
| `env-dependent` | assertion branching on `os.Getenv("APP_ENV")` | `KindEnv`, key = APP_ENV |
| `file-leak` | polluter writing to a fixed path instead of `t.TempDir()` | `KindOrder` + `FilesTouched` |
| `repeat-accumulate` | global counter, fails on round 2 | `KindRepeat` |
| `clause-mismatch` | mixed `package foo` + `package foo_test` | `ErrUnsupportedTest` |
| `synctest-bubble` | `testing/synctest` bubble (needs Go ≥ 1.26) | facts only: `TestSynctest` names the bubble tests, none flaky |

| `parallel-clause` | `t.Parallel()` with the polluter in the other clause | `ErrUnsupportedTest` |


### 11.3. Golden files

The generated test file is compared byte-by-byte (`testdata/golden/*.go`). Code-generator regressions are caught here. Refreshed with the `-update` flag.

### 11.4. Our own tests must not be flaky

- Integration tests are skipped with `testing.Short()`; a separate CI job.
- No test depends on real time/ports/network.
- PRs run `-count=3` on the unit/probe packages and the integration layer once per OS; the full `-count=10` sweep runs nightly (`nightly.yml`); CI also runs our own tool against our own repo (`self-hunt` job) — dogfooding.

### 11.5. Why the CLI's integration tests live in their own packages

`internal/cli/hunt`, `huntci`, `huntevidence`, `compile`, `replay` and `surface` are test-only packages; the shared harness is `internal/clitest`. Nothing here is a product package — the split is a scheduling decision.

Every CLI integration test spawns real `go test` subprocesses (a single `hunt` on `order-basic` spawns 33 `go` processes), and the suite used to be one package: `go test` runs tests *inside* a package serially, so 38 subprocess-driven tests took 185 s on one core and the same 185 s again on every CI OS. Split across packages, `go test ./...` schedules them in parallel — `-p` is the CPU count — and the same assertions finish in ~50 s. The cost measurement is reproducible: `make cost` prints wall time and the number of `go` invocations.

Two rules keep the split from becoming a hiding place:

- **A test moves, it does not disappear.** The acceptance criterion for any further split is that the set of test names (subtests included) before and after is identical, checked from `go test -json`, not by eye.
- **Coverage needs `-coverpkg`.** With the tests in a neighbouring package, `go test -coverprofile ./...` measures each package only with the tests physically inside it; `internal/cli` would read as ~0% while its tests all live next door. `make cover` passes `-coverpkg=./...` for that reason (measured: 51% without, 82% with).

---

## 12. Build history: the order it was built in, and why

**Vertical slice first, horizontal layers later.** The build went: module skeleton + domain types → runner (`Discover` + `Run`) → `facts` AST scanner → harness → **`repeat` probe end-to-end** (discover → run → capsule → report) → budget/cancellation → ddmin → `order` probe → `compile` → CLI + markdown/JSON output → `parallel`/`env` probes → GitHub Action + docs. Each step’s acceptance criteria are no longer prose here — they are named tests and fixtures in the tree.

The first end-to-end slice (through the `repeat` probe) already produced a usable product; every later probe plugged into working infrastructure instead of a layer cake of half-compiled types. Writing layer by layer (all types first, then the whole runner, then all probes) is the path that produces the "nothing works yet" feeling three weeks in — avoided on purpose.

Remaining roadmap items live in the GitHub issue tracker, not in this document.

---

## 13. Risks and open questions

| # | Risk | Impact | Mitigation |
|---|---|---|---|
| R1 | `-shuffle` seed space is huge; an order not found in the seed list | False negative | Default 12 seeds (a 40-seed sweep cost ~12 s/package for no recall gain); raise with `--seeds`; the report says "not found in this range" |
| R2 | Tests use an external service/DB → reproduction impossible | `ErrNotReproduced` | `status: probabilistic`; warning to the user: "this package uses external dependencies" |
| R3 | Package contains 200+ tests; one ddmin sweep takes 40s | Budget not enough | Pre-filter with `-run`; `-failfast`; `--seeds 10` on expensive packages |
| R4 | On Windows, `exec`, path, and port behavior differ | Platform bug | MVP: darwin/linux guaranteed; windows best-effort, smoke in CI |
| R5 | `TestMain` resets global state; harness ineffective | Not findable | `facts.HasTestMain` → report note; the hypothesis is still tried |
| R6 | The user's `GOFLAGS` injects `-count` or `-mod=vendor` | Silent wrong result | `GOFLAGS` printed to the debug log; `-count` always passed explicitly (last wins) |
| R7 | `t.Parallel()` subtest order is unpredictable | Wrong classification | Order hypothesis only under `-parallel=1`; parallel is a separate class |
| R8 | The tool produces flakes against itself (harness leftovers) | Trust loss | `cleanupStale` + signal capture + nightly `-count=10` sweep + `self-hunt` CI job |

---

## 14. What we will NOT do (scope fence)

The Red Team analysis (K1) makes this the MVP's survival condition:

- ❌ **Dashboard / web UI** — none. Output is stdout + JSON + markdown.
- ❌ **Server / SaaS / account** — none. Local files + CI artifacts.
- ❌ **LLM / automatic fix** — none. It shows evidence; it does not patch code.
- ❌ **Go toolchain patch / runtime fork / WASM** — none (ReplayGo's territory).
- ❌ **DB/HTTP replay** — none. Package-wide syntax hints name clock, network,
  database and random calls before probes but never infer a root cause or skip
  search. Only call identities are stored. A synctest virtual clock does not
  isolate external I/O; no bubbles are synthesized around another test.
- Subtest trees run through their enclosing top-level test.
- ❌ **Cross-package order *within one process*** — none. `--polluter-pkg` runs two `go test`
  processes in a chosen sequence and merges the verdicts, so only state that survives the
  process boundary (files, ports, cwd) is expressible there; an in-memory global that the
  first binary dirtied is gone by the time the second one starts. The old blanket ban on
  "cross-package order" was wrong twice over: that engine ships, and two *clauses* of one
  directory (`pkg` + `pkg_test`) are not two binaries at all — they link into one.
- ❌ **Test impact analysis / quarantine / flake trend** — Phase 5.

This fence is the written answer to "one more feature" requests: every item is a competitor's territory or a scope beyond a single developer.

---

## 15. One-page summary

| Question | Answer |
|---|---|
| What? | A CLI that drives `go test` and shrinks order/parallelism/environment flakes into a **permanent regression test** |
| How many packages? | 12 internal packages, all under `internal/`; external dependencies: **zero** |
| Most critical type? | `domain.RunSpec` / `RunResult` / `Witness` — single source of truth |
| Most critical interface? | `gotest.Runner` — the foundation of all testability |
| Most critical file? | `internal/harness` — both the search and the artifact generator |
| How is order controlled? | Temporary `zz_goflakehunter_witness_*_test.go` + `t.Run` chain (`-run` cannot give order) |
| First working slice? | `hunt` + repeat probe + capsule (first end-to-end build) |
| MVP? | four probes + `compile` (shipped in `v0.1.0`) |
| What it doesn't do? | Dashboard, SaaS, LLM, runtime patch, DB replay |

### Regression output ownership

New persistent files use `<source>_<victim>_goflakehunter_test.go`, one regression
per victim, so compiling siblings cannot erase earlier regressions. Recompiling
a victim updates its existing witness. For upgrades, a legacy generated
`<source>_goflakehunter_test.go` declaring that victim's regression is reused;
other victims get separate files. Output owned by the user is still refused.
