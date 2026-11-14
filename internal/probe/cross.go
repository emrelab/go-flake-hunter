package probe

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/emrelab/go-flake-hunter/internal/domain"
	"github.com/emrelab/go-flake-hunter/internal/gotest"
	"github.com/emrelab/go-flake-hunter/internal/policy"
	"github.com/emrelab/go-flake-hunter/internal/search"
)

// OrderCross detects cross-binary order pollution (#6): a test in another
// package ran FIRST, and the
// state it left OUTSIDE its process made the victim fail in a later, separate
// `go test` invocation. Files on disk, bound-then-released ports, cwd edits:
// all survive the process boundary. In-memory globals do not, and this probe
// deliberately cannot see them — that is the same-binary order probe's job,
// and pretending otherwise would manufacture witnesses physics cannot replay.
//
// The reproduction is not a witness file but a command with two stages:
//
//	go test <polluter> -run '^(TestBad)$' -count=1 && go test <victim> -run '^TestV$' -count=1
//
// So the capsule is compiled-refusal by contract (see cli/compile.go) and the
// two-stage command IS the permanent record; `replay` runs the stages too.
//
// The search space "every package in the module" is unbounded and its price is
// unbounded, so the user points at the suspect with --polluter-pkg. That is
// also the honest shape: CI logs, upstream issues and git blame name a suspect
// long before a tool could brute-force one.
type OrderCross struct {
	// PolluterDir identifies a separate go test invocation, even if the directory is the same.
	PolluterDir string
	// Verify is how many times the two-stage pair must re-flip. 0 -> 3.
	Verify int
}

// Name implements Probe.
func (OrderCross) Name() string { return "order-cross" }

// Kind implements Probe. Cross-binary is a shape of the ORDER class, not a
// fifth class: the failure still means "something ran before me". The
// CrossBinary flag on the witness carries the shape.
func (OrderCross) Kind() domain.Kind { return domain.KindOrder }

// Probe implements Probe.
func (p OrderCross) Probe(ctx context.Context, tgt Target, r gotest.Runner) (*domain.Witness, error) {
	ctx, unlock, lockErr := policy.LockedDirs(ctx, tgt.Dir, p.PolluterDir)
	if lockErr != nil {
		return nil, lockErr
	}
	defer unlock()
	verify := p.Verify
	if verify <= 0 {
		verify = 3
	}

	cands, err := r.Discover(ctx, p.PolluterDir)
	if err != nil {
		return nil, domain.Wrap(domain.ErrBuildFailed, "polluter package %s could not be listed", p.PolluterDir)
	}
	if p.PolluterDir == tgt.Dir {
		// Same directory: the victim cannot pollute itself. A different
		// package MAY host a same-named test — that is a different polluter.
		cands = withoutName(cands, tgt.Test.Name)
	}
	if len(cands) == 0 {
		return nil, ErrNotReproduced
	}

	victim := domain.RunSpec{
		Package:  tgt.Dir,
		RunRegex: "^" + regexp.QuoteMeta(tgt.Test.Name) + "$",
		Count:    1, Parallel: 1,
	}

	// (1) The victim alone passes; a test that fails alone is broken, not
	// polluted. This is also the baseline the whole witness is "difference
	// against": without a passing alone-run, "it failed after the polluter"
	// proves nothing.
	base, err := runFor(ctx, r, tgt.Test.Name, victim)
	if err != nil {
		return nil, err
	}
	if base.BuildFailed {
		return nil, buildFailed(tgt, base)
	}
	if !base.Passed(tgt.Test.Name) {
		return nil, ErrNotReproduced
	}

	// round makes every stage spec unique: the result cache must never serve
	// one round's stage-2 PASS to the next round, whose stage-1 ran different
	// polluters (the exact trap RunSpec.Source exists for).
	round := 0
	pred := func(ctx context.Context, subset []domain.TestID) (bool, error) {
		if len(subset) == 0 {
			return false, nil // no polluter ran: not an order witness, drop the empty set
		}
		round++
		s1 := domain.RunSpec{
			Package:  p.PolluterDir,
			RunRegex: unionRegex(subset),
			Count:    1, Parallel: 1,
			Source: fmt.Sprintf("cross/%d/1", round),
		}
		res1, err := r.Run(ctx, s1)
		if err != nil {
			return false, err
		}
		if res1.BuildFailed {
			return false, domain.Wrap(domain.ErrBuildFailed, "polluter package does not build: %s", firstLine(res1.BuildOutput))
		}

		if res1.ExitCode != 0 {
			return false, nil
		}
		s2 := victim
		s2.Source = fmt.Sprintf("cross/%d/2", round)
		res2, err := runFor(ctx, r, tgt.Test.Name, s2)
		if err != nil {
			return false, err
		}
		if res2.BuildFailed {
			return false, buildFailed(tgt, res2)
		}
		// Only the victim's own verdict counts: the polluter suite going red
		// for its own reasons is news, but not evidence about THIS flake.
		return res2.Failed(tgt.Test.Name), nil
	}

	// (2) The whole candidate set first: one honest answer to "does anything
	// cross the boundary at all" before paying for the shrink. ddmin assumes
	// the full set reproduces — entering it on a set that does not would turn
	// "not this class" into a shrunk lie.
	ok, err := pred(ctx, cands)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, ErrNotReproduced
	}

	// Minimize shrinks by ddmin AND re-verifies the final set `verify` times,
	// every verification re-running both stages — determinism across processes
	// is exactly what this class must prove.
	min, err := search.Minimize(ctx, cands, pred, verify)
	var uncertain *search.VerificationError
	if err != nil && (!errors.As(err, &uncertain) || uncertain.Observation.Failures <= 0) {
		return nil, err
	}
	if len(min) == 0 {
		return nil, ErrNotReproduced
	}

	steps := make([]domain.Step, 0, len(min)+1)
	for _, t := range min {
		steps = append(steps, domain.Step{
			Label:   domain.PolluterLabel(t.Name),
			Test:    t,
			Package: t.Package,
		})
	}
	steps = append(steps, domain.Step{
		Label:   domain.VictimLabel(tgt.Test.Name),
		Test:    tgt.Test,
		Package: tgt.Test.Package,
	})

	w := &domain.Witness{
		Kind:        domain.KindOrder,
		CrossBinary: true,
		Victim:      tgt.Test,
		Polluters:   min,
		Steps:       steps,
		Flags:       domain.Flags{Parallel: 1},
		Runs:        verify,
	}
	if uncertain != nil {
		w.Verification = &uncertain.Observation
		return w, uncertain
	}
	return w, nil
}

// unionRegex is the -run pattern that executes exactly the subset, in source
// order: "^(TestA|TestB)$". Names are quoted: a test name is user input here.
func unionRegex(subset []domain.TestID) string {
	names := make([]string, 0, len(subset))
	for _, t := range subset {
		names = append(names, regexp.QuoteMeta(t.Name))
	}
	return "^(" + strings.Join(names, "|") + ")$"
}

// withoutName drops one test from the candidate list.
func withoutName(cands []domain.TestID, name string) []domain.TestID {
	out := make([]domain.TestID, 0, len(cands))
	for _, c := range cands {
		if c.Name != name {
			out = append(out, c)
		}
	}
	return out
}
