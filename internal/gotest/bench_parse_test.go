package gotest

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/emrelab/go-flake-hunter/internal/domain"
)

// noisyLog is what a real package's -json stream looks like: every test emits
// its own output lines between run and pass, plus package-level build noise.
// 120 tests is a mid-size package; the cost probe (P1/B11) needs a stream this
// shape, not the three-line fixtures.
func noisyLog(tests int) []byte {
	var b strings.Builder
	const pkg = "example.com/noisy"
	for i := range 200 {
		fmt.Fprintf(&b, `{"Action":"output","Package":%q,"Output":"package noise line %d\n"}`+"\n", pkg, i)
	}
	for i := range tests {
		name := fmt.Sprintf("TestNoisy%03d", i)
		fmt.Fprintf(&b, `{"Action":"run","Package":%q,"Test":%q}`+"\n", pkg, name)
		for j := range 20 {
			fmt.Fprintf(&b, `{"Action":"output","Package":%q,"Test":%q,"Output":"  step %d: assertion log with a realistic amount of text\n"}`+"\n", pkg, name, j)
		}
		fmt.Fprintf(&b, `{"Action":"pass","Package":%q,"Test":%q,"Elapsed":0.01}`+"\n", pkg, name)
	}
	fmt.Fprintf(&b, `{"Action":"pass","Package":%q,"Elapsed":2.5}`+"\n", pkg)
	return []byte(b.String())
}

// Recall must not move: every terminal event survives the fold.
func TestParseNoisyLogKeepsEveryOutcome(t *testing.T) {
	const tests = 120
	res, err := parseEvents(noisyLog(tests), domain.RunSpec{Package: "x", Count: 1}, 0, time.Second, false)
	if err != nil {
		t.Fatal(err)
	}
	if res.State != domain.RunPassed || len(res.Outcomes) != tests || len(res.Order) != tests {
		t.Fatalf("state=%s outcomes=%d order=%d", res.State, len(res.Outcomes), len(res.Order))
	}
}

func BenchmarkParseEventsNoisyLog(b *testing.B) {
	data := noisyLog(120)
	b.SetBytes(int64(len(data)))
	b.ReportAllocs()
	for b.Loop() {
		if _, err := parseEvents(data, domain.RunSpec{Package: "x", Count: 1}, 0, time.Second, false); err != nil {
			b.Fatal(err)
		}
	}
}
