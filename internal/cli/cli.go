// Package cli manages the command-line surface and the exit codes.
//
// Execute is pure: stdout/stderr are injected, it never calls os.Exit.
// So every command can be verified with a table test.
package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/emrelab/go-flake-hunter/internal/domain"
)

// Version is filled at build time via -ldflags.
var Version = "dev"

// IO are the streams the command writes to.
type IO struct {
	Stdout io.Writer
	Stderr io.Writer
	// Debug is enabled by --debug: internal error detail is shown to the user.
	// In normal mode only an id and "use --debug for detail" are printed.
	Debug bool
	// Progress receives scan snapshots; callers must not block the observer.
	Progress func(domain.ScanProgress)
	// ExpectedRegression binds a TUI write to its approved source; nil skips the check.
	ExpectedRegression []byte
}

// Command is a single subcommand.
type Command struct {
	Name    string
	Summary string
	// Task is the development task that introduced the command
	// (docs/ARCHITECTURE.md §12). 0 -> shipped in this release.
	Task int
	Run  func(ctx context.Context, args []string, out IO) error
}

// Execute routes arguments to a command and returns the exit code.
func Execute(ctx context.Context, args []string, out IO) int {
	if out.Stdout == nil {
		out.Stdout = io.Discard
	}
	if out.Stderr == nil {
		out.Stderr = io.Discard
	}

	// --debug is global: extracted here instead of adding it to every
	// command's flag set, so `hunt . --debug` works too.
	args, out.Debug = stripDebug(args)

	cmds := commands()
	if len(args) == 0 {
		if interactiveTUI(out.Stdout) {
			return Execute(ctx, []string{"tui"}, out)
		}
		usage(out.Stderr, cmds)
		return domain.ExitUsage
	}

	name := args[0]
	switch name {
	case "-h", "--help", "help":
		if len(args) == 2 && name == "help" {
			return Execute(ctx, []string{args[1], "--help"}, out)
		}
		if len(args) > 1 {
			_, _ = fmt.Fprintln(out.Stderr, "usage: go-flake-hunter help [command]")
			return domain.ExitUsage
		}
		usage(out.Stdout, cmds)
		return domain.ExitOK
	case "-v", "--version", "version":
		_, _ = fmt.Fprintf(out.Stdout, "go-flake-hunter %s\n", Version)
		return domain.ExitOK
	}

	for _, c := range cmds {
		if c.Name != name {
			continue
		}
		if c.Run == nil {
			_, _ = fmt.Fprintf(out.Stderr, "go-flake-hunter: %q not implemented yet (task %d)\n", c.Name, c.Task)
			return domain.ExitUsage
		}
		err := c.Run(ctx, args[1:], out)
		if err == nil {
			return domain.ExitOK
		}
		// -h/--help: the flag package already printed usage. A help request
		// is not a usage error; ExitCode(err) would give this a 2 and mislead
		// CI into thinking the command is broken.
		if errors.Is(err, flag.ErrHelp) {
			return domain.ExitOK
		}
		if msg := message(err, out.Debug); msg != "" {
			_, _ = fmt.Fprintf(out.Stderr, "go-flake-hunter: %s\n", msg)
		}
		return domain.ExitCode(err)
	}

	_, _ = fmt.Fprintf(out.Stderr, "go-flake-hunter: unknown command %q\n\n", name)
	usage(out.Stderr, cmds)
	return domain.ExitUsage
}

// parseFlags is the single entry point for flag parsing. flag.ErrHelp is
// returned as-is — a help request is not a usage error and maps to ExitOK
// in Execute. Folding everything into ErrUsage would report `hunt --help`
// to CI as exit 2, "the command is broken".
func parseFlags(fs *flag.FlagSet, args []string) error {
	fs.Usage = func() { commandHelp(fs) }
	if err := fs.Parse(reorderFlags(fs, args)); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return err
		}
		return domain.ErrUsage
	}
	return nil
}

// stripDebug removes the global --debug flag from the argument list.
func stripDebug(args []string) ([]string, bool) {
	rest := make([]string, 0, len(args))
	debug := false
	for i, a := range args {
		if a == "--" {
			rest = append(rest, args[i:]...)
			return rest, debug
		}
		if a == "--debug" {
			debug = true
			continue
		}
		rest = append(rest, a)
	}
	return rest, debug
}

// message picks the text shown to the user.
// For ErrInternal the detail shows only with --debug; a stack is never shown.
//
// # Wrapped errors carry their cause as a suffix
//
// Wrapped errors carry their cause as a suffix ("...: unsupported test
// format"). ExitCode already turned that cause into the exit code, so repeating
// it in the message adds nothing a user can act on — the sentence before it is
// the diagnosis.
func message(err error, debug bool) string {
	var ie internalError
	if errors.As(err, &ie) {
		if debug && ie.err != nil {
			return "internal error: " + ie.id + ": " + ie.err.Error()
		}
		return "internal error: " + ie.id + " (use --debug for detail)"
	}
	switch {
	case errors.Is(err, context.Canceled):
		return "cancelled (SIGINT); unfinished witness files deleted"
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout exceeded; increase it with --timeout"
	}
	return trimCause(err.Error())
}

func trimCause(s string) string {
	for _, cause := range []error{
		domain.ErrNotReproduced, domain.ErrNotReproducible, domain.ErrUnsupportedTest,
		domain.ErrBudgetExceeded, domain.ErrBuildFailed, domain.ErrUsage,
		domain.ErrFlakeFound, domain.ErrInternal,
	} {
		s = strings.TrimSuffix(s, ": "+cause.Error())
	}
	return s
}

// internalError hides unexpected errors from the user.
type internalError struct {
	id  string
	err error
}

func (e internalError) Error() string { return e.err.Error() }
func (e internalError) Unwrap() error { return e.err }

// Internal wraps an unexpected error without leaking it to the user.
func Internal(id string, err error) error { return internalError{id: id, err: err} }

func usage(w io.Writer, cmds []Command) {
	_, _ = fmt.Fprint(w, "go-flake-hunter — reproduce flaky Go tests and preserve the regression\n\nUsage:\n  go-flake-hunter <command> [arguments] [flags]\n\nStart here:\n  go-flake-hunter doctor ./...\n  go-flake-hunter hunt ./...\n  go-flake-hunter compile --verify\n\nCommands:\n")
	for _, c := range cmds {
		_, _ = fmt.Fprintf(w, "  %-10s %s\n", c.Name, c.Summary)
	}
	_, _ = fmt.Fprint(w, "\nHelp:\n  go-flake-hunter help <command>   Usage, examples and options\n  go-flake-hunter version          Show the installed version\n  --debug                         Include internal error details\n\nDocs: https://github.com/emrelab/go-flake-hunter#readme\n")
}

// commands lists the CLI in workflow order.
func commands() []Command {
	return []Command{
		{Name: "tui", Summary: "open the interactive terminal workspace", Run: tuiCmd},
		{Name: "hunt", Summary: "scan packages, find flake candidates", Run: hunt},
		{Name: "repro", Summary: "reproduce and shrink one known flake", Run: reproCmd},
		{Name: "compile", Summary: "generate a permanent regression test from a capsule", Run: compileCmd},
		{Name: "replay", Summary: "run a capsule N times, verify determinism", Run: replayCmd},
		{Name: "explain", Summary: "render a capsule as a human-readable report", Run: explainCmd},
		{Name: "init", Summary: "write a GitHub Actions workflow scaffold", Run: initCmd},
		{Name: "doctor", Summary: "check Go, packages and write access without running tests", Run: doctorCmd},
	}
}
