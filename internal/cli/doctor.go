package cli

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"go/version"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/emrelab/go-flake-hunter/internal/domain"
	"github.com/emrelab/go-flake-hunter/internal/gotest"
)

type doctorCheck struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Detail string `json:"detail"`
}

func doctorCmd(ctx context.Context, args []string, out IO) error {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	fs.SetOutput(out.Stderr)
	format := fs.String("format", "text", "output format: text|json")
	timeout := fs.Duration("timeout", 30*time.Second, "time allowed for environment checks")
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	if *format != "text" && *format != "json" {
		return domain.Wrap(domain.ErrUsage, "unknown format %q (text|json)", *format)
	}
	if *timeout <= 0 {
		return domain.Wrap(domain.ErrUsage, "--timeout must be positive")
	}
	ctx, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()
	checks := []doctorCheck{}
	ready := true
	add := func(name, status, detail string) {
		checks = append(checks, doctorCheck{name, status, trimCause(detail)})
		if status == "FAIL" {
			ready = false
		}
	}
	root := "."
	if len(fs.Args()) > 0 {
		if st, err := os.Stat(fs.Arg(0)); err == nil && st.IsDir() {
			root = fs.Arg(0)
		}
	}
	// A bare doctor checks the checkout: package roots rarely carry their own
	// tests, so "." would fail on the most common repository layout.
	pkgs := fs.Args()
	if len(pkgs) == 0 {
		pkgs = []string{"./..."}
	}
	env, err := gotest.Environment(ctx, root)
	if err != nil {
		add("Go", "FAIL", err.Error())
	} else {
		v := env["GOVERSION"]
		if !version.IsValid(v) || version.Compare(v, "go1.24.0") < 0 {
			add("Go", "FAIL", "Go 1.24+ required; selected toolchain: "+v)
		} else {
			add("Go", "OK", v+" ("+env["GOOS"]+"/"+env["GOARCH"]+")")
		}
		if (env["GOMOD"] == "" || env["GOMOD"] == os.DevNull) && (env["GOWORK"] == "" || env["GOWORK"] == "off") {
			add("Module", "FAIL", "no Go module or workspace; run from the target checkout")
		} else {
			module := env["GOMOD"]
			if module == "" || module == os.DevNull {
				module = env["GOWORK"]
			}
			add("Module", "OK", filepath.Dir(module))
		}
		dirs, err := gotest.ExpandPatterns(ctx, pkgs)
		if err != nil {
			add("Packages", "FAIL", err.Error())
		} else {
			add("Packages", "OK", fmt.Sprintf("%d test package directories resolved (tests not run)", len(dirs)))
			for _, dir := range dirs {
				if err := writableDirectory(dir); err != nil {
					add("Write access", "FAIL", dir+": "+err.Error())
				} else {
					add("Write access", "OK", dir)
				}
				store := filepath.Join(dir, domain.CapsuleDir)
				if _, err := os.Stat(store); err == nil {
					if err := writableDirectory(store); err != nil {
						add("Capsule store", "FAIL", store+": "+err.Error())
					}
				} else if !os.IsNotExist(err) {
					add("Capsule store", "FAIL", store+": "+err.Error())
				}
			}
		}
		if env["CGO_ENABLED"] == "0" {
			add("Race detector", "WARN", "CGO is disabled; --race verification needs CGO and a supported C compiler/target")
		}
	}
	if _, err := exec.LookPath("git"); err != nil {
		add("Git", "WARN", "not on PATH; commit provenance and --changed will be unavailable")
	} else {
		add("Git", "OK", "available")
	}
	if *format == "json" {
		if err := json.NewEncoder(out.Stdout).Encode(struct {
			Schema string        `json:"schema"`
			Ready  bool          `json:"ready"`
			Checks []doctorCheck `json:"checks"`
		}{"go-flake-hunter/doctor/v1", ready, checks}); err != nil {
			return err
		}
	} else {
		_, _ = fmt.Fprint(out.Stdout, "Environment checks\n\n")
		for _, check := range checks {
			_, _ = fmt.Fprintf(out.Stdout, "  %-4s %-14s %s\n", check.Status, check.Name, displayText(check.Detail))
		}
		if ready {
			_, _ = fmt.Fprintln(out.Stdout, "\nPrerequisites passed. No tests were run.\nNext: go-flake-hunter hunt "+displayText(joinPackageArgs(pkgs)))
		}
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if !ready {
		return domain.Wrap(domain.ErrUsage, "environment checks failed; fix the items marked FAIL and run doctor again")
	}
	return nil
}

func joinPackageArgs(args []string) string {
	if len(args) == 0 {
		return "."
	}
	text := ""
	for _, arg := range args {
		if text != "" {
			text += " "
		}
		text += quoteArg(arg)
	}
	return text
}

func writableDirectory(dir string) error {
	f, err := os.CreateTemp(dir, ".gfh-doctor-*")
	if err != nil {
		return err
	}
	closeErr := f.Close()
	removeErr := os.Remove(f.Name())
	if closeErr != nil {
		return closeErr
	}
	return removeErr
}
