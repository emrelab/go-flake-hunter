package clihuntci

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/emrelab/go-flake-hunter/internal/capsule"
	"github.com/emrelab/go-flake-hunter/internal/clitest"
	"github.com/emrelab/go-flake-hunter/internal/domain"
)

// The baseline's whole promise: a known flake does not break the gate, a new
// one does. Both in one test, because a lone "green" gate is ambiguous — it
// may mean a real flake was seen or that nothing at all was found.
func TestHuntBaselineGatesOnNewFlakesOnly(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	dir := clitest.FixtureDir(t, "order-basic")
	// The default baseline path is relative to the invoked dir: the test's cwd
	// must be temporary so nothing is written into the repo or testdata.
	work := t.TempDir()
	t.Chdir(work)
	store := filepath.Join(t.TempDir(), "c")

	code, _, stderr := clitest.Run("hunt", dir, "--capsule-dir", store, "--write-baseline")
	if code != domain.ExitOK {
		t.Fatalf("--write-baseline: code = %d (stderr=%s)", code, stderr)
	}
	path := filepath.Join(work, domain.CapsuleDir, capsule.BaselineFileName)
	b, err := capsule.LoadBaseline(path)
	if err != nil {
		t.Fatalf("baseline was not written (%s): %v", path, err)
	}
	if len(b.IDs) != 1 {
		t.Fatalf("baseline carries %d ids, expected 1: %v", len(b.IDs), b.IDs)
	}

	// The baseline shares the capsule directory in ordinary use; it must never
	// become the newest "capsule" selected by compile/replay.
	if err := capsule.WriteBaseline(filepath.Join(store, capsule.BaselineFileName), b.IDs); err != nil {
		t.Fatal(err)
	}
	if code, _, stderr := clitest.Run("compile", "--capsule-dir", store, "--pkg", dir, "--dry-run"); code != domain.ExitOK {
		t.Fatalf("baseline masked newest capsule: %d %s", code, stderr)
	}

	// 1) Known flake: the gate is green, but the flake is still reported.
	code, stdout, stderr := clitest.Run("hunt", dir, "--capsule-dir", store,
		"--baseline", path, "--fail-on-flake")
	if code != domain.ExitOK {
		t.Fatalf("known flake broke the gate: code = %d (stderr=%s)", code, stderr)
	}
	if !strings.Contains(stderr, "known flake: gfh_") {
		t.Errorf("known flake was not reported:\n%s", stderr)
	}
	if !strings.Contains(stderr, "baseline: 1 known, 0 new") {
		t.Errorf("summary line missing:\n%s", stderr)
	}
	if !strings.Contains(stdout, "TestVictim") {
		t.Errorf("found flake report disappeared:\n%s", stdout)
	}

	// 2) An id outside the baseline: the gate is red (exit 1) and the flake is
	// not counted "known". A one-line baseline is enough here — what is really
	// measured is the direction of the id comparison.
	foreign := filepath.Join(t.TempDir(), "baseline.json")
	if err := capsule.WriteBaseline(foreign, []string{"gfh_000000000000"}); err != nil {
		t.Fatal(err)
	}
	code, _, stderr = clitest.Run("hunt", dir, "--capsule-dir", store,
		"--baseline", foreign, "--fail-on-flake")
	if code != domain.ExitFlakeFound {
		t.Fatalf("new flake did not break the gate: code = %d, want %d (stderr=%s)",
			code, domain.ExitFlakeFound, stderr)
	}
	if strings.Contains(stderr, "known flake") {
		t.Errorf("new flake counted as known:\n%s", stderr)
	}
}

// A missing baseline file is never guessed: if the user made a typo, a
// "clean" result would hide that nothing was compared.
func TestHuntMissingBaselineIsUsageError(t *testing.T) {
	dir := clitest.FixtureDir(t, "clean")
	code, _, stderr := clitest.Run("hunt", dir, "--baseline", filepath.Join(t.TempDir(), "missing.json"))
	if code != domain.ExitUsage {
		t.Fatalf("code = %d, want %d (stderr=%s)", code, domain.ExitUsage, stderr)
	}
	if !strings.Contains(stderr, "baseline not found") {
		t.Errorf("no explanation: %q", stderr)
	}
}

func TestBaselineSurvivesUnrelatedCommit(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	dir := clitest.FixtureDir(t, "order-basic")
	t.Chdir(dir)
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-c", "user.name=go-flake-hunter", "-c", "user.email=f@example.com", "-c", "core.hooksPath="}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git("init", "-b", "main")
	git("add", "go.mod", "checkout_test.go")
	git("commit", "-m", "initial")
	store := t.TempDir()
	baseline := filepath.Join(t.TempDir(), "baseline.json")
	if code, _, stderr := clitest.Run("hunt", ".", "--capsule-dir", store, "--write-baseline", "--baseline", baseline); code != 0 {
		t.Fatalf("baseline: %d %s", code, stderr)
	}
	ids, _ := capsule.NewStore(store).List()
	if len(ids) != 1 {
		t.Fatalf("capsules = %v", ids)
	}
	before, err := capsule.NewStore(store).Read(ids[0])
	if err != nil {
		t.Fatal(err)
	}
	git("commit", "--allow-empty", "-m", "unrelated change")
	code, stdout, stderr := clitest.Run("hunt", ".", "--capsule-dir", store, "--baseline", baseline, "--fail-on-flake")
	if code != 0 || !strings.Contains(stderr, "baseline: 1 known, 0 new") || !strings.Contains(stdout, "TestVictim") {
		t.Fatalf("known defect after commit: %d\n%s\n%s", code, stdout, stderr)
	}
	ids, _ = capsule.NewStore(store).List()
	if len(ids) != 2 {
		t.Fatalf("commit-specific evidence lost: %v", ids)
	}
	after, err := capsule.NewStore(store).Read(ids[0])
	if err != nil {
		t.Fatal(err)
	}
	if before.Repo.Commit == after.Repo.Commit || before.ID == after.ID {
		t.Fatal("new commit provenance not preserved")
	}
	// A second victim must still break the gate.
	src, err := os.ReadFile("checkout_test.go")
	if err != nil {
		t.Fatal(err)
	}
	src = append(src, []byte("\nfunc TestNewVictim(t *testing.T) { TestVictim(t) }\n")...)
	if err := os.WriteFile("checkout_test.go", src, 0o644); err != nil {
		t.Fatal(err)
	}
	code, _, stderr = clitest.Run("hunt", ".", "--capsule-dir", store, "--baseline", baseline, "--fail-on-flake")
	if code != domain.ExitFlakeFound || !strings.Contains(stderr, "baseline: 1 known, 1 new") {
		t.Fatalf("new defect did not trip gate: %d %s", code, stderr)
	}
}

func TestAuditC12_DefaultBaselineAppliesToGate(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	dir := clitest.FixtureDir(t, "repeat-accumulate")
	t.Chdir(dir)
	if code, _, stderr := clitest.Run("hunt", "--write-baseline"); code != 0 {
		t.Fatalf("write exit=%d %s", code, stderr)
	}
	if code, _, stderr := clitest.Run("hunt", "--fail-on-flake"); code != 0 || !strings.Contains(stderr, "baseline: 1 known, 0 new") {
		t.Fatalf("gate exit=%d %s", code, stderr)
	}
}

func TestAuditC14_ConcurrentBaselineUpdatesMerge(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	base := filepath.Join(t.TempDir(), "baseline.json")
	dirs := []string{clitest.FixtureDir(t, "repeat-accumulate"), clitest.FixtureDir(t, "repeat-accumulate")}
	if err := os.WriteFile(filepath.Join(dirs[1], "go.mod"), []byte("module fixture/other\n\ngo 1.24\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, dir := range dirs {
		t.Run(filepath.Base(filepath.Dir(dir))+filepath.Base(dir), func(t *testing.T) {
			t.Parallel()
			if code, _, stderr := clitest.Run("hunt", dir, "--write-baseline", "--baseline", base); code != 0 {
				t.Errorf("write exit=%d %s", code, stderr)
			}
		})
	}
	t.Cleanup(func() {
		b, err := capsule.LoadBaseline(base)
		if err != nil || len(b.IDs) != 2 {
			t.Errorf("baseline ids=%v err=%v", b.IDs, err)
		}
	})
}
