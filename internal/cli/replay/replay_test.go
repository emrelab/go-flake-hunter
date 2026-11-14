package clireplay

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/emrelab/go-flake-hunter/internal/capsule"
	"github.com/emrelab/go-flake-hunter/internal/cli"
	"github.com/emrelab/go-flake-hunter/internal/clitest"
	"github.com/emrelab/go-flake-hunter/internal/domain"
)

// replay: re-runs the witness. For env flakes the value is not in the capsule,
// so it must honestly refuse without --env.
func TestReplayRefusesEnvCapsuleWithoutValue(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	dir := clitest.FixtureDir(t, "env-dependent")
	store := filepath.Join(t.TempDir(), "c")
	if code, _, errOut := clitest.Run("hunt", dir, "-capsule-dir", store, "--format", "json"); code != domain.ExitOK {
		t.Fatalf("hunt code=%d %s", code, errOut)
	}
	code, _, errOut := clitest.Run("replay", "-capsule-dir", store, "--pkg", dir)
	if code != domain.ExitUsage {
		t.Fatalf("code = %d, want %d (%s)", code, domain.ExitUsage, errOut)
	}
	if !strings.Contains(errOut, "--env") {
		t.Errorf("should name the fix: %q", errOut)
	}
}

// Replay path per class: repeat runs with -count, env with --env, order and
// parallel with the witness chain. If all three collapse to one code, the
// "deterministic" report is unevidenced.
func TestReplayKindPaths(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	cases := []struct {
		fixture string
		env     string
	}{
		{"order-basic", ""},
		{"repeat-accumulate", ""},
		{"env-dependent", "FIXTURE_MODE=ci"},
	}
	for _, tc := range cases {
		t.Run(tc.fixture, func(t *testing.T) {
			dir := clitest.FixtureDir(t, tc.fixture)
			store := filepath.Join(t.TempDir(), "c")
			if code, _, errOut := clitest.Run("hunt", dir, "-capsule-dir", store, "--format", "json"); code != domain.ExitOK {
				t.Fatalf("hunt code=%d %s", code, errOut)
			}
			args := []string{"replay", "-capsule-dir", store, "--pkg", dir, "-n", "2"}
			if tc.env != "" {
				args = append(args, "--env", tc.env)
			}
			code, out, errOut := clitest.Run(args...)
			if code != domain.ExitOK {
				t.Fatalf("replay code=%d %s\n%s", code, errOut, out)
			}
			if !strings.Contains(out, "2/2 reproduced") || !strings.Contains(out, "deterministic") {
				t.Errorf("expected a deterministic report:\n%s", out)
			}
		})
	}
}

// Unusable capsules must be refused without running: a half-written capsule
// has no verified witness, broken JSON has no fields at all, and -n 0 asks no
// question. In all three cases the only right answer is a usage error.
func TestReplayRejectsUnusableCapsule(t *testing.T) {
	store := t.TempDir()
	confirmed := func(id string) domain.Capsule {
		return domain.Capsule{
			Schema: domain.SchemaVersion,
			ID:     id,
			Kind:   domain.KindRepeat,
			Witness: domain.Witness{
				Kind:   domain.KindRepeat,
				Victim: domain.TestID{Name: "TestAccumulates", Package: "example.com/m"},
				Runs:   2,
			},
			Target: domain.Target{Package: "example.com/m", Victim: "TestAccumulates"},
			Status: domain.StatusConfirmed,
		}
	}
	partial := confirmed("gfh_partial")
	partial.Status = domain.StatusPartial
	partial.Note = "budget exceeded: 1/1 runs"
	if _, err := capsule.NewStore(store).Write(partial); err != nil {
		t.Fatalf("could not write capsule: %v", err)
	}
	if _, err := capsule.NewStore(store).Write(confirmed("gfh_ok")); err != nil {
		t.Fatalf("could not write capsule: %v", err)
	}
	if err := os.WriteFile(filepath.Join(store, "gfh_broken.json"), []byte("broken"), 0o644); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name string
		args []string
		want string
	}{
		{"two arguments", []string{"replay", "a", "b"}, "at most one capsule"},
		{"partial capsule", []string{"replay", "gfh_partial", "-capsule-dir", store}, "partial capsule"},
		{"broken json", []string{"replay", "gfh_broken", "-capsule-dir", store}, "capsule is corrupt"},
		{"zero runs", []string{"replay", "gfh_ok", "-capsule-dir", store, "-n", "0"}, "-n must be positive"},
		{"negative runs", []string{"replay", "gfh_ok", "-capsule-dir", store, "-n", "-3"}, "-n must be positive"},
		{"missing capsule", []string{"replay", "gfh_yok", "-capsule-dir", store}, "capsule not found"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, _, errOut := clitest.Run(tc.args...)
			if code != domain.ExitUsage {
				t.Fatalf("code = %d, want %d (%s)", code, domain.ExitUsage, errOut)
			}
			if !strings.Contains(errOut, tc.want) {
				t.Errorf("stderr %q should contain %q", errOut, tc.want)
			}
		})
	}
}

// repro targets one test: no need for hunt to scan the whole package, the
// budget goes to the single suspect.
func TestReproTargetsOneTest(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	dir := clitest.FixtureDir(t, "order-basic")
	var out, errOut bytes.Buffer
	code := cli.Execute(context.Background(),
		[]string{"repro", dir, "TestVictim", "-capsule-dir", filepath.Join(t.TempDir(), "c")},
		cli.IO{Stdout: &out, Stderr: &errOut})
	if code != domain.ExitOK {
		t.Fatalf("code = %d stderr=%s", code, errOut.String())
	}
	if !strings.Contains(out.String(), "polluter     : TestPolluter") {
		t.Errorf("witness not narrowed:\n%s", out.String())
	}
}

// repro answers a single question: no answer means exit 3. That is the difference from hunt.
func TestReproNotReproducedExitsThree(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	dir := clitest.FixtureDir(t, "clean")
	code, out, _ := clitest.Run("repro", dir, "TestSum", "-capsule-dir", t.TempDir())
	if code != domain.ExitNotReproduced {
		t.Fatalf("code = %d, want %d\n%s", code, domain.ExitNotReproduced, out)
	}
}

// repro --from-json (#5): the CI log already paid for the discovery; repro owes
// only the witness. The env-dependent fixture is the target of a hand-written
// failure log, and the import path in that log must resolve to the fixture
// through `go list` — which is why the command must run inside the repo the
// log came from (t.Chdir here is the CI checkout).
func TestReproFromJSONLog(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	dir := clitest.FixtureDir(t, "env-dependent")
	t.Chdir(dir)

	log := `{"Action":"start","Package":"fixture/envdependent"}
{"Action":"run","Package":"fixture/envdependent","Test":"TestDependsOnEnv"}
{"Action":"fail","Package":"fixture/envdependent","Test":"TestDependsOnEnv"}
`
	logFile := filepath.Join(t.TempDir(), "failure.json")
	if err := os.WriteFile(logFile, []byte(log), 0o644); err != nil {
		t.Fatal(err)
	}
	store := filepath.Join(t.TempDir(), "c")

	code, out, errOut := clitest.Run("repro", "--from-json", logFile,
		"-capsule-dir", store, "--timeout", "150s", "--budget", "400")
	if code != domain.ExitOK {
		t.Fatalf("code=%d\n%s\n%s", code, out, errOut)
	}
	ids, err := capsule.NewStore(store).List()
	if err != nil || len(ids) == 0 {
		t.Fatalf("no capsule written: %v", err)
	}
	c, err := capsule.NewStore(store).Read(ids[0])
	if err != nil {
		t.Fatal(err)
	}
	if c.Kind != domain.KindEnv || c.Target.Victim != "TestDependsOnEnv" {
		t.Errorf("capsule = %s/%s, want env/TestDependsOnEnv", c.Kind, c.Target.Victim)
	}
}

// A failing log with a package this repo does not have is a warning, not a
// veto; a log with no failures says so honestly with exit 3.
func TestReproFromJSONEdgeCases(t *testing.T) {
	write := func(t *testing.T, body string) string {
		t.Helper()
		f := filepath.Join(t.TempDir(), "failure.json")
		if err := os.WriteFile(f, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return f
	}

	f := write(t, `{"Action":"pass","Package":"p","Test":"TestGreen"}`+"\n")
	if code, _, errOut := clitest.Run("repro", "--from-json", f); code != domain.ExitNotReproduced {
		t.Errorf("clean log: code=%d want %d (%s)", code, domain.ExitNotReproduced, errOut)
	}

	f = write(t, "this is not a test2json log\nat all\n")
	if code, _, _ := clitest.Run("repro", "--from-json", f); code != domain.ExitUsage {
		t.Errorf("garbage log: code=%d want %d", code, domain.ExitUsage)
	}

	// Unknown package: warn and continue; with no other suspects the honest
	// answer is still "not reproduced" (exit 3), not a crash.
	f = write(t, `{"Action":"fail","Package":"example.com/not/here","Test":"TestGone"}`+"\n")
	code, _, errOut := clitest.Run("repro", "--from-json", f)
	if code != domain.ExitNotReproduced {
		t.Errorf("foreign package: code=%d want %d (%s)", code, domain.ExitNotReproduced, errOut)
	}
	if !strings.Contains(errOut, "cannot be resolved") {
		t.Errorf("the unresolvable package must be named: %q", errOut)
	}
}

// A package that already defines TestFlakeWitness forces the hunt to pick the
// NEXT free name. Replay must install that same name: recomputing from a
// constant collides with the user's test, the package stops compiling, and the
// run reads as "0/2 reproduced — the flake may already be fixed". That is the
// worst answer this command can give, because the user then deletes the capsule.
func TestReplayUsesTheWitnessNameFromTheCapsule(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	dir := clitest.FixtureDir(t, "order-basic")
	user := filepath.Join(dir, "collision_test.go")
	if err := os.WriteFile(user, []byte(`package orderbasic

import "testing"

// The user's own test, named exactly what the witness would like to be.
func TestFlakeWitness(t *testing.T) {}
`), 0o644); err != nil {
		t.Fatal(err)
	}

	store := filepath.Join(t.TempDir(), "c")
	if code, _, errOut := clitest.Run("hunt", dir, "-capsule-dir", store, "--format", "json"); code != domain.ExitOK {
		t.Fatalf("hunt code=%d %s", code, errOut)
	}
	ids, err := capsule.NewStore(store).List()
	if err != nil || len(ids) == 0 {
		t.Fatalf("no capsule: %v", err)
	}
	want := domain.WitnessFuncName + "_2"
	c, err := capsule.NewStore(store).Read(ids[0])
	if err != nil {
		t.Fatal(err)
	}
	if c.Witness.Func != want {
		t.Errorf("capsule witness func = %q, want %q", c.Witness.Func, want)
	}
	if !strings.HasPrefix(c.Repro.Command, "go-flake-hunter replay ") {
		t.Errorf("reproduce command must reinstall the colliding-free witness: %s", c.Repro.Command)
	}

	code, out, errOut := clitest.Run("replay", "-capsule-dir", store, "--pkg", dir, "-n", "2")
	if code != domain.ExitOK {
		t.Fatalf("replay code=%d (want a deterministic replay)\n%s\n%s", code, out, errOut)
	}
	if !strings.Contains(out, "2/2 reproduced") {
		t.Errorf("replay must reproduce under the capsule's own witness name:\n%s", out)
	}
}

// An uncompilable package is exit 6, not "the flake is gone" (exit 3).
// replay used to ignore BuildFailed: the missing witness made every test absent
// from the JSON stream, which reads as StatusSkip, which reads as "not failed".
func TestReplayReportsBuildFailureAsSix(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	dir := clitest.FixtureDir(t, "order-basic")
	store := filepath.Join(t.TempDir(), "c")
	if code, _, errOut := clitest.Run("hunt", dir, "-capsule-dir", store, "--format", "json"); code != domain.ExitOK {
		t.Fatalf("hunt code=%d %s", code, errOut)
	}

	if err := os.WriteFile(filepath.Join(dir, "broken.go"), []byte(`package orderbasic

func broken() { undefined() }
`), 0o644); err != nil {
		t.Fatal(err)
	}

	code, out, errOut := clitest.Run("replay", "-capsule-dir", store, "--pkg", dir, "-n", "1")
	if code != domain.ExitBuildFailed {
		t.Fatalf("code = %d, want %d\nstdout:\n%s\nstderr:\n%s", code, domain.ExitBuildFailed, out, errOut)
	}
	if strings.Contains(out, "stale capsule") {
		t.Errorf("a build error must not be reported as a fixed flake:\n%s", out)
	}
	if !strings.Contains(out, "does not build") {
		t.Errorf("the report should name the real reason:\n%s", out)
	}
}

func TestAuditS3_ResolvesOnlyCheckoutPackages(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	parent := t.TempDir()
	root := filepath.Join(parent, "checkout")
	outside := filepath.Join(parent, "outside")
	nested := filepath.Join(root, "nested")
	for dir, module := range map[string]string{root: "fixture/checkout", outside: "fixture/outside", nested: "fixture/nested"} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module "+module+"\n\ngo 1.24\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "root.go"), []byte("package checkout\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "outside_test.go"), []byte(`package outside
import ("os"; "testing")
func TestOutside(t *testing.T) { if err := os.WriteFile("ran", nil, 0600); err != nil { t.Fatal(err) } }
`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nested, "nested_test.go"), []byte(`package nested
import "testing"
var calls int
func TestRepeat(t *testing.T) { calls++; if calls > 1 { t.Fatal("repeat") } }
`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)
	for _, tc := range []struct {
		name, pkg, test string
		want            int
	}{
		{"traversal", "../outside", "TestOutside", domain.ExitNotReproduced},
		{"symlink", "./linked", "TestOutside", domain.ExitNotReproduced},
		{"nested", "fixture/nested", "TestRepeat", domain.ExitOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.name == "symlink" {
				if err := os.Symlink(outside, filepath.Join(root, "linked")); err != nil {
					t.Skipf("symlink unavailable: %v", err)
				}
			}
			log := filepath.Join(t.TempDir(), "failure.json")
			data := fmt.Appendf(nil, "{\"Action\":\"fail\",\"Package\":%q,\"Test\":%q}\n", tc.pkg, tc.test)
			if err := os.WriteFile(log, data, 0o600); err != nil {
				t.Fatal(err)
			}
			code, out, stderr := clitest.Run("repro", "--from-json", log)
			if _, err := os.Stat(filepath.Join(outside, "ran")); !os.IsNotExist(err) {
				t.Errorf("test outside checkout was executed: %v", err)
				_ = os.Remove(filepath.Join(outside, "ran"))
			}
			if code != tc.want || tc.want != domain.ExitOK && !strings.Contains(stderr, "outside this checkout") {
				t.Errorf("repro exit=%d, want %d\n%s\n%s", code, tc.want, out, stderr)
			}
		})
	}
}

func TestAuditS5_ReplayRejectsWrongModuleAndEnvKey(t *testing.T) {
	dir := clitest.FixtureDir(t, "env-dependent")
	wrong := clitest.FixtureDir(t, "env-dependent")
	if err := os.WriteFile(filepath.Join(wrong, "go.mod"), []byte("module fixture/wrong\n\ngo 1.24\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	store := capsule.NewStore(t.TempDir())
	const pkg = "fixture/envdependent"
	const victim = "TestDependsOnEnv"
	path, err := store.Write(domain.Capsule{
		ID: "gfh_audit", Schema: domain.SchemaVersion, Status: domain.StatusConfirmed, Kind: domain.KindEnv,
		Target:  domain.Target{Package: pkg, RelDir: dir, Victim: victim},
		Witness: domain.Witness{Victim: domain.TestID{Package: pkg, Name: victim}, EnvDiff: []string{"FIXTURE_MODE"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ dir, kv, want string }{
		{wrong, "FIXTURE_MODE=ci", pkg},
		{dir, "OTHER=audit-secret", "FIXTURE_MODE"},
	} {
		code, out, stderr := clitest.Run("replay", path, "--pkg", tc.dir, "--env", tc.kv, "-n", "1")
		if code != domain.ExitUsage || out != "" || !strings.Contains(stderr, tc.want) || strings.Contains(stderr, "audit-secret") {
			t.Errorf("replay exit=%d stdout=%s stderr=%s", code, out, stderr)
		}
	}
}

func TestAuditC6_PrintedReplayRunsFromCleanCheckout(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	dir := clitest.FixtureDir(t, "order-basic")
	store := t.TempDir()
	code, _, stderr := clitest.Run("hunt", dir, "--short", "--capsule-dir", store)
	if code != 0 {
		t.Fatal(stderr)
	}
	s := capsule.NewStore(store)
	ids, err := s.List()
	if err != nil || len(ids) != 1 {
		t.Fatalf("capsules: %v %v", ids, err)
	}
	c, err := s.Read(ids[0])
	if err != nil {
		t.Fatal(err)
	}
	cmd := strings.Split(c.Repro.Command, "'")
	if !c.Witness.Flags.Short || len(cmd) != 5 || cmd[0] != "go-flake-hunter replay " || cmd[1] != s.Path(c.ID) || cmd[3] != dir {
		t.Fatalf("printed command cannot reinstall the witness: %s", c.Repro.Command)
	}
	code, out, stderr := clitest.Run("replay", cmd[1], "--pkg", cmd[3], "-n", "1")
	if code != 0 || !strings.Contains(out, "1/1 reproduced") || !strings.Contains(c.Repro.Command, "-n 1") {
		t.Fatalf("replay exit=%d %s %s", code, out, stderr)
	}
}
