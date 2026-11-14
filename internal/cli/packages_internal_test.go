package cli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/emrelab/go-flake-hunter/internal/domain"
	"github.com/emrelab/go-flake-hunter/internal/gotest"
	"github.com/emrelab/go-flake-hunter/internal/policy"
	"github.com/emrelab/go-flake-hunter/internal/probe"
)

type scanProbe struct {
	fn func(context.Context, probe.Target, gotest.Runner) (*domain.Witness, error)
}

func (p scanProbe) Name() string      { return "scripted" }
func (p scanProbe) Kind() domain.Kind { return domain.KindOrder }
func (p scanProbe) Probe(ctx context.Context, t probe.Target, r gotest.Runner) (*domain.Witness, error) {
	return p.fn(ctx, t, r)
}

func packageDirs(t *testing.T) []string {
	t.Helper()
	return []string{testModule(t), testModule(t)}
}

func testModule(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for name, src := range map[string]string{
		"go.mod": "module fixture/scanner\n\ngo 1.24\n",
		"p.go":   "package p\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(src), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}
func testScanner(p probe.Probe, r gotest.Runner) *scanner {
	return &scanner{o: huntOptions{format: "json"}, out: IO{Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}}, registry: probe.NewRegistry(p), runner: r, budget: policy.New(100, time.Minute)}
}
func TestPackagesOverlapAndDeduplicate(t *testing.T) {
	dirs := packageDirs(t)
	entered := make(chan struct{}, 2)
	release := make(chan struct{})
	var calls atomic.Int32
	p := scanProbe{fn: func(ctx context.Context, _ probe.Target, _ gotest.Runner) (*domain.Witness, error) {
		calls.Add(1)
		entered <- struct{}{}
		select {
		case <-release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		return nil, probe.ErrNotReproduced
	}}
	r := &gotest.Fake{DiscoverFn: func(d string) ([]domain.TestID, error) { return []domain.TestID{{Package: d, Name: "TestPlain"}}, nil }}
	s := testScanner(p, r)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- s.packages(ctx, []string{dirs[0], dirs[1], dirs[0]}, 2) }()
	for i := 0; i < 2; i++ {
		select {
		case <-entered:
		case <-ctx.Done():
			t.Fatal("packages did not overlap")
		}
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 {
		t.Fatalf("duplicate directory scanned: %d", calls.Load())
	}
}
func TestPackagesShareBudgetAndPreservePartial(t *testing.T) {
	dirs := packageDirs(t)
	fake := &gotest.Fake{DiscoverFn: func(d string) ([]domain.TestID, error) { return []domain.TestID{{Package: d, Name: "TestPlain"}}, nil }}
	p := scanProbe{fn: func(ctx context.Context, target probe.Target, r gotest.Runner) (*domain.Witness, error) {
		_, err := r.Run(ctx, domain.RunSpec{Package: target.Dir, Count: 1})
		if err != nil {
			return nil, err
		}
		return nil, probe.ErrNotReproduced
	}}
	s := testScanner(p, fake)
	s.budget = policy.New(1, time.Minute)
	s.runner = gotest.Budgeted{Inner: fake, Budget: s.budget}
	if err := s.packages(context.Background(), dirs, 2); !errors.Is(err, domain.ErrBudgetExceeded) {
		t.Fatalf("err=%v", err)
	}
	if s.budget.Runs() != 1 || fake.CallCount() != 1 {
		t.Fatalf("budget=%d executions=%d", s.budget.Runs(), fake.CallCount())
	}
	if len(s.collected) != 1 || s.collected[0].Status != domain.StatusPartial {
		t.Fatalf("partial=%+v", s.collected)
	}
}
func TestSuspectedDoesNotEnterBaselineOrGate(t *testing.T) {
	dir := testModule(t)
	o := domain.Observe(3, 2)
	p := scanProbe{fn: func(_ context.Context, target probe.Target, _ gotest.Runner) (*domain.Witness, error) {
		return &domain.Witness{Kind: domain.KindOrder, Victim: target.Test, Polluters: []domain.TestID{{Name: "TestPolluter"}}, Runs: 3, Verification: &o}, domain.ErrNotReproducible
	}}
	r := &gotest.Fake{DiscoverFn: func(d string) ([]domain.TestID, error) {
		return []domain.TestID{{Package: "p", Name: "TestVictim"}}, nil
	}}
	s := testScanner(p, r)
	s.o.dir = t.TempDir()
	s.o.failOnFlake = true
	if err := s.pkg(context.Background(), dir); err != nil {
		t.Fatal(err)
	}
	if len(s.collected) != 1 || s.collected[0].Status != domain.StatusSuspected || s.collected[0].Observation.Failures != 2 {
		t.Fatalf("capsules=%+v", s.collected)
	}
	if s.found != 0 || s.fresh != 0 || len(s.ids) != 0 || s.outcome(false) != nil || !errors.Is(s.outcome(true), domain.ErrNotReproduced) {
		t.Fatalf("suspected promoted to confirmed: %+v", s)
	}
}

func TestPackagesKeepInputOrderAndSerializeWhenJobsOne(t *testing.T) {
	dirs := packageDirs(t)
	var active, maxActive atomic.Int32
	p := scanProbe{fn: func(_ context.Context, target probe.Target, _ gotest.Runner) (*domain.Witness, error) {
		n := active.Add(1)
		for old := maxActive.Load(); n > old; old = maxActive.Load() {
			if maxActive.CompareAndSwap(old, n) {
				break
			}
		}
		defer active.Add(-1)
		return &domain.Witness{Kind: domain.KindOrder, Victim: target.Test, Polluters: []domain.TestID{{Name: "TestPolluter"}}, Runs: 3}, nil
	}}
	r := &gotest.Fake{DiscoverFn: func(d string) ([]domain.TestID, error) { return []domain.TestID{{Package: d, Name: "TestVictim"}}, nil }}
	for _, jobs := range []int{1, 2} {
		s := testScanner(p, r)
		s.o.dir = t.TempDir()
		if err := s.packages(context.Background(), dirs, jobs); err != nil {
			t.Fatal(err)
		}
		if len(s.collected) != 2 || s.collected[0].Target.Package != dirs[0] || s.collected[1].Target.Package != dirs[1] {
			t.Fatalf("reports reordered: %+v", s.collected)
		}
		if jobs == 1 && maxActive.Load() != 1 {
			t.Fatalf("jobs=1 overlapped %d probes", maxActive.Load())
		}
	}
}

func TestDependencyHintArrivesBeforeProbeUnderScheduler(t *testing.T) {
	dir := testModule(t)
	if err := os.WriteFile(filepath.Join(dir, "app.go"), []byte("package p\nimport \"time\"\nfunc clock(){time.Now()}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	var diagnostics bytes.Buffer
	p := scanProbe{fn: func(_ context.Context, _ probe.Target, _ gotest.Runner) (*domain.Witness, error) {
		if diagnostics.Len() == 0 {
			t.Error("dependency hint was buffered until after probing")
		}
		return nil, probe.ErrNotReproduced
	}}
	r := &gotest.Fake{DiscoverFn: func(string) ([]domain.TestID, error) { return []domain.TestID{{Name: "TestPlain"}}, nil }}
	s := testScanner(p, r)
	s.out.Stderr = &diagnostics
	if err := s.packages(context.Background(), []string{dir}, 2); err != nil {
		t.Fatal(err)
	}
}
