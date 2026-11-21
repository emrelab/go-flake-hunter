package domain

import "time"

// ScanProgress is an observation of a scan, never an input to its verdict.
type ScanProgress struct {
	Package, Phase                  string
	Finished                        bool
	Completed, Total, Runs, MaxRuns int
	Elapsed                         time.Duration
	Active                          map[string]string
}
