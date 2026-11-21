package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/emrelab/go-flake-hunter/internal/capsule"
	"github.com/emrelab/go-flake-hunter/internal/domain"
	"github.com/emrelab/go-flake-hunter/internal/gotest"
)

type tuiConfig struct {
	targets      []string
	store        string
	budget, jobs int
	timeout      time.Duration
	short, race  bool
}

type tuiFinding struct {
	capsule  domain.Capsule
	path     string
	modified time.Time
}
type tuiRequest struct {
	kind    string
	finding tuiFinding
	secret  string
	preview string
}
type tuiResult struct {
	catalogLoaded                               bool
	kind                                        string
	code                                        int
	goVersion                                   string
	dirs                                        []string
	findings                                    []tuiFinding
	notice                                      string
	preview                                     string
	previewFinding                              tuiFinding
	confirmed, suspected, partial, broken, runs int
}

type tuiTask struct {
	mu       sync.Mutex
	kind     string
	started  time.Time
	cancel   context.CancelFunc
	result   chan tuiResult
	logs     []string
	partial  string
	progress domain.ScanProgress
	seen     map[string]bool
}

func (t *tuiTask) Write(data []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, part := range bytes.SplitAfter(data, []byte{'\n'}) {
		if len(part) == 0 {
			continue
		}
		// Only the visible tail is retained; a noisy target cannot grow the UI log.
		if len(t.partial) < 4096 {
			t.partial += string(part[:min(len(part), 4096-len(t.partial))])
		}
		if part[len(part)-1] == '\n' {
			t.appendLine(strings.TrimRight(t.partial, "\r\n"))
			t.partial = ""
		}
	}
	return len(data), nil
}

func (t *tuiTask) appendLine(line string) {
	t.logs = append(t.logs, displayText(line))
	if len(t.logs) > 200 {
		copy(t.logs, t.logs[len(t.logs)-200:])
		t.logs = t.logs[:200]
	}
}

func (t *tuiTask) snapshot() ([]string, domain.ScanProgress) {
	t.mu.Lock()
	defer t.mu.Unlock()
	logs := append([]string(nil), t.logs...)
	if t.partial != "" {
		logs = append(logs, displayText(t.partial))
	}
	return logs, t.progress
}

func (t *tuiTask) observe(p domain.ScanProgress) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.progress = p
	if p.Package != "" {
		t.seen[p.Package] = true
	}
}

type tuiCapture struct{ bytes.Buffer }

func (b *tuiCapture) WriteString(s string) (int, error) {
	if b.Len()+len(s) > 8<<20 {
		return 0, fmt.Errorf("TUI report exceeds 8 MiB; stored capsules remain available in Findings")
	}
	return b.Buffer.WriteString(s)
}

func (b *tuiCapture) Write(p []byte) (int, error) {
	if b.Len()+len(p) > 8<<20 {
		return 0, fmt.Errorf("TUI report exceeds 8 MiB; stored capsules remain available in Findings")
	}
	return b.Buffer.Write(p)
}

func tuiCatalog(ctx context.Context, dirs []string, store string) ([]tuiFinding, []string) {
	seen := map[string]bool{}
	var findings []tuiFinding
	var warnings []string
	for _, dir := range dirs {
		if ctx.Err() != nil {
			break
		}
		path, err := filepath.Abs(capsuleDir(store, dir))
		if err != nil || seen[path] {
			continue
		}
		seen[path] = true
		s := capsule.NewStore(path)
		ids, err := s.List()
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("%s: %v", path, err))
			continue
		}
		for _, id := range ids {
			if ctx.Err() != nil {
				break
			}
			c, err := s.Read(id)
			if err != nil {
				warnings = append(warnings, fmt.Sprintf("%s: %v", id, err))
				continue
			}
			modified := c.CreatedAt
			if st, err := os.Stat(s.Path(id)); err == nil {
				modified = st.ModTime()
			}
			findings = append(findings, tuiFinding{c, s.Path(id), modified})
		}
	}
	sort.Slice(findings, func(i, j int) bool {
		if findings[i].modified.Equal(findings[j].modified) {
			return findings[i].path < findings[j].path
		}
		return findings[i].modified.After(findings[j].modified)
	})
	return findings, warnings
}

func tuiRunTask(ctx context.Context, request tuiRequest, config tuiConfig, dirs []string, task *tuiTask, execute func(context.Context, []string, IO) int) tuiResult {
	result := tuiResult{kind: request.kind, dirs: append([]string(nil), dirs...)}
	var output tuiCapture
	streams := IO{Stdout: &output, Stderr: task, Progress: task.observe}
	var args []string
	switch request.kind {
	case "workspace":
		result.dirs = nil
		metaCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		root := "."
		if len(config.targets) > 0 {
			if st, err := os.Stat(config.targets[0]); err == nil && st.IsDir() {
				root = config.targets[0]
			}
		}
		env, err := gotest.Environment(metaCtx, root)
		if err == nil {
			result.goVersion = env["GOVERSION"]
		} else {
			_, _ = fmt.Fprintln(task, err)
			result.code = domain.ExitCode(err)
		}
		if discovered, err := gotest.ExpandPatterns(metaCtx, config.targets); err == nil {
			result.dirs = discovered
		} else {
			_, _ = fmt.Fprintln(task, err)
			result.code = domain.ExitCode(err)
		}
		result.notice = "Workspace loaded. Choose an action; no tests have been run."
		if result.code != 0 {
			result.notice = "Workspace discovery needs attention. Open Activity for details."
		}
	case "scan":
		args = []string{"hunt", "--format", "json", "--progress", "off", "--budget", strconv.Itoa(config.budget), "--timeout", config.timeout.String(), "--jobs", strconv.Itoa(config.jobs)}
		if config.short {
			args = append(args, "--short")
		}
		if config.race {
			args = append(args, "--race")
		}
		if config.store != "" {
			args = append(args, "--capsule-dir", config.store)
		}
		args = append(args, "--")
		args = append(args, config.targets...)
	case "doctor":
		args = append([]string{"doctor", "--"}, config.targets...)
		result.notice = "Environment checks finished. These checks do not run target tests."
	case "replay":
		args = []string{"replay", request.finding.path, "-n", "3"}
		if request.finding.capsule.Kind == domain.KindEnv {
			keys := request.finding.capsule.Witness.EnvDiff
			if len(keys) == 0 {
				result.code = domain.ExitUsage
				result.notice = "Capsule has no recorded environment key."
				return result
			}
			args = append(args, "--env", keys[0]+"="+request.secret)
			// Target diagnostics may echo the value. Never retain any of that output.
			streams.Stdout, streams.Stderr = io.Discard, io.Discard
			_, _ = fmt.Fprintln(task, "Environment replay: value masked; detailed command output is not retained.")
		}
	case "preview":
		current, err := capsule.NewStore(filepath.Dir(request.finding.path)).Read(strings.TrimSuffix(filepath.Base(request.finding.path), ".json"))
		if err != nil {
			result.code = domain.ExitUsage
			result.notice = err.Error()
			return result
		}
		request.finding.capsule = current
		args = []string{"compile", request.finding.path, "--print"}
	case "compile":
		current, err := capsule.NewStore(filepath.Dir(request.finding.path)).Read(strings.TrimSuffix(filepath.Base(request.finding.path), ".json"))
		if err != nil || !reflect.DeepEqual(current, request.finding.capsule) {
			result.code = domain.ExitUsage
			result.notice = "Capsule changed since preview. Reload and preview it again before writing."
			return result
		}
		args = []string{"compile", request.finding.path}
		streams.ExpectedRegression = []byte(request.preview)
	}
	if args != nil {
		result.code = execute(ctx, args, streams)
		request.secret = ""
		switch request.kind {
		case "scan":
			var report struct {
				Flakes   []domain.Capsule
				Packages []domain.PackageResult
				Runs     int
			}
			if err := json.Unmarshal(output.Bytes(), &report); err == nil {
				result.runs = report.Runs
				for _, c := range report.Flakes {
					switch c.Status {
					case domain.StatusConfirmed:
						result.confirmed++
					case domain.StatusSuspected:
						result.suspected++
					case domain.StatusPartial:
						result.partial++
					}
				}
				for _, p := range report.Packages {
					if p.Status == domain.RunBroken {
						result.broken++
					}
				}
				result.notice = fmt.Sprintf("Scan: %d confirmed, %d suspected, %d partial, %d broken; %d runs (exit %d).", result.confirmed, result.suspected, result.partial, result.broken, result.runs, result.code)
				_, _ = fmt.Fprintln(task, result.notice)
			} else {
				if result.code == 0 {
					result.code = domain.ExitInternal
				}
				result.notice = "Scan report was not complete; inspect Activity and stored findings."
				_, _ = fmt.Fprintln(task, result.notice)
			}
		case "preview":
			if result.code == 0 {
				result.preview, result.previewFinding = output.String(), request.finding
				result.notice = "Preview only. Press w to confirm writing the regression test."
			}
		default:
			if output.Len() > 0 {
				_, _ = task.Write(output.Bytes())
			}
			if result.notice == "" {
				result.notice = fmt.Sprintf("%s finished (exit %d). Open Activity for the result.", request.kind, result.code)
			}
			if request.kind == "replay" {
				switch result.code {
				case 0:
					result.notice = "Witness reproduced in all three replay rounds."
				case domain.ExitNotReproduced:
					result.notice = "Witness did not reproduce consistently. It may have changed or already be fixed."
				case domain.ExitBuildFailed:
					result.notice = "Replay target does not build. Open Activity for details (environment replay output stays private)."
				}
			}
		}
		if result.code != 0 && request.kind == "preview" {
			result.notice = "This capsule could not be compiled. See Activity for the reason."
		}
	}
	if result.code == domain.ExitCanceled {
		result.notice = "Stopped. Any partial scan evidence was preserved by the engine."
	}
	seenDirs := make(map[string]bool, len(result.dirs))
	unique := result.dirs[:0]
	for _, dir := range result.dirs {
		if !seenDirs[dir] {
			seenDirs[dir] = true
			unique = append(unique, dir)
		}
	}
	result.dirs = unique
	task.mu.Lock()
	for path := range task.seen {
		if abs, err := filepath.Abs(path); err == nil && !seenDirs[abs] {
			seenDirs[abs] = true
			result.dirs = append(result.dirs, abs)
		}
	}
	task.mu.Unlock()
	sort.Strings(result.dirs)
	root, _ := os.Getwd()
	var warnings []string
	catalogCtx, cancelCatalog := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancelCatalog()
	result.findings, warnings = tuiCatalog(catalogCtx, append([]string{root}, result.dirs...), config.store)
	result.catalogLoaded = catalogCtx.Err() == nil
	for _, warning := range warnings {
		_, _ = fmt.Fprintln(task, warning)
	}
	return result
}
