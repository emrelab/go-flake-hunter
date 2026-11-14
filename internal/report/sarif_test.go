package report_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/emrelab/go-flake-hunter/internal/domain"
	"github.com/emrelab/go-flake-hunter/internal/report"
)

// hunt --format sarif (#4): GitHub code scanning pins each flake on the PR
// diff at the victim's declaration. The document is small on purpose — the
// fields asserted here are exactly what codeql-action/upload-sarif reads.
func TestSARIFAnnotatesVictim(t *testing.T) {
	c := domain.Capsule{
		Kind: domain.KindOrder,
		Target: domain.Target{
			Package: "example.com/app", RelDir: "internal/app",
			Victim: "TestOrder", VictimFile: "app_test.go", VictimLine: 42,
		},
		Witness: domain.Witness{
			Kind:      domain.KindOrder,
			Victim:    domain.TestID{Package: "example.com/app", Name: "TestOrder"},
			Polluters: []domain.TestID{{Package: "example.com/app", Name: "TestSeed"}},
		},
		Repro: domain.Repro{Command: "go test internal/app -run 'TestGoFlakeHunterWitness$' -count=1"},
	}

	var buf strings.Builder
	if err := report.SARIF(&buf, []domain.Capsule{c}, "v0.2.0"); err != nil {
		t.Fatal(err)
	}

	var doc sarifShape
	if err := json.Unmarshal([]byte(buf.String()), &doc); err != nil {
		t.Fatalf("not valid JSON: %v\n%s", err, buf.String())
	}
	if doc.Version != "2.1.0" {
		t.Errorf("version = %q", doc.Version)
	}
	if len(doc.Runs) != 1 || doc.Runs[0].Tool.Driver.Name != "go-flake-hunter" {
		t.Fatalf("runs/tool: %+v", doc.Runs)
	}
	if len(doc.Runs[0].Tool.Driver.Rules) != 5 {
		t.Errorf("the four flake classes must all be declared as rules")
	}
	res := doc.Runs[0].Results
	if len(res) != 1 {
		t.Fatalf("results = %d, want 1", len(res))
	}
	if res[0].RuleID != "go-flake-hunter/order" || res[0].Level != "warning" {
		t.Errorf("result = %+v", res[0])
	}
	if !strings.Contains(res[0].Message.Text, "TestSeed") ||
		!strings.Contains(res[0].Message.Text, "reproduce: go test") {
		t.Errorf("message must carry polluter and the reproduce command: %q", res[0].Message.Text)
	}
	loc := res[0].Locations
	if len(loc) != 1 {
		t.Fatalf("victim location missing: %+v", res[0])
	}
	p := loc[0].Physical
	if p.Artifact.URI != "internal/app/app_test.go" || p.Artifact.URIBaseID != "%SRCROOT%" {
		t.Errorf("artifact = %+v, want internal/app/app_test.go @ %%SRCROOT%%", p.Artifact)
	}
	if p.Region.StartLine != 42 {
		t.Errorf("startLine = %d, want 52", p.Region.StartLine)
	}
}

// A capsule without a static position (old capsule, or a victim the scan did
// not see) must still appear — message-only, in the alert list. Silence would
// hide a real finding from the code-scanning UI.
func TestSARIFKeepsResultsWithoutLocation(t *testing.T) {
	c := domain.Capsule{
		Kind:    domain.KindEnv,
		Target:  domain.Target{Package: "p", Victim: "TestEnv"},
		Witness: domain.Witness{Kind: domain.KindEnv, EnvDiff: []string{"FIXTURE_MODE"}},
	}
	var buf strings.Builder
	if err := report.SARIF(&buf, []domain.Capsule{c}, ""); err != nil {
		t.Fatal(err)
	}
	var doc sarifShape
	if err := json.Unmarshal([]byte(buf.String()), &doc); err != nil {
		t.Fatal(err)
	}
	res := doc.Runs[0].Results
	if len(res) != 1 || len(res[0].Locations) != 0 {
		t.Fatalf("want one location-less result, got %+v", res)
	}
	if !strings.Contains(res[0].Message.Text, "FIXTURE_MODE") {
		t.Errorf("env key missing from message: %q", res[0].Message.Text)
	}
}

// sarifShape mirrors only the fields the tests read: asserting the whole spec
// would couple these tests to SARIF features we never emit.
type sarifShape struct {
	Version string `json:"version"`
	Runs    []struct {
		Tool struct {
			Driver struct {
				Name  string `json:"name"`
				Rules []struct {
					ID string `json:"id"`
				} `json:"rules"`
			} `json:"driver"`
		} `json:"tool"`
		Results []struct {
			RuleID  string `json:"ruleId"`
			Level   string `json:"level"`
			Message struct {
				Text string `json:"text"`
			} `json:"message"`
			Locations []struct {
				Physical struct {
					Artifact struct {
						URI       string `json:"uri"`
						URIBaseID string `json:"uriBaseId"`
					} `json:"artifactLocation"`
					Region struct {
						StartLine int `json:"startLine"`
					} `json:"region"`
				} `json:"physicalLocation"`
			} `json:"locations"`
		} `json:"results"`
	} `json:"runs"`
}

func TestSuspectedSARIFIsReviewNote(t *testing.T) {
	c := domain.Capsule{Status: domain.StatusSuspected, Kind: domain.KindParallel, Target: domain.Target{Victim: "TestTiming"}, Observation: domain.Observe(3, 2)}
	var out strings.Builder
	if err := report.SARIF(&out, []domain.Capsule{c}, "test"); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"kind": "review"`, `"level": "note"`, `suspected flaky`} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("missing %s: %s", want, out.String())
		}
	}
}
