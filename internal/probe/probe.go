package probe

import (
	"context"

	"github.com/emrelab/go-flake-hunter/internal/domain"
	"github.com/emrelab/go-flake-hunter/internal/facts"
	"github.com/emrelab/go-flake-hunter/internal/gotest"
)

// Target is what a probe investigates.
type Target struct {
	Test  domain.TestID
	Dir   string // package directory (the probe's cwd for go test)
	Facts facts.PkgFacts
}

// Probe is one hypothesis. Implementations must be safe to call with a target
// they cannot explain: returning ErrNotReproduced is a normal answer, not a
// failure.
type Probe interface {
	// Name is stable; it appears in reports and in the registry order.
	Name() string
	// Kind is the classification this probe is looking for.
	Kind() domain.Kind
	Probe(ctx context.Context, tgt Target, r gotest.Runner) (*domain.Witness, error)
}

// Gate is an optional interface: a probe that can rule itself out from the
// static facts alone, without spending a single build.
//
// The distinction matters to the user. "No t.Parallel() in this package" is a
// fact about their code; "tried it, does not explain" is a fact about our search. Merging
// them into one "not this class" line hides which of the two happened, and that
// is exactly the confusion this tool exists to remove.
type Gate interface {
	Applicable(tgt Target) bool
}

// GateReason is an optional companion to Gate: the one sentence explaining why
// the probe cannot apply, shown in the "tried" list.
type GateReason interface {
	InapplicableReason(tgt Target) string
}

// ErrNotReproduced means "this hypothesis does not explain the flake".
// It is deliberately distinct from a real error so the registry can continue.
var ErrNotReproduced = domain.ErrNotReproduced

// Registry holds probes in cost order.
type Registry struct{ probes []Probe }

// NewRegistry returns the default probe order.
func NewRegistry(ps ...Probe) *Registry { return &Registry{probes: ps} }

// Probes returns the registered probes in order.
func (r *Registry) Probes() []Probe {
	out := make([]Probe, len(r.probes))
	copy(out, r.probes)
	return out
}

// Result is what a full hunt produced.
type Result struct {
	Witness   *domain.Witness
	Suspected *domain.Witness
	// Tried lists every probe name that ran, in order, with its outcome.
	Tried []Attempt
}

// Attempt records one probe's verdict, so a "not found" report can be honest
// about what was actually tried instead of a bare empty answer.
type Attempt struct {
	Probe string
	Kind  domain.Kind
	Err   error
	// Skipped is set when the probe ruled itself out statically (Gate): no
	// build was spent, and Reason says why.
	Skipped bool
	Reason  string
}

// Run applies probes in order and returns the first verified witness.
func (r *Registry) Run(ctx context.Context, tgt Target, runner gotest.Runner) (*Result, error) {
	res := &Result{}
	var refusal error // a probe that found the shape but cannot express it (#6)
	for _, p := range r.probes {
		if err := ctx.Err(); err != nil {
			return res, err
		}
		if g, ok := p.(Gate); ok && !g.Applicable(tgt) {
			reason := "statically impossible"
			if gr, ok := p.(GateReason); ok {
				reason = gr.InapplicableReason(tgt)
			}
			res.Tried = append(res.Tried, Attempt{
				Probe: p.Name(), Kind: p.Kind(), Skipped: true, Reason: reason,
			})
			continue
		}

		w, err := p.Probe(ctx, tgt, runner)
		res.Tried = append(res.Tried, Attempt{Probe: p.Name(), Kind: p.Kind(), Err: err})
		if err != nil {
			if w != nil && w.Verification != nil && errorsIs(err, domain.ErrNotReproducible) {
				res.Suspected = w
				continue
			}
			if isNotReproduced(err) {
				continue
			}
			// Preserve an unsupported shape while trying later hypotheses.
			if errorsIs(err, domain.ErrUnsupportedTest) {
				refusal = err
				continue
			}
			return res, err // real failure (build error, budget, ctx) stops the hunt
		}
		if w == nil {
			continue
		}
		res.Witness = w
		return res, nil
	}
	if res.Suspected != nil {
		return res, domain.ErrNotReproducible
	}
	if refusal != nil {
		return res, refusal
	}
	return res, ErrNotReproduced
}

func isNotReproduced(err error) bool {
	return err != nil && (errorsIs(err, domain.ErrNotReproduced))
}

// runFor rejects a real run that lacks a terminal result for the probe's victim.
func runFor(ctx context.Context, r gotest.Runner, name string, spec domain.RunSpec) (*domain.RunResult, error) {
	res, err := r.Run(ctx, spec)
	if err == nil && !res.BuildFailed && res.Terminal != nil && !res.Completed(name, spec.WithDefaults().Count) {
		err = domain.Wrap(domain.ErrIncomplete, "missing results for %s", name)
	}
	return res, err
}
