package probe_test

import (
	"strings"
	"testing"

	"github.com/emrelab/go-flake-hunter/internal/probe"
)

// K4: the gate rules the env probe out when os.Getenv is called with a
// non-literal key. Saying "no literal read" claimed the read was absent and
// sent the reader looking for code that exists.
func TestAuditK4_EnvGateNamesTheRealGap(t *testing.T) {
	got := probe.Env{}.InapplicableReason(probe.Target{})
	if !strings.Contains(got, "read") || !strings.Contains(got, "literal key") {
		t.Errorf("reason does not name the gap: %q", got)
	}
}
