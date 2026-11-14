package compile_test

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/emrelab/go-flake-hunter/internal/compile"
	"github.com/emrelab/go-flake-hunter/internal/domain"
)

func capsule() domain.Capsule {
	victim := domain.TestID{Package: "fixture/orderbasic", Name: "TestVictim"}
	polluter := domain.TestID{Package: "fixture/orderbasic", Name: "TestPolluter"}
	return domain.Capsule{
		Schema: domain.SchemaVersion,
		ID:     "gfh_5112091d7096",
		Kind:   domain.KindOrder,
		Target: domain.Target{Package: "fixture/orderbasic", RelDir: ".", Victim: "TestVictim"},
		Witness: domain.Witness{
			Kind:      domain.KindOrder,
			Victim:    victim,
			Polluters: []domain.TestID{polluter},
			Steps: []domain.Step{
				{Label: domain.PolluterLabel("TestPolluter"), Test: polluter},
				{Label: domain.VictimLabel("TestVictim"), Test: victim},
			},
		},
	}
}

func TestRenderIsValidGo(t *testing.T) {
	src, err := compile.Render(capsule(), "orderbasic")
	if err != nil {
		t.Fatal(err)
	}
	s := string(src)
	for _, want := range []string{
		"//go:build goflakehunter",
		"package orderbasic",
		"func TestFlakeRegression_TestVictim(t *testing.T)",
		`t.Run("polluter/TestPolluter", TestPolluter)`,
		"DO NOT EDIT",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("%q missing from generated test:\n%s", want, s)
		}
	}
}

// fixtureDir copies the order-basic fixture into a temp dir.
//
// Tests must never write into testdata: a generated file left behind by an
// earlier manual run redeclares the regression function and turns every later
// test run red for a reason that has nothing to do with the code.
func fixtureDir(t *testing.T) string {
	t.Helper()
	src, err := filepath.Abs(filepath.Join("..", "..", "testdata", "fixtures", "order-basic"))
	if err != nil {
		t.Fatal(err)
	}
	dst := t.TempDir()
	// copyFixture skips go-flake-hunter's own output: a leftover from a manual run
	// would redeclare the regression function inside the copy and turn the
	// test red for a reason that has nothing to do with the code.
	err = filepath.WalkDir(src, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
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
		t.Fatal(err)
	}
	return dst
}

// The generated file must really compile: we cannot break the user's build.
func TestGeneratedTestCompiles(t *testing.T) {
	dir := fixtureDir(t)
	src, err := compile.Render(capsule(), "orderbasic")
	if err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(dir, "zz_compile_check_test.go")
	if err := os.WriteFile(path, src, 0o644); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Remove(path) }()

	cmd := exec.Command("go", "vet", "-tags", "goflakehunter", ".")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go vet failed: %v\n%s", err, out)
	}
}

// The default suite without the tag must be unaffected: that is the build tag's whole purpose.
func TestGeneratedTestIsExcludedWithoutTag(t *testing.T) {
	dir := fixtureDir(t)
	src, err := compile.Render(capsule(), "orderbasic")
	if err != nil {
		t.Fatal(err)
	}

	// Write with the real name: this also verifies the naming rule.
	path := filepath.Join(dir, "checkout_goflakehunter_test.go")
	if err := os.WriteFile(path, src, 0o644); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Remove(path) }()

	// Without the tag: the build tag is off, so the regression test does not
	// compile and no test runs. In this case `go test` returns 0 and prints
	// "[no tests to run]" — the default suite neither goes red nor slows down.
	cmd := exec.Command("go", "test", "-count=1", "-run", "^TestFlakeRegression_", ".")
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("an untagged run must not error: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "no tests to run") {
		t.Fatalf("the regression test must not run in an untagged run:\n%s", out)
	}
}

func TestWriteRefusesForeignFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "order_TestVictim_goflakehunter_test.go")
	if err := os.WriteFile(path, []byte("package p\n// handwritten by a user\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := compile.Write(capsule(), "orderbasic", compile.Options{Dir: dir, SourceFile: "order.go", Write: true}); err == nil {
		t.Fatal("a file go-flake-hunter did not generate must be preserved")
	}
}

// Writing the same capsule twice must not change the file: no git noise.
func TestWriteIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	opt := compile.Options{Dir: dir, SourceFile: "checkout_test.go", Write: true}

	first, err := compile.Write(capsule(), "orderbasic", opt)
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(first)
	if err != nil {
		t.Fatal(err)
	}
	stat1, _ := os.Stat(first)

	second, err := compile.Write(capsule(), "orderbasic", opt)
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Errorf("path changed: %s != %s", first, second)
	}
	after, _ := os.ReadFile(second)
	if !bytes.Equal(before, after) {
		t.Error("the second write changed the content")
	}
	stat2, _ := os.Stat(second)
	if stat1.ModTime() != stat2.ModTime() {
		t.Error("an unchanged file must not be rewritten (mtime changed)")
	}
}

func TestCommandLine(t *testing.T) {
	got := compile.CommandLine(capsule())
	if !strings.Contains(got, "-tags goflakehunter") || !strings.Contains(got, "TestFlakeRegression_TestVictim") {
		t.Errorf("incomplete command: %s", got)
	}
}

// Every class must get its own reproduction form.
//
// These four asserts are the regression of the "single serial template" bug:
// repeat, parallel and env capsules were rendered with the order template and
// the generated test stayed green — compile still said "will be RED".
func TestRenderIsKindSpecific(t *testing.T) {
	cases := []struct {
		name string
		c    domain.Capsule
		want []string
		deny []string
	}{
		{
			name: "repeat",
			c: domain.Capsule{
				ID:     "gfh_r",
				Kind:   domain.KindRepeat,
				Target: domain.Target{Victim: "TestAccumulates"},
				Witness: domain.Witness{
					Kind:   domain.KindRepeat,
					Victim: domain.TestID{Name: "TestAccumulates"},
					Flags:  domain.Flags{Count: 7},
				},
			},
			want: []string{"for i := 0; i < 7; i++", "strconv.Itoa(i+1)", "TestAccumulates"},
			deny: []string{"t.Parallel()"},
		},
		{
			name: "parallel",
			c: domain.Capsule{
				ID:     "gfh_p",
				Kind:   domain.KindParallel,
				Target: domain.Target{Victim: "TestBeta"},
				Witness: domain.Witness{
					Kind:   domain.KindParallel,
					Victim: domain.TestID{Name: "TestBeta"},
					Polluters: []domain.TestID{
						{Name: "TestAlpha"},
					},
					Flags: domain.Flags{Parallel: 2},
					Steps: []domain.Step{
						{Label: "polluter/TestAlpha", Test: domain.TestID{Name: "TestAlpha"}, WrapParallel: true},
						{Label: "victim/TestBeta", Test: domain.TestID{Name: "TestBeta"}, WrapParallel: true},
					},
				},
			},
			want: []string{"t.Parallel()", "TestAlpha(t)", "TestBeta(t)"},
		},
		{
			name: "env",
			c: domain.Capsule{
				ID:     "gfh_e",
				Kind:   domain.KindEnv,
				Target: domain.Target{Victim: "TestEnv"},
				Witness: domain.Witness{
					Kind:    domain.KindEnv,
					Victim:  domain.TestID{Name: "TestEnv"},
					EnvDiff: []string{"FIXTURE_MODE"},
				},
			},
			want: []string{"os.LookupEnv", "FIXTURE_MODE", "t.Skip"},
			deny: []string{"t.Parallel()"},
		},
		{
			name: "order",
			c:    capsule(),
			want: []string{`t.Run("polluter/TestPolluter", TestPolluter)`, `t.Run("victim/TestVictim", TestVictim)`},
			deny: []string{"t.Parallel()", "os.LookupEnv"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			src, err := compile.Render(tc.c, "p")
			if err != nil {
				t.Fatal(err)
			}
			s := string(src)
			for _, want := range tc.want {
				if !strings.Contains(s, want) {
					t.Errorf("%q missing:\n%s", want, s)
				}
			}
			for _, deny := range tc.deny {
				if strings.Contains(s, deny) {
					t.Errorf("%q must not leak:\n%s", deny, s)
				}
			}
		})
	}
}

// CommandLine must carry the flags the class requires: a parallel witness run
// with -parallel=1 says "passed" and lies.
func TestCommandLineIsKindSpecific(t *testing.T) {
	repeat := domain.Capsule{
		Kind:    domain.KindRepeat,
		Target:  domain.Target{RelDir: "."},
		Witness: domain.Witness{Victim: domain.TestID{Name: "T"}, Flags: domain.Flags{Count: 7}},
	}
	if got := compile.CommandLine(repeat); !strings.Contains(got, "-count=1") {
		t.Errorf("the repeat command must run the body once: %s", got)
	}

	parallel := domain.Capsule{
		Kind:    domain.KindParallel,
		Target:  domain.Target{RelDir: "."},
		Witness: domain.Witness{Victim: domain.TestID{Name: "T"}, Flags: domain.Flags{Parallel: 3}},
	}
	if got := compile.CommandLine(parallel); !strings.Contains(got, "-parallel=3") {
		t.Errorf("the parallel command must contain -parallel=3: %s", got)
	}
}

func TestRenderRejectsEmptyClause(t *testing.T) {
	if _, err := compile.Render(capsule(), ""); err == nil {
		t.Fatal("rendering without a clause must be refused")
	}
}

// If -race is part of the witness, both the generated command and the verify run must carry it.
//
// On a race-only flake, -race dropped from the command leaves the artifact
// green: `compile` says "will be RED", the test passes, and `--verify` wrongly
// says "not reproducing". The evidence's label is part of the command.
func TestRaceWitnessCarriesRaceIntoCommandAndVerify(t *testing.T) {
	c := capsule()
	c.Witness.Flags.Race = true

	if got := compile.CommandLine(c); !strings.Contains(got, "go test -race ") {
		t.Errorf("a race witness must carry -race in the command: %s", got)
	}
	if spec := compile.VerifySpec(c, "."); !spec.Race {
		t.Error("VerifySpec must carry -race: verification must happen under the witness's conditions")
	}
	src, err := compile.Render(c, "orderbasic")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(src), "go test -race . -tags goflakehunter") {
		t.Errorf("the template's \"Reproduce\" line does not carry -race:\n%s", src)
	}
}

// Without --race the output must stay exactly the same: where the flag does
// not leak is where there is no regression.
func TestPlainWitnessOmitsRace(t *testing.T) {
	c := capsule()
	if got := compile.CommandLine(c); strings.Contains(got, "-race") {
		t.Errorf("-race must not leak into a non-race witness: %s", got)
	}
	if spec := compile.VerifySpec(c, "."); spec.Race {
		t.Error("VerifySpec.Race must be false for a non-race witness")
	}
}

func TestWriteRetainsLegacyVictimWithoutOverwritingSibling(t *testing.T) {
	dir := t.TempDir()
	first := capsule()
	src, err := compile.Render(first, "orderbasic")
	if err != nil {
		t.Fatal(err)
	}
	legacy := filepath.Join(dir, domain.PersistentFileName("checkout_test.go"))
	if err := os.WriteFile(legacy, src, 0o644); err != nil {
		t.Fatal(err)
	}
	opt := compile.Options{Dir: dir, SourceFile: "checkout_test.go", Write: true}
	second := capsule()
	second.Witness.Victim.Name = "TestOtherVictim"
	path, err := compile.Write(second, "orderbasic", opt)
	if err != nil {
		t.Fatal(err)
	}
	if path == legacy {
		t.Fatal("second victim overwrote legacy regression")
	}
	if got, err := os.ReadFile(legacy); err != nil || !bytes.Equal(got, src) {
		t.Fatalf("legacy lost: %v", err)
	}
	// Updating the original must reuse its old path, avoiding duplicate declarations.
	first.ID = "gfh_updated"
	got, err := compile.Write(first, "orderbasic", opt)
	if err != nil || got != legacy {
		t.Fatalf("legacy update: %s %v", got, err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal("sibling lost", err)
	}
}

func TestAuditC10_RepeatCommandRunsOneBody(t *testing.T) {
	c := capsule()
	c.Kind = domain.KindRepeat
	c.Witness.Flags.Count = 7
	got := compile.CommandLine(c)
	if !strings.Contains(got, "-count=1") || strings.Contains(got, "-count=7") {
		t.Fatalf("repeat body already runs seven times: %s", got)
	}
}
