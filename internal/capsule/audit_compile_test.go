package capsule_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/emrelab/go-flake-hunter/internal/clitest"
	"github.com/emrelab/go-flake-hunter/internal/domain"
)

func TestAuditS1_RejectsStatementInStepLabel(t *testing.T) {
	dir := clitest.FixtureDir(t, "order-basic")
	path := writeAuditCapsule(t, dir, "TestVictim); t.Error(\"INJECTED\") //")
	code, out, stderr := clitest.Run("compile", "--pkg", dir, "--print", path)
	if code == domain.ExitOK || out != "" {
		t.Fatalf("compile emitted source from a statement in the label: exit=%d\n%s\n%s", code, out, stderr)
	}
}

func TestAuditS1_RejectsNonTestAndWrongClauseBeforeVerify(t *testing.T) {
	for _, tc := range []struct{ name, src string }{
		{"non-test", "package orderbasic\nimport \"testing\"\nfunc TestRogue(t *testing.T, ignored int) {}\n"},
		{"wrong-clause", "package orderbasic_test\nimport \"testing\"\nfunc TestRogue(t *testing.T) {}\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := clitest.FixtureDir(t, "order-basic")
			if err := os.WriteFile(filepath.Join(dir, "rogue_test.go"), []byte(tc.src), 0o600); err != nil {
				t.Fatal(err)
			}
			path := writeAuditCapsule(t, dir, "polluter/TestRogue", "victim/TestVictim")
			code, out, stderr := clitest.Run("compile", "--pkg", dir, "--verify", path)
			files, err := filepath.Glob(filepath.Join(dir, "*"+domain.PersistentFileSuffix))
			if err != nil || code == domain.ExitOK || len(files) != 0 {
				t.Fatalf("invalid test declaration reached verification: exit=%d files=%v (%v)\n%s\n%s", code, files, err, out, stderr)
			}
		})
	}
}

func writeAuditCapsule(t *testing.T, dir string, labels ...string) string {
	t.Helper()
	const pkg = "fixture/orderbasic"
	victim := domain.TestID{Package: pkg, Name: "TestVictim"}
	c := domain.Capsule{
		Schema: domain.SchemaVersion, ID: "gfh_0123456789ab", Kind: domain.KindOrder, Status: domain.StatusConfirmed,
		Target:  domain.Target{Package: pkg, Victim: victim.Name},
		Witness: domain.Witness{Kind: domain.KindOrder, Victim: victim},
	}
	for _, label := range labels {
		c.Witness.Steps = append(c.Witness.Steps, domain.Step{Label: label})
	}
	store := filepath.Join(dir, domain.CapsuleDir)
	if err := os.MkdirAll(store, 0o755); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(store, c.ID+".json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestAuditS1_RejectsCommentInjection(t *testing.T) {
	dir := clitest.FixtureDir(t, "order-basic")
	path := writeAuditCapsule(t, dir, "victim/TestVictim")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var c domain.Capsule
	if err := json.Unmarshal(data, &c); err != nil {
		t.Fatal(err)
	}
	c.Target.RelDir = ".\n//line /injected:1\n//"
	data, err = json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	code, out, stderr := clitest.Run("compile", "--pkg", dir, "--print", path)
	if code == domain.ExitOK && strings.Contains(out, "\n//line /injected:1") {
		t.Fatalf("compile emitted a directive from a capsule comment:\n%s\n%s", out, stderr)
	}
}

func TestAuditC1_VerifiesOnlyVictimFailure(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	for _, kind := range []domain.Kind{domain.KindOrder, domain.KindParallel, domain.KindEnv, domain.KindRepeat} {
		t.Run(string(kind), func(t *testing.T) {
			for _, victimFails := range []bool{false, true} {
				name := "victim-passes"
				if victimFails {
					name = "victim-fails"
				}
				t.Run(name, func(t *testing.T) {
					dir := clitest.FixtureDir(t, "order-basic")
					path := writeAuditCapsule(t, dir, "polluter/TestPolluter", "victim/TestVictim")
					data, err := os.ReadFile(path)
					if err != nil {
						t.Fatal(err)
					}
					var c domain.Capsule
					if err := json.Unmarshal(data, &c); err != nil {
						t.Fatal(err)
					}
					c.Kind, c.Witness.Kind = kind, kind
					c.Witness.Flags.Count = 3
					if kind == domain.KindEnv {
						c.Witness.EnvDiff = []string{"GFH_AUDIT_ENV"}
						t.Setenv("GFH_AUDIT_ENV", "present")
					}
					data, err = json.Marshal(c)
					if err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(path, data, 0o600); err != nil {
						t.Fatal(err)
					}
					victim := ""
					if victimFails {
						victim = "if runs == 2 { t.Error(\"victim failed\") }"
						if kind != domain.KindRepeat {
							victim = "t.Error(\"victim failed\")"
						}
					}
					src := "package orderbasic\nimport \"testing\"\nvar runs int\n" +
						"func TestPolluter(t *testing.T) { t.Error(\"polluter failed\") }\n" +
						"func TestVictim(t *testing.T) { runs++; " + victim + " }\n"
					if err := os.WriteFile(filepath.Join(dir, "checkout_test.go"), []byte(src), 0o600); err != nil {
						t.Fatal(err)
					}
					wantCode, wantStatus := domain.ExitNotReproduced, domain.VerifyGreenNotReproduced
					if victimFails {
						wantCode, wantStatus = domain.ExitOK, domain.VerifyVerified
					}
					code, out, stderr := clitest.Run("compile", "--pkg", dir, "--verify", path)
					gotStatus := clitest.StoredVerifyStatus(t, filepath.Dir(path))
					if code != wantCode || gotStatus != wantStatus {
						t.Fatalf("compile --verify: exit=%d status=%q, want %d %q\n%s\n%s", code, gotStatus, wantCode, wantStatus, out, stderr)
					}
					t.Logf("compile --verify: exit=%d status=%q", code, gotStatus)
				})
			}
		})
	}
}

func TestAuditC1_RejectsInconsistentProcessExit(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	dir := clitest.FixtureDir(t, "order-basic")
	path := writeAuditCapsule(t, dir, "victim/TestVictim")
	const src = `package orderbasic
import ("os"; "testing")
func TestMain(m *testing.M) { m.Run(); os.Exit(0) }
func TestVictim(t *testing.T) { t.Error("victim failed") }
`
	if err := os.WriteFile(filepath.Join(dir, "checkout_test.go"), []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	code, out, stderr := clitest.Run("compile", "--pkg", dir, "--verify", path)
	status := clitest.StoredVerifyStatus(t, filepath.Dir(path))
	if code == domain.ExitOK || status == domain.VerifyVerified {
		t.Fatalf("compile --verify accepted a failed victim with a successful process: exit=%d status=%q\n%s\n%s", code, status, out, stderr)
	}
	t.Logf("compile --verify: exit=%d status=%q", code, status)
}

func TestAuditS4_RejectsPersistentSymlink(t *testing.T) {
	dir := clitest.FixtureDir(t, "order-basic")
	capsulePath := writeAuditCapsule(t, dir, "victim/TestVictim")
	path := filepath.Join(dir, domain.PersistentFileName("checkout_test.go", "TestVictim"))
	target := filepath.Join(t.TempDir(), "outside.go")
	if err := os.Symlink(target, path); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if code, out, stderr := clitest.Run("compile", "--pkg", dir, capsulePath); code == domain.ExitOK {
		t.Errorf("compile accepted a dangling regression symlink: %s %s", out, stderr)
	}
	if _, err := os.Lstat(target); !os.IsNotExist(err) {
		t.Fatalf("outside symlink target was created: %v", err)
	}
}

func TestAuditS5_CompileRejectsWrongModule(t *testing.T) {
	dir := clitest.FixtureDir(t, "order-basic")
	path := writeAuditCapsule(t, dir, "victim/TestVictim")
	wrong := clitest.FixtureDir(t, "order-basic")
	if err := os.WriteFile(filepath.Join(wrong, "go.mod"), []byte("module fixture/wrong\n\ngo 1.24\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(wrong)
	for _, args := range [][]string{{"--pkg", wrong, path}, {"--capsule-dir", filepath.Dir(path)}} {
		code, out, stderr := clitest.Run(append([]string{"compile", "--print"}, args...)...)
		if code != domain.ExitUsage || out != "" || !strings.Contains(stderr, "fixture/orderbasic") {
			t.Errorf("compile exit=%d stdout=%s stderr=%s", code, out, stderr)
		}
	}
}
