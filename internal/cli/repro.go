package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/emrelab/go-flake-hunter/internal/domain"
	"github.com/emrelab/go-flake-hunter/internal/gotest"
)

// reproCmd re-derives and shrinks the witness for one known test.
//
// Difference from hunt: hunt asks "is anything here flaky?" and pays for a full
// scan; repro is handed the suspect (the name usually comes from a previous
// capsule or a CI failure) and spends its whole budget on that one test. Same
// scanner underneath, so the classification cannot drift between the commands.
func reproCmd(ctx context.Context, args []string, out IO) error {
	var o huntOptions
	var fromJSON string
	fs := flag.NewFlagSet("repro", flag.ContinueOnError)
	fs.SetOutput(out.Stderr)
	o.addFlags(fs)
	fs.StringVar(&fromJSON, "from-json", "",
		"failed `go test -json` log as suspect source (file, or - for stdin)")
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	if err := validateRunFormat(o.format); err != nil {
		return err
	}
	rest := fs.Args()

	if err := o.resolvePolluter(ctx); err != nil {
		return err
	}

	if fromJSON != "" {
		if len(rest) > 0 {
			_, _ = fmt.Fprint(out.Stderr, "usage: go-flake-hunter repro --from-json <failure.json>   (no package argument)\n")
			return domain.ErrUsage
		}
		return reproFromJSON(ctx, fromJSON, o, out)
	}

	if len(rest) == 0 || len(rest) > 2 {
		_, _ = fmt.Fprint(out.Stderr, "usage: go-flake-hunter repro <package-dir> [TestName] | --from-json <file|->\n")
		return domain.ErrUsage
	}
	pkg := rest[0]
	var only string
	if len(rest) == 2 {
		only = rest[1]
	}

	dirs, err := gotest.ExpandPatterns(ctx, []string{pkg})
	if err != nil {
		return err
	}
	s := newScanner(out, o, only)
	var runErr error
	for _, dir := range dirs {
		if err := s.pkg(ctx, dir); err != nil {
			runErr = err
			break
		}
	}
	if err := s.flush(); err != nil {
		return err
	}
	if runErr != nil {
		return runErr
	}
	return s.outcome(true)
}

// reproFromJSON turns a failed CI log into suspect-shrinking (#5).
//
// The adoption path: CI goes red, the log artifact says exactly which tests
// failed, and re-running a full hunt to rediscover what CI already knew burns
// budget for nothing. Each failing test gets the identical budgeted `repro`
// treatment — one shared budget and one shared report across all suspects — and
// the exit contract is `repro`'s own: found 0 / not reproduced 3 / budget 4.
//
// It must run inside the repository the log came from: package names in the log
// are import paths, and resolving them to directories is `go list`'s job.
func reproFromJSON(ctx context.Context, fromJSON string, o huntOptions, out IO) error {
	var (
		data []byte
		err  error
	)
	if fromJSON == "-" {
		data, err = io.ReadAll(os.Stdin)
	} else {
		data, err = os.ReadFile(fromJSON)
	}
	if err != nil {
		return domain.Wrap(domain.ErrUsage, "could not read %s", fromJSON)
	}

	suspects, err := gotest.Failures(data)
	if err != nil {
		return err // ErrUsage: not a test2json log at all
	}
	if len(suspects) == 0 {
		_, _ = fmt.Fprintln(out.Stderr, "the log has no failing tests: nothing to reproduce")
		return domain.ErrNotReproduced
	}
	_, _ = fmt.Fprintf(out.Stderr, "from-json: %d failing tests\n", len(suspects))

	dirs, err := resolveCheckoutDirs(ctx, ".")
	if err != nil {
		return err
	}
	s := newScanner(out, o, "")
	warned := map[string]bool{}
	var runErr error
	for _, sus := range suspects {
		dir := dirs[sus.Package]
		if dir == "" {
			if !warned[sus.Package] {
				_, _ = fmt.Fprintf(out.Stderr, "  skip %s: package %s is outside this checkout or cannot be resolved\n",
					sus.Name, sus.Package)
				warned[sus.Package] = true
			}
			continue
		}

		s.only = sus.Name
		if err := s.pkg(ctx, dir); err != nil {
			if errors.Is(err, domain.ErrUsage) {
				// The log names a test the package no longer has (a stale CI
				// artifact): report it, do not let it veto the remaining suspects.
				_, _ = fmt.Fprintf(out.Stderr, "  skip %s: %v\n", sus.Name, err)
				continue
			}
			runErr = err // budget/cancel/build: already reported by s.pkg
			break
		}
	}
	if err := s.flush(); err != nil {
		return err
	}
	if runErr != nil {
		return runErr
	}
	return s.outcome(true)
}

func resolveCheckoutDirs(ctx context.Context, root string, tags ...string) (map[string]string, error) {
	if dir, err := gitOutput(ctx, root, "rev-parse", "--show-toplevel"); err == nil {
		root = dir
	}
	roots, err := checkoutModules(ctx, root)
	if err != nil {
		return nil, err
	}
	return gotest.ResolveImportDirs(ctx, roots, tags...)
}

// checkoutModules finds module roots without following directory symlinks.
func checkoutModules(ctx context.Context, root string) ([]string, error) {
	var roots []string
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		name := entry.Name()
		if entry.IsDir() && path != root &&
			(strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_") || name == "vendor" || name == "testdata") {
			return filepath.SkipDir
		}
		if name == "go.mod" && entry.Type().IsRegular() {
			roots = append(roots, filepath.Dir(path))
		}
		return nil
	})
	return roots, err
}
