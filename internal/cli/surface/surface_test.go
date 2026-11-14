package clisurface

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/emrelab/go-flake-hunter/internal/clitest"
	"github.com/emrelab/go-flake-hunter/internal/domain"
)

func TestVersion(t *testing.T) {
	code, out, _ := clitest.Run("version")
	if code != domain.ExitOK {
		t.Fatalf("code = %d", code)
	}
	if !strings.HasPrefix(out, "go-flake-hunter ") {
		t.Errorf("out = %q", out)
	}
}

// --debug is a global flag: it must be accepted before and after the
// command, and being stripped it must not raise an unknown-flag error.
func TestDebugFlagIsGlobal(t *testing.T) {
	for _, args := range [][]string{{"--debug", "version"}, {"version", "--debug"}} {
		code, out, errOut := clitest.Run(args...)
		if code != domain.ExitOK {
			t.Fatalf("%v: code=%d err=%s", args, code, errOut)
		}
		if !strings.HasPrefix(out, "go-flake-hunter ") {
			t.Errorf("%v: out = %q", args, out)
		}
	}
}

func TestNoArgsIsUsageError(t *testing.T) {
	code, _, errOut := clitest.Run()
	if code != domain.ExitUsage {
		t.Fatalf("code = %d, want %d", code, domain.ExitUsage)
	}
	if !strings.Contains(errOut, "Usage") {
		t.Errorf("usage text missing: %q", errOut)
	}
}

func TestHelpListsPlannedCommands(t *testing.T) {
	code, out, _ := clitest.Run("help")
	if code != domain.ExitOK {
		t.Fatalf("code = %d", code)
	}
	for _, want := range []string{"hunt", "repro", "compile", "replay", "explain", "init"} {
		if !strings.Contains(out, want) {
			t.Errorf("%q missing from help text", want)
		}
	}
}

// TestSubcommandHelpExitsZero: `hunt --help` is a help request, not a usage
// error. If flag.ErrHelp fell into ExitCode, CI would get a 2 (usage error)
// and be misled into thinking the command is broken.
func TestSubcommandHelpExitsZero(t *testing.T) {
	for _, name := range []string{"hunt", "compile", "repro", "replay", "explain", "init"} {
		code, _, errOut := clitest.Run(name, "--help")
		if code != domain.ExitOK {
			t.Errorf("%s --help: code = %d, want %d", name, code, domain.ExitOK)
		}
		if strings.Contains(errOut, "help requested") {
			t.Errorf("%s --help: help request printed as an error: %q", name, errOut)
		}
	}
}

func TestUnknownCommand(t *testing.T) {
	code, _, errOut := clitest.Run("nope")
	if code != domain.ExitUsage {
		t.Fatalf("code = %d", code)
	}
	if !strings.Contains(errOut, "unknown command") {
		t.Errorf("stderr = %q", errOut)
	}
}

// repro without arguments must print usage: silently returning 0 reads as
// "I hunted, it's clean".
func TestReproWithoutArgsIsUsageError(t *testing.T) {
	code, _, errOut := clitest.Run("repro")
	if code != domain.ExitUsage {
		t.Fatalf("code = %d", code)
	}
	if !strings.Contains(errOut, "usage") {
		t.Errorf("stderr = %q", errOut)
	}
}

// A capsule reference must be accepted in three forms: raw id, `ls .go-flake-hunter`
// output (bare file name) and a real path. Users cannot retype what they
// pasted; forcing them to memorize the id format is a product bug. A real repo
// had dropped one form: `replay gfh_x.json` → "capsule not found: gfh_x.json".
func TestCapsuleRefAcceptsIdNameAndPath(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	dir := clitest.FixtureDir(t, "repeat-accumulate")
	store := filepath.Join(t.TempDir(), "c")
	if code, _, errOut := clitest.Run("hunt", dir, "-capsule-dir", store, "--format", "json"); code != domain.ExitOK {
		t.Fatalf("hunt code=%d %s", code, errOut)
	}
	entries, err := os.ReadDir(store)
	if err != nil || len(entries) == 0 {
		t.Fatalf("capsule was not written: %v", err)
	}
	name := entries[0].Name() // gfh_xxx.json
	id := strings.TrimSuffix(name, ".json")

	for _, ref := range []string{id, name, filepath.Join(store, name)} {
		code, out, errOut := clitest.Run("explain", "-capsule-dir", store, ref, "--format", "markdown")
		if code != domain.ExitOK {
			t.Errorf("ref %q: code=%d %s", ref, code, errOut)
			continue
		}
		if !strings.Contains(out, "| class | repeat |") {
			t.Errorf("ref %q: report missing:\n%s", ref, out)
		}
	}
}

// init does not overwrite an existing workflow: a file the team edited must not vanish.
func TestInitIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	if code, _, errOut := clitest.Run("init", dir); code != domain.ExitOK {
		t.Fatalf("code=%d %s", code, errOut)
	}
	path := filepath.Join(dir, ".github", "workflows", "go-flake-hunter.yml")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("workflow was not written: %v", err)
	}
	if err := os.WriteFile(path, append(before, []byte("# added by hand\n")...), 0o644); err != nil {
		t.Fatal(err)
	}

	code, out, _ := clitest.Run("init", dir)
	if code != domain.ExitOK {
		t.Fatalf("second init code=%d", code)
	}
	if !strings.Contains(out, "untouched") {
		t.Errorf("out = %q", out)
	}
	after, _ := os.ReadFile(path)
	if !strings.Contains(string(after), "added by hand") {
		t.Error("init overwrote the user's edit")
	}
}

// explain runs no tests: the report rendered from a capsule must be identical
// even on a machine without `go`.
func TestExplainRerendersCapsule(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	dir := clitest.FixtureDir(t, "repeat-accumulate")
	store := filepath.Join(t.TempDir(), "c")
	if code, _, errOut := clitest.Run("hunt", dir, "-capsule-dir", store, "--format", "json"); code != domain.ExitOK {
		t.Fatalf("hunt code=%d %s", code, errOut)
	}
	code, out, errOut := clitest.Run("explain", "-capsule-dir", store, "--format", "markdown")
	if code != domain.ExitOK {
		t.Fatalf("explain code=%d %s", code, errOut)
	}
	if !strings.Contains(out, "| class | repeat |") {
		t.Errorf("explain output missing:\n%s", out)
	}
	leftovers, _ := filepath.Glob(filepath.Join(dir, "zz_goflakehunter_witness_*"))
	if len(leftovers) > 0 {
		t.Errorf("explain must not run anything, left witness files: %v", leftovers)
	}
}

func TestInitAddsRecordedCapsuleGate(t *testing.T) {
	dir := t.TempDir()
	code, _, errs := clitest.Run("init", dir, "--replay-capsule", ".go-flake-hunter/gfh_123.json")
	if code != 0 {
		t.Fatalf("init %d %s", code, errs)
	}
	data, err := os.ReadFile(filepath.Join(dir, ".github/workflows/go-flake-hunter.yml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"guard recorded flake", `replay ".go-flake-hunter/gfh_123.json" -n 3`, "0/3) echo", `exit "$status"`, "contents: read"} {
		if !strings.Contains(string(data), want) {
			t.Fatalf("missing %q: %s", want, data)
		}
	}
	code, _, _ = clitest.Run("init", t.TempDir(), "--replay-capsule", "../outside.json")
	if code != domain.ExitUsage {
		t.Fatalf("nonportable capsule accepted: %d", code)
	}
}
