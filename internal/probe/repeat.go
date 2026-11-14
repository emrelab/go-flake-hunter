package probe

import (
	"context"

	"github.com/emrelab/go-flake-hunter/internal/domain"
	"github.com/emrelab/go-flake-hunter/internal/gotest"
)

// Repeat detects state that survives between executions of the same test in one
// process: `go test -count=1` passes, `-count=N` fails on a later round.
//
// This is the cheapest probe by an order of magnitude — one extra invocation —
// so it runs first. A global counter, a reused temp path, a closed channel and
// a leaked goroutine all surface here without any ordering logic.
type Repeat struct {
	// Count is how many times to run the test in-process. 0 -> DefaultRepeatCount.
	Count int
}

// DefaultRepeatCount is enough for accumulation bugs and still fast.
const DefaultRepeatCount = 10

// Name implements Probe.
func (Repeat) Name() string { return "repeat" }

// Kind implements Probe.
func (Repeat) Kind() domain.Kind { return domain.KindRepeat }

// Probe implements Probe.
//
// Cost discipline: a clean test costs ONE run, not four. The repeat run comes
// first, because `-count=N` also covers the "fails always" case — a test that
// is simply broken fails there too, and only that path pays for the extra
// baseline that distinguishes "broken" from "flaky". With ~100 tests in a real
// package, the order of these two calls is the difference between a 5 second
// scan and a 20 second one.
func (p Repeat) Probe(ctx context.Context, tgt Target, r gotest.Runner) (*domain.Witness, error) {
	count := p.Count
	if count <= 0 {
		count = DefaultRepeatCount
	}
	regex := "^" + tgt.Test.Name + "$"

	// Repeated: state accumulates in-process.
	rep, err := runFor(ctx, r, tgt.Test.Name, domain.RunSpec{
		Package:  tgt.Dir,
		RunRegex: regex,
		Count:    count,
	})
	if err != nil {
		return nil, err
	}
	if rep.BuildFailed {
		return nil, buildFailed(tgt, rep)
	}
	if !rep.Failed(tgt.Test.Name) {
		// Passed on a single run and on the repeat: this class is out.
		return nil, ErrNotReproduced
	}

	// It failed. Broken or flaky? A single run decides: broken tests fail
	// alone too, and no ordering edit or repeat count will help them.
	base, err := runFor(ctx, r, tgt.Test.Name, domain.RunSpec{
		Package:  tgt.Dir,
		RunRegex: regex,
		Count:    1,
	})
	if err != nil {
		return nil, err
	}
	if base.BuildFailed {
		return nil, buildFailed(tgt, base)
	}
	if base.Failed(tgt.Test.Name) {
		// Fails on its own too: not a flake, a broken test.
		return nil, domain.ErrBrokenTest
	}

	// Confirm determinism: a single failing round can be noise.
	again, err := runFor(domain.FreshRuns(ctx), r, tgt.Test.Name, domain.RunSpec{
		Package:  tgt.Dir,
		RunRegex: regex,
		Count:    count,
	})
	if err != nil {
		return nil, err
	}
	if again.BuildFailed {
		return nil, buildFailed(tgt, again)
	}
	if !again.Failed(tgt.Test.Name) {
		return nil, domain.Wrap(domain.ErrNotReproducible, "%s did not fail on the second -count=%d run", tgt.Test.Name, count)
	}

	return &domain.Witness{
		Kind:      domain.KindRepeat,
		Victim:    tgt.Test,
		Polluters: nil, // single test: no polluter
		Flags:     domain.Flags{Count: count},
		Runs:      2,
	}, nil
}
