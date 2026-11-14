package clihuntci

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/emrelab/go-flake-hunter/internal/capsule"
	"github.com/emrelab/go-flake-hunter/internal/clitest"
	"github.com/emrelab/go-flake-hunter/internal/domain"
)

// The capsule records the commit it came from and whether the tree was dirty.
// Capsule ids are built from that commit, so a wrong value means two different
// trees can share an id — the artifact's whole claim to being reproducible.
//
// Nothing asserted this explicitly before: the fields were covered only because
// two tests happened to hunt a fixture that lives inside go-flake-hunter's own
// repository, so the values came from whatever the developer's working tree
// looked like at that moment (and went uncovered the day those tests were moved
// onto fixture copies). This pins them in a repository the test owns.
func TestHuntRecordsCapsuleProvenance(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}

	repo := t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
		// The user's global identity and hooks must not affect the test.
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=go-flake-hunter", "GIT_AUTHOR_EMAIL=f@example.com",
			"GIT_COMMITTER_NAME=go-flake-hunter", "GIT_COMMITTER_EMAIL=f@example.com",
			"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	write := func(path, body string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	git("init", "-b", "main")
	write(filepath.Join(repo, "go.mod"), "module fixture/provenance\n\ngo 1.21\n")
	// A real flake is needed: without a confirmed witness there is no capsule.
	flake := filepath.Join(repo, "demo", "accum_test.go")
	write(flake, `package demo

import (
	"sync"
	"testing"
)

var (
	mu   sync.Mutex
	runs int
)

func TestAccumulates(t *testing.T) {
	mu.Lock()
	runs++
	n := runs
	mu.Unlock()
	if n > 1 {
		t.Fatalf("state carried into round %d", n)
	}
}
`)
	git("add", "-A")
	git("commit", "-m", "fixture")
	head := git("rev-parse", "--short", "HEAD")

	// Dirty the tree on purpose: the flag must reflect the state the hunt saw.
	// A non-Go file, so the package still compiles — git counts untracked files
	// as a dirty worktree too.
	write(filepath.Join(repo, "dirty.txt"), "kept dirty by the test\n")

	t.Chdir(repo)
	store := t.TempDir()
	if code, out, errOut := clitest.Run("hunt", "./demo", "-capsule-dir", store, "--format", "json"); code != domain.ExitOK {
		t.Fatalf("hunt code=%d stdout=%s stderr=%s", code, out, errOut)
	}

	ids, err := capsule.NewStore(store).List()
	if err != nil || len(ids) == 0 {
		t.Fatalf("no capsule: %v", err)
	}
	c, err := capsule.NewStore(store).Read(ids[0])
	if err != nil {
		t.Fatal(err)
	}
	if c.Repo.Commit != head {
		t.Errorf("capsule commit = %q, want the short HEAD %q", c.Repo.Commit, head)
	}
	if !c.Repo.Dirty {
		t.Error("capsule says the tree was clean; the test left it dirty on purpose")
	}

	// The same claim has to survive the JSON shape a CI consumer reads.
	data, err := os.ReadFile(filepath.Join(store, ids[0]+".json"))
	if err != nil {
		t.Fatalf("capsule file: %v", err)
	}
	var doc struct {
		Repo struct {
			Commit string `json:"commit"`
			Dirty  bool   `json:"dirty"`
		} `json:"repo"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("capsule is not JSON: %v", err)
	}
	if doc.Repo.Commit != head || !doc.Repo.Dirty {
		t.Errorf("JSON repo = %+v, want commit %q and dirty=true", doc.Repo, head)
	}
}

func TestAuditC13_HuntOutputsDoNotDirtyCheckout(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	dir := clitest.FixtureDir(t, "repeat-accumulate")
	t.Chdir(dir)
	for _, args := range [][]string{{"init"}, {"add", "."}, {"commit", "-m", "fixture"}} {
		cmd := exec.Command("git", append([]string{"-c", "user.name=fixture", "-c", "user.email=fixture@example.com", "-c", "core.hooksPath="}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %s (%v)", args, out, err)
		}
	}
	for i := range 2 {
		code, _, stderr := clitest.Run("hunt", ".")
		if code != 0 {
			t.Fatal(stderr)
		}
		store := capsule.NewStore(domain.CapsuleDir)
		ids, err := store.List()
		if err != nil || len(ids) != 1 {
			t.Fatalf("capsules %v: %v", ids, err)
		}
		c, err := store.Read(ids[0])
		if err != nil || c.Repo.Dirty {
			t.Errorf("run %d: dirty=%t err=%v", i+1, c.Repo.Dirty, err)
		}
	}
}
