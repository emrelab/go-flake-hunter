package domain

import (
	"context"
	"errors"
	"fmt"
)

// Sentinel errors. Each carries a meaningful user message and a fixed
// exit code (docs/ARCHITECTURE.md §10.1). Raw panic/stack never leaks to the user.
var (
	// ErrNotReproduced no witness found within the budget.
	ErrNotReproduced = errors.New("flake could not be reproduced")
	// ErrNotReproducible a witness was found but could not be re-verified (not deterministic).
	ErrNotReproducible = errors.New("witness is not deterministic")
	// ErrUnsupportedTest subtest / TestMain / different test package.
	ErrUnsupportedTest = errors.New("unsupported test form")
	// ErrBudgetExceeded run or time budget exceeded.
	ErrBudgetExceeded = errors.New("budget exceeded")
	// ErrBuildFailed the package does not compile; not a flake, a user error.
	ErrBuildFailed   = errors.New("package failed to build")
	ErrPackageFailed = errors.New("package failed")
	ErrIncomplete    = errors.New("test run incomplete")
	ErrBrokenTest    = errors.New("test fails in isolation")
	// ErrUsage bad argument.
	ErrUsage = errors.New("usage error")
	// ErrFlakeFound the CI gate with --fail-on-flake.
	ErrFlakeFound = errors.New("flake found")
	// ErrInternal unexpected error; details are never shown to the user.
	ErrInternal = errors.New("internal error")
)

// Exit codes. Fixed: CI scripts bind to these.
const (
	ExitOK            = 0
	ExitFlakeFound    = 1
	ExitUsage         = 2
	ExitNotReproduced = 3
	ExitBudget        = 4
	ExitUnsupported   = 5
	ExitBuildFailed   = 6
	ExitPackageFailed = 7
	ExitCanceled      = 130 // 128+SIGINT: shell convention; CI reads it as "stopped manually"
	ExitInternal      = 10
)

// ExitCode translates an error into a code CI understands.
// An unknown error is ExitInternal; it never returns 0.
func ExitCode(err error) int {
	switch {
	case err == nil:
		return ExitOK
	case errors.Is(err, ErrFlakeFound):
		return ExitFlakeFound
	case errors.Is(err, ErrUsage):
		return ExitUsage
	case errors.Is(err, ErrNotReproduced), errors.Is(err, ErrNotReproducible):
		return ExitNotReproduced
	case errors.Is(err, ErrBudgetExceeded):
		return ExitBudget
	case errors.Is(err, ErrUnsupportedTest):
		return ExitUnsupported
	case errors.Is(err, ErrBuildFailed):
		return ExitBuildFailed
	case errors.Is(err, ErrPackageFailed), errors.Is(err, ErrIncomplete):
		return ExitPackageFailed
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		// Cancellation is not a fault but the user's decision: 10 (internal error) would be wrong.
		return ExitCanceled
	case errors.Is(err, ErrInternal):
		return ExitInternal
	default:
		return ExitInternal
	}
}

// Wrap adds context to an error while preserving its identity (errors.Is keeps working).
func Wrap(err error, format string, args ...any) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s: %w", fmt.Sprintf(format, args...), err)
}
