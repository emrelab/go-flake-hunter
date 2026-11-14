package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	iofs "io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/emrelab/go-flake-hunter/internal/domain"
)

// workflowPath is where the generated CI job lives.
const workflowPath = ".github/workflows/go-flake-hunter.yml"

// workflowTemplate is the job `init` writes.
//
// Deliberately narrow: it hints (`continue-on-error`) instead of gating, because
// a newly adopted flake hunter that turns CI red on day one gets removed on day
// two. `--fail-on-flake` is one line away in the file for teams that want a gate.
const workflowTemplate = `# go-flake-hunter: flaky test hunting.
#
# Generated: go-flake-hunter init. Edit freely; this file is never
# written again (init does not touch an existing file).
name: go-flake-hunter

on:
  pull_request:
  workflow_dispatch:
  schedule:
    - cron: "0 3 * * 1" # weekly: so new flakes do not pile up silently

jobs:
  hunt:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v7
      - uses: actions/setup-go@v7
        with:
          go-version-file: go.mod
          cache: false

      # Note: soft mode on first adoption so CI is not broken. For a gate,
      # add --fail-on-flake and delete continue-on-error.
      - name: hunt flakes
        continue-on-error: true
        run: |
          set -o pipefail
          go run github.com/emrelab/go-flake-hunter/cmd/go-flake-hunter@latest hunt ./... \
            --format markdown --budget 200 --timeout 10m | tee go-flake-hunter-report.md

      - name: attach report
        if: always()
        run: cat go-flake-hunter-report.md >> "$GITHUB_STEP_SUMMARY"

      - name: store capsules
        if: always()
        uses: actions/upload-artifact@v7
        with:
          name: go-flake-hunter-capsules
          path: "**/.go-flake-hunter/*.json"
          if-no-files-found: ignore
`

// initCmd writes the CI scaffolding a repository needs.
//
// It refuses to overwrite: a team that edited the workflow (added a matrix,
// changed the gate) must not lose that on a second run. Re-running is therefore
// safe and idempotent.
func initCmd(ctx context.Context, args []string, out IO) error {
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	fs.SetOutput(out.Stderr)
	var replayCapsule string
	fs.StringVar(&replayCapsule, "replay-capsule", "", "add a CI regression gate for a committed capsule JSON (repo-relative path)")
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	if replayCapsule != "" {
		if filepath.IsAbs(replayCapsule) || strings.HasPrefix(filepath.Clean(replayCapsule), "..") || !strings.HasSuffix(replayCapsule, ".json") {
			return domain.Wrap(domain.ErrUsage, "--replay-capsule must be a repo-relative JSON path")
		}
		for _, r := range replayCapsule {
			allowed := r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("./_-", r)
			if !allowed {
				return domain.Wrap(domain.ErrUsage, "--replay-capsule path may contain only letters, digits, dot, slash, underscore and hyphen")
			}
		}
	}

	dir := "."
	switch rest := fs.Args(); {
	case len(rest) > 1:
		return domain.Wrap(domain.ErrUsage, "at most one directory")
	case len(rest) == 1:
		dir = rest[0]
	}

	path := filepath.Join(dir, workflowPath)
	if _, err := os.Stat(path); err == nil {
		_, _ = fmt.Fprintf(out.Stdout, "already exists, untouched: %s\n", path)
		return nil
	} else if !errors.Is(err, iofs.ErrNotExist) {
		return domain.Wrap(domain.ErrUsage, "could not read %s: %v", path, err)
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return domain.Wrap(domain.ErrInternal, "could not create directory: %v", err)
	}
	workflow := workflowTemplate
	if replayCapsule != "" {
		workflow = strings.Replace(workflow, "      - name: attach report", replayGate(replayCapsule)+"      - name: attach report", 1)
	}
	if err := os.WriteFile(path, []byte(workflow), 0o644); err != nil {
		return domain.Wrap(domain.ErrInternal, "could not write %s: %v", path, err)
	}

	_, _ = fmt.Fprintf(out.Stdout, "written: %s\n", path)
	_, _ = fmt.Fprintln(out.Stdout, "\nNext step: go-flake-hunter hunt ./... (run it once locally, see the output)")
	return nil
}

// replayGate preserves infrastructure errors while turning a reproduced
// capsule RED and a non-reproducing (fixed) capsule GREEN. Replay reinstalls
// the appropriate witness, including the two-process cross-binary sequence.
func replayGate(path string) string {
	return fmt.Sprintf(`      - name: guard recorded flake
        run: |
          set +e
          GOBIN="$RUNNER_TEMP/go-flake-hunter-bin" go install github.com/emrelab/go-flake-hunter/cmd/go-flake-hunter@latest || exit $?
          "$RUNNER_TEMP/go-flake-hunter-bin/go-flake-hunter" replay %s -n 3
          status=$?
          case "$status" in
            0) echo "Recorded flake reproduced"; exit 1 ;;
            3) echo "Recorded flake no longer reproduces strictly"; exit 0 ;;
            *) exit "$status" ;;
          esac

`, quoteArg(path))
}
