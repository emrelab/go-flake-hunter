package probe_test

import (
	"context"
	"errors"
	"testing"

	"github.com/emrelab/go-flake-hunter/internal/domain"
	"github.com/emrelab/go-flake-hunter/internal/gotest"
	"github.com/emrelab/go-flake-hunter/internal/probe"
)

func victim() domain.TestID { return domain.TestID{Package: "p", Name: "TestA"} }

// Passes on a single run, fails on the repeat: classic state accumulation.
func TestRepeatFindsAccumulation(t *testing.T) {
	tgt := probe.Target{Test: victim(), Dir: "dir"}
	fake := &gotest.Fake{RunFn: func(s domain.RunSpec) (*domain.RunResult, error) {
		if s.Count == 1 {
			return gotest.Result(s, map[string]domain.Status{"TestA": domain.StatusPass}), nil
		}
		return gotest.Result(s, map[string]domain.Status{"TestA": domain.StatusFail}), nil
	}}

	w, err := probe.Repeat{Count: 10}.Probe(context.Background(), tgt, fake)
	if err != nil {
		t.Fatalf("a witness was expected: %v", err)
	}
	if w.Kind != domain.KindRepeat {
		t.Errorf("Kind = %s", w.Kind)
	}
	if len(w.Polluters) != 0 {
		t.Error("a repeat flake should have no polluter")
	}
	if w.Runs != 2 {
		t.Errorf("Runs = %d, verification means 2 runs", w.Runs)
	}
	// Three runs: repeat (-count=10), baseline (broken?), verification repeat.
	// Repeat first: a clean test is eliminated with one run, a 4x difference in real packages.
	if fake.CallCount() != 3 {
		t.Errorf("CallCount = %d, want 3", fake.CallCount())
	}
	if fake.Runs()[0].Count != 10 {
		t.Errorf("the first run should be -count=10, got %d", fake.Runs()[0].Count)
	}
	if fake.Runs()[1].Count != 1 {
		t.Errorf("the baseline should be -count=1, got %d", fake.Runs()[1].Count)
	}
}

// A test that fails on its own is broken, not flaky: repeat does not explain it.
func TestRepeatReportsAlwaysFailing(t *testing.T) {
	tgt := probe.Target{Test: victim(), Dir: "dir"}
	fake := &gotest.Fake{RunFn: func(s domain.RunSpec) (*domain.RunResult, error) {
		return gotest.Result(s, map[string]domain.Status{"TestA": domain.StatusFail}), nil
	}}

	_, err := probe.Repeat{}.Probe(context.Background(), tgt, fake)
	if !errors.Is(err, domain.ErrBrokenTest) {
		t.Fatalf("expected ErrBrokenTest, got %v", err)
	}
	// Two runs: -count=10 (failed) + baseline (fails alone too → broken).
	// No third verification run: there is no witness, no point gathering evidence.
	if fake.CallCount() != 2 {
		t.Errorf("waste: %d runs made, 2 would have sufficed", fake.CallCount())
	}
}

// A stable test: should not run at all, yet still must not answer wrongly.
func TestRepeatStableTest(t *testing.T) {
	tgt := probe.Target{Test: victim(), Dir: "dir"}
	fake := &gotest.Fake{RunFn: func(s domain.RunSpec) (*domain.RunResult, error) {
		return gotest.Result(s, map[string]domain.Status{"TestA": domain.StatusPass}), nil
	}}
	if _, err := (probe.Repeat{}).Probe(context.Background(), tgt, fake); !errors.Is(err, domain.ErrNotReproduced) {
		t.Fatalf("expected ErrNotReproduced, got %v", err)
	}
}

// The repeat fails but the second repeat does not: not deterministic;
// silently saying "found it" would produce a wrong witness.
func TestRepeatRequiresDeterminism(t *testing.T) {
	tgt := probe.Target{Test: victim(), Dir: "dir"}
	call := 0
	fake := &gotest.Fake{RunFn: func(s domain.RunSpec) (*domain.RunResult, error) {
		if s.Count == 1 {
			return gotest.Result(s, map[string]domain.Status{"TestA": domain.StatusPass}), nil
		}
		call++
		if call == 1 {
			return gotest.Result(s, map[string]domain.Status{"TestA": domain.StatusFail}), nil
		}
		return gotest.Result(s, map[string]domain.Status{"TestA": domain.StatusPass}), nil
	}}

	_, err := probe.Repeat{}.Probe(context.Background(), tgt, fake)
	if !errors.Is(err, domain.ErrNotReproducible) {
		t.Fatalf("expected ErrNotReproducible, got %v", err)
	}
}

// A build error must stop the hunt: this is not a flake, it is a user error.
func TestRepeatStopsOnBuildFailure(t *testing.T) {
	tgt := probe.Target{Test: victim(), Dir: "dir"}
	fake := &gotest.Fake{RunFn: func(s domain.RunSpec) (*domain.RunResult, error) {
		return &domain.RunResult{Spec: s, BuildFailed: true, BuildOutput: "syntax error"}, nil
	}}
	_, err := probe.Repeat{}.Probe(context.Background(), tgt, fake)
	if !errors.Is(err, domain.ErrBuildFailed) {
		t.Fatalf("expected ErrBuildFailed, got %v", err)
	}
}

// Registry tries probes in order and returns the first witness.
func TestRegistryStopsAtFirstWitness(t *testing.T) {
	tgt := probe.Target{Test: victim(), Dir: "dir"}
	fake := &gotest.Fake{RunFn: func(s domain.RunSpec) (*domain.RunResult, error) {
		return gotest.Result(s, map[string]domain.Status{"TestA": domain.StatusPass}), nil
	}}
	reg := probe.NewRegistry(probe.Repeat{})

	res, err := reg.Run(context.Background(), tgt, fake)
	if !errors.Is(err, probe.ErrNotReproduced) {
		t.Fatalf("expected ErrNotReproduced, got %v", err)
	}
	if len(res.Tried) != 1 || res.Tried[0].Probe != "repeat" {
		t.Fatalf("the tried list must be reported: %+v", res.Tried)
	}
}
