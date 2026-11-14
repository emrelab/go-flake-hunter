package probe

import (
	"context"
	"os"

	"github.com/emrelab/go-flake-hunter/internal/domain"
	"github.com/emrelab/go-flake-hunter/internal/gotest"
)

// Env detects environment-dependent flakes: the test passes under one value of
// an environment variable and fails under another. This is the classic "works
// on my machine, red in CI" failure, and it is invisible to repeat and order
// because nothing in the process changes between runs.
//
// Two deliberate constraints:
//
//   - Candidate values come from the code (`os.Getenv("MODE") == "ci"`), never
//     from a guess. Guessing a CI flag from a variable name is how a tool ends
//     up reporting fantasies.
//   - The VALUE is never written to the capsule. Only the key is. A capsule is
//     committed to a repository; an env value is frequently a token.
type Env struct {
	// Verify is how many times a witness must reproduce. 0 -> 3.
	Verify int
}

// Name implements Probe.
func (Env) Name() string { return "env" }

// Applicable implements Gate: without a literal os.Getenv key there is nothing
// to vary, and a probe that guesses a CI flag from a variable name reports
// fantasies.
func (Env) Applicable(tgt Target) bool { return len(envKeys(tgt)) > 0 }

// InapplicableReason implements GateReason.
func (Env) InapplicableReason(Target) string {
	return "no literal os.Getenv/LookupEnv read"
}

// Kind implements Probe.
func (Env) Kind() domain.Kind { return domain.KindEnv }

// Probe implements Probe.
func (p Env) Probe(ctx context.Context, tgt Target, r gotest.Runner) (*domain.Witness, error) {
	verify := p.Verify
	if verify <= 0 {
		verify = 3
	}
	keys := envKeys(tgt)
	regex := "^" + tgt.Test.Name + "$"
	spec := domain.RunSpec{Package: tgt.Dir, RunRegex: regex, Count: 1, Parallel: 1}

	baseline, err := runFor(ctx, r, tgt.Test.Name, spec)
	if err != nil {
		return nil, err
	}
	if baseline.BuildFailed {
		return nil, buildFailed(tgt, baseline)
	}
	baselineFailed := baseline.Failed(tgt.Test.Name)

	for _, key := range keys {
		values, ok := tgt.Facts.EnvValues[key]
		if !ok || len(values) == 0 {
			continue
		}
		for _, v := range values {
			if os.Getenv(key) == v && baselineFailed {
				continue // already running with this value; no new information
			}
			probeSpec := spec
			probeSpec.Env = []string{key + "=" + v}

			res, err := runFor(ctx, r, tgt.Test.Name, probeSpec)
			if err != nil {
				return nil, err
			}
			if res.BuildFailed {
				return nil, buildFailed(tgt, res)
			}
			// The flake is the DIFFERENCE: fail here, pass there.
			if res.Failed(tgt.Test.Name) == baselineFailed {
				continue
			}

			w := &domain.Witness{
				Kind:   domain.KindEnv,
				Victim: tgt.Test,
				Steps:  []domain.Step{{Label: domain.VictimLabel(tgt.Test.Name), Test: tgt.Test}},
				// Key only: the value may be a secret and the capsule goes into the repo.
				EnvDiff: []string{key},
				Runs:    1,
			}
			if err := p.verify(ctx, r, spec, probeSpec, tgt, verify); err != nil {
				return nil, err
			}
			w.Runs = verify
			return w, nil
		}
	}
	return nil, ErrNotReproduced
}

// verify re-observes BOTH sides of the difference.
//
// Checking only the variant is not enough: this class of flake is "outcome
// depends on the value", so the proof is that the two outcomes still differ.
// A witness that reproduces sometimes is a lie once it is committed to a repo.
func (p Env) verify(ctx context.Context, r gotest.Runner, base, variant domain.RunSpec, tgt Target, n int) error {
	ctx = domain.FreshRuns(ctx)
	for i := 0; i < n; i++ {
		b, err := runFor(ctx, r, tgt.Test.Name, base)
		if err != nil {
			return err
		}
		v, err := runFor(ctx, r, tgt.Test.Name, variant)
		if err != nil {
			return err
		}
		if b.Failed(tgt.Test.Name) == v.Failed(tgt.Test.Name) {
			return domain.Wrap(domain.ErrNotReproducible,
				"env witness did not reproduce on verification run %d/%d", i+1, n)
		}
	}
	return nil
}

// envKeys is the ordered set of env variables the victim (or its package) reads
// literally, filtered to those we have candidate values for. Test-file reads
// come first, application-file reads second: a variable the package code reads
// can still flip the test's outcome (the witness runs the real `go test`), but
// app code also reads dozens of variables no test outcome depends on, so they
// must not spend the probe's budget before the local keys.
func envKeys(tgt Target) []string {
	seen := map[string]bool{}
	var out []string
	for _, group := range [][]string{tgt.Facts.EnvReads, tgt.Facts.AppEnvReads} {
		for _, k := range group {
			if k == "" || seen[k] {
				continue
			}
			if _, ok := tgt.Facts.EnvValues[k]; !ok {
				continue
			}
			seen[k] = true
			out = append(out, k)
		}
	}
	return out
}
