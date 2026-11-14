package gotest

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/emrelab/go-flake-hunter/internal/domain"
	"github.com/emrelab/go-flake-hunter/internal/policy"
)

// Runner is the only interface the rest of the tree sees. Every layer above
// this one is testable with Fake because of it.
//
// Contract: docs/ARCHITECTURE.md §5.
type Runner interface {
	// Discover lists the package's tests in source order.
	Discover(ctx context.Context, pkgDir string) ([]domain.TestID, error)
	// Run executes one `go test` invocation. A failing test is data, not an
	// error: only infrastructure failures return a non-nil error.
	Run(ctx context.Context, spec domain.RunSpec) (*domain.RunResult, error)
}

// Exec is the real Runner. It is the single place in the tree that calls exec.
type Exec struct {
	// GoBin overrides the go binary ("" -> "go" from PATH).
	GoBin string
	// Short appends -short to every invocation. This is an OPERATOR choice,
	// not a spec property, so it lives on the runner instead of RunSpec: a
	// hunt run (--short) scans packages under the shortened test set, which
	// is how self-hunt stays meaningful on repos whose integration tests
	// declare testing.Short(). The whole run shares one Exec configuration,
	// so cached and live results inside one hunt stay consistent.
	Short bool
}

func (e *Exec) goBin() string {
	if e.GoBin != "" {
		return e.GoBin
	}
	return "go"
}

// ctxSlack is how much longer the context deadline is than the go test
// timeout, so Go's own timeout fires first and leaves a goroutine dump in the
// output instead of being killed by us.
const ctxSlack = 20 * time.Second

// Run executes spec and parses the JSON event stream.
func (e *Exec) Run(ctx context.Context, spec domain.RunSpec) (*domain.RunResult, error) {
	spec = spec.WithDefaults()

	// Two runs in the same package directory never overlap: the witness file,
	// ports and temp dirs are shared. The lock is re-entrant, so being called
	// inside the lock the probe holds does not deadlock.
	ctx, release, err := policy.Locked(ctx, spec.Package)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, domain.Wrap(domain.ErrInternal, "could not acquire lock: %v", err)
	}
	defer release()

	args := []string{
		"test", "-json",
		"-count", strconv.Itoa(spec.Count),
		"-timeout", spec.Timeout.String(),
	}
	if spec.RunRegex != "" {
		args = append(args, "-run", spec.RunRegex)
	}
	if spec.Parallel > 0 {
		args = append(args, "-parallel", strconv.Itoa(spec.Parallel))
	}
	if spec.CPU > 0 {
		args = append(args, "-cpu", strconv.Itoa(spec.CPU))
	}
	if spec.ShuffleSeed != 0 {
		args = append(args, "-shuffle", strconv.FormatInt(spec.ShuffleSeed, 10))
	}
	if spec.Race {
		args = append(args, "-race")
	}
	if len(spec.Tags) > 0 {
		args = append(args, "-tags", strings.Join(spec.Tags, ","))
	}
	if e.Short || spec.Short {
		args = append(args, "-short")
	}
	args = append(args, ".")

	runCtx, cancel := context.WithTimeout(ctx, spec.Timeout+ctxSlack)
	defer cancel()

	cmd := exec.CommandContext(runCtx, e.goBin(), args...)
	cmd.Dir = spec.Package
	cmd.Env = append(os.Environ(), spec.Env...)

	// The stream is buffered whole before parsing. Measured (M4, 120 tests ×
	// 20 output lines ≈ 375 KB): 6.8 ms and 4.5 MB of allocations per parse,
	// so a 50-package scan stays in tens of MB — streaming buys nothing yet.
	// shortcut: one package whose -json log passes ~50 MB would sit in memory
	// twice; feed the scanner an io.Reader then.
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	start := time.Now()
	runErr := cmd.Run()
	elapsed := time.Since(start)

	// If the user cancelled, there is no result: reading an interrupted run as
	// "build failure" or "passed" would report a result that does not exist.
	// (runCtx's own timeout never enters here; Go's -timeout fires first.)
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	// A failing test exits non-zero. That is the normal path, not an error.
	exitCode := 0
	if runErr != nil {
		var exitErr *exec.ExitError
		if !asExitError(runErr, &exitErr) {
			// Could not start the process at all (go missing, bad dir).
			return nil, domain.Wrap(domain.ErrInternal, "could not start go test (%s)", spec.Package)
		}
		exitCode = exitErr.ExitCode()
	}

	timedOut := runCtx.Err() == context.DeadlineExceeded

	res, perr := parseEvents(stdout.Bytes(), spec, exitCode, elapsed, timedOut)
	if perr != nil {
		// Unparseable stream: surface the raw tail, never pretend success.
		return nil, domain.Wrap(domain.ErrInternal, "could not read test2json output (%s): %s",
			spec.Package, tail(stderr.String(), 2))
	}

	// Build failure: nothing ran, and the package did not compile.
	if res.BuildFailed || !timedOut && len(res.Outcomes) == 0 && exitCode != 0 &&
		(strings.Contains(res.BuildOutput, "[setup failed]") || strings.Contains(res.BuildOutput, "[build failed]")) {
		res.BuildFailed = true
		if strings.TrimSpace(res.BuildOutput) == "" {
			res.BuildOutput = firstLines(stderr.String(), 5)
		}
	}
	if !res.BuildFailed {
		switch res.State {
		case domain.RunPackageFailed:
			return res, domain.Wrap(domain.ErrPackageFailed, "%s", spec.Package)
		case domain.RunIncomplete:
			return res, domain.Wrap(domain.ErrIncomplete, "%s", spec.Package)
		}
	}
	return res, nil
}
