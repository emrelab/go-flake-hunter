package clihuntevidence

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/emrelab/go-flake-hunter/internal/capsule"
	"github.com/emrelab/go-flake-hunter/internal/cli"
	"github.com/emrelab/go-flake-hunter/internal/clitest"
	"github.com/emrelab/go-flake-hunter/internal/domain"
)

// --race must NOT lower recall.
//
// Measured regression: when -race entered the search, race overhead pushed the
// timing witness down to 1/3 verification and the parallel-race fixture was
// never found with --race (exit 3), while it was found without race (exit 0).
// Since the evidence is collected in a separate run, both commands now find the
// same flake; only the label differs, and that label tells the truth.
func TestHuntRaceKeepsRecall(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	dir := clitest.FixtureDir(t, "parallel-race")
	store := filepath.Join(t.TempDir(), "c")
	var out, errOut bytes.Buffer
	code := cli.Execute(context.Background(),
		[]string{"hunt", dir, "--race", "-capsule-dir", store},
		cli.IO{Stdout: &out, Stderr: &errOut})
	if code != domain.ExitOK {
		t.Fatalf("code = %d stderr=%s (race lowered recall)", code, errOut.String())
	}
	if !strings.Contains(out.String(), "flake found: TestParallelBeta") {
		t.Fatalf("flake not found:\n%s", out.String())
	}

	entries, err := os.ReadDir(store)
	if err != nil || len(entries) != 1 {
		t.Fatalf("expected exactly one capsule: %v %v", entries, err)
	}
	data, err := os.ReadFile(filepath.Join(store, entries[0].Name()))
	if err != nil {
		t.Fatal(err)
	}
	// The label comes from the run's OWN result. This fixture is timing-based:
	// on a loaded machine the race run may fail to reproduce the witness. So the
	// claim is not "true will hold" but "flag and note will not contradict": if
	// it reproduced, race=true and no note; if not, false (the field drops via
	// omitempty) plus a one-line note.
	text := string(data)
	raced := strings.Contains(text, `"race": true`)
	noted := strings.Contains(text, "not reproduced under a race run")
	if raced == noted {
		t.Errorf("race label and note contradict (raced=%t noted=%t):\n%s", raced, noted, text)
	}
}

// hunt --format sarif (#4) end to end: the order fixture's victim must be
// pinned at its real declaration — rule id, file uri and line come from the
// AST scan, and if any of them is missing GitHub shows nothing on the diff.
func TestHuntSARIFPinsVictimOnDiff(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	dirty := clitest.FixtureDir(t, "order-basic")
	code, out, errOut := clitest.Run("hunt", dirty, "--format", "sarif", "-capsule-dir", t.TempDir())
	if code != domain.ExitOK {
		t.Fatalf("code=%d\n%s", code, errOut)
	}

	var doc struct {
		Version string `json:"version"`
		Runs    []struct {
			Results []struct {
				RuleID  string `json:"ruleId"`
				Message struct {
					Text string `json:"text"`
				} `json:"message"`
				Locations []struct {
					Physical struct {
						Artifact struct {
							URI string `json:"uri"`
						} `json:"artifactLocation"`
						Region struct {
							StartLine int `json:"startLine"`
						} `json:"region"`
					} `json:"physicalLocation"`
				} `json:"locations"`
			} `json:"results"`
		} `json:"runs"`
	}
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("sarif output is not JSON: %v\n%s", err, out)
	}
	if doc.Version != "2.1.0" || len(doc.Runs) != 1 || len(doc.Runs[0].Results) != 1 {
		t.Fatalf("unexpected document: %s", out)
	}
	res := doc.Runs[0].Results[0]
	if res.RuleID != "go-flake-hunter/order" {
		t.Errorf("ruleId = %q", res.RuleID)
	}
	if len(res.Locations) != 1 {
		t.Fatalf("victim must be pinned, got %s", out)
	}
	phys := res.Locations[0].Physical
	if !strings.HasSuffix(phys.Artifact.URI, "_test.go") {
		t.Errorf("uri = %q, want a *_test.go path", phys.Artifact.URI)
	}
	if phys.Region.StartLine < 1 {
		t.Errorf("startLine = %d", phys.Region.StartLine)
	}
}

// Cross-binary pollution (#6) end to end: package a dirties a file, package
// b's victim fails only after a ran. hunt must find the witness across the
// two `go test` binaries (when --polluter-pkg names the suspect), shrink away
// the innocent TestNoop, refuse to compile the capsule to a single test file,
// and replay the two-stage command deterministically.
func TestHuntCrossBinaryEndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	dir := clitest.FixtureDir(t, "cross-package")
	a := filepath.Join(dir, "a")
	b := filepath.Join(dir, "b")
	store := filepath.Join(t.TempDir(), "c")

	code, out, errOut := clitest.Run("hunt", b, "--polluter-pkg", a,
		"-capsule-dir", store, "--timeout", "150s", "--budget", "800")
	if code != domain.ExitOK {
		t.Fatalf("hunt code=%d\n%s\n%s", code, out, errOut)
	}
	if !strings.Contains(out, "cross-binary") {
		t.Errorf("the report must say cross-binary:\n%s", out)
	}

	ids, err := capsule.NewStore(store).List()
	if err != nil || len(ids) == 0 {
		t.Fatalf("no capsule: %v", err)
	}
	c, err := capsule.NewStore(store).Read(ids[0])
	if err != nil {
		t.Fatal(err)
	}
	if !c.Witness.CrossBinary || c.Kind != domain.KindOrder {
		t.Fatalf("capsule = %s cross=%v", c.Kind, c.Witness.CrossBinary)
	}
	if len(c.Witness.Polluters) != 1 || c.Witness.Polluters[0].Name != "TestBad" {
		t.Errorf("polluters = %v, want [TestBad] only", c.Witness.Polluters)
	}
	if !strings.Contains(c.Repro.Command, "go-flake-hunter replay") || !strings.Contains(c.Repro.Command, "--polluter-pkg") {
		t.Errorf("the capsule command must be the two-stage reproduction: %q", c.Repro.Command)
	}

	// compile must refuse (exit 5) and hand back the runnable command.
	code, _, errOut = clitest.Run("compile", "-capsule-dir", store, "--pkg", b)
	if code != domain.ExitUnsupported {
		t.Fatalf("compile code=%d want %d (%s)", code, domain.ExitUnsupported, errOut)
	}
	if !strings.Contains(errOut, "reproduces with: go-flake-hunter replay") {
		t.Errorf("the refusal must carry the command: %q", errOut)
	}

	// replay runs the two stages and must stay deterministic.
	code, out, errOut = clitest.Run("replay", "-capsule-dir", store, "--pkg", b,
		"--polluter-pkg", a, "-n", "3")
	if code != domain.ExitOK {
		t.Fatalf("replay code=%d\n%s\n%s", code, out, errOut)
	}
	if !strings.Contains(out, "deterministic") || !strings.Contains(out, "3/3") {
		t.Errorf("replay output:\n%s", out)
	}
}

// Without --polluter-pkg the foreign-package flake stays invisible — and hunt
// must say "no flake" honestly (exit 0 for a scan), never invent a witness in
// the victim's own binary. The suspect list is the user's, the engine is ours.
func TestHuntCrossBinaryNeedsSuspect(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	dir := clitest.FixtureDir(t, "cross-package")
	b := filepath.Join(dir, "b")
	code, _, _ := clitest.Run("hunt", b, "-capsule-dir", t.TempDir(), "--timeout", "120s", "--budget", "600")
	if code != domain.ExitOK {
		t.Fatalf("a scan that finds nothing is exit 0, got %d", code)
	}
}

func TestCrossClauseSharedMemoryLifecycle(t *testing.T) {
	if testing.Short() {
		t.Skip("integration")
	}
	dir := clitest.FixtureDir(t, "clause-mismatch")
	store := t.TempDir()
	code, out, errs := clitest.Run("repro", dir, "TestOuterVictim", "--capsule-dir", store, "--budget", "150")
	if code != 0 {
		t.Fatalf("repro %d %s %s", code, out, errs)
	}
	ids, err := capsule.NewStore(store).List()
	if err != nil || len(ids) != 1 {
		t.Fatalf("ids=%v %v", ids, err)
	}
	c, err := capsule.NewStore(store).Read(ids[0])
	if err != nil {
		t.Fatal(err)
	}
	if !c.Witness.CrossClause || c.Witness.CrossBinary || c.Status != domain.StatusConfirmed || strings.Contains(c.Repro.Command, "&&") {
		t.Fatalf("capsule=%+v", c)
	}
	code, out, errs = clitest.Run("replay", ids[0], "--pkg", dir, "--capsule-dir", store, "-n", "3")
	if code != 0 || !strings.Contains(out, "3/3") {
		t.Fatalf("replay %d %s %s", code, out, errs)
	}
	code, _, errs = clitest.Run("compile", ids[0], "--pkg", dir, "--capsule-dir", store)
	if code != domain.ExitUnsupported || !strings.Contains(errs, "go-flake-hunter replay") {
		t.Fatalf("compile %d %s", code, errs)
	}
}

func TestApplicationGlobalsWitnessRecallMatrix(t *testing.T) {
	if testing.Short() {
		t.Skip("integration")
	}
	root := clitest.FixtureDir(t, "app-globals")
	for _, shape := range []string{"direct-read", "direct-write", "method-write"} {
		t.Run(shape, func(t *testing.T) {
			store := t.TempDir()
			dir := filepath.Join(root, shape)
			code, out, errs := clitest.Run("repro", dir, "TestVictim", "--capsule-dir", store, "--budget", "100")
			if code != 0 {
				t.Fatalf("repro %d %s %s", code, out, errs)
			}
			ids, err := capsule.NewStore(store).List()
			if err != nil || len(ids) != 1 {
				t.Fatalf("capsules=%v %v", ids, err)
			}
			c, err := capsule.NewStore(store).Read(ids[0])
			if err != nil {
				t.Fatal(err)
			}
			if c.Status != domain.StatusConfirmed || len(c.Witness.Polluters) != 1 || c.Witness.Polluters[0].Name != "TestPolluter" {
				t.Fatalf("witness=%+v", c)
			}
			if len(c.Evidence.GlobalsWritten) != 0 || len(c.Evidence.GlobalsRead) != 0 {
				t.Fatalf("unexpected evidence=%+v", c.Evidence)
			}
		})
	}
}
