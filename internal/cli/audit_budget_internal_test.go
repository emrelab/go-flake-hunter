package cli

import (
	"flag"
	"os"
	"strings"
	"testing"
)

// B12: the wall-clock budget is checked between runs, so one in-flight run may
// overshoot it. Both surfaces a user reads — the flag help and the README —
// must say the same thing; a promise of a hard ceiling would make CI timeouts
// the user's surprise instead of their choice.
func TestAuditB12_TimeoutIsDocumentedAsSoftCeiling(t *testing.T) {
	fs := flag.NewFlagSet("hunt", flag.ContinueOnError)
	var o huntOptions
	o.addFlags(fs)
	if usage := fs.Lookup("timeout").Usage; !strings.Contains(usage, "between runs") {
		t.Errorf("--timeout help promises something the budget does not do: %s", usage)
	}
	readme, err := os.ReadFile("../../README.md")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"checked **between**", "may overshoot"} {
		if !strings.Contains(string(readme), want) {
			t.Errorf("README does not state the soft ceiling (%q)", want)
		}
	}
}
