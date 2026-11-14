package gotest

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/emrelab/go-flake-hunter/internal/domain"
)

// event is one test2json record (`go test -json`).
type event struct {
	Action  string  `json:"Action"`
	Package string  `json:"Package"`
	Test    string  `json:"Test"`
	Elapsed float64 `json:"Elapsed"`
	Output  string  `json:"Output"`
}

// parseEvents folds the JSON stream into a RunResult.
//
// Two details that matter:
//   - with -count=N a test emits run/pass|fail N times; the WORST status wins so
//     a test that passes then fails is reported as failed (repeat probe depends
//     on this).
//   - Order keeps only top-level tests in "run" event order, because subtests
//     interleave and would corrupt the sequence.
func parseEvents(data []byte, spec domain.RunSpec, exitCode int, elapsed time.Duration, timedOut bool) (*domain.RunResult, error) {
	res := &domain.RunResult{
		Spec:     spec,
		Outcomes: make(map[string]domain.Outcome),
		Terminal: make(map[string]int),
		ExitCode: exitCode,
		Duration: elapsed,
		TimedOut: timedOut,
	}
	outputs := make(map[string]*strings.Builder)
	pkgOutput := make(map[string]*strings.Builder)

	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024) // test output lines can be long

	seen := make(map[string]bool)
	started := make(map[string]int)
	var packageAction string
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 || line[0] != '{' {
			continue // `go test` may print non-JSON noise before the stream
		}
		var ev event
		if err := json.Unmarshal(line, &ev); err != nil {
			continue // a single corrupt record must not sink the whole run
		}
		if ev.Test == "" {
			switch ev.Action {
			case "pass", "fail", "skip":
				packageAction = ev.Action
			case "build-fail":
				res.BuildFailed = true
			}
			// Package-level events. Build errors arrive here
			// (Action:"output", Test:"") and are kept for BuildOutput.
			if ev.Action == "output" && ev.Output != "" {
				b := pkgOutput[ev.Package]
				if b == nil {
					b = &strings.Builder{}
					pkgOutput[ev.Package] = b
				}
				b.WriteString(ev.Output)
			}
			continue
		}

		switch ev.Action {
		case "run":
			started[ev.Test]++
			if !strings.Contains(ev.Test, "/") && !seen[ev.Test] {
				seen[ev.Test] = true
				res.Order = append(res.Order, ev.Test)
			}
		case "output":
			if outputs[ev.Test] == nil {
				outputs[ev.Test] = &strings.Builder{}
			}
			outputs[ev.Test].WriteString(ev.Output)
		case "pass", "fail", "skip":
			res.Terminal[ev.Test]++
			st := statusOf(ev.Action)
			prev, ok := res.Outcomes[ev.Test]
			o := domain.Outcome{
				Status:  worst(prev.Status, st, ok),
				Elapsed: time.Duration(ev.Elapsed * float64(time.Second)),
			}
			if b := outputs[ev.Test]; b != nil {
				o.Output = Normalize(b.String())
			}
			res.Outcomes[ev.Test] = o
		}
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("json stream: %w", err)
	}

	// Build/setup output: merge into the BuildOutput field.
	for _, b := range pkgOutput {
		res.BuildOutput += b.String()
	}
	res.BuildOutput = Normalize(res.BuildOutput)

	switch {
	case timedOut || packageAction == "" || exitCode < 0:
		res.State = domain.RunIncomplete
	case packageAction == "fail" && exitCode != 0:
		res.State = domain.RunPackageFailed
		for _, outcome := range res.Outcomes {
			if outcome.Status == domain.StatusFail {
				res.State = domain.RunTestFailed
				break
			}
		}
	case (packageAction == "pass" || packageAction == "skip") && exitCode == 0:
		res.State = domain.RunPassed
	default:
		res.State = domain.RunIncomplete
	}
	for name, runs := range started {
		if res.Terminal[name] < runs {
			res.State = domain.RunIncomplete
			break
		}
	}

	// Attach output to outcomes that never got a terminal event (timeout case).
	for name, b := range outputs {
		if _, ok := res.Outcomes[name]; ok {
			continue
		}
		res.Outcomes[name] = domain.Outcome{Status: domain.StatusSkip, Output: Normalize(b.String())}
	}
	return res, nil
}

// Failures extracts the failing tests from a complete `go test -json` log.
//
// This is the suspect list `repro --from-json` shrinks one by one: CI already
// paid for the discovery, and re-running a full hunt to rediscover what the
// log says is waste. Subtest failures ("TestX/sub") become the parent test —
// repro reproduces whole test functions, and a subtest cannot be named by
// -run without running its parent anyway.
//
// A line that is not JSON is skipped (build noise around the stream); an input
// with NO JSON records at all is an error, because "the file is not a test2json
// log" must never read as "the log had no failures".
func Failures(data []byte) ([]domain.TestID, error) {
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)

	seen := map[string]bool{}
	anyRecord := false
	var out []domain.TestID
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 || line[0] != '{' {
			continue
		}
		var ev event
		if err := json.Unmarshal(line, &ev); err != nil {
			continue
		}
		anyRecord = true
		if ev.Action != "fail" || ev.Test == "" {
			continue // package-level fails are build/setup failures: no test to repro
		}
		name := ev.Test
		if i := strings.Index(name, "/"); i >= 0 {
			name = name[:i]
		}
		key := ev.Package + "\x00" + name
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, domain.TestID{Package: ev.Package, Name: name})
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("json stream: %w", err)
	}
	if !anyRecord {
		return nil, fmt.Errorf("%w: no test2json records found", domain.ErrUsage)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Package != out[j].Package {
			return out[i].Package < out[j].Package
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

func statusOf(action string) domain.Status {
	switch action {
	case "pass":
		return domain.StatusPass
	case "fail":
		return domain.StatusFail
	default:
		return domain.StatusSkip
	}
}

// worst returns the more severe status. fail > pass > skip.
// Callers pass prev zero-value with ok=false for the first event.
func worst(prev, next domain.Status, havePrev bool) domain.Status {
	if !havePrev {
		return next
	}
	if prev == domain.StatusFail || next == domain.StatusFail {
		return domain.StatusFail
	}
	if prev == domain.StatusPass || next == domain.StatusPass {
		return domain.StatusPass
	}
	return domain.StatusSkip
}
