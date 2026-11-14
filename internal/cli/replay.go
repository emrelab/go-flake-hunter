package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/emrelab/go-flake-hunter/internal/capsule"
	"github.com/emrelab/go-flake-hunter/internal/domain"
	"github.com/emrelab/go-flake-hunter/internal/facts"
	"github.com/emrelab/go-flake-hunter/internal/gotest"
	"github.com/emrelab/go-flake-hunter/internal/harness"
	"github.com/emrelab/go-flake-hunter/internal/policy"
)

// replayCmd re-runs a capsule's witness N times and reports how often the
// flake reproduced.
//
// The point is trust, not discovery: a capsule is only worth committing if
// someone else can reproduce it tomorrow. Replay is also the honest way to
// notice that a flake has already been fixed — the witness goes green and the
// capsule is stale rather than wrong.
func replayCmd(ctx context.Context, args []string, out IO) error {
	var (
		n           int
		envKV       string
		dir         string
		pkgArg      string
		polluterArg string
	)
	fs := flag.NewFlagSet("replay", flag.ContinueOnError)
	fs.SetOutput(out.Stderr)
	fs.IntVar(&n, "n", 5, "how many times to run")
	fs.StringVar(&envKV, "env", "", "KEY=VALUE for env flakes (the capsule does not store the value)")
	fs.StringVar(&dir, "capsule-dir", "", "capsule directory (default .go-flake-hunter)")
	fs.StringVar(&pkgArg, "pkg", "", "package directory (default: resolve the capsule package in this checkout)")
	fs.StringVar(&polluterArg, "polluter-pkg", "", "polluter package directory (cross-binary capsules; default: resolve the polluter package in this checkout)")
	if err := parseFlags(fs, args); err != nil {
		return err
	}

	id, storeDir, _, err := resolveCapsule(fs.Args(), dir, "")
	if err != nil {
		return err
	}
	c, err := capsule.NewStore(storeDir).Read(id)
	if err != nil {
		return domain.Wrap(domain.ErrUsage, "%s", err)
	}
	if c.Status != domain.StatusConfirmed && c.Status != domain.StatusSuspected {
		return domain.Wrap(domain.ErrUsage,
			"%s is a partial capsule (%s); no verified witness to re-run", c.ID, c.Status)
	}
	if c.Witness.CrossBinary {
		return replayCross(ctx, c, pkgArg, n, polluterArg, out)
	}
	if n <= 0 {
		return domain.Wrap(domain.ErrUsage, "-n must be positive")
	}

	pkgDir, f, err := replayTarget(ctx, pkgArg, c)
	if err != nil {
		return err
	}
	clause, err := harness.ResolveClause(f.Clause, c.Witness.Victim.Name, nil)
	if c.Kind != domain.KindRepeat && c.Kind != domain.KindEnv && !c.Witness.CrossClause {
		clause, err = compileClause(c, f)
	}
	if err != nil {
		return err
	}
	fn := replayFuncName(c, f)

	spec := domain.RunSpec{
		Package:  pkgDir,
		RunRegex: "^" + c.Witness.Victim.Name + "$",
		Count:    1,
		Parallel: 1,
		// If the capsule was verified with -race, replay must also run -race:
		// otherwise half the evidence (the race detector's report) silently
		// vanishes and replay saying "not reproduced" would not be honest.
		Race: c.Witness.Flags.Race,
	}
	switch c.Kind {
	case domain.KindEnv:
		// The value is not in the capsule and never will be: the user provides it.
		// Without it the run says "not reproduced" — correct, the info is missing.
		key, _, ok := strings.Cut(envKV, "=")
		if !ok || !slices.Contains(c.Witness.EnvDiff, key) {
			return domain.Wrap(domain.ErrUsage, "supply --env KEY=VALUE for a recorded key: %s",
				strings.Join(c.Witness.EnvDiff, ", "))
		}
		spec.Env = []string{envKV}
	case domain.KindRepeat:
		spec.RunRegex = "^" + c.Witness.Victim.Name + "$"
		spec.Count = replayRepeatCount(c)
	default:
		spec.RunRegex = "^" + fn + "$"
		spec.Parallel = c.Witness.Flags.Parallel
		if spec.Parallel <= 0 {
			spec.Parallel = 1
		}
	}

	if c.Witness.CrossClause {
		spec.RunRegex = domain.SelectionRegex(c.Witness)
		spec.ShuffleSeed = c.Witness.Seed
	}
	runner := &gotest.Exec{}
	outcome := func() (bool, error) {
		if c.Witness.CrossClause {
			res, err := runner.Run(ctx, spec)
			if err != nil {
				return false, err
			}
			if res.BuildFailed {
				return false, buildFailure(pkgDir, res)
			}
			return res.Failed(c.Witness.Victim.Name), nil
		}
		switch c.Kind {
		case domain.KindEnv, domain.KindRepeat:
			res, err := runner.Run(ctx, spec)
			if err != nil {
				return false, err
			}
			if res.BuildFailed {
				return false, buildFailure(pkgDir, res)
			}
			return res.Failed(c.Witness.Victim.Name), nil
		default:
			// Witness chain: the verdict is in the subtest, not the parent test.
			ctx, unlock, lerr := policy.Locked(ctx, pkgDir)
			if lerr != nil {
				return false, lerr
			}
			defer unlock()

			h, err := installForKind(pkgDir, clause, fn, c)
			if err != nil {
				return false, err
			}
			defer func() { _ = h.Remove() }()
			spec.Source = h.Digest()

			res, err := runner.Run(ctx, spec)
			if err != nil {
				return false, err
			}
			if res.BuildFailed {
				// A package that does not compile did not "stop reproducing":
				// the witness could not be built at all. Reporting the old
				// "stale capsule" line here told the user their flake was gone.
				return false, buildFailure(pkgDir, res)
			}
			return res.Failed(domain.WitnessSubtest(fn, c.Witness.Victim.Name)), nil
		}
	}

	var failed int
	var firstErr error
	for i := 0; i < n; i++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		ok, err := outcome()
		if err != nil {
			firstErr = err
			break
		}
		if ok {
			failed++
		}
	}

	_, _ = fmt.Fprintf(out.Stdout, "\ncapsule: %s (%s)\n", c.ID, c.Kind)
	_, _ = fmt.Fprintf(out.Stdout, "victim : %s\n", c.Witness.Victim.Name)
	_, _ = fmt.Fprintf(out.Stdout, "run    : %d/%d reproduced\n", failed, n)

	// An interrupted replay has no verdict. Without this the run fell through to
	// "0/N reproduced → the flake may already be fixed", i.e. a build error read
	// as a fixed flake — the one wrong answer this command exists to avoid.
	if firstErr != nil {
		if errors.Is(firstErr, domain.ErrBuildFailed) {
			_, _ = fmt.Fprintln(out.Stdout, "result : package does not build — the witness could not be run")
		} else {
			_, _ = fmt.Fprintf(out.Stdout, "result : %v\n", firstErr)
		}
		return firstErr
	}

	switch {
	case failed == n && n > 0:
		_, _ = fmt.Fprintln(out.Stdout, "result : deterministic")
		return nil
	case failed == 0:
		_, _ = fmt.Fprintln(out.Stdout, "result : never reproduced — the flake may already be fixed (stale capsule)")
		return domain.ErrNotReproduced
	default:
		_, _ = fmt.Fprintf(out.Stdout, "result : flaky (%d/%d) — probabilistic\n", failed, n)
		return domain.Wrap(domain.ErrNotReproducible, "reproduced in %d/%d runs", failed, n)
	}
}

// installForKind writes the witness with the template its class needs.
func installForKind(pkgDir, clause, fn string, c domain.Capsule) (*harness.Harness, error) {
	steps := c.Witness.Steps
	if len(steps) == 0 {
		steps = []domain.Step{{Label: domain.VictimLabel(c.Witness.Victim.Name), Test: c.Witness.Victim}}
	}
	if c.Kind == domain.KindParallel {
		return harness.InstallParallel(pkgDir, clause, fn, steps, "replay")
	}
	return harness.Install(pkgDir, clause, fn, steps, "replay")
}

// replayFuncName is the witness function to install, decided in ONE place.
//
// A capsule written by a current build carries the name the hunt actually used
// (Witness.Func) — that is what makes replay faithful: the hunt may have picked
// TestFlakeWitness_2 because the package already defines TestFlakeWitness, and
// re-deriving it from a package that changed since would install a colliding
// file, break the build, and read as "the flake is gone".
//
// An older capsule (no Func) gets the best available answer: the same
// collision-free choice the hunt would make today.
func replayFuncName(c domain.Capsule, f facts.PkgFacts) string {
	if n := c.Witness.Func; n != "" {
		return n
	}
	return harness.FuncName(f.Clause)
}

// buildFailure turns an uncompilable package into the exit-6 verdict.
//
// Distinguishing it from "did not reproduce" matters: a user who deleted a
// helper or is mid-refactor must not read the resulting build error as "the
// flake has been fixed, drop the capsule".
func buildFailure(pkgDir string, res *domain.RunResult) error {
	detail := firstBuildLine(res.BuildOutput)
	if detail == "" {
		detail = pkgDir
	}
	return domain.Wrap(domain.ErrBuildFailed, "%s", detail)
}

// replayRepeatCount is the -count a repeat witness has to run with.
//
// Witness.Flags.Count is what the hunt actually used. Older capsules only carry
// Observation.Runs (the verification rounds), which is 2 — replaying with that
// makes a -count=50 flake look fixed. Prefer the explicit count, fall back to
// the probe default, and never to the verification count.
func replayRepeatCount(c domain.Capsule) int {
	if c.Witness.Flags.Count > 0 {
		return c.Witness.Flags.Count
	}
	return 10
}

func replayTarget(ctx context.Context, pkgArg string, c domain.Capsule) (string, facts.PkgFacts, error) {
	dir, err := resolveTargetDir(ctx, c.Target.Package, pkgArg, ".")
	if err != nil {
		return "", facts.PkgFacts{}, err
	}
	f, err := facts.Scan(dir)
	if err != nil {
		return "", facts.PkgFacts{}, err
	}
	return dir, f, nil
}

// replayCross replays a two-process witness (#6): stage 1 runs the shrunk
// polluters in the polluter package, stage 2 runs the victim alone. Failed
// means the VICTIM failed in stage 2 — the polluter suite failing for its own
// reasons is not evidence about this flake.
//
// Every round re-runs both stages; results never come from the cache because
// this runner is a bare Exec and each spec carries a round-stamped Source.
func replayCross(ctx context.Context, c domain.Capsule, pkgArg string, n int, polluterArg string, out IO) error {
	if n <= 0 {
		return domain.Wrap(domain.ErrUsage, "-n must be positive")
	}
	victimDir, _, err := replayTarget(ctx, pkgArg, c)
	if err != nil {
		return err
	}
	var polluterPackage string
	names := make([]string, 0, len(c.Witness.Steps))
	for _, s := range c.Witness.Steps {
		if strings.HasPrefix(s.Label, domain.StepLabelPolluter) {
			if s.Package == "" || polluterPackage != "" && s.Package != polluterPackage {
				return domain.Wrap(domain.ErrUsage, "cross-binary polluter steps must name one package")
			}
			polluterPackage = s.Package
			names = append(names, regexp.QuoteMeta(s.Func()))
		}
	}

	if len(names) == 0 {
		return domain.Wrap(domain.ErrUsage, "cross-binary capsule carries no polluter steps")
	}

	polluterDir, err := resolveTargetDir(ctx, polluterPackage, polluterArg, ".")
	if err != nil {
		return fmt.Errorf("polluter: %w", err)
	}

	ctx, unlock, err := policy.LockedDirs(ctx, polluterDir, victimDir)
	if err != nil {
		return err
	}
	defer unlock()
	runner := &gotest.Exec{}
	var failed int
	for i := 0; i < n; i++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		res1, err := runner.Run(ctx, domain.RunSpec{
			Package:  polluterDir,
			RunRegex: "^(" + strings.Join(names, "|") + ")$",
			Count:    1, Parallel: 1,
			Source: fmt.Sprintf("replay-cross/%d/1", i),
		})
		if err != nil {
			return err
		}
		if res1.BuildFailed {
			return domain.Wrap(domain.ErrBuildFailed, "polluter package %s does not build", polluterDir)
		}
		if res1.ExitCode != 0 {
			return domain.Wrap(domain.ErrNotReproduced, "polluter stage failed; victim stage was not run")
		}
		res2, err := runner.Run(ctx, domain.RunSpec{
			Package:  victimDir,
			RunRegex: "^" + regexp.QuoteMeta(c.Witness.Victim.Name) + "$",
			Count:    1, Parallel: 1,
			Race:   c.Witness.Flags.Race,
			Source: fmt.Sprintf("replay-cross/%d/2", i),
		})
		if err != nil {
			return err
		}
		if res2.BuildFailed {
			return domain.Wrap(domain.ErrBuildFailed, "victim package %s does not build", victimDir)
		}
		if res2.Failed(c.Witness.Victim.Name) {
			failed++
		}
	}

	_, _ = fmt.Fprintf(out.Stdout, "\ncapsule  : %s (%s, cross-binary)\n", c.ID, c.Kind)
	_, _ = fmt.Fprintf(out.Stdout, "victim   : %s\n", c.Witness.Victim.Name)
	_, _ = fmt.Fprintf(out.Stdout, "polluter : %s (%s)\n", strings.Join(names, ", "), polluterDir)
	_, _ = fmt.Fprintf(out.Stdout, "run      : %d/%d reproduced\n", failed, n)
	if failed == n && n > 0 {
		_, _ = fmt.Fprintln(out.Stdout, "result   : deterministic")
		return nil
	}
	if failed == 0 {
		_, _ = fmt.Fprintln(out.Stdout, "result   : not reproduced — the pollution may already be fixed")
		return domain.ErrNotReproduced
	}
	_, _ = fmt.Fprintf(out.Stdout, "result   : flaky (%d/%d) — probabilistic\n", failed, n)
	return domain.Wrap(domain.ErrNotReproducible, "reproduced in %d/%d runs", failed, n)
}
