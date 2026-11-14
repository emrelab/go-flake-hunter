package cli

import (
	"context"
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"

	"github.com/emrelab/go-flake-hunter/internal/capsule"
	"github.com/emrelab/go-flake-hunter/internal/compile"
	"github.com/emrelab/go-flake-hunter/internal/domain"
	"github.com/emrelab/go-flake-hunter/internal/facts"
	"github.com/emrelab/go-flake-hunter/internal/gotest"
	"github.com/emrelab/go-flake-hunter/internal/harness"
)

// compileCmd turns one capsule (or the newest in the store) into a permanent
// regression test in the package it belongs to.
func compileCmd(ctx context.Context, args []string, out IO) error {
	var (
		dir    string
		pkg    string
		print  bool
		dryRun bool
		verify bool
	)
	fs := flag.NewFlagSet("compile", flag.ContinueOnError)
	fs.SetOutput(out.Stderr)
	fs.StringVar(&dir, "capsule-dir", "", "capsule directory (default <pkg>/.go-flake-hunter)")
	fs.StringVar(&pkg, "pkg", "", "package directory (default: the directory the capsule recorded)")
	fs.BoolVar(&print, "print", false, "print to stdout without writing to disk")
	fs.BoolVar(&dryRun, "dry-run", false, "only say what would be written")
	fs.BoolVar(&verify, "verify", false, "run the generated test once, verify it really goes red")
	if err := parseFlags(fs, args); err != nil {
		return err
	}

	id, storeDir, _, err := resolveCapsule(fs.Args(), dir, pkg)
	if err != nil {
		return err
	}

	store := capsule.NewStore(storeDir)
	c, err := store.Read(id)
	if err != nil {
		return domain.Wrap(domain.ErrUsage, "%s", err)
	}

	if c.Status != domain.StatusConfirmed {
		return domain.Wrap(domain.ErrUnsupportedTest, "only confirmed capsules can be compiled; %s is %s", c.ID, c.Status)
	}

	if pkg == "" && len(fs.Args()) == 1 {
		if info, err := os.Stat(fs.Args()[0]); err == nil && info.IsDir() {
			pkg = fs.Args()[0]
		}
	}
	pkgDir, err := resolveTargetDir(ctx, c.Target.Package, pkg, ".")
	if err != nil {
		return err
	}

	if c.Witness.CrossClause {
		return domain.Wrap(domain.ErrUnsupportedTest, "tests in different package clauses cannot be called from one generated file; reproduce with: %s", reproCommand(c, store.Path(id), pkgDir, ""))
	}

	// A cross-binary witness (#6) cannot become a single regression test file:
	// its reproduction is two `go test` processes. Refusing with the command
	// attached turns the exit 5 into a copy-pasteable action instead of a dead
	// end; the scaffold from `go-flake-hunter init` runs that command as the gate.
	if c.Witness.CrossBinary {
		return domain.Wrap(domain.ErrUnsupportedTest,
			"this witness spans two test binaries and cannot be a regression test file; it reproduces with: %s",
			reproCommand(c, store.Path(id), pkgDir, ""))
	}

	c.Target.RelDir = pkgDir

	// Clause determines which test package the generated test belongs to.
	f, err := facts.Scan(pkgDir)
	if err != nil {
		return domain.Wrap(domain.ErrUsage, "could not read %s", pkgDir)
	}
	clause, err := compileClause(c, f)
	if err != nil {
		return err
	}

	if dryRun && !print {
		if _, err := compile.Render(c, clause); err != nil {
			return err
		}
	}
	if print {
		src, err := compile.Render(c, clause)
		if err != nil {
			return err
		}
		if _, err := out.Stdout.Write(src); err != nil {
			return err
		}
		return nil
	}

	if dryRun {
		_, _ = fmt.Fprintf(out.Stdout, "will write: %s\n", filepath.Join(pkgDir, compile.OutputName(pkgDir, sourceFile(pkgDir, c.Witness.Victim.Name), c.Witness.Victim.Name)))
		_, _ = fmt.Fprintf(out.Stdout, "command   : %s\n", compile.CommandLine(c))
		return nil
	}

	path, err := compile.Write(c, clause, compile.Options{
		Dir:        pkgDir,
		SourceFile: sourceFile(pkgDir, c.Witness.Victim.Name),
		Write:      true,
	})
	if err != nil {
		return err
	}

	_, _ = fmt.Fprintf(out.Stdout, "regression test written: %s\n", path)
	_, _ = fmt.Fprintf(out.Stdout, "run              : %s\n", compile.CommandLine(c))

	if verify {
		st, verr := verifyCompiled(ctx, &gotest.Exec{}, pkgDir, c, out)
		if st != "" {
			// The verdict belongs to the capsule even when it is bad: the whole
			// point of the contract is that downstream tooling can bind to
			// verifyStatus instead of guessing from an exit code.
			c.VerifyStatus = st
			if _, werr := store.Write(c); werr != nil {
				return werr
			}
		}
		if verr != nil {
			return verr
		}
	}

	if c.Kind == domain.KindEnv {
		_, _ = fmt.Fprintf(out.Stdout, "\nNote: the test turns RED when the env value is given (the value is not stored in the capsule):\n")
		_, _ = fmt.Fprintf(out.Stdout, "     %s\n", compile.CommandLine(c))
	} else {
		_, _ = fmt.Fprintf(out.Stdout, "\nNote: the test will be RED right now (the flake is not fixed yet).\n")
		_, _ = fmt.Fprintf(out.Stdout, "     After the fix it turns green and guards the regression permanently.\n")
	}
	return nil
}

func compileClause(c domain.Capsule, f facts.PkgFacts) (string, error) {
	victim := c.Witness.Victim
	var polluters []string
	for _, step := range c.Witness.Steps {
		if (step.Func() == victim.Name) != strings.HasPrefix(step.Label, domain.StepLabelVictim) {
			return "", domain.Wrap(domain.ErrUnsupportedTest, "witness step %q does not match the victim %s", step.Label, victim.Name)
		}
		if step.Package != "" && step.Package != victim.Package {
			return "", domain.Wrap(domain.ErrUnsupportedTest, "witness step %q names a different package: %s", step.Label, step.Package)
		}
		if step.Func() != victim.Name {
			polluters = append(polluters, step.Func())
		}
	}
	clause, err := harness.ResolveClause(f.Clause, victim.Name, polluters)
	if err != nil {
		return "", err
	}
	files := map[string]*ast.File{}
	for _, name := range append(polluters, victim.Name) {
		path := f.TestPos[name].File
		file := files[path]
		if file == nil {
			file, err = parser.ParseFile(token.NewFileSet(), path, nil, parser.SkipObjectResolution)
			if err != nil {
				return "", domain.Wrap(domain.ErrUnsupportedTest, "cannot read test declaration %q: %v", name, err)
			}
			files[path] = file
		}
		if file.Name.Name != clause || !declaresTest(file, name) {
			return "", domain.Wrap(domain.ErrUnsupportedTest, "%q is not a Go test function in package %s", name, clause)
		}
	}
	return clause, nil
}

func declaresTest(file *ast.File, name string) bool {
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name.Name != name || fn.Recv != nil {
			continue
		}
		t := fn.Type
		if t.TypeParams != nil || t.Results != nil && len(t.Results.List) != 0 ||
			len(t.Params.List) != 1 || len(t.Params.List[0].Names) > 1 {
			return false
		}
		ptr, ok := t.Params.List[0].Type.(*ast.StarExpr)
		if !ok {
			return false
		}
		switch typ := ptr.X.(type) {
		case *ast.Ident:
			return typ.Name == "T"
		case *ast.SelectorExpr:
			pkg, ok := typ.X.(*ast.Ident)
			if !ok || typ.Sel.Name != "T" {
				return false
			}
			for _, imp := range file.Imports {
				if imp.Path.Value == `"testing"` || imp.Path.Value == "`testing`" {
					return imp.Name == nil && pkg.Name == "testing" || imp.Name != nil && pkg.Name == imp.Name.Name
				}
			}
		}
	}
	return false
}

// verifyCompiled runs the artifact once, exactly as the user would, and returns
// the reproduction contract's verdict (#8):
//
//	verified              the artifact ran and went RED — the flake is reproduced
//	green-not-reproduced  the artifact ran and stayed GREEN — worthless artifact
//	skipped-missing-env   the env witness was inert — verification never happened
//
// A SKIP is not a verification: a suite that skips our regression prints `ok`,
// and a CI that reads that as "verified" is the trust bug this contract closes.
// Both non-verified verdicts are loud: they return ErrNotReproducible (exit 3),
// the same code "stayed green" has always had. An empty status with a non-nil
// error means no verdict was reached (build failure) and nothing is recorded.
func verifyCompiled(ctx context.Context, r gotest.Runner, dir string, c domain.Capsule, out IO) (domain.VerifyStatus, error) {
	res, err := r.Run(ctx, compile.VerifySpec(c, dir))
	if err != nil {
		return "", err
	}
	fn := domain.PersistentFuncName(c.Witness.Victim.Name)
	parent, hasParent := res.Outcomes[fn]

	switch {
	case res.BuildFailed:
		return "", domain.Wrap(domain.ErrBuildFailed, "the generated regression test did not compile: %s",
			firstBuildLine(res.BuildOutput))
	case res.TimedOut || !hasParent || res.ExitCode < 0:
		return "", domain.Wrap(domain.ErrNotReproducible, "verification did not complete")
	case c.Kind == domain.KindEnv && parent.Status == domain.StatusSkip && res.ExitCode == 0 &&
		strings.Contains(parent.Output, "this regression does not run without the env variable set:"):
		// The env class has no value in the capsule; an unset variable makes the
		// test inert. Inert is not verified — name the missing keys and exit 3.
		keys := strings.Join(c.Witness.EnvDiff, ", ")
		_, _ = fmt.Fprintf(out.Stdout, "verification     : unverified (env variable missing: %s)\n", keys)
		return domain.VerifySkippedMissingEnv, domain.Wrap(domain.ErrNotReproducible,
			"the generated test SKIPPED instead of running: %s is not set — run again with the variable set", keys)
	}

	victimFailed := false
	for name := range compile.VerifyTests(c) {
		if _, ok := res.Outcomes[name]; !ok {
			return "", domain.Wrap(domain.ErrNotReproducible, "verification did not finish victim subtest %s", name)
		}
		victimFailed = victimFailed || res.Failed(name)
	}
	if parent.Status == domain.StatusFail && res.ExitCode == 1 && victimFailed {
		_, _ = fmt.Fprintln(out.Stdout, "verification     : RED (verified — the flake is still there)")
		return domain.VerifyVerified, nil
	}
	if victimFailed {
		return "", domain.Wrap(domain.ErrNotReproducible, "victim failure did not match the regression test and process outcomes")
	}
	_, _ = fmt.Fprintln(out.Stdout, "verification     : victim did not fail (not reproduced)")
	return domain.VerifyGreenNotReproduced, domain.Wrap(domain.ErrNotReproducible,
		"the victim did not fail: for class %s the artifact does not reproduce the flake", c.Kind)
}

// firstBuildLine keeps a compiler diagnostic on one line.
func firstBuildLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			return line
		}
	}
	return "no detail"
}

// resolveCapsule turns the command's positional argument into a capsule id and
// the two directories that matter: where the JSON lives and which package the
// capsule is about.
//
// They are not the same directory. `replay .go-flake-hunter/gfh_x.json` used to take
// the file's parent as the package, then look for its store inside that parent
// (<pkg>/.go-flake-hunter/.go-flake-hunter) and report "capsule not found" for a file
// sitting right there.
func resolveCapsule(args []string, dirFlag, pkgFlag string) (id, storeDir, pkgDir string, err error) {
	pkg := pkgOrDefault(pkgFlag, ".")

	switch {
	case len(args) > 1:
		return "", "", "", domain.Wrap(domain.ErrUsage, "at most one capsule")
	case len(args) == 1:
		arg := args[0]
		if st, statErr := os.Stat(arg); statErr == nil && !st.IsDir() {
			// A path to a capsule file: the store is its directory.
			store := filepath.Dir(arg)
			return stemExt(arg), store, pkgOrDefault(pkgFlag, filepath.Dir(store)), nil
		}
		if st, statErr := os.Stat(arg); statErr == nil && st.IsDir() {
			id, pkgDir, err = newestIn(capsuleDir(dirFlag, arg), arg)
			return id, capsuleDir(dirFlag, pkgDir), pkgDir, err
		}
		// Bare file name: whatever `ls .go-flake-hunter` prints gets pasted.
		// The .json extension is not part of the id; look it up as an id. If no
		// directory is given, the store comes from --capsule-dir, not from cwd.
		if filepath.Ext(arg) == ".json" {
			if d := filepath.Dir(arg); d != "." {
				return stemExt(arg), d, pkgOrDefault(pkgFlag, filepath.Dir(d)), nil
			}
			return stemExt(arg), capsuleDir(dirFlag, pkg), pkg, nil
		}
		return arg, capsuleDir(dirFlag, pkg), pkg, nil
	}
	id, pkgDir, err = newestIn(capsuleDir(dirFlag, pkg), pkg)
	return id, capsuleDir(dirFlag, pkgDir), pkgDir, err
}

func pkgOrDefault(pkg, fallback string) string {
	if pkg != "" {
		return pkg
	}
	return fallback
}

// resolveTargetDir uses only checkout roots and directories selected by the operator.
func resolveTargetDir(ctx context.Context, importPath, explicit, checkout string) (string, error) {
	if explicit != "" {
		dirs, err := gotest.ResolveImportDirs(ctx, []string{explicit})
		if err != nil {
			return "", err
		}
		abs, err := filepath.Abs(explicit)
		if err == nil && dirs[importPath] != "" && canonical(dirs[importPath]) == canonical(abs) {
			return abs, nil
		}
		return "", domain.Wrap(domain.ErrUsage, "--pkg %s does not match capsule package %s", explicit, importPath)
	}
	dirs, err := resolveCheckoutDirs(ctx, checkout)
	if err != nil {
		return "", err
	}
	if dir := dirs[importPath]; dir != "" {
		return dir, nil
	}
	return "", domain.Wrap(domain.ErrUsage, "capsule package %s is outside this checkout; specify it with --pkg", importPath)
}

func newestIn(dir, pkgDir string) (string, string, error) {
	ids, err := capsule.NewStore(dir).List()
	if err != nil {
		return "", "", domain.Wrap(domain.ErrUsage, "%s", err)
	}
	if len(ids) == 0 {
		return "", "", domain.Wrap(domain.ErrUsage, "no capsule in %s; run go-flake-hunter hunt first", dir)
	}
	return ids[0], pkgDir, nil
}

func stemExt(path string) string {
	base := filepath.Base(path)
	if ext := filepath.Ext(base); ext != "" {
		base = base[:len(base)-len(ext)]
	}
	return base
}

// sourceFile names the file the victim lives in; the generated file is named
// after it. The victim is passed so the name follows the test, not directory
// order, and so go-flake-hunter's own output is never picked again.
func sourceFile(dir, victim string) string { return targetSourceFile(dir, victim) }
