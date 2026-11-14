package probe

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/emrelab/go-flake-hunter/internal/domain"
	"github.com/emrelab/go-flake-hunter/internal/gotest"
	"github.com/emrelab/go-flake-hunter/internal/harness"
	"github.com/emrelab/go-flake-hunter/internal/policy"
	"github.com/emrelab/go-flake-hunter/internal/search"
)

// Order detects sequence-dependent flakes: the victim passes alone but fails
// when another test ran first and left shared state behind.
//
// This is the flagship hypothesis — the product's whole premise — and also the
// most expensive: it needs a shuffle search plus ddmin, each step a build.
//
// Two constraints shape the implementation:
//
//   - Sequence is only meaningful at -parallel=1. With t.Parallel the JSON
//     "run" order reflects the pause point, not execution, so a witness
//     derived from it would be fiction. Parallel flakes are a separate class.
//   - Same-clause chains use a generated file. Mixed clauses use a measured
//     top-level selection with the observed shuffle seed; regex alternation
//     alone never defines execution order.
type Order struct {
	// Seeds are the -shuffle seeds to try before giving up. 0 -> DefaultSeeds.
	Seeds []int64
	// Verify is how many times a minimized witness must reproduce. 0 -> 3.
	//
	//	Classification and shrinking ALWAYS run without race. The -race
	// compiler slows timing and pushes the timing witness below the threshold;
	// measured: the parallel-race fixture failed verification 1/3 with --race.
	// So Race is not a search parameter but extra evidence for the witness
	// found (see ConfirmRace).
	Verify int
}

// DefaultSeeds is the coverage/budget compromise.
//
// Each seed is a full package build (~0.3 s warm), so the count IS the price:
// 40 seeds cost ~12 s per package even when nothing is wrong. 12 orderings
// catch the realistic cases (a polluter that runs first in source order shows
// up immediately; a deep one usually surfaces within a dozen permutations) and
// keep an honest hunt under a few seconds. `--seeds N` raises it when a package
// is suspected but clean.
var DefaultSeeds = []int64{1, 2, 3, 5, 8, 13, 21, 34, 55, 89, 144, 233}

// SeedsN returns the first n default seeds, cycling if n exceeds the list, so
// every value is still a distinct permutation.
func SeedsN(n int) []int64 {
	if n <= 0 {
		return DefaultSeeds
	}
	if n <= len(DefaultSeeds) {
		return DefaultSeeds[:n]
	}
	out := make([]int64, 0, n)
	for i := 0; i < n; i++ {
		base := DefaultSeeds[i%len(DefaultSeeds)]
		out = append(out, base+int64(i/len(DefaultSeeds))*1000)
	}
	return out
}

// Name implements Probe.
func (Order) Name() string { return "order" }

// Kind implements Probe.
func (Order) Kind() domain.Kind { return domain.KindOrder }

// Probe implements Probe.
func (p Order) Probe(ctx context.Context, tgt Target, r gotest.Runner) (*domain.Witness, error) {
	seeds := p.Seeds
	if len(seeds) == 0 {
		seeds = DefaultSeeds
	}
	verify := p.Verify
	if verify <= 0 {
		verify = 3
	}

	regex := "^" + tgt.Test.Name + "$"

	// (1) The whole package, source order, single-threaded. One run, and it is
	// cached for every other test in this package: it answers both "does the
	// package already fail in its default order?" and "is this test broken on
	// its own?" for the common case.
	all, err := r.Run(ctx, domain.RunSpec{Package: tgt.Dir, Count: 1, Parallel: 1})
	if err != nil {
		return nil, err
	}
	if all.BuildFailed {
		return nil, buildFailed(tgt, all)
	}

	// (2) Alone, single-threaded. Only paid when the package run failed: a test
	// that fails by itself is broken, not flaky, and no ordering edit will help.
	if all.Failed(tgt.Test.Name) {
		alone, err := r.Run(ctx, domain.RunSpec{
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
		return p.minimize(ctx, tgt, r, all.Order, verify, 0)
	}

	// (3) Shuffle search. This is what makes order-dependence findable in a
	// package whose source order happens to be lucky.
	for _, seed := range seeds {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		res, err := r.Run(ctx, domain.RunSpec{
			Package: tgt.Dir, Count: 1, Parallel: 1, ShuffleSeed: seed,
		})
		if err != nil {
			return nil, err
		}
		if res.BuildFailed {
			return nil, buildFailed(tgt, res)
		}
		if res.Failed(tgt.Test.Name) {
			return p.minimize(ctx, tgt, r, res.Order, verify, seed)
		}
	}
	return nil, ErrNotReproduced
}

// minimize shrinks the set of tests that ran before the victim.
func (p Order) minimize(ctx context.Context, tgt Target, r gotest.Runner, order []string, verify int, seed int64) (*domain.Witness, error) {
	candidates := before(order, tgt.Test.Name)
	if len(candidates) == 0 {
		// The failure came from the environment, not from a sibling test.
		return nil, ErrNotReproduced
	}

	// A witness can only call tests that live in the victim's own package
	// clause. `pkg` and `pkg_test` are two visibility boundaries inside ONE
	// test binary, so their tests do run in the same process — but the witness
	// file is generated into one clause and calls functions by name, and a test
	// function of the other clause is not addressable from there
	// ("undefined: TestOther"). A mixed-clause polluter therefore needs a measured top-level selection.
	same, cross := splitByClause(candidates, tgt.Facts.Clause, tgt.Test.Name)

	clause, fn, err := p.witnessShape(tgt)
	if err != nil {
		return nil, err
	}

	if len(same) == 0 && len(cross) > 0 {
		return p.minimizeSelection(ctx, tgt, r, candidates, verify, seed)
	}

	pred := p.predicate(tgt, r, clause, fn)
	min, err := search.Minimize(ctx, same, pred, verify)
	var uncertain *search.VerificationError
	if errors.As(err, &uncertain) && uncertain.Observation.Failures > 0 {
		// Keep the candidate as suspected, while the registry may still find a confirmed explanation.
	} else if err != nil {
		// Nothing in the victim's binary reproduces the failure, yet the
		// package as a whole did fail: the missing ingredient is on the other
		// side of the visibility boundary.
		if errors.Is(err, domain.ErrNotReproducible) && len(cross) > 0 {
			return p.minimizeSelection(ctx, tgt, r, candidates, verify, seed)
		}
		return nil, err
	}
	if len(min) == 0 {
		return nil, ErrNotReproduced
	}

	steps := make([]domain.Step, 0, len(min)+1)
	for _, t := range min {
		steps = append(steps, domain.Step{Label: domain.PolluterLabel(t.Name), Test: t})
	}
	steps = append(steps, domain.Step{Label: domain.VictimLabel(tgt.Test.Name), Test: tgt.Test})

	w := &domain.Witness{
		Kind:      domain.KindOrder,
		Victim:    tgt.Test,
		Polluters: min,
		Steps:     steps,
		Flags:     domain.Flags{Parallel: 1},
		Runs:      verify,
		// The exact name the predicate installed. Replay re-creates the same
		// file; recomputing it later (from a package that has since gained a
		// TestFlakeWitness) would collide and read as "flake fixed".
		Func: fn,
	}
	if uncertain != nil {
		w.Verification = &uncertain.Observation
		return w, uncertain
	}
	return w, nil
}

// predicate builds "install a witness of subset + victim, run it, did the
// victim fail?".
//
// The harness is installed and removed around every single call: a witness file
// that survives one iteration would be compiled into the next one and turn the
// search into a self-fulfilling prophecy.
func (p Order) predicate(tgt Target, r gotest.Runner, clause, fn string) search.Predicate {
	return func(ctx context.Context, subset []domain.TestID) (bool, error) {
		steps := make([]domain.Step, 0, len(subset)+1)
		for _, t := range subset {
			steps = append(steps, domain.Step{Label: domain.PolluterLabel(t.Name), Test: t})
		}
		steps = append(steps, domain.Step{Label: domain.VictimLabel(tgt.Test.Name), Test: tgt.Test})

		// The lock is held from install to remove: if another process builds
		// the package in between, it sees the witness and gets an unknown test error.
		ctx, unlock, lerr := policy.Locked(ctx, tgt.Dir)
		if lerr != nil {
			return false, lerr
		}
		defer unlock()

		h, err := harness.Install(tgt.Dir, clause, fn, steps, "search")
		if err != nil {
			return false, err
		}
		// Remove even on panic: a leaked witness poisons every later build.
		defer func() { _ = h.Remove() }()

		res, err := r.Run(ctx, domain.RunSpec{
			Package:  tgt.Dir,
			RunRegex: "^" + fn + "$",
			Count:    1,
			Parallel: 1,
			Source:   h.Digest(),
		})
		if err != nil {
			return false, err
		}
		if res.BuildFailed {
			// Not "no": the witness itself failed to compile, which is our bug.
			return false, domain.Wrap(domain.ErrInternal, "witness did not compile: %s", res.BuildOutput)
		}
		// The victim's verdict lives on the subtest, never on the parent.
		return res.Failed(domain.WitnessSubtest(fn, tgt.Test.Name)), nil
	}
}

// witnessShape resolves the package clause and a non-colliding function name.
func (p Order) witnessShape(tgt Target) (clause, fn string, err error) {
	clause, err = harness.ResolveClause(tgt.Facts.Clause, tgt.Test.Name, nil)
	if err != nil {
		return "", "", err
	}
	return clause, harness.FuncName(tgt.Facts.Clause), nil
}

// buildFailed explains an uncompilable package with the first compiler line,
// not just the directory: "package failed to build: /path" leaves the user to run
// `go build` themselves to find out why.
func buildFailed(tgt Target, res *domain.RunResult) error {
	detail := firstLine(res.BuildOutput)
	if detail == "" {
		detail = tgt.Dir
	}
	return domain.Wrap(domain.ErrBuildFailed, "%s", detail)
}

func firstLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			return line
		}
	}
	return ""
}

// splitByClause divides candidates into those a witness for victim can call and
// those it cannot, because they belong to the other visibility clause.
//
// A candidate without a known clause (a subtest name, or a name the AST scan
// did not see) is treated as same-clause: dropping it on a guess would lose
// real polluters, while keeping it only costs a build that fails loudly.
func splitByClause(cands []domain.TestID, clauseOf map[string]string, victim string) (same, cross []domain.TestID) {
	victimClause := clauseOf[victim]
	for _, c := range cands {
		cl, ok := clauseOf[c.Name]
		if !ok || cl == victimClause {
			same = append(same, c)
			continue
		}
		cross = append(cross, c)
	}
	return same, cross
}

// crossClauseError names the conflict. A bare "unsupported" would leave the
// user guessing which tests, in which packages, defeated the tool.
//
// The witness kind is a parameter because the same finding defeats two different
// shapes: an order chain and a parallel overlap. Naming the wrong one would send
// the reader looking in the wrong place.
func crossClauseError(tgt Target, cross []domain.TestID, kind string) error {
	victimClause := tgt.Facts.Clause[tgt.Test.Name]
	parts := make([]string, 0, len(cross))
	for _, c := range cross {
		parts = append(parts, fmt.Sprintf("%s (package %s)", c.Name, tgt.Facts.Clause[c.Name]))
	}
	return domain.Wrap(domain.ErrUnsupportedTest,
		"%s (package %s) and %s are not in the same test package clause; no %s can be built",
		tgt.Test.Name, victimClause, strings.Join(parts, ", "), kind)
}

// before returns the tests that ran before victim, in the observed order.
// Unknown victim (victim never ran) yields nil, which the caller treats as
// "no sibling polluter".
func before(order []string, victim string) []domain.TestID {
	out := make([]domain.TestID, 0, len(order))
	for _, name := range order {
		if name == victim {
			return out
		}
		out = append(out, domain.TestID{Name: name})
	}
	return nil
}

// minimizeSelection preserves a real top-level ordering across visibility clauses.
// Both clauses belong to one binary; no generated Go file can call both.
func (p Order) minimizeSelection(ctx context.Context, tgt Target, r gotest.Runner, candidates []domain.TestID, verify int, seed int64) (*domain.Witness, error) {
	pred := func(ctx context.Context, subset []domain.TestID) (bool, error) {
		w := domain.Witness{Victim: tgt.Test, Polluters: subset}
		res, err := r.Run(ctx, domain.RunSpec{Package: tgt.Dir, RunRegex: domain.SelectionRegex(w), Count: 1, Parallel: 1, ShuffleSeed: seed})
		if err != nil {
			return false, err
		}
		if res.BuildFailed {
			return false, buildFailed(tgt, res)
		}
		// Removing tests changes a seeded permutation: measure the selected subset,
		// never assume that regex alternation defines execution order.
		return res.Failed(tgt.Test.Name), nil
	}
	min, err := search.Minimize(ctx, candidates, pred, verify)
	var uncertain *search.VerificationError
	if err != nil && (!errors.As(err, &uncertain) || uncertain.Observation.Failures <= 0) {
		return nil, err
	}
	w := &domain.Witness{Kind: domain.KindOrder, Victim: tgt.Test, Polluters: min, Flags: domain.Flags{Parallel: 1}, Runs: verify, CrossClause: true, Seed: seed}
	if uncertain != nil {
		w.Verification = &uncertain.Observation
		return w, uncertain
	}
	return w, nil
}
