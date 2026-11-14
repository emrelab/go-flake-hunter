package cli

import (
	"strings"
	"testing"

	"github.com/emrelab/go-flake-hunter/internal/compile"
	"github.com/emrelab/go-flake-hunter/internal/domain"
)

// H-T1 / S6: a printed command is pasted into cmd or PowerShell as often as
// into sh. POSIX single quotes are literal characters there, so the replay
// path became a directory that does not exist. Double quotes are the only form
// all three shells strip.
func TestAuditS6_PrintedCommandsAvoidPOSIXQuotes(t *testing.T) {
	for _, arg := range []string{`C:\work\repo`, `/tmp/a b/c`} {
		if got := quoteArg(arg); strings.Contains(got, "'") || !strings.HasPrefix(got, `"`) {
			t.Errorf("quoteArg(%q) = %s", arg, got)
		}
	}
	if got := quoteArg(`a"b`); !strings.Contains(got, `\"`) {
		t.Errorf("embedded quote not escaped: %s", got)
	}
	c := domain.Capsule{
		ID:      "gfh_x",
		Kind:    domain.KindOrder,
		Target:  domain.Target{Package: "p", RelDir: "sub/dir"},
		Witness: domain.Witness{Victim: domain.TestID{Name: "TestVictim"}},
	}
	if got := compile.CommandLine(c); !strings.Contains(got, `-run "^TestFlakeRegression_TestVictim$"`) {
		t.Errorf("-run keeps POSIX quoting: %s", got)
	}
}
