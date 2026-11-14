package report

import (
	"encoding/json"
	"io"
	"path"
	"strings"

	"github.com/emrelab/go-flake-hunter/internal/domain"
)

// SARIF 2.1.0 is the output format GitHub code scanning consumes: every found
// flake becomes an inline annotation on the PR diff (#4), which is where
// developers actually notice tooling — not in a job log someone has to open.
//
// SARIF is an output format, not a policy: the exit-code contract of hunt is
// identical in every format, and the upload step stays opt-in in the action.
// Findings annotate; they never block.
//
// A result without a file/line (capsule from a run without static positions)
// is still emitted: GitHub lists message-only results instead of pinning them,
// and being in the alert list beats being invisible.

// SARIF writes the run as a SARIF 2.1.0 document with one run and one tool.
func SARIF(w io.Writer, flakes []domain.Capsule, version string) error {
	results := make([]sarifResult, 0, len(flakes))
	for _, c := range flakes {
		results = append(results, sarifForResult(c))
	}
	doc := sarifDoc{
		Version: "2.1.0",
		Schema:  "https://json.schemastore.org/sarif-2.1.0.json",
		Runs: []sarifRun{{
			Tool: sarifTool{Driver: sarifDriver{
				Name:           "go-flake-hunter",
				Version:        version,
				InformationURI: "https://github.com/emrelab/go-flake-hunter",
				Rules:          sarifRules(),
			}},
			Results: results,
		}},
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(doc)
}

// sarifRules declares the four flake classes up front. Stable ids matter: code
// scanning dedupes alerts by rule + location, and a rule list that changes
// between runs re-opens every alert on every push.
func sarifRules() []sarifRule {
	mk := func(id, short, help string) sarifRule {
		return sarifRule{
			ID:               id,
			ShortDescription: sarifText{short},
			FullDescription:  sarifText{short},
			Help:             sarifText{help},
		}
	}
	return []sarifRule{
		mk("go-flake-hunter/inconclusive", "inconclusive test investigation", "The investigation ended before a witness was confirmed."),
		mk("go-flake-hunter/repeat", "repeat-dependent flake",
			"The test fails when run again in the same process: state leaks between runs of the same test. Reproduce with `go test -count=N`; go-flake-hunter's capsule records the count that proved it."),
		mk("go-flake-hunter/order", "order-dependent flake",
			"The test fails only after another test (the polluter) has run first: shared package state. The capsule names the minimal polluter set; fix ownership, not the seed."),
		mk("go-flake-hunter/parallel", "parallel-dependent flake",
			"The test fails only when run concurrently with another test: unsynchronized shared state. Run the witness with the -parallel width it records."),
		mk("go-flake-hunter/env", "environment-dependent flake",
			"The test outcome changes with the value of an environment variable the code reads literally. The capsule stores the key, never the value; set it to reproduce."),
	}
}

func sarifForResult(c domain.Capsule) sarifResult {
	r := sarifResult{}
	if c.Status == domain.StatusPartial {
		r = sarifResult{
			RuleID: "go-flake-hunter/inconclusive", Level: "note", Kind: "review",
			Message: sarifText{c.Target.Package + ":" + c.Target.Victim + " is inconclusive: " + c.Note},
		}
	} else {
		r = sarifFlakeResult(c)
	}
	if c.Target.VictimFile != "" && c.Target.VictimLine > 0 {
		// RelDir is the path as the user invoked it (repo-root-relative in CI);
		// %SRCROOT% lets code scanning resolve it against the checkout root.
		uri := path.Join(c.Target.RelDir, c.Target.VictimFile)
		uri = strings.ReplaceAll(uri, "\\", "/")
		r.Locations = []sarifLocation{{Physical: sarifPhysical{
			Artifact: sarifArtifact{URI: uri, URIBaseID: "%SRCROOT%"},
			Region:   sarifRegion{StartLine: c.Target.VictimLine},
		}}}
	}
	return r
}

func sarifFlakeResult(c domain.Capsule) sarifResult {
	v := c.Witness
	var b strings.Builder
	b.WriteString(c.Target.Victim)
	if c.Status == domain.StatusSuspected {
		b.WriteString(" is suspected flaky (class: ")
	} else {
		b.WriteString(" is flaky (class: ")
	}
	b.WriteString(string(c.Kind))
	b.WriteString(")")
	if len(v.Polluters) > 0 {
		b.WriteString(" · polluter: ")
		for i, p := range v.Polluters {
			if i > 0 {
				b.WriteString(", ")
			}
			b.WriteString(p.Name)
		}
	}
	if len(v.EnvDiff) > 0 {
		b.WriteString(" · env: ")
		b.WriteString(strings.Join(v.EnvDiff, ", "))
	}
	if c.Repro.Command != "" {
		b.WriteString("\n\nreproduce: ")
		b.WriteString(c.Repro.Command)
	}

	r := sarifResult{
		RuleID:  "go-flake-hunter/" + string(c.Kind),
		Level:   "warning",
		Message: sarifText{b.String()},
	}
	if c.Status == domain.StatusSuspected {
		r.Level = "note"
		r.Kind = "review"
		r.Properties = map[string]any{"status": c.Status, "observation": c.Observation}
	}
	return r
}

// The shapes below mirror the SARIF 2.1.0 fields we emit; the spec allows
// dozens more, and naming exactly what we write keeps the document honest.

type sarifDoc struct {
	Version string     `json:"version"`
	Schema  string     `json:"$schema"`
	Runs    []sarifRun `json:"runs"`
}

type sarifRun struct {
	Tool    sarifTool     `json:"tool"`
	Results []sarifResult `json:"results"`
}

type sarifTool struct {
	Driver sarifDriver `json:"driver"`
}

type sarifDriver struct {
	Name           string      `json:"name"`
	Version        string      `json:"version,omitempty"`
	InformationURI string      `json:"informationUri,omitempty"`
	Rules          []sarifRule `json:"rules"`
}

type sarifRule struct {
	ID               string    `json:"id"`
	ShortDescription sarifText `json:"shortDescription"`
	FullDescription  sarifText `json:"fullDescription,omitempty"`
	Help             sarifText `json:"help,omitempty"`
}

type sarifResult struct {
	Properties map[string]any  `json:"properties,omitempty"`
	Kind       string          `json:"kind,omitempty"`
	RuleID     string          `json:"ruleId"`
	Level      string          `json:"level"`
	Message    sarifText       `json:"message"`
	Locations  []sarifLocation `json:"locations,omitempty"`
}

type sarifLocation struct {
	Physical sarifPhysical `json:"physicalLocation"`
}

type sarifPhysical struct {
	Artifact sarifArtifact `json:"artifactLocation"`
	Region   sarifRegion   `json:"region"`
}

type sarifArtifact struct {
	URI       string `json:"uri"`
	URIBaseID string `json:"uriBaseId,omitempty"`
}

type sarifRegion struct {
	StartLine int `json:"startLine"`
}

type sarifText struct {
	Text string `json:"text"`
}
