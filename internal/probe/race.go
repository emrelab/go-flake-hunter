package probe

import (
	"context"

	"github.com/emrelab/go-flake-hunter/internal/domain"
	"github.com/emrelab/go-flake-hunter/internal/gotest"
	"github.com/emrelab/go-flake-hunter/internal/harness"
	"github.com/emrelab/go-flake-hunter/internal/policy"
)

// ConfirmRace runs an already-found witness once under -race and reports
// whether it reproduced.
//
// Why verification, not search: the -race compiler slows every run and pushes
// the timing witness below the verification threshold. Measured: the
// parallel-race fixture failed 1/3 with `hunt . --race` and was never found,
// while `hunt .` found it. So --race must ADD evidence, never NARROW the search.
//
// That is why classification and shrinking always run without race; exactly one
// extra run happens here. If it reproduces, Flags.Race=true (evidence
// strengthened); if not, it stays false and a one-line note goes into the
// report — claiming "scanned in race mode" is a lie unless the evidence changed.
//
// A non-empty returned note is written into the report; if empty, the witness
// was either confirmed or race is not meaningful for this class.
func ConfirmRace(ctx context.Context, tgt Target, w *domain.Witness, r gotest.Runner) (race bool, note string, err error) {
	if w.CrossBinary {
		return false, "race confirmation is not available for a cross-binary sequence", nil
	}
	if w.CrossClause {
		res, e := runFor(domain.FreshRuns(ctx), r, tgt.Test.Name, domain.RunSpec{Package: tgt.Dir, RunRegex: domain.SelectionRegex(*w), Count: 1, Parallel: 1, ShuffleSeed: w.Seed, Race: true})
		if e != nil {
			return false, "", e
		}
		if res.BuildFailed {
			return false, "", buildFailed(tgt, res)
		}
		if !res.Failed(w.Victim.Name) {
			return false, "witness did not reproduce under -race", nil
		}
		return true, "", nil
	}
	switch w.Kind {
	case domain.KindOrder, domain.KindParallel:
		return confirmChain(ctx, tgt, w, r)
	case domain.KindRepeat:
		return confirmRepeat(ctx, tgt, w, r)
	default:
		// Env: the value is never written to the capsule, so the run cannot be
		// rebuilt. The class itself is not race-related either: adding a note
		// would only be noise for the user.
		return false, "", nil
	}
}

// confirmChain reinstalls the witness and runs it with -race.
//
// The witness file is installed and removed on every run (same contract as the
// probe predicate): a witness left on disk leaks into later builds.
func confirmChain(ctx context.Context, tgt Target, w *domain.Witness, r gotest.Runner) (bool, string, error) {
	if len(w.Steps) == 0 {
		return false, "", nil
	}

	names := make([]string, 0, len(w.Polluters))
	for _, p := range w.Polluters {
		names = append(names, p.Name)
	}
	clause, err := harness.ResolveClause(tgt.Facts.Clause, w.Victim.Name, names)
	if err != nil {
		return false, "", err
	}
	// The probe already picked a collision-free name and the predicate proved
	// the witness under it; reuse it. Recomputing risks a different name when
	// ConfirmRace runs on a capsule found by an older build (Func empty).
	fn := w.Func
	if fn == "" {
		fn = harness.FuncName(tgt.Facts.Clause)
	}

	ctx, unlock, lerr := policy.Locked(ctx, tgt.Dir)
	if lerr != nil {
		return false, "", lerr
	}
	defer unlock()

	install := harness.Install
	if w.Kind == domain.KindParallel {
		install = harness.InstallParallel
	}
	h, err := install(tgt.Dir, clause, fn, w.Steps, "race")
	if err != nil {
		return false, "", err
	}
	defer func() { _ = h.Remove() }()

	width := 1
	if w.Flags.Parallel > 0 {
		width = w.Flags.Parallel
	}
	res, err := runFor(ctx, r, domain.WitnessSubtest(fn, tgt.Test.Name), domain.RunSpec{
		Package:  tgt.Dir,
		RunRegex: "^" + fn + "$",
		Count:    1,
		Parallel: width,
		Race:     true,
		Source:   h.Digest(),
	})
	if err != nil {
		return false, "", err
	}
	if res.BuildFailed {
		// The -race compiler may be missing (CGO disabled, unsupported platform).
		// This is absence of evidence, not a flake finding: that is the honest label.
		return false, "race build failed: no -race evidence", nil
	}
	if res.Failed(domain.WitnessSubtest(fn, tgt.Test.Name)) {
		return true, "", nil
	}
	return false, "not reproduced under a race run", nil
}

// confirmRepeat reruns the same in-process repetition with -race.
//
// The repeat class's witness is not a t.Run chain but the same test running N
// times in the same process; so no harness is installed, the exact same -count
// is simply rerun.
func confirmRepeat(ctx context.Context, tgt Target, w *domain.Witness, r gotest.Runner) (bool, string, error) {
	count := w.Flags.Count
	if count <= 0 {
		count = DefaultRepeatCount
	}
	res, err := runFor(ctx, r, tgt.Test.Name, domain.RunSpec{
		Package:  tgt.Dir,
		RunRegex: "^" + tgt.Test.Name + "$",
		Count:    count,
		Race:     true,
	})
	if err != nil {
		return false, "", err
	}
	if res.BuildFailed {
		return false, "race build failed: no -race evidence", nil
	}
	if res.Failed(tgt.Test.Name) {
		return true, "", nil
	}
	return false, "not reproduced under a race run", nil
}
