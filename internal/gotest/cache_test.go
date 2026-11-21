package gotest_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/emrelab/go-flake-hunter/internal/domain"
	"github.com/emrelab/go-flake-hunter/internal/gotest"
)

func TestCacheHonorsTimeoutAndCancellation(t *testing.T) {
	fake := &gotest.Fake{RunFn: func(s domain.RunSpec) (*domain.RunResult, error) {
		return gotest.Result(s, map[string]domain.Status{"TestA": domain.StatusPass}), nil
	}}
	c := gotest.NewCache(fake, 0)
	spec := domain.RunSpec{Package: "p", Count: 1, Timeout: time.Minute}
	_, _ = c.Run(context.Background(), spec)
	spec.Timeout = time.Second
	_, _ = c.Run(context.Background(), spec)
	if fake.CallCount() != 2 {
		t.Fatal("different timeouts reused the same observation")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.Run(ctx, spec); !errors.Is(err, context.Canceled) {
		t.Fatalf("cached result ignored cancellation: %v", err)
	}
}

// A passing run asked again must not reach the real binary: the shuffle sweep
// should be paid once per package, not once per test.
func TestCacheSkipsRepeatedPassingRun(t *testing.T) {
	fake := &gotest.Fake{RunFn: func(s domain.RunSpec) (*domain.RunResult, error) {
		return gotest.Result(s, map[string]domain.Status{"TestA": domain.StatusPass}), nil
	}}
	c := gotest.NewCache(fake, 0)
	ctx := context.Background()
	spec := domain.RunSpec{Package: "p", Count: 1, ShuffleSeed: 7}

	for i := 0; i < 5; i++ {
		if _, err := c.Run(ctx, spec); err != nil {
			t.Fatal(err)
		}
	}
	if fake.CallCount() != 1 {
		t.Fatalf("5 questions should mean 1 run, ran %d", fake.CallCount())
	}
	hits, misses := c.Stats()
	if hits != 4 || misses != 1 {
		t.Errorf("hits=%d misses=%d", hits, misses)
	}
}

// A failing result must NEVER be cached: determinism checking needs a real run.
func TestCacheNeverStoresFailure(t *testing.T) {
	calls := 0
	fake := &gotest.Fake{RunFn: func(s domain.RunSpec) (*domain.RunResult, error) {
		calls++
		return gotest.Result(s, map[string]domain.Status{"TestA": domain.StatusFail}), nil
	}}
	c := gotest.NewCache(fake, 0)
	for i := 0; i < 3; i++ {
		if _, err := c.Run(context.Background(), domain.RunSpec{Package: "p", Count: 1}); err != nil {
			t.Fatal(err)
		}
	}
	if calls != 3 {
		t.Fatalf("a failing run must re-run every time, ran %d", calls)
	}
}

// A different spec is a different question.
func TestCacheKeyIncludesSpec(t *testing.T) {
	fake := &gotest.Fake{RunFn: func(s domain.RunSpec) (*domain.RunResult, error) {
		return gotest.Result(s, map[string]domain.Status{"TestA": domain.StatusPass}), nil
	}}
	c := gotest.NewCache(fake, 0)
	ctx := context.Background()
	_, _ = c.Run(ctx, domain.RunSpec{Package: "p", Count: 1})
	_, _ = c.Run(ctx, domain.RunSpec{Package: "p", Count: 1, Parallel: 1})
	_, _ = c.Run(ctx, domain.RunSpec{Package: "p", Count: 1, ShuffleSeed: 3})
	if fake.CallCount() != 3 {
		t.Fatalf("3 different specs = 3 runs, got %d", fake.CallCount())
	}
}

// Same package + same -run, different witness content = different question.
//
// Without this distinction the PASS of one ddmin subset was served to the
// next, the search eliminated the real polluter and verification said "not
// deterministic".
func TestCacheDistinguishesWitnessSource(t *testing.T) {
	fake := &gotest.Fake{RunFn: func(s domain.RunSpec) (*domain.RunResult, error) {
		return gotest.Result(s, map[string]domain.Status{"TestFlakeWitness": domain.StatusPass}), nil
	}}
	c := gotest.NewCache(fake, 0)
	ctx := context.Background()
	base := domain.RunSpec{Package: "p", RunRegex: "^TestFlakeWitness$", Count: 1, Parallel: 1}

	first := base
	first.Source = "subset-a"
	second := base
	second.Source = "subset-b"

	_, _ = c.Run(ctx, first)
	_, _ = c.Run(ctx, second)
	if fake.CallCount() != 2 {
		t.Fatalf("two different witnesses = 2 runs, got %d", fake.CallCount())
	}
	// Asking for the same witness again must hit the cache.
	_, _ = c.Run(ctx, first)
	if fake.CallCount() != 2 {
		t.Fatalf("the same witness must come from cache, ran %d", fake.CallCount())
	}
}

func TestIndependentVerificationBypassesCachedPass(t *testing.T) {
	fake := &gotest.Fake{RunFn: func(s domain.RunSpec) (*domain.RunResult, error) {
		return gotest.Result(s, map[string]domain.Status{"TestA": domain.StatusPass}), nil
	}}
	c := gotest.NewCache(fake, 0)
	spec := domain.RunSpec{Package: "p", Count: 1}
	if _, err := c.Run(context.Background(), spec); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if _, err := c.Run(domain.FreshRuns(context.Background()), spec); err != nil {
			t.Fatal(err)
		}
	}
	if fake.CallCount() != 4 {
		t.Fatalf("verification reused a pass: %d executions", fake.CallCount())
	}
}
