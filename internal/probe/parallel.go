package probe

import (
	"context"
	"errors"
	"sort"
	"strings"

	"github.com/emrelab/go-flake-hunter/internal/domain"
	"github.com/emrelab/go-flake-hunter/internal/gotest"
	"github.com/emrelab/go-flake-hunter/internal/harness"
	"github.com/emrelab/go-flake-hunter/internal/policy"
	"github.com/emrelab/go-flake-hunter/internal/search"
)

// Parallel detects concurrency-dependent flakes: the victim passes alone and
// passes when the package runs single-threaded, but fails when another test is
// running at the same time.
//
// Why the sequential witness cannot express this: a witness is a chain of
// t.Run calls, and a chain is by definition serial. The only way to ask the go
// tool to overlap them is t.Parallel() inside each subtest plus `-parallel N`
// with N >= the number of subtests. Both facts are encoded in the witness
// (Flags.Parallel), so a replay reproduces the overlap rather than the order.
type Parallel struct {
	// Verify is how many times a minimized witness must reproduce. 0 -> 3.
	//
	// No race: the search runs without race; -race is applied later as
	// extra evidence on the witness found (ConfirmRace).
	Verify int
}

// Name implements Probe.
func (Parallel) Name() string { return "parallel" }

// Applicable implements Gate: with no t.Parallel() in the package, two tests
// cannot overlap, so this class is impossible here and the probe spends nothing.
// In a real repository this is the common case, and it is the difference
// between a scan that costs one run per test and one that costs three.
func (Parallel) Applicable(tgt Target) bool { return packageUsesParallel(tgt) }

// InapplicableReason implements GateReason.
func (Parallel) InapplicableReason(Target) string {
	return "no t.Parallel() in the package"
}

// Kind implements Probe.
func (Parallel) Kind() domain.Kind { return domain.KindParallel }

// Probe implements Probe.
func (p Parallel) Probe(ctx context.Context, tgt Target, r gotest.Runner) (*domain.Witness, error) {
	verify := p.Verify
	if verify <= 0 {
		verify = 3
	}
	regex := "^" + tgt.Test.Name + "$"

	// (1) Alone: a test that fails on its own is broken, not concurrent.
	alone, err := runFor(ctx, r, tgt.Test.Name, domain.RunSpec{
		Package: tgt.Dir, RunRegex: regex, Count: 1, Parallel: 1,
	})
	if err != nil {
		return nil, err
	}
	if alone.BuildFailed {
		return nil, buildFailed(tgt, alone)
	}
	if alone.Failed(tgt.Test.Name) {
		return nil, ErrNotReproduced
	}

	// (2) Whose bug is it? If the package already fails single-threaded, the
	// flake is about ordering, and the order probe owns it. Answering here too
	// would let two probes claim the same capsule with different witnesses.
	serial, err := runFor(ctx, r, tgt.Test.Name, domain.RunSpec{Package: tgt.Dir, Count: 1, Parallel: 1})
	if err != nil {
		return nil, err
	}
	if serial.BuildFailed {
		return nil, buildFailed(tgt, serial)
	}
	if serial.Failed(tgt.Test.Name) {
		return nil, ErrNotReproduced
	}

	// (3) The whole package, overlapped. This is the cheap existential check:
	// without it there is nothing to minimize and no reason to build witnesses.
	width := concurrencyWidth(len(tgt.Facts.Clause))
	conc, err := runFor(ctx, r, tgt.Test.Name, domain.RunSpec{Package: tgt.Dir, Count: 1, Parallel: width})
	if err != nil {
		return nil, err
	}
	if conc.BuildFailed {
		return nil, buildFailed(tgt, conc)
	}
	if !conc.Failed(tgt.Test.Name) {
		return nil, ErrNotReproduced
	}

	// The clause is resolved before candidate collection: an unknown clause is
	// ErrUnsupportedTest, and it is what makes the split below meaningful.
	clause, fn, err := p.witnessShape(tgt)
	if err != nil {
		return nil, err
	}

	same, cross := parallelCandidates(tgt)
	if len(same) == 0 {
		if len(cross) > 0 {
			// The victim only failed under concurrency and every test it could
			// have overlapped with lives in the other clause: the shape is
			// real, the witness form cannot call those functions.
			return nil, crossClauseError(tgt, cross, "parallel witness")
		}
		// Nothing to overlap with: the package already proved it fails only
		// under concurrency, but the only test in it is the victim.
		return nil, ErrNotReproduced
	}

	pred := p.predicate(tgt, r, clause, fn)
	min, err := search.Minimize(ctx, same, pred, verify)
	var uncertain *search.VerificationError
	if errors.As(err, &uncertain) && uncertain.Observation.Failures > 0 {
		// Keep the candidate as suspected, while the registry may still find a confirmed explanation.
	} else if err != nil {
		// Same-clause candidates do not explain the overlap while the other
		// clause does: the missing polluter is on the other side of the clause
		// boundary. Report it instead of the generic "not reproducible".
		if errors.Is(err, domain.ErrNotReproducible) && len(cross) > 0 {
			return nil, crossClauseError(tgt, cross, "parallel witness")
		}
		return nil, err
	}
	if len(min) == 0 {
		return nil, ErrNotReproduced
	}

	steps := make([]domain.Step, 0, len(min)+1)
	for _, t := range min {
		steps = append(steps, domain.Step{
			Label: domain.PolluterLabel(t.Name), Test: t,
			WrapParallel: needsParallelWrap(tgt, t.Name),
		})
	}
	steps = append(steps, domain.Step{
		Label: domain.VictimLabel(tgt.Test.Name), Test: tgt.Test,
		WrapParallel: needsParallelWrap(tgt, tgt.Test.Name),
	})

	w := &domain.Witness{
		Kind:      domain.KindParallel,
		Victim:    tgt.Test,
		Polluters: min,
		Steps:     steps,
		// One slot per subtest: any smaller and Go serialises the witness,
		// turning a proof into a false negative.
		Flags: domain.Flags{Parallel: len(steps)},
		Runs:  verify,
		// Same reason as the order probe: replay reinstalls this file by name.
		Func: fn,
	}
	if uncertain != nil {
		w.Verification = &uncertain.Observation
		return w, uncertain
	}
	return w, nil
}

// predicate builds "install a parallel witness of subset + victim, run it with
// enough width, did the victim fail?".
func (p Parallel) predicate(tgt Target, r gotest.Runner, clause, fn string) search.Predicate {
	return func(ctx context.Context, subset []domain.TestID) (bool, error) {
		steps := make([]domain.Step, 0, len(subset)+1)
		for _, t := range subset {
			steps = append(steps, domain.Step{
				Label: domain.PolluterLabel(t.Name), Test: t,
				WrapParallel: needsParallelWrap(tgt, t.Name),
			})
		}
		steps = append(steps, domain.Step{
			Label: domain.VictimLabel(tgt.Test.Name), Test: tgt.Test,
			WrapParallel: needsParallelWrap(tgt, tgt.Test.Name),
		})

		ctx, unlock, lerr := policy.Locked(ctx, tgt.Dir)
		if lerr != nil {
			return false, lerr
		}
		defer unlock()

		h, err := harness.InstallParallel(tgt.Dir, clause, fn, steps, "parallel")
		if err != nil {
			return false, err
		}
		defer func() { _ = h.Remove() }()

		res, err := runFor(ctx, r, domain.WitnessSubtest(fn, tgt.Test.Name), domain.RunSpec{
			Package:  tgt.Dir,
			RunRegex: "^" + fn + "$",
			Count:    1,
			Parallel: len(steps),
			Source:   h.Digest(),
		})
		if err != nil {
			return false, err
		}
		if res.BuildFailed {
			return false, domain.Wrap(domain.ErrInternal, "witness did not compile: %s", res.BuildOutput)
		}
		return res.Failed(domain.WitnessSubtest(fn, tgt.Test.Name)), nil
	}
}

// witnessShape resolves the package clause and a non-colliding function name.
func (p Parallel) witnessShape(tgt Target) (clause, fn string, err error) {
	clause, err = harness.ResolveClause(tgt.Facts.Clause, tgt.Test.Name, nil)
	if err != nil {
		return "", "", err
	}
	return clause, harness.FuncName(tgt.Facts.Clause), nil
}

// parallelCandidates lists every other test the witness could call, split by
// package clause.
//
// The split is a correctness requirement, not an optimisation. The witness is
// generated into the victim's own clause and calls test functions by name, so a
// test from the other clause (`p` vs `p_test`) is not in scope there: the
// compiler says "undefined: TestOther". Handing those names to search.Minimize
// turns a legitimate "this shape exists but I cannot express it" into
// domain.ErrInternal ("witness did not compile"), which ddmin propagates
// straight out of the probe.
//
// Note what a clause is: a package-visibility boundary, not a binary boundary —
// the two clauses of one directory are linked into ONE test binary, so their
// tests really do overlap, and one of them really can be the polluter. That is
// why a cross-clause candidate is reported as unsupported instead of dropped:
// silence would read as "no flake".
func parallelCandidates(tgt Target) (same, cross []domain.TestID) {
	names := make([]string, 0, len(tgt.Facts.Clause))
	for name := range tgt.Facts.Clause {
		// TestMain takes *testing.M, not *testing.T: calling it from a witness
		// is a compile error at best and a hijacked process at worst.
		if name == tgt.Test.Name || name == "TestMain" || !strings.HasPrefix(name, "Test") {
			continue
		}
		names = append(names, name)
	}
	sort.Strings(names)

	cands := make([]domain.TestID, 0, len(names))
	for _, name := range names {
		cands = append(cands, domain.TestID{Package: tgt.Test.Package, Name: name})
	}
	return splitByClause(cands, tgt.Facts.Clause, tgt.Test.Name)
}

// concurrencyWidth is how many tests to overlap when asking "is this package
// concurrency-sensitive at all". Small on purpose: the check is existential, and
// a large width costs CPU with no extra signal.
func concurrencyWidth(tests int) int {
	if tests < 2 {
		return 1
	}
	if tests > 4 {
		return 4
	}
	return tests
}

// packageUsesParallel reports any t.Parallel() in the victim's test binary.
func packageUsesParallel(tgt Target) bool {
	for _, has := range tgt.Facts.TestParallel {
		if has {
			return true
		}
	}
	return false
}

// needsParallelWrap reports whether the witness has to add t.Parallel() for
// this test.
//
// A test that calls t.Parallel() itself already lands in the parallel pool;
// wrapping it again panics the run ("testing: t.Parallel called multiple
// times"). A test that does not call it would stay serial inside the witness,
// so the steps never overlap and the witness proves nothing — that is the case
// the wrapper exists for.
func needsParallelWrap(tgt Target, name string) bool {
	return !tgt.Facts.TestParallel[name]
}
