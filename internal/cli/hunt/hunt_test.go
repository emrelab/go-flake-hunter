package clihunt

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/emrelab/go-flake-hunter/internal/cli"
	"github.com/emrelab/go-flake-hunter/internal/clitest"
	"github.com/emrelab/go-flake-hunter/internal/domain"
)

// End-to-end: hunt runs on a real fixture and writes a capsule.
// This is the automated form of T5's acceptance criterion.
func TestHuntEndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	// A copy, not the live fixture: a hunt writes witness files into the
	// package and this package now runs beside five others.
	dir := clitest.FixtureDir(t, "repeat-accumulate")
	store := filepath.Join(t.TempDir(), "capsules")

	var out, errOut bytes.Buffer
	code := cli.Execute(context.Background(),
		[]string{"hunt", dir, "-capsule-dir", store},
		cli.IO{Stdout: &out, Stderr: &errOut})
	if code != domain.ExitOK {
		t.Fatalf("code = %d, stderr = %s", code, errOut.String())
	}
	if !strings.Contains(out.String(), "TestAccumulates") {
		t.Errorf("no test name in output: %s", out.String())
	}

	entries, err := os.ReadDir(store)
	if err != nil || len(entries) != 1 {
		t.Fatalf("exactly one capsule should be written: %v (%d files)", err, len(entries))
	}

	// Same command a second time: the SAME file, not a new copy.
	var out2 bytes.Buffer
	if code := cli.Execute(context.Background(),
		[]string{"hunt", dir, "-capsule-dir", store},
		cli.IO{Stdout: &out2, Stderr: &bytes.Buffer{}}); code != domain.ExitOK {
		t.Fatalf("second run code = %d", code)
	}
	entries2, _ := os.ReadDir(store)
	if len(entries2) != 1 {
		t.Fatalf("must be idempotent: %d files", len(entries2))
	}
}

// With no flake: exit 0 and an honest explanation: which probe said what.
//
// "I looked and found nothing" is a successful scan; turning CI red makes
// people remove the tool on day one in a clean repo. A single asked-about
// test going unanswered is `repro`'s problem (exit 3).
//
// The `clean` fixture is deliberately outside all four classes; "not found"
// here is a real result, not a probe gap.
func TestHuntCleanExitsZero(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	dir := clitest.FixtureDir(t, "clean")
	var out, errOut bytes.Buffer
	code := cli.Execute(context.Background(),
		[]string{"hunt", dir, "-capsule-dir", t.TempDir()},
		cli.IO{Stdout: &out, Stderr: &errOut})
	if code != domain.ExitOK {
		t.Fatalf("code = %d, want %d (stdout=%s)", code, domain.ExitOK, out.String())
	}
	if !strings.Contains(out.String(), "flake not found") {
		t.Errorf("no explanation: %s", out.String())
	}
	// All four probes must have been tried: hiding coverage makes missing
	// coverage look like "clean".
	for _, p := range []string{"repeat", "order", "parallel", "env"} {
		if !strings.Contains(out.String(), p) {
			t.Errorf("probe %s not reported: %s", p, out.String())
		}
	}
	// B13: a clean scan is one summary block, not six lines per test. The
	// fixture has two tests; the old shape printed twelve lines.
	if lines := strings.Count(strings.TrimSpace(out.String()), "\n") + 1; lines > 8 {
		t.Errorf("clean scan printed %d lines, want a summary block:\n%s", lines, out.String())
	}
	// -v keeps the per-test detail: one "flake not found" per test.
	out.Reset()
	errOut.Reset()
	code = cli.Execute(context.Background(),
		[]string{"hunt", dir, "-v", "-capsule-dir", t.TempDir()},
		cli.IO{Stdout: &out, Stderr: &errOut})
	if code != domain.ExitOK || strings.Count(out.String(), "flake not found") != 2 {
		t.Errorf("-v lost the per-test explanation (code %d):\n%s", code, out.String())
	}
}

// When the budget runs out: exit 4, a partial capsule is written, no witness
// file remains. If "cut short" and "clean" share one code, CI is lying.
func TestHuntBudgetWritesPartialCapsule(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	// A copy, not the live fixture: a hunt writes witness files into the
	// package and this package now runs beside five others.
	dir := clitest.FixtureDir(t, "repeat-accumulate")
	store := filepath.Join(t.TempDir(), "capsules")

	var out, errOut bytes.Buffer
	code := cli.Execute(context.Background(),
		[]string{"hunt", dir, "-capsule-dir", store, "--budget", "1"},
		cli.IO{Stdout: &out, Stderr: &errOut})
	if code != domain.ExitBudget {
		t.Fatalf("code = %d, want %d (stdout=%s stderr=%s)",
			code, domain.ExitBudget, out.String(), errOut.String())
	}
	if !strings.Contains(out.String(), "status         : partial") {
		t.Errorf("no partial report:\n%s", out.String())
	}

	entries, err := os.ReadDir(store)
	if err != nil || len(entries) != 1 {
		t.Fatalf("expected exactly one partial capsule: %v %v", entries, err)
	}
	data, err := os.ReadFile(filepath.Join(store, entries[0].Name()))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"status": "partial"`) {
		t.Errorf("capsule is not partial:\n%s", data)
	}

	// After a cancel/interruption no witness file may remain in the package.
	leftovers, _ := filepath.Glob(filepath.Join(dir, "zz_goflakehunter_witness_*"))
	if len(leftovers) > 0 {
		t.Errorf("witness files left behind: %v", leftovers)
	}
}

// Markdown output must be a single document: prose mixed in breaks the table
// in the PR comment.
func TestHuntMarkdownIsASingleDocument(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	dir := clitest.FixtureDir(t, "order-basic")
	var out, errOut bytes.Buffer
	code := cli.Execute(context.Background(),
		[]string{"hunt", dir, "-capsule-dir", filepath.Join(t.TempDir(), "c"), "--format", "markdown"},
		cli.IO{Stdout: &out, Stderr: &errOut})
	if code != domain.ExitOK {
		t.Fatalf("code = %d stderr=%s", code, errOut.String())
	}
	s := out.String()
	if !strings.Contains(s, "### flake: TestVictim") || !strings.Contains(s, "| field | value |") {
		t.Fatalf("markdown has no table:\n%s", s)
	}
	if strings.Contains(s, "flake not found") {
		t.Errorf("prose mixed into markdown output:\n%s", s)
	}
}

// --fail-on-flake: the CI gate. A found flake must not pass with code 0.
func TestHuntFailOnFlake(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	dir := clitest.FixtureDir(t, "repeat-accumulate")
	var out, errOut bytes.Buffer
	code := cli.Execute(context.Background(),
		[]string{"hunt", dir, "-capsule-dir", filepath.Join(t.TempDir(), "c"), "--fail-on-flake"},
		cli.IO{Stdout: &out, Stderr: &errOut})
	if code != domain.ExitFlakeFound {
		t.Fatalf("code = %d, want %d", code, domain.ExitFlakeFound)
	}
}

// --race: witness runs happen with -race and the flag is written to the capsule.
//
// Defining the flag in the CLI but wiring it to nothing lets the user say "I
// scanned in race mode"; unless the evidence itself changed, that is a lie.
func TestHuntRaceFlag(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	dir := clitest.FixtureDir(t, "order-basic")
	store := filepath.Join(t.TempDir(), "c")
	var out, errOut bytes.Buffer
	code := cli.Execute(context.Background(),
		[]string{"hunt", dir, "--race", "-capsule-dir", store},
		cli.IO{Stdout: &out, Stderr: &errOut})
	if code != domain.ExitOK {
		t.Fatalf("code = %d stderr=%s", code, errOut.String())
	}
	if !strings.Contains(out.String(), "witness reproduced under -race") {
		t.Errorf("-race confirmation missing from report:\n%s", out.String())
	}

	entries, err := os.ReadDir(store)
	if err != nil || len(entries) != 1 {
		t.Fatalf("expected exactly one capsule: %v %v", entries, err)
	}
	data, err := os.ReadFile(filepath.Join(store, entries[0].Name()))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"race": true`) {
		t.Errorf("capsule does not carry the race flag:\n%s", data)
	}
}

// --format json must be a single document: an empty array on a clean run, two
// elements for two flakes. Consecutive pretty-printed objects are not JSON and
// no consumer can read them.
func TestHuntJSONIsOneDocument(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	parse := func(t *testing.T, out string) map[string]any {
		t.Helper()
		var doc map[string]any
		if err := json.Unmarshal([]byte(out), &doc); err != nil {
			t.Fatalf("invalid JSON: %v\n%s", err, out)
		}
		return doc
	}

	clean := clitest.FixtureDir(t, "clean")
	code, out, _ := clitest.Run("hunt", clean, "--format", "json", "-capsule-dir", t.TempDir())
	if code != domain.ExitOK {
		t.Fatalf("clean hunt code=%d", code)
	}
	doc := parse(t, out)
	flakes, ok := doc["flakes"].([]any)
	if !ok {
		t.Fatalf("flakes is not an array: %s", out)
	}
	if len(flakes) != 0 {
		t.Errorf("flakes must be empty on a clean run: %s", out)
	}

	dirty := clitest.FixtureDir(t, "order-basic")
	code, out, _ = clitest.Run("hunt", dirty, "--format", "json", "-capsule-dir", t.TempDir())
	if code != domain.ExitOK {
		t.Fatalf("hunt with flake code=%d", code)
	}
	doc = parse(t, out)
	flakes, _ = doc["flakes"].([]any)
	if len(flakes) == 0 {
		t.Errorf("flakes must not be empty: %s", out)
	}
}

// --format sarif promises ONE parseable document, and a partial capsule is a
// finding too. The partial path used to print the human summary to stdout and
// only then the SARIF body, so a budget-capped CI run produced a file whose
// first byte was "n", not "{": code scanning rejected the whole upload.
func TestHuntSARIFPartialIsOneDocument(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	dir := clitest.FixtureDir(t, "order-basic")
	code, out, _ := clitest.Run("hunt", dir, "--format", "sarif",
		"-capsule-dir", filepath.Join(t.TempDir(), "c"), "--budget", "1")
	if code != domain.ExitBudget {
		t.Fatalf("code = %d, want %d (budget)", code, domain.ExitBudget)
	}
	var doc map[string]any
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("partial SARIF is not JSON: %v\n%s", err, out)
	}
	if doc["version"] != "2.1.0" {
		t.Errorf("not a SARIF document: %v", doc["version"])
	}
}

// The --sarif side file is written from the same collection as --format json;
// a partial capsule must land in it instead of vanishing, or the two machine
// outputs describe different runs.
func TestHuntSARIFSideFileCarriesThePartialCapsule(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	dir := clitest.FixtureDir(t, "order-basic")
	side := filepath.Join(t.TempDir(), "go-flake-hunter.sarif")
	code, _, errOut := clitest.Run("hunt", dir, "--sarif", side,
		"-capsule-dir", filepath.Join(t.TempDir(), "c"), "--budget", "1")
	if code != domain.ExitBudget {
		t.Fatalf("code = %d, want %d (%s)", code, domain.ExitBudget, errOut)
	}
	data, err := os.ReadFile(side)
	if err != nil {
		t.Fatalf("side file missing: %v", err)
	}
	var doc struct {
		Runs []struct {
			Results []any `json:"results"`
		} `json:"runs"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("side SARIF is not JSON: %v", err)
	}
	if len(doc.Runs) != 1 || len(doc.Runs[0].Results) != 1 {
		t.Errorf("expected the partial capsule as one result, got %+v", doc.Runs)
	}
}
