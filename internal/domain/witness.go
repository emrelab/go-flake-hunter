package domain

import (
	"go/token"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Kind is the flake class: the output of the four probes in docs/ARCHITECTURE.md §7.
type Kind string

const (
	KindRepeat   Kind = "repeat"   // fails when repeated in the same process
	KindOrder    Kind = "order"    // fails when another test runs first
	KindParallel Kind = "parallel" // fails only when run in parallel
	KindEnv      Kind = "env"      // depends on an environment variable
)

// Valid confirms the class is a known value (capsule schema check).
func (k Kind) Valid() bool {
	switch k {
	case KindRepeat, KindOrder, KindParallel, KindEnv:
		return true
	}
	return false
}

// Flags are the go test flags needed for reproduction.
type Flags struct {
	Parallel int      `json:"parallel,omitempty"`
	Race     bool     `json:"race,omitempty"`
	Short    bool     `json:"short,omitempty"`
	Tags     []string `json:"tags,omitempty"`
	// Count is the -count value a repeat witness was found with ("--repeat N").
	// Without it a capsule found with -count=50 replays with the verification
	// count (2), goes green, and the tool reports "flake fixed" — a false
	// diagnosis that costs the user trust in every later capsule.
	Count int `json:"count,omitempty"`
}

// Step is a single t.Run call inside the harness.
//
// The Test field is not written to JSON: its name is already inside Label and
// keeping it in two places would bloat the capsule. But a Step read back from
// a capsule has an empty Test — code generation must use Func(), not Test.Name.
type Step struct {
	Label string `json:"label"` // "polluter/TestSeedDB"
	Test  TestID `json:"-"`
	// Package is the import path this step's test lives in. Only cross-binary
	// witnesses (#6) set it: within one witness file every step shares the
	// victim's binary, so the field would be redundant — and a step whose
	// Package differs from the victim's tells renderers this is a two-process
	// witness that no single test file can express.
	Package string `json:"package,omitempty"`
	// WrapParallel marks a step whose test does NOT call t.Parallel() itself.
	// The parallel witness has to add the call for it, otherwise the steps stay
	// serial and the witness proves nothing. A test that already calls
	// t.Parallel() must be left alone: testing.T panics on the second call
	// ("t.Parallel called multiple times").
	WrapParallel bool `json:"wrapParallel,omitempty"`
}

// Func returns the test function name this step calls.
//
// Prefers the in-memory TestID and falls back to parsing the label, so a step
// reconstructed from a capsule renders the same code as one built in memory.
func (s Step) Func() string {
	if s.Test.Name != "" {
		return s.Test.Name
	}
	if i := strings.LastIndex(s.Label, "/"); i >= 0 && i+1 < len(s.Label) {
		return s.Label[i+1:]
	}
	return s.Label
}

// ValidTestName checks the spelling of a top-level Go test function.
func ValidTestName(name string) bool {
	suffix, ok := strings.CutPrefix(name, "Test")
	first, _ := utf8.DecodeRuneInString(suffix)
	return ok && name != "TestMain" && token.IsIdentifier(name) && !unicode.IsLower(first)
}

// ValidLabel requires the label and the function call to name the same test.
func (s Step) ValidLabel() bool {
	name := s.Func()
	return ValidTestName(name) && (s.Label == PolluterLabel(name) || s.Label == VictimLabel(name))
}

// ValidateTestNames checks the names from a witness before they reach Go source
// or a test selector.
func (w Witness) ValidateTestNames() error {
	if !ValidTestName(w.Victim.Name) {
		return Wrap(ErrUnsupportedTest, "invalid witness victim %q", w.Victim.Name)
	}
	if w.Func != "" && !ValidTestName(w.Func) {
		return Wrap(ErrUnsupportedTest, "invalid witness function %q", w.Func)
	}
	for _, p := range w.Polluters {
		if !ValidTestName(p.Name) {
			return Wrap(ErrUnsupportedTest, "invalid witness polluter %q", p.Name)
		}
	}
	for _, step := range w.Steps {
		if !step.ValidLabel() {
			return Wrap(ErrUnsupportedTest, "invalid witness step %q", step.Label)
		}
	}
	return nil
}

// Witness is the minimal witness found: the smallest set of preceding tests that makes the victim fail.
type Witness struct {
	// Verification carries completed independent rounds when strict confirmation failed.
	Verification *Observation `json:"verification,omitempty"`
	// CrossClause replays top-level tests in one process instead of generating a cross-visibility call chain.
	CrossClause bool     `json:"crossClause,omitempty"`
	Kind        Kind     `json:"kind"`
	Victim      TestID   `json:"victim"`
	Polluters   []TestID `json:"polluters,omitempty"`
	Steps       []Step   `json:"steps"`
	Flags       Flags    `json:"flags"`
	Seed        int64    `json:"seed,omitempty"`    // -shuffle seed for KindOrder
	EnvDiff     []string `json:"envDiff,omitempty"` // key names only
	Runs        int      `json:"runs"`              // verified over how many runs
	// Func is the generated witness function name this witness was proven with
	// ("TestFlakeWitness", or "TestFlakeWitness_2" when the package already
	// defines TestFlakeWitness).
	//
	// It is provenance, not identity: `harness.FuncName` derives it from the
	// package's own test names, so it changes when a user adds a test, not when
	// the bug changes. It is therefore NOT part of capsule.ID — but it MUST be
	// carried, because replay and the printed reproduce command reinstall the
	// witness file: guessing the name collides with the user's test, the
	// package stops compiling, and the run reads as "the flake is gone".
	Func string `json:"func,omitempty"`
	// CrossBinary marks an order witness whose polluter runs in a SEPARATE
	// `go test` process (#6): the pollution it proves survives the process
	// boundary — files, ports, cwd — never in-memory globals. A witness file
	// cannot express it, so compile refuses these capsules; replay and the
	// capsule command run the two stages in sequence.
	CrossBinary bool `json:"crossBinary,omitempty"`
}

// FuncName is the witness function to install for this witness.
//
// Capsules written before Func existed fall back to the fixed name — the best
// answer available, and the one replay used to hardcode.
func (w *Witness) FuncName() string {
	if w == nil || w.Func == "" {
		return WitnessFuncName
	}
	return w.Func
}

// Minimal reports whether this is a verified minimal witness (at least one polluter and at least 2 runs).
func (w *Witness) Minimal() bool { return w.Verification == nil && len(w.Polluters) > 0 && w.Runs >= 2 }

// SelectionRegex selects the recorded top-level tests in one test binary.
func SelectionRegex(w Witness) string {
	names := make([]string, 0, len(w.Polluters)+1)
	for _, p := range w.Polluters {
		names = append(names, regexp.QuoteMeta(p.Name))
	}
	names = append(names, regexp.QuoteMeta(w.Victim.Name))
	return "^(" + strings.Join(names, "|") + ")$"
}
