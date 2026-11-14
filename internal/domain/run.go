package domain

import (
	"context"
	"time"
)

// RunSpec is the full description of a "go test" invocation.
// Same spec + same commit = same run.
type RunSpec struct {
	Package     string // package directory (used as the working directory)
	RunRegex    string // "" -> all tests
	Count       int    // always >= 1; 1 also breaks the test cache
	Parallel    int    // -parallel; 0 -> leave alone
	CPU         int    // -cpu; 0 -> leave alone
	ShuffleSeed int64  // 0 -> -shuffle off
	Short       bool
	Race        bool          // -race
	Env         []string      // "KEY=VALUE" layer; nil -> inherit
	Tags        []string      // -tags; changes the set of compiled files
	Timeout     time.Duration // 0 -> DefaultTimeout
	// Source identifies the generated witness file this run executes.
	//
	// It never reaches the go command; it exists so the result cache can tell
	// two witness chains apart. Without it every subset of a ddmin search asks
	// with the same spec (same package, same -run regex, same flags) and the
	// PASS of one subset is served to the next: the search then eliminates the
	// real polluter and the final verification fails.
	Source string
}

// DefaultTimeout is the upper bound for a single package run.
const DefaultTimeout = 120 * time.Second

// WithDefaults pulls zero fields to safe defaults.
// Count is never left 0: -count=1 breaks the test cache and hides no flake.
func (s RunSpec) WithDefaults() RunSpec {
	if s.Count < 1 {
		s.Count = 1
	}
	if s.Timeout <= 0 {
		s.Timeout = DefaultTimeout
	}
	return s
}

// RunResult is the observation of one run. Data, not an error.
type RunResult struct {
	Spec        RunSpec
	Outcomes    map[string]Outcome // test name -> outcome
	Order       []string           // order of the -json "run" events
	BuildFailed bool               // build error != test error
	BuildOutput string
	ExitCode    int
	Duration    time.Duration
	TimedOut    bool
	State       RunState
	Terminal    map[string]int // terminal events by test, including repeated rounds
}

type RunState string

const (
	RunPassed        RunState = "passed"
	RunTestFailed    RunState = "test-failed"
	RunPackageFailed RunState = "package-failed"
	RunIncomplete    RunState = "incomplete"
	RunBroken        RunState = "broken"
)

type PackageResult struct {
	Package string   `json:"package"`
	Test    string   `json:"test,omitempty"`
	Status  RunState `json:"status"`
}

// Completed requires all expected terminal events. Fake runners usually supply
// only outcomes, so they can model one completed observation without a stream.
func (r *RunResult) Completed(name string, count int) bool {
	if r.Terminal != nil {
		return r.Terminal[name] >= count
	}
	_, ok := r.Outcomes[name]
	return ok
}

// Status returns a test's outcome safely (StatusSkip if absent from the map).
func (r *RunResult) Status(name string) Status {
	if r == nil {
		return StatusSkip
	}
	if o, ok := r.Outcomes[name]; ok {
		return o.Status
	}
	return StatusSkip
}

// Failed reports whether the test failed.
func (r *RunResult) Failed(name string) bool { return r.Status(name) == StatusFail }

// Passed reports whether the test passed.
func (r *RunResult) Passed(name string) bool { return r.Status(name) == StatusPass }

type freshRunsKey struct{}

// FreshRuns marks independent verification rounds; a result cache must bypass them.
func FreshRuns(ctx context.Context) context.Context {
	return context.WithValue(ctx, freshRunsKey{}, true)
}

// RequiresFreshRuns reports whether a caller needs a new observation.
func RequiresFreshRuns(ctx context.Context) bool { v, _ := ctx.Value(freshRunsKey{}).(bool); return v }

// PackageFiles is the active file set reported by go list.
type PackageFiles struct {
	Dir, ImportPath                              string
	GoFiles, CgoFiles, TestGoFiles, XTestGoFiles []string
}
