package harness_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/emrelab/go-flake-hunter/internal/domain"
	"github.com/emrelab/go-flake-hunter/internal/gotest"
	"github.com/emrelab/go-flake-hunter/internal/harness"
	"github.com/emrelab/go-flake-hunter/internal/policy"
)

// fixture copies a fixture module into a temp dir.
//
// The live directory is never used: another test package installs its own
// witness in testdata/fixtures/order-basic, and a cleanup there would delete
// this test's witness between install and run.
func fixture(t *testing.T, name string) string {
	t.Helper()
	src, err := filepath.Abs(filepath.Join("..", "..", "testdata", "fixtures", name))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(src); err != nil {
		t.Fatalf("fixture not found: %s", src)
	}
	dst := t.TempDir()
	err = filepath.WalkDir(src, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(src, path)
		if err != nil || rel == "." {
			return err
		}
		for _, marker := range []string{domain.CapsuleDir, domain.PersistentFileSuffix, domain.WitnessFilePrefix} {
			if strings.Contains(path, marker) {
				if d.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o644)
	})
	if err != nil {
		t.Fatalf("cannot copy fixture: %v", err)
	}
	return dst
}

func TestRenderIsGofmtClean(t *testing.T) {
	steps := []domain.Step{
		{Label: domain.PolluterLabel("TestPolluter"), Test: domain.TestID{Name: "TestPolluter"}},
		{Label: domain.VictimLabel("TestVictim"), Test: domain.TestID{Name: "TestVictim"}},
	}
	src, err := harness.Render("orderbasic", "TestFlakeWitness", steps)
	if err != nil {
		t.Fatal(err)
	}
	s := string(src)
	for _, want := range []string{"package orderbasic", "func TestFlakeWitness(t *testing.T)", `t.Run("polluter/TestPolluter", TestPolluter)`} {
		if !strings.Contains(s, want) {
			t.Errorf("%q missing from generated source:\n%s", want, s)
		}
	}
	if !strings.Contains(s, "DO NOT EDIT") {
		t.Error("generated file must carry the DO NOT EDIT header")
	}
}

func TestRenderRejectsEmptyInput(t *testing.T) {
	if _, err := harness.Render("p", "F", nil); err == nil {
		t.Error("empty step list must be rejected")
	}
	if _, err := harness.Render("", "F", []domain.Step{{Label: "a", Test: domain.TestID{Name: "A"}}}); err == nil {
		t.Error("empty clause must be rejected")
	}
}

// The parallel template wraps only steps that do not call t.Parallel() themselves.
//
// Two bugs are guarded against at once: never using the template (the steps
// stay serial, concurrency cannot be proven) and re-wrapping a calling test
// (testing.T panics on the second call: "t.Parallel called multiple times").
func TestRenderParallelWrapsOnlyWhatNeedsIt(t *testing.T) {
	steps := []domain.Step{
		{Label: "polluter/TestSelf", Test: domain.TestID{Name: "TestSelf"}},                     // calls it itself
		{Label: "victim/TestPlain", Test: domain.TestID{Name: "TestPlain"}, WrapParallel: true}, // does not call it
	}
	src, err := harness.RenderParallel("p", "TestW", steps)
	if err != nil {
		t.Fatal(err)
	}
	s := string(src)
	if got := strings.Count(s, "t.Parallel()"); got != 1 {
		t.Fatalf("t.Parallel() count = %d, want 1:\n%s", got, s)
	}
	if !strings.Contains(s, "TestPlain(t)") || !strings.Contains(s, "TestSelf(t)") {
		t.Fatalf("steps were not generated:\n%s", s)
	}

	serial, err := harness.Render("p", "TestW", steps)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(serial), "t.Parallel()") {
		t.Errorf("serial template must not contain t.Parallel():\n%s", serial)
	}
}

// Real evidence: the generated witness must fail the victim 5/5.
func TestInstalledWitnessReproducesFlake(t *testing.T) {
	dir := fixture(t, "order-basic")
	if _, err := harness.CleanupStale(dir); err != nil {
		t.Fatal(err)
	}

	steps := []domain.Step{
		{Label: domain.PolluterLabel("TestPolluter"), Test: domain.TestID{Name: "TestPolluter"}},
		{Label: domain.VictimLabel("TestVictim"), Test: domain.TestID{Name: "TestVictim"}},
	}

	// Contract: the directory lock is held while the witness file is on disk.
	ctx, unlock, err := policy.Locked(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()

	h, err := harness.Install(dir, "orderbasic", "TestFlakeWitness", steps, "unit")
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := h.Remove(); err != nil {
			t.Errorf("Remove: %v", err)
		}
	}()

	runner := &gotest.Exec{}
	ctx, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()

	// The victim's result arrives under the subtest name: TestFlakeWitness/victim/TestVictim
	subtest := domain.WitnessSubtest("TestFlakeWitness", "TestVictim")

	failures := 0
	for i := 0; i < 5; i++ {
		res, err := runner.Run(ctx, domain.RunSpec{
			Package:  dir,
			RunRegex: "^TestFlakeWitness$",
			Count:    1,
			Parallel: 1,
			Timeout:  60 * time.Second,
		})
		if err != nil {
			t.Fatal(err)
		}
		if res.Failed(subtest) {
			failures++
		}
	}
	if failures != 5 {
		t.Fatalf("witness should have failed 5/5, failed %d/5", failures)
	}
}

func TestRemoveIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	h, err := harness.Install(dir, "p", "TestFlakeWitness",
		[]domain.Step{{Label: "a", Test: domain.TestID{Name: "A"}}}, "idem")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(h.Path()); err != nil {
		t.Fatalf("witness was not written: %v", err)
	}
	for i := 0; i < 3; i++ {
		if err := h.Remove(); err != nil {
			t.Fatalf("Remove #%d: %v", i+1, err)
		}
	}
	if _, err := os.Stat(h.Path()); !os.IsNotExist(err) {
		t.Error("file should have been deleted")
	}
}

func TestCleanupStale(t *testing.T) {
	dir := t.TempDir()
	_, unlock, err := policy.Locked(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	h, err := harness.Install(dir, "p", domain.WitnessFuncName,
		[]domain.Step{{Label: domain.VictimLabel("TestVictim")}}, "old")
	if err != nil {
		t.Fatal(err)
	}
	stale := h.Path() // A killed run leaves the file without calling Remove.
	mine := filepath.Join(dir, "checkout_test.go")
	if err := os.WriteFile(mine, []byte("package p"), 0o644); err != nil {
		t.Fatal(err)
	}

	removed, err := harness.CleanupStale(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(removed) != 1 {
		t.Fatalf("one file should have been deleted, %d deleted: %v", len(removed), removed)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Error("stale witness should have been deleted")
	}
	if _, err := os.Stat(mine); err != nil {
		t.Error("must not touch the user's file")
	}
}

func TestResolveClause(t *testing.T) {
	ok := map[string]string{"TestA": "p", "TestB": "p"}
	if c, err := harness.ResolveClause(ok, "TestB", []string{"TestA"}); err != nil || c != "p" {
		t.Fatalf("clause = %q, err = %v", c, err)
	}

	// foo vs foo_test: not the same binary, must be rejected.
	mixed := map[string]string{"TestInner": "p", "TestOuter": "p_test"}
	if _, err := harness.ResolveClause(mixed, "TestOuter", []string{"TestInner"}); err == nil {
		t.Fatal("clause mismatch must be rejected")
	} else if !strings.Contains(err.Error(), "different test packages") {
		t.Errorf("message must state the reason: %v", err)
	}

	// A name that is not a test.
	if _, err := harness.ResolveClause(ok, "TestYok", nil); err == nil {
		t.Fatal("unknown test must be rejected")
	}
}

func TestFuncNameAvoidsCollision(t *testing.T) {
	if got := harness.FuncName(map[string]string{}); got != domain.WitnessFuncName {
		t.Errorf("base name when there is no collision, got %q", got)
	}
	taken := map[string]string{domain.WitnessFuncName: "p"}
	if got := harness.FuncName(taken); got != domain.WitnessFuncName+"_2" {
		t.Errorf("suffix on collision, got %q", got)
	}
}
