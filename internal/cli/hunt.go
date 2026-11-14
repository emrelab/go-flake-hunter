package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/emrelab/go-flake-hunter/internal/capsule"
	"github.com/emrelab/go-flake-hunter/internal/compile"
	"github.com/emrelab/go-flake-hunter/internal/domain"
	"github.com/emrelab/go-flake-hunter/internal/evidence"
	"github.com/emrelab/go-flake-hunter/internal/facts"
	"github.com/emrelab/go-flake-hunter/internal/gotest"
	"github.com/emrelab/go-flake-hunter/internal/policy"
	"github.com/emrelab/go-flake-hunter/internal/probe"
	"github.com/emrelab/go-flake-hunter/internal/report"
)

// huntOptions are the flags shared by `hunt` and `repro`.
type huntOptions struct {
	format string
	// sarifPath (--sarif): a SARIF 2.1.0 document is written to this file in
	// addition to whatever --format prints. The GitHub Action needs both —
	// markdown for the job summary, SARIF for code scanning — from ONE run;
	// a second hunt would double the budget.
	sarifPath   string
	repeat      int
	dir         string
	seeds       int
	budget      int
	timeout     time.Duration
	verbose     bool
	failOnFlake bool
	race        bool
	short       bool
	// polluterPkg is the user-typed suspect for the two-binary engine (#6);
	// polluterDir is it resolved, empty when the flag is absent.
	polluterPkg string
	polluterDir string
}

// addFlags registers the shared flags on fs, so hunt and repro cannot drift
// apart in name, default or help text.
func (o *huntOptions) addFlags(fs *flag.FlagSet) {
	fs.StringVar(&o.format, "format", "text", "output format: text|json|markdown|sarif")
	fs.StringVar(&o.sarifPath, "sarif", "", "also write a SARIF 2.1.0 document to this file (any --format)")
	fs.IntVar(&o.repeat, "repeat", probe.DefaultRepeatCount, "repeat count for -count")
	fs.StringVar(&o.dir, "capsule-dir", "", "capsule directory (default <pkg>/.go-flake-hunter)")
	fs.IntVar(&o.seeds, "seeds", 0, "shuffle seed count (0 = default 12)")
	fs.DurationVar(&o.timeout, "timeout", policy.DefaultMaxWall, "total time budget (0 = default 5m)")
	fs.IntVar(&o.budget, "budget", policy.DefaultMaxRuns, "total go test run budget (0 = default 1000)")
	fs.BoolVar(&o.verbose, "v", false, "report every probe in detail")
	fs.BoolVar(&o.failOnFlake, "fail-on-flake", false, "exit 1 if a flake is found (CI gate)")
	// --race is NOT a search parameter: classification and shrinking always
	// run race-free; -race is applied to the found witness only, as a single
	// extra run (probe.ConfirmRace). The reverse would push the timing
	// witness below the verification threshold under race overhead — the flag
	// would hide the flake instead of adding evidence.
	fs.BoolVar(&o.race, "race", false, "verify the found witness once with -race (data race evidence)")
	// short is the operator's choice: every `go test` runs with -short.
	// Hunting a repo whose integration tests honor testing.Short() would
	// otherwise burn the wall budget re-running them once per shuffle seed
	// (self-hunt @ d92e895 learned this the expensive way).
	fs.BoolVar(&o.short, "short", false, "scan with -short (skip the target's short-skipping tests)")
	fs.StringVar(&o.polluterPkg, "polluter-pkg", "",
		"suspect polluter package (dir or import path): enables cross-binary order reproduction (#6)")
}

// resolvePolluter turns --polluter-pkg into a single directory, once per run.
// The module boundary is what makes import paths resolvable: like the CI log
// of repro --from-json, this runs where `go list` can see the repo.
func (o *huntOptions) resolvePolluter(ctx context.Context) error {
	o.polluterDir = ""
	if o.polluterPkg == "" {
		return nil
	}
	dirs, err := gotest.ExpandPatterns(ctx, []string{o.polluterPkg})
	if err != nil {
		return err
	}
	if len(dirs) != 1 {
		return domain.Wrap(domain.ErrUsage,
			"--polluter-pkg must resolve to exactly one package directory, got %d", len(dirs))
	}
	o.polluterDir = dirs[0]
	return nil
}

// scanner runs the probe registry over packages and writes the artifacts.
//
// one method, one place: hunt, repro and any future command must classify,
// budget and report identically or the product has three dialects of the same
// answer.
type scanner struct {
	o        huntOptions
	out      IO
	registry *probe.Registry
	runner   gotest.Runner
	budget   *policy.Budget
	// only limits the scan to one test name ("" = every test).
	only  string
	found int
	// fresh is the number of flakes NOT in the baseline. The gate looks at this:
	// a CI gate that goes red because of a known flake makes the team silently
	// delete the baseline — the gate would break the very thing it protects
	// against.
	fresh int
	// when baseline is set (--baseline), ids are compared.
	baseline domain.Baseline
	// ids are the confirmed defect fingerprints found by this run (--write-baseline).
	ids []string
	// collected holds the capsules a json run emits at the end. A streaming
	// pretty-printer would produce one object per capsule — not JSON.
	collected []domain.Capsule
	results   []domain.PackageResult
	broken    bool
	cwd       string
	// gitState memoizes the repo provenance per package directory: every
	// capsule a package yields shares it, and asking git again per capsule
	// doubled the subprocess count for no new information.
	gitMu    sync.Mutex
	gitState map[string]repoState
}

// newScanner wires a budgeted, cached runner and the probe registry.
func newScanner(out IO, o huntOptions, only string) *scanner {
	// Budget: every run counts. The cache is OUTSIDE the budget: a cached
	// result never ran, so it does not spend budget.
	budget := policy.New(o.budget, o.timeout)
	// Cache: the shuffle scan is paid once per package, not per test.
	// 23s -> a few seconds on a 3-test fixture; 10x difference at 30 tests.
	runner := gotest.NewCache(gotest.Budgeted{Inner: &gotest.Exec{Short: o.short}, Budget: budget}, 0)
	// The probe set and its cost order live in probe.Default; the CLI only
	// supplies the flag values. That is what keeps the registry from drifting.
	registry := probe.Default(probe.Config{
		Repeat:      o.repeat,
		Seeds:       probe.SeedsN(o.seeds),
		PolluterDir: o.polluterDir,
	})
	cwd, _ := os.Getwd()
	return &scanner{o: o, out: out, registry: registry, runner: runner, budget: budget, only: only, cwd: cwd}
}

// emit renders or collects one capsule.
func (s *scanner) emit(c domain.Capsule) error {
	// The machine-readable formats are one document per run: they collect and
	// write at flush, including on interrupted paths. A --sarif side file
	// collects too, whichever format streams.
	if s.o.format == "json" || s.o.format == "sarif" || s.o.sarifPath != "" {
		s.collected = append(s.collected, c)
	}
	if s.o.format == "json" || s.o.format == "sarif" {
		return nil
	}
	return writeCapsule(s.out, s.o.format, c)
}

// flush writes the single document a machine-readable run owes its consumer.
// Every path that produced capsules calls it, including the error paths: a
// partial run still has findings.
func (s *scanner) flush() error {
	switch s.o.format {
	case "json":
		if err := report.JSONRun(s.out.Stdout, s.collected, s.budget.Runs(), s.budget.MaxRuns, s.results); err != nil {
			return err
		}
	case "sarif":
		if err := report.SARIF(s.out.Stdout, s.collected, Version); err != nil {
			return err
		}
	}
	if s.o.sarifPath != "" && s.o.format != "sarif" {
		f, err := os.Create(s.o.sarifPath)
		if err != nil {
			return fmt.Errorf("sarif file: %w", err)
		}
		// A close failure after a successful write is not actionable: the
		// SARIF document already reached the file. Say so instead of hiding it.
		defer func() { _ = f.Close() }()
		return report.SARIF(f, s.collected, Version)
	}
	return nil
}

// relDir is the package path as a command printed in the capsule can use it:
// relative to where the user invoked go-flake-hunter. Target.RelDir used to be an
// absolute path, so a capsule committed to a PR carried /Users/<name>/... and
// the reproduce command only worked on the machine that produced it.
func (s *scanner) relDir(dir string) string {
	if s.cwd == "" {
		return dir
	}
	rel, err := filepath.Rel(s.cwd, dir)
	if err != nil || rel == "" {
		return dir
	}
	// A directory outside ("../other/repo") is no more portable than an
	// absolute path; relative to cwd always produces the right command.
	return rel
}

// polluterRel is the --polluter-pkg directory in the same run-relative form
// relDir produces; empty when the cross-binary engine (#6) was not enabled.
func (s *scanner) polluterRel() string {
	if s.o.polluterDir == "" {
		return ""
	}
	return s.relDir(s.o.polluterDir)
}

// pkg scans one package directory.
func (s *scanner) pkg(ctx context.Context, pkgArg string) error {
	dir, err := filepath.Abs(pkgArg)
	if err != nil {
		return domain.Wrap(domain.ErrUsage, "%s", pkgArg)
	}
	if st, err := os.Stat(dir); err != nil || !st.IsDir() {
		return domain.Wrap(domain.ErrUsage, "%s is not a directory", pkgArg)
	}
	relDir := s.relDir(dir)

	// Crash leftover: if the previous run was killed, a witness file remains
	// and breaks every build in this package.
	//
	// The lock is REQUIRED: the install contract says "the directory is
	// locked while a witness is on disk". An unlocked cleanup would delete the
	// LIVE witness of another go-flake-hunter running on the same package; that run
	// would later report "0/5 failed". Discover runs under the same lock: a
	// witness installed in between would enter the list as a user-owned test.
	lockCtx, unlock, lerr := policy.Locked(ctx, dir)
	if lerr != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return domain.Wrap(domain.ErrInternal, "could not acquire lock: %v", lerr)
	}
	if removed, err := cleanupWitnesses(dir); err == nil && len(removed) > 0 && s.o.verbose {
		_, _ = fmt.Fprintf(s.out.Stderr, "cleaned: %s\n", strings.Join(removed, ", "))
	}

	tests, err := s.runner.Discover(lockCtx, dir)
	unlock()
	if err != nil {
		return err
	}
	if s.only != "" {
		tests = filterTests(tests, s.only)
		if len(tests) == 0 {
			return domain.Wrap(domain.ErrUsage, "%s has no test %q", pkgArg, s.only)
		}
	}
	if len(tests) == 0 {
		_, _ = fmt.Fprintf(s.out.Stderr, "%s: no tests found\n", pkgArg)
		return nil
	}

	pkgFacts, err := facts.Scan(dir)
	if err != nil {
		return domain.Wrap(domain.ErrUsage, "could not read %s", pkgArg)
	}

	if len(pkgFacts.ExternalDependencies) > 0 {
		_, _ = fmt.Fprintf(s.out.Stderr, "%s: external dependencies (static, package-wide): %s; clock/network/database control is outside go-flake-hunter; reproduction probes still run\n", pkgArg, strings.Join(pkgFacts.ExternalDependencies, "; "))
	}
	for _, t := range tests {
		res, err := s.registry.Run(ctx, probe.Target{Test: t, Dir: dir, Facts: pkgFacts}, s.runner)
		switch {
		case errors.Is(err, domain.ErrBrokenTest):
			s.broken = true
			s.results = append(s.results, domain.PackageResult{Package: t.Package, Test: t.Name, Status: domain.RunBroken})
			writer := s.out.Stdout
			if s.o.format == "json" || s.o.format == "sarif" {
				writer = s.out.Stderr
			}
			_, _ = fmt.Fprintf(writer, "%s:%s: broken (fails in isolation)\n", t.Package, t.Name)
			continue
		case errors.Is(err, domain.ErrPackageFailed):
			s.results = append(s.results, domain.PackageResult{Package: t.Package, Test: t.Name, Status: domain.RunPackageFailed})
			return err
		case errors.Is(err, domain.ErrIncomplete):
			s.results = append(s.results, domain.PackageResult{Package: t.Package, Test: t.Name, Status: domain.RunIncomplete})
			return err
		case errors.Is(err, domain.ErrNotReproducible) && res != nil && res.Suspected != nil:
			res.Witness = res.Suspected
		case errors.Is(err, probe.ErrNotReproduced):
			// Always write: "no flake" alone hides the difference between
			// "nothing is wrong" and "this probe cannot see this class".
			// The user must see what was tried.
			writeNoFlake(s.out, s.o, t, res)
			continue
		case stopped(ctx, err):
			// Budget/cancel: not finding the thing is not an answer, it is an
			// interruption. A partial capsule is written and the exit code says so.
			return s.writePartial(ctx, t, dir, relDir, res, err)
		case err != nil:
			return err
		}
		if res == nil || res.Witness == nil {
			continue
		}

		// --race is the single extra run: the search above was race-free; the
		// witness found here is tested once with -race. The evidence label then
		// tells the truth — "scanned in race mode" would not be enough.
		note := ""
		if s.o.race && res.Witness.Verification == nil {
			race, raceNote, rerr := probe.ConfirmRace(ctx,
				probe.Target{Test: t, Dir: dir, Facts: pkgFacts}, res.Witness, s.runner)
			if rerr != nil {
				if stopped(ctx, rerr) {
					return s.writePartial(ctx, t, dir, relDir, res, rerr)
				}
				return rerr
			}
			res.Witness.Flags.Race = race
			note = raceNote
		}

		c := buildCapsule(res.Witness, dir, s.repoStateFor(ctx, dir), pkgFacts, relDir, s.polluterRel())
		c.Note = note
		if c.Status == domain.StatusSuspected {
			c.Note = "partial reproduction; not confirmed and excluded from the CI flake gate and baseline"
			c.Repro.Command = fmt.Sprintf("go-flake-hunter replay %s --pkg %s --capsule-dir %s -n 10", c.ID, quoteArg(dir), quoteArg(capsuleDir(s.o.dir, dir)))
		}
		store := capsule.NewStore(capsuleDir(s.o.dir, dir))
		path, err := store.Write(c)
		if err != nil {
			return err
		}
		if c.Status == domain.StatusConfirmed {
			s.found++
			fingerprint := capsule.Fingerprint(c.Witness, c.Repo.Module)
			s.ids = append(s.ids, fingerprint)
			// A known flake is reported but not counted; silently swallowing it
			// would let the user say "clean". stderr: in text mode stdout is the
			// witness report, in json mode the one-document rule applies.
			if len(s.baseline.IDs) > 0 {
				if s.baseline.Contains(fingerprint) {
					_, _ = fmt.Fprintf(s.out.Stderr, "known flake: %s (%s:%s)\n",
						c.ID, c.Target.Package, c.Target.Victim)
				} else {
					s.fresh++
				}
			} else {
				s.fresh++
			}
		}
		if err := s.emit(c); err != nil {
			return err
		}
		if s.o.verbose {
			_, _ = fmt.Fprintf(s.out.Stdout, "\n  written: %s\n", path)
		}
	}
	return nil
}

// outcome is the exit decision shared by hunt and repro.
//
// A clean hunt is exit 0: "I looked and found nothing" is a successful scan,
// and README has always documented 0 as clean. Only `repro`, whose entire job
// is one named suspect, treats "not reproduced" as a failed request (3) — there
// the caller asked a question and did not get an answer.
func (s *scanner) outcome(notFoundIsError bool) error {
	switch {
	case (s.fresh > 0 || s.broken) && s.o.failOnFlake:
		return domain.ErrFlakeFound
	case s.found > 0:
		return nil
	case notFoundIsError:
		return domain.ErrNotReproduced
	default:
		return nil
	}
}

// writeCapsule renders one capsule in the requested format.
func writeCapsule(out IO, format string, c domain.Capsule) error {
	switch format {
	case "json":
		return report.JSON(out.Stdout, c)
	case "markdown", "md":
		return report.Markdown(out.Stdout, c)
	case "text", "":
		return report.Text(out.Stdout, c)
	default:
		return domain.Wrap(domain.ErrUsage, "unknown format %q (text|json|markdown|sarif — sarif is a run format, see hunt)", format)
	}
}

// filterTests keeps the named test, accepting either "TestX" or a
// "package:TestX" identity as printed by the report.
func filterTests(tests []domain.TestID, name string) []domain.TestID {
	if i := strings.LastIndex(name, ":"); i >= 0 {
		name = name[i+1:]
	}
	var out []domain.TestID
	for _, t := range tests {
		if t.Name == name {
			out = append(out, t)
		}
	}
	return out
}

// hunt scans one or more package directories and reports the flakes it finds.
func hunt(ctx context.Context, args []string, out IO) error {
	var o huntOptions
	jobs := min(runtime.NumCPU(), 4)
	fs := flag.NewFlagSet("hunt", flag.ContinueOnError)
	fs.SetOutput(out.Stderr)
	o.addFlags(fs)
	fs.IntVar(&jobs, "jobs", jobs, "maximum packages scanned concurrently (shared total budget)")
	// --changed is hunt-only: repro already targets one test, where a
	// "changed packages" set has no meaning. Putting it in HuntOptions would
	// ship a flag to both commands that repro never uses.
	var (
		changed bool
		base    string
		// --baseline: the known-flake list. Empty = no baseline (today's
		// behavior is preserved exactly).
		baselinePath string
		// --write-baseline: write what this run found to the baseline.
		writeBaseline bool
	)
	fs.BoolVar(&changed, "changed", false, "scan only packages changed in git diff")
	fs.StringVar(&base, "base", "", "change base (default origin/main, else HEAD~1)")
	fs.StringVar(&baselinePath, "baseline", "", "known flake list (gate goes red only on NEW flakes)")
	fs.BoolVar(&writeBaseline, "write-baseline", false,
		"write the ids found by this run to the baseline file (default .go-flake-hunter/baseline.json)")
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	if jobs < 1 {
		return domain.Wrap(domain.ErrUsage, "--jobs must be positive")
	}
	if o.polluterPkg != "" {
		jobs = 1
	}
	if err := o.resolvePolluter(ctx); err != nil {
		return err
	}
	pkgs := fs.Args()
	if len(pkgs) == 0 {
		pkgs = []string{"."}
	}

	var dirs []string
	taban := ""
	if changed {
		cwd, _ := os.Getwd()
		all, used, err := changedPackages(ctx, cwd, base)
		if err != nil {
			return err
		}
		taban = used
		dirs, err = restrictToArgs(ctx, all, fs.Args())
		if err != nil {
			return err
		}
	} else {
		var err error
		dirs, err = gotest.ExpandPatterns(ctx, pkgs)
		if err != nil {
			return err
		}
	}

	s := newScanner(out, o, "")
	// The baseline loads BEFORE the scan: if the file is corrupt, say it now,
	// not after a 10-second scan.
	if baselinePath != "" && !writeBaseline {
		bl, err := capsule.LoadBaseline(baselinePath)
		if err != nil {
			return err
		}
		s.baseline = bl
	}
	if changed {
		// The count shows with --v: "--changed scanned nothing" and "no package
		// changed" must not vanish into the same silence.
		if o.verbose {
			_, _ = fmt.Fprintf(out.Stderr, "--changed: base %s, packages to scan: %d\n", taban, len(dirs))
			for _, d := range dirs {
				_, _ = fmt.Fprintf(out.Stderr, "  %s\n", s.relDir(d))
			}
		}
		if len(dirs) == 0 {
			_, _ = fmt.Fprintf(out.Stderr, "--changed: no test package to scan in %s...HEAD\n", taban)
		}
	}

	runErr := s.packages(ctx, dirs, jobs)
	// JSON is one document: keep the findings of an interrupted run inside.
	if err := s.flush(); err != nil {
		return err
	}
	if runErr != nil {
		return runErr
	}
	if writeBaseline {
		return writeBaselineFile(out, s, baselinePath)
	}
	// One summary line: the answer to "why is the gate green" must appear in
	// the report, or the user thinks the flake was not found.
	if len(s.baseline.IDs) > 0 {
		_, _ = fmt.Fprintf(out.Stderr, "baseline: %d known, %d new\n",
			s.found-s.fresh, s.fresh)
	}
	return s.outcome(false)
}

// writeBaselineFile records this run's ids, keeping the ones already known.
//
// Union: `--write-baseline` naturally means "add these findings to the list".
// Ignoring the existing file would delete the first package's baseline while
// writing the second one's.
//
// The gate is CLOSED for this run: a run that writes the baseline judging
// itself by the very list it wrote is circular.
func writeBaselineFile(out IO, s *scanner, path string) error {
	if path == "" {
		path = capsule.DefaultBaselinePath(s.cwd)
	}
	ids := append([]string(nil), s.ids...)
	if existing, err := capsule.LoadBaseline(path); err == nil {
		ids = append(ids, existing.IDs...)
	} else if errors.Is(err, capsule.ErrLegacyBaseline) {
		_, _ = fmt.Fprintln(out.Stderr, "replacing baseline v1 with fingerprints from this scan; rescan every previously baselined package")
	} else if !baselineMissing(path) {
		return err
	}
	if err := capsule.WriteBaseline(path, ids); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(out.Stderr, "baseline written: %s (%d ids)\n", path, len(s.ids))
	return nil
}

// stopped reports a hunt that ended because it ran out of budget or the user
// interrupted it. Both are outcomes to report, not errors to swallow: the exit
// code must differ from "no flake" or CI reads a cut-short hunt as a clean one.
func stopped(ctx context.Context, err error) bool {
	if err == nil {
		return false
	}
	return errors.Is(err, domain.ErrBudgetExceeded) ||
		errors.Is(err, context.Canceled) ||
		errors.Is(err, context.DeadlineExceeded) ||
		ctx.Err() != nil
}

// writePartial records what a stopped hunt managed to rule out.
//
// The capsule is written before the error is returned, so the user keeps
// something to inspect; a bare exit code 4 would throw away the work already
// done. Note carries the reason because Status alone cannot distinguish "budget
// exhausted" from "Ctrl-C".
func (s *scanner) writePartial(ctx context.Context, t domain.TestID, dir, relDir string,
	res *probe.Result, cause error) error {

	note := partialNote(cause, s.o)
	c := buildPartialCapsule(t, s.repoStateFor(ctx, dir), relDir, note)

	infos := attemptInfos(res)
	path, err := capsule.NewStore(capsuleDir(s.o.dir, dir)).Write(c)
	if err != nil {
		return err
	}
	// Every machine-readable surface must collect the partial capsule too:
	// --format json, --format sarif and the --sarif side file all promise ONE
	// document, and a partial result is a finding. Printing the human summary
	// to stdout for these formats produced two documents — the leading prose
	// made the JSON/SARIF unparseable.
	if s.o.format == "json" || s.o.format == "sarif" || s.o.sarifPath != "" {
		if err := s.emit(c); err != nil {
			return err
		}
	}
	if s.o.format == "json" || s.o.format == "sarif" {
		// The single document is written by flush; the note belongs on stderr
		// so the artifact stays clean.
		_, _ = fmt.Fprintf(s.out.Stderr, "%s: inconclusive (%s)\n", c.Target.Victim, c.Note)
		return cause
	}
	if s.o.format == "markdown" {
		if err := report.Markdown(s.out.Stdout, c); err != nil {
			return err
		}
		return cause
	}
	if err := report.Partial(s.out.Stdout, c, infos, path, partialHint(cause)); err != nil {
		return err
	}
	// Keep the error: the exit code (4 / 130) comes from here.
	return cause
}

// partialNote turns the cause into the one sentence a user reads first.
func partialNote(cause error, o huntOptions) string {
	switch {
	case errors.Is(cause, domain.ErrBudgetExceeded):
		return strings.TrimSuffix(cause.Error(), ": "+domain.ErrBudgetExceeded.Error())
	case errors.Is(cause, context.Canceled), errors.Is(cause, context.DeadlineExceeded):
		return "cancelled (SIGINT); attempts saved"
	default:
		return cause.Error()
	}
}

// partialHint is the one action that helps for this particular ending. For an
// interrupt there is none: the user already knows how to start again.
func partialHint(cause error) string {
	if errors.Is(cause, domain.ErrBudgetExceeded) {
		return "raise the budget: --budget N --timeout 10m"
	}
	return ""
}

// buildPartialCapsule is the artifact of a hunt that produced no witness.
func buildPartialCapsule(t domain.TestID, st repoState, relDir, note string) domain.Capsule {
	base := baseCapsule(st)
	target := domain.Target{Package: t.Package, RelDir: relDir, Victim: t.Name}
	c := base
	c.Target = target
	c.Status = domain.StatusPartial
	c.Note = note
	c.ID = capsule.PartialID(target, domain.StatusPartial, note, base.Repo.Module, base.Repo.Commit)
	return c
}

// writeNoFlake explains an empty result instead of printing nothing.
//
// Only in text mode does it go to stdout. A machine-readable run (json,
// markdown) must emit exactly one document; prose in the middle of it is how a
// JSON consumer starts failing on "flake not found." — a bug that looks like
// the tool's, not the caller's.
func writeNoFlake(out IO, o huntOptions, t domain.TestID, res *probe.Result) {
	if res == nil {
		return
	}
	infos := attemptInfos(res)
	if o.format != "text" && o.format != "" {
		if o.verbose {
			_, _ = fmt.Fprintf(out.Stderr, "%s:%s: no flake\n", t.Package, t.Name)
		}
		return
	}
	_, _ = fmt.Fprintf(out.Stdout, "%s:%s", t.Package, t.Name)
	_ = report.NoFlake(out.Stdout, infos)
}

func attemptInfos(res *probe.Result) []report.AttemptInfo {
	if res == nil {
		return nil
	}
	infos := make([]report.AttemptInfo, 0, len(res.Tried))
	for _, a := range res.Tried {
		infos = append(infos, report.AttemptInfo{
			Probe:         a.Probe,
			Err:           a.Err,
			NotReproduced: errors.Is(a.Err, domain.ErrNotReproduced),
			BuildFailed:   errors.Is(a.Err, domain.ErrBuildFailed),
			Skipped:       a.Skipped,
			Reason:        a.Reason,
		})
	}
	return infos
}

// buildCapsule assembles the artifact from the verified witness plus whatever
// static evidence the AST scan can supply.
func buildCapsule(w *domain.Witness, dir string, st repoState, f facts.PkgFacts, relDir, polluterRelDir string) domain.Capsule {
	c := baseCapsule(st)
	c.Target = domain.Target{Package: w.Victim.Package, RelDir: relDir, Victim: w.Victim.Name}
	if pos, ok := f.TestPos[w.Victim.Name]; ok && pos.Line > 0 {
		c.Target.VictimFile = filepath.Base(pos.File)
		c.Target.VictimLine = pos.Line
	}
	if w.CrossBinary {
		c.Target.PolluterRelDir = polluterRelDir
	}
	c.Kind = w.Kind
	c.Witness = domain.Witness{
		Kind:         w.Kind,
		Victim:       w.Victim,
		Polluters:    w.Polluters,
		Flags:        w.Flags,
		EnvDiff:      w.EnvDiff,
		Runs:         w.Runs,
		Func:         w.Func,
		CrossBinary:  w.CrossBinary,
		CrossClause:  w.CrossClause,
		Seed:         w.Seed,
		Verification: w.Verification,
	}
	c.Observation = domain.Observe(w.Runs, w.Runs)
	c.Evidence = evidence.Build(w, f)
	c.Status = domain.StatusConfirmed
	if w.Verification != nil {
		c.Status = domain.StatusSuspected
		c.Observation = *w.Verification
	}
	c.Witness.Steps = stepsFor(w)
	c.ID = capsule.ID(c.Witness, st.Module, st.Commit)
	c.Repro.Command = reproCommand(c)
	c.Repro.GeneratedTest = compile.OutputName(dir, targetSourceFile(dir, w.Victim.Name), w.Victim.Name)
	if c.Status != domain.StatusConfirmed || w.CrossClause || w.CrossBinary {
		c.Repro.GeneratedTest = ""
	}
	return c
}

// baseCapsule fills the fields every capsule shares, whatever its status.
//
// st is the provenance read once per package (repoStateFor): module, commit and
// the dirty flag are facts about the run, not about the capsule, and re-reading
// them per capsule spent four git subprocesses on the same answer.
func baseCapsule(st repoState) domain.Capsule {
	return domain.Capsule{
		Schema:    domain.SchemaVersion,
		CreatedAt: time.Now(),
		Repo:      domain.Repo{Module: st.Module, Commit: st.Commit, Dirty: st.Dirty},
		Tool: domain.Tool{
			Version: Version,
			Go:      runtime.Version(),
			OS:      runtime.GOOS,
			Arch:    runtime.GOARCH,
		},
	}
}

// stepsFor renders the witness as the t.Run chain the harness will emit.
//
// The probe's own steps win when present: they carry per-step facts the
// polluter list cannot reconstruct (WrapParallel). Rebuilding them from names
// silently dropped that bit, so a parallel capsule replayed from disk rendered
// a serial witness.
func stepsFor(w *domain.Witness) []domain.Step {
	if len(w.Steps) > 0 {
		return append([]domain.Step(nil), w.Steps...)
	}
	steps := make([]domain.Step, 0, len(w.Polluters)+1)
	for _, p := range w.Polluters {
		steps = append(steps, domain.Step{Label: domain.PolluterLabel(p.Name), Test: p})
	}
	steps = append(steps, domain.Step{Label: domain.VictimLabel(w.Victim.Name), Test: w.Victim})
	return steps
}

// reproCommand is the exact shell line that reproduces the flake.
//
// Every branch is derived from the witness, never hardcoded: a parallel
// witness replayed with -parallel=1 executes its subtests one after another
// and proves nothing, which is the worst possible artifact — a command that
// looks right and reports "passed".
func reproCommand(c domain.Capsule) string {
	dir := c.Target.RelDir
	if dir == "" {
		dir = "."
	}

	if c.Witness.CrossClause {
		race := ""
		if c.Witness.Flags.Race {
			race = " -race"
		}
		return fmt.Sprintf("go test%s %s -run %s -count=1 -parallel=1 -shuffle=%s", race, quoteArg(dir), quoteArg(domain.SelectionRegex(c.Witness)), shuffleArgument(c.Witness.Seed))
	}

	// A cross-binary witness (#6) is not a test file, it is a sequence of two
	// `go test` processes. The command IS the reproduction contract: compile
	// refuses these capsules, so this line is the permanent record.
	if c.Witness.CrossBinary {
		polluterDir := c.Target.PolluterRelDir
		if polluterDir == "" {
			polluterDir = dir
		}
		names := make([]string, 0, len(c.Witness.Polluters))
		for _, p := range c.Witness.Polluters {
			names = append(names, p.Name)
		}
		return fmt.Sprintf("go test %s -run '^(%s)$' -count=1 -parallel=1 && go test %s -run '^%s$' -count=1",
			polluterDir, strings.Join(names, "|"), dir, c.Witness.Victim.Name)
	}

	var b strings.Builder
	// For env flakes the value is not written to the capsule; the user puts
	// their own value in. Leaving it empty would break the command.
	for _, key := range c.Witness.EnvDiff {
		fmt.Fprintf(&b, "%s=%s ", key, policy.RedactionPlaceholder)
	}

	switch c.Kind {
	case domain.KindRepeat:
		count := c.Witness.Flags.Count
		if count <= 0 {
			count = probe.DefaultRepeatCount
		}
		fmt.Fprintf(&b, "go test %s -run '^%s$' -count=%d",
			dir, c.Witness.Victim.Name, count)
	case domain.KindEnv:
		// The victim itself, not a shrunk witness: the difference is only the environment.
		fmt.Fprintf(&b, "go test %s -run '^%s$' -count=1",
			dir, c.Witness.Victim.Name)
	default:
		parallel := c.Witness.Flags.Parallel
		if parallel <= 0 {
			parallel = 1
		}
		// -race is part of the witness: if it drops from the command, the user
		// reproduces only the ordering, not the race.
		race := ""
		if c.Witness.Flags.Race {
			race = "-race "
		}
		// The witness function name comes from the capsule, never from a
		// constant: a package that defines its own TestFlakeWitness made the
		// probe pick TestFlakeWitness_2, and printing the bare name handed the
		// user a command that runs THEIR test and reports a pass.
		fmt.Fprintf(&b, "go test %s%s -run '^%s$' -count=1 -parallel=%d",
			race, dir, c.Witness.FuncName(), parallel)
	}
	return b.String()
}

// reorderFlags moves flags in front of positional arguments.
//
// Go's flag package stops at the first non-flag argument, so `hunt . -v` would
// silently treat -v as a package. Users type flags after the target constantly;
// a usage error there reads as a bug in the tool, not in their command.
//
// Whether a flag swallows the next token is asked of the FlagSet itself
// (IsBoolFlag), never a hardcoded list: the old list silently misparsed every
// new boolean flag (`--short --budget 200` handed "200" to -short and left
// --budget facing --timeout — found when self-hunt's CI step added --short).
func reorderFlags(fs *flag.FlagSet, args []string) []string {
	var flags, positional []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if strings.HasPrefix(a, "-") && a != "-" {
			flags = append(flags, a)
			// A flag written as "-name value" takes the next token with it.
			if !strings.Contains(a, "=") && i+1 < len(args) && takesValue(fs, a) {
				i++
				flags = append(flags, args[i])
			}
			continue
		}
		positional = append(positional, a)
	}
	return append(flags, positional...)
}

// takesValue reports whether the flag needs a following argument. Boolean
// flags and unknown names (flag.Parse will produce the usage error) do not
// consume the next token.
func takesValue(fs *flag.FlagSet, name string) bool {
	trimmed := strings.TrimLeft(name, "-")
	if i := strings.Index(trimmed, "="); i >= 0 {
		return false
	}
	f := fs.Lookup(trimmed)
	if f == nil {
		return false // unknown flag: reorder it verbatim; Parse reports it
	}
	bf, ok := f.Value.(interface{ IsBoolFlag() bool })
	return !ok || !bf.IsBoolFlag()
}

// targetSourceFile picks the file the regression test is named after: the file
// that actually declares the victim, so `compile` writing TestVictim's test
// lands in checkout_goflakehunter_test.go rather than next to an unrelated file.
//
// go-flake-hunter's own output is never a candidate. Naming a generated file again
// compounds the suffix (checkout_go-flake-hunter_goflakehunter_test.go) and, worse,
// leaves two files declaring the same function — the package stops compiling.
func targetSourceFile(dir, victim string) string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "goflakehunter"
	}
	fallback := ""
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, "_test.go") || strings.HasSuffix(name, domain.PersistentFileSuffix) {
			continue
		}
		if victim != "" {
			if src, err := os.ReadFile(filepath.Join(dir, name)); err == nil &&
				strings.Contains(string(src), "func "+victim+"(") {
				return name
			}
		}
		if fallback == "" {
			fallback = name
		}
	}
	if fallback != "" {
		return fallback
	}
	return "goflakehunter"
}

func capsuleDir(flagValue, pkgDir string) string {
	if flagValue != "" {
		if filepath.IsAbs(flagValue) {
			return flagValue
		}
		return filepath.Join(pkgDir, flagValue)
	}
	return filepath.Join(pkgDir, domain.CapsuleDir)
}

func modulePath(dir string) string {
	cur := dir
	for {
		if _, err := os.Stat(filepath.Join(cur, "go.mod")); err == nil {
			if data, err := os.ReadFile(filepath.Join(cur, "go.mod")); err == nil {
				for _, line := range strings.Split(string(data), "\n") {
					line = strings.TrimSpace(line)
					if strings.HasPrefix(line, "module ") {
						return strings.TrimSpace(strings.TrimPrefix(line, "module "))
					}
				}
			}
			return ""
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return ""
		}
		cur = parent
	}
}

func baselineMissing(path string) bool {
	_, err := os.Stat(path)
	return os.IsNotExist(err)
}

func shuffleArgument(seed int64) string {
	if seed == 0 {
		return "off"
	}
	return fmt.Sprint(seed)
}

func quoteArg(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }
