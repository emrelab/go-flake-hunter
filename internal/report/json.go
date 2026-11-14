package report

import (
	"encoding/json"
	"io"

	"github.com/emrelab/go-flake-hunter/internal/domain"
)

// RunReport is the single JSON document `hunt`/`repro` emit.
//
// One document, never a stream of pretty-printed objects: a machine-readable
// run that prints two capsules back to back is not JSON, and a clean run that
// prints nothing at all gives a consumer no way to tell "no flakes" from "the
// tool crashed". Flakes is always an array, empty when nothing was found.
type RunReport struct {
	Schema   string                 `json:"schema"`
	Flakes   []domain.Capsule       `json:"flakes"`
	Runs     int                    `json:"runs"`
	MaxRuns  int                    `json:"maxRuns,omitempty"`
	Packages []domain.PackageResult `json:"packages"`
}

// JSONRun writes the run report.
func JSONRun(w io.Writer, flakes []domain.Capsule, runs, maxRuns int, packages []domain.PackageResult) error {
	if flakes == nil {
		flakes = []domain.Capsule{}
	}
	if packages == nil {
		packages = []domain.PackageResult{}
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(RunReport{
		Schema:   domain.SchemaVersion,
		Flakes:   flakes,
		Runs:     runs,
		MaxRuns:  maxRuns,
		Packages: packages,
	})
}

// JSON writes the capsule as indented JSON (machine-readable CI output).
func JSON(w io.Writer, c domain.Capsule) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(c)
}
