package harness

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"github.com/emrelab/go-flake-hunter/internal/domain"
)

// Harness is one installed witness file. Remove is safe to call twice.
type Harness struct {
	path   string
	digest string
	file   fs.FileInfo
	once   sync.Once
	err    error
}

// Digest is a short hash of the witness source this harness wrote.
//
// The runner hands it to the result cache as RunSpec.Source: two witness
// chains can share package, -run regex and flags while executing different
// subsets, and a cache that cannot tell them apart answers the wrong question.
func (h *Harness) Digest() string { return h.digest }

// New returns a harness bound to a package directory. It does not touch disk.
func New(pkgDir string) *Harness { return &Harness{path: filepath.Join(pkgDir, "")} }

// Install writes the witness file and returns its path.
//
// side suffix disambiguates concurrent installs in the same package (only one
// file may exist at a time; the go tool compiles the directory as a unit).
//
// CONTRACT: the caller holds the directory lock for the lifetime of the file,
// i.e. from Install until Remove. Locking only around the write is not enough —
// the bytes stay on disk while the caller builds, and a concurrent process that
// lists tests in that window sees a test that does not exist.
//
// The probe predicate is the intended owner (policy.Locked around
// Install → Run → Remove); Install deliberately does not take the lock itself
// because a nested acquisition would have to guess whether the caller already
// holds it.
func Install(pkgDir, clause, fn string, steps []domain.Step, side string) (*Harness, error) {
	return install(pkgDir, side, func() ([]byte, error) { return Render(clause, fn, steps) })
}

// InstallParallel installs a witness whose steps run as parallel subtests.
//
// Same contract as Install (caller holds the directory lock for the file's
// lifetime); the only difference is the template.
func InstallParallel(pkgDir, clause, fn string, steps []domain.Step, side string) (*Harness, error) {
	return install(pkgDir, side, func() ([]byte, error) { return RenderParallel(clause, fn, steps) })
}

func install(pkgDir, side string, build func() ([]byte, error)) (*Harness, error) {
	src, err := build()
	if err != nil {
		return nil, err
	}

	name := domain.WitnessFileName(side)
	if !filepath.IsLocal(name) || filepath.Base(name) != name {
		return nil, domain.Wrap(domain.ErrUsage, "invalid witness file name %q", name)
	}
	h := &Harness{path: filepath.Join(pkgDir, name), digest: witnessDigest(src)}
	f, err := os.OpenFile(h.path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return nil, fmt.Errorf("%w: cannot write witness: %v", domain.ErrInternal, err)
	}
	h.file, err = f.Stat()
	if err == nil {
		_, err = f.Write(append([]byte(witnessMarker+h.digest+"\n"), src...))
	}
	if err = errors.Join(err, f.Close()); err != nil {
		_, removeErr := removeWitness(h.path, h.file)
		return nil, fmt.Errorf("%w: cannot write witness: %v", domain.ErrInternal, errors.Join(err, removeErr))
	}
	return h, nil
}

const witnessMarker = "// go-flake-hunter temporary witness checksum: "

func witnessDigest(src []byte) string { return fmt.Sprintf("%x", sha256.Sum256(src))[:16] }

// Path is the file written by Install.
func (h *Harness) Path() string { return h.path }

// Remove deletes the installed witness if it still has the same ownership and content.
func (h *Harness) Remove() error {
	h.once.Do(func() {
		if h.file != nil {
			info, err := ownedWitness(h.path, false)
			if err == nil && info != nil && os.SameFile(info, h.file) {
				_, err = removeWitness(h.path, info)
			}
			h.err = err
		}
	})
	return h.err
}

// CleanupStale deletes witness files left behind by a killed run.
//
// The caller holds the package lock so it cannot remove an active witness.
func CleanupStale(pkgDir string) ([]string, error) {
	entries, err := os.ReadDir(pkgDir)
	if err != nil {
		return nil, err
	}
	var removed []string
	for _, e := range entries {
		if !e.Type().IsRegular() || !domain.IsWitnessFileName(e.Name()) {
			continue
		}
		p := filepath.Join(pkgDir, e.Name())
		info, err := ownedWitness(p, true)
		if err != nil {
			return removed, err
		}
		deleted, err := removeWitness(p, info)
		if err != nil {
			return removed, err
		}
		if deleted {
			removed = append(removed, p)
		}
	}
	return removed, nil
}

func ownedWitness(path string, allowLegacy bool) (fs.FileInfo, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil || !info.Mode().IsRegular() {
		return nil, err
	}
	src, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	header, body, ok := bytes.Cut(src, []byte("\n"))
	if ok && string(header) == witnessMarker+witnessDigest(body) {
		return info, nil
	}
	if allowLegacy && isLegacyWitness(src) {
		return info, nil
	}
	return nil, nil
}

var legacyWitnessName = regexp.MustCompile(`^` + domain.WitnessFuncName + `(_[0-9]+)?$`)

func isLegacyWitness(src []byte) bool {
	if !bytes.HasPrefix(src, []byte(witnessHeader)) {
		return false
	}
	file, err := parser.ParseFile(token.NewFileSet(), "", src, parser.SkipObjectResolution)
	if err != nil || len(file.Decls) != 2 || len(file.Imports) != 1 {
		return false
	}
	imp := file.Imports[0]
	fn, ok := file.Decls[1].(*ast.FuncDecl)
	if imp.Name != nil || imp.Path.Value != `"testing"` ||
		!ok || fn.Recv != nil || fn.Body == nil || !legacyWitnessName.MatchString(fn.Name.Name) ||
		fn.Type.TypeParams != nil || fn.Type.Results != nil || len(fn.Type.Params.List) != 1 {
		return false
	}
	param := fn.Type.Params.List[0]
	if len(param.Names) != 1 || param.Names[0].Name != "t" {
		return false
	}
	ptr, ok := param.Type.(*ast.StarExpr)
	if !ok {
		return false
	}
	sel, ok := ptr.X.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "T" {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	return ok && pkg.Name == "testing"
}

func removeWitness(path string, expected fs.FileInfo) (bool, error) {
	if expected == nil {
		return false, nil
	}
	info, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil || !os.SameFile(expected, info) {
		return false, err
	}
	err = os.Remove(path)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	return err == nil, err
}

// ResolveClause decides the package clause for a witness that calls the given
// test functions.
//
// Non-negotiable: every test in `names` must live in the same test package.
// `foo` and `foo_test` are two different binaries, so a witness in one cannot
// call a test from the other — the compiler says "undefined: TestX" and the
// user would see a confusing build error instead of our diagnosis.
func ResolveClause(clauseOf map[string]string, victim string, polluters []string) (string, error) {
	victimClause, ok := clauseOf[victim]
	if !ok {
		return "", fmt.Errorf("%w: %q is not a test function", domain.ErrUnsupportedTest, victim)
	}
	if victimClause == "" {
		return "", fmt.Errorf("%w: package clause of %q is unknown", domain.ErrUnsupportedTest, victim)
	}

	var mismatched []string
	for _, p := range polluters {
		c, ok := clauseOf[p]
		if !ok {
			return "", fmt.Errorf("%w: %q is not a test function", domain.ErrUnsupportedTest, p)
		}
		if c != victimClause {
			mismatched = append(mismatched, fmt.Sprintf("%s (package %s)", p, c))
		}
	}
	if len(mismatched) > 0 {
		return "", fmt.Errorf(
			"%w: %s (package %s) cannot be called in the same test binary as %s; "+
				"the two live in different test packages",
			domain.ErrUnsupportedTest, victim, victimClause, strings.Join(mismatched, ", "))
	}
	return victimClause, nil
}

// FuncName picks a witness function name that does not collide with a test the
// user already wrote (a package may legitimately have TestFlakeWitness).
func FuncName(existing map[string]string) string {
	base := domain.WitnessFuncName
	if _, taken := existing[base]; !taken {
		return base
	}
	for i := 2; i < 100; i++ {
		candidate := fmt.Sprintf("%s_%d", base, i)
		if _, taken := existing[candidate]; !taken {
			return candidate
		}
	}
	return base + "_x"
}
