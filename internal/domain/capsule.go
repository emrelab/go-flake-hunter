package domain

import (
	"math"
	"time"
)

// SchemaVersion is the version of the capsule JSON schema.
// It is increased on breaking changes; old capsules remain readable.
//
// The name inside the schema id is part of the contract, so the rename from
// flakesmith to go-flake-hunter is a version bump even though the document's
// shape did not change: without it a pre-rename capsule would look current and
// silently keep an id computed under the old schema.
const SchemaVersion = "go-flake-hunter/v2"

// LegacySchema is the schema id written before the rename. Those capsules have
// the same shape as SchemaVersion — only the identity string differs — so they
// stay readable; ids are never recomputed for a capsule already on disk.
const LegacySchema = "flakesmith/v1"

// ReadableSchema reports whether a capsule schema id is one this build can read.
func ReadableSchema(schema string) bool {
	return schema == SchemaVersion || schema == LegacySchema
}

// CapsuleStatus is the confidence level of a witness.
type CapsuleStatus string

const (
	// StatusConfirmed reproduced deterministically.
	StatusConfirmed CapsuleStatus = "confirmed"
	// StatusSuspected reproduced in only some independent verification rounds.
	StatusSuspected CapsuleStatus = "suspected"
	// StatusPartial the budget ran out; what was tried is reported.
	StatusPartial CapsuleStatus = "partial"
	// StatusProbabilistic there is an external dependency; determinism is not guaranteed.
	StatusProbabilistic CapsuleStatus = "probabilistic"
)

// VerifyStatus is the outcome of the last `compile --verify` for this capsule.
// It exists because SKIP and false-green are verdicts a CI can misread: a suite
// with one skipped regression prints `ok`. Only a real RED is "verified".
type VerifyStatus string

const (
	// VerifyVerified the artifact ran and went RED: the flake is reproduced.
	VerifyVerified VerifyStatus = "verified"
	// VerifyGreenNotReproduced the artifact ran and stayed GREEN: the witness
	// does not reproduce the flake, the artifact is worthless as written.
	VerifyGreenNotReproduced VerifyStatus = "green-not-reproduced"
	// VerifySkippedMissingEnv the env witness was inert (the variable was not
	// set): verification did not happen. Not a success; not a failure to
	// reproduce — simply not verified.
	VerifySkippedMissingEnv VerifyStatus = "skipped-missing-env"
)

// Repo is the code state the run was performed against.
type Repo struct {
	Module string `json:"module"`
	Commit string `json:"commit"`
	Dirty  bool   `json:"dirty"`
}

// Tool is the tool's identity, for report reproducibility.
type Tool struct {
	Version  string `json:"version"`
	Go       string `json:"go"`
	TargetGo string `json:"targetGo,omitempty"`
	OS       string `json:"os"`
	Arch     string `json:"arch"`
}

// Target is the test under investigation.
type Target struct {
	// Package is the import path (stable identity, survives a different cwd).
	Package string `json:"package"`
	// RelDir is the directory as the user typed it, so the reported
	// reproduce command is runnable from where they invoked go-flake-hunter.
	RelDir string `json:"relDir,omitempty"`
	Victim string `json:"victim"`
	// VictimFile is the base name of the file declaring the victim test
	// (join with RelDir for the path), VictimLine its 1-based line — when the
	// static scan saw the declaration. SARIF annotations need them; old
	// capsules simply omit both (additive field, schema unchanged).
	VictimFile string `json:"victimFile,omitempty"`
	VictimLine int    `json:"victimLine,omitempty"`
	// PolluterRelDir locates the polluter package of a cross-binary witness
	// (#6), as the user can run it from the hunt's cwd. Empty on same-binary
	// capsules — the victim's own directory hosts every step there.
	PolluterRelDir string `json:"polluterRelDir,omitempty"`
}

// Observation is how often it failed across how many runs.
type Observation struct {
	Runs          int                 `json:"runs"`
	Failures      int                 `json:"failures"`
	Deterministic bool                `json:"deterministic"`
	Rate          float64             `json:"failureRate"`
	Wilson95      *ConfidenceInterval `json:"wilson95,omitempty"`
}

// Evidence records observations; it is not a "root cause".
type Evidence struct {
	ExternalDependencies []string            `json:"externalDependencies,omitempty"`
	GlobalsWritten       map[string][]string `json:"globalsWritten,omitempty"`
	GlobalsRead          map[string][]string `json:"globalsRead,omitempty"`
	EnvDiff              []string            `json:"envDiff,omitempty"`
	FilesTouched         []string            `json:"filesTouched,omitempty"`
	Ports                []string            `json:"ports,omitempty"`
	Race                 *string             `json:"race,omitempty"`
	GoroutineTop         []string            `json:"goroutineTop,omitempty"`
}

// Repro is how to reproduce the failure.
type Repro struct {
	Command       string `json:"command"`
	GeneratedTest string `json:"generatedTest,omitempty"`
}

// Capsule is the persistent artifact. The content-addressed ID makes it idempotent.
type Capsule struct {
	Schema      string        `json:"schema"`
	ID          string        `json:"id"`
	CreatedAt   time.Time     `json:"createdAt"`
	Repo        Repo          `json:"repo"`
	Tool        Tool          `json:"tool"`
	Target      Target        `json:"target"`
	Kind        Kind          `json:"kind"`
	Witness     Witness       `json:"witness"`
	Observation Observation   `json:"observation"`
	Evidence    Evidence      `json:"evidence"`
	Repro       Repro         `json:"repro"`
	Status      CapsuleStatus `json:"status"`
	// VerifyStatus records the verdict of the last `compile --verify`. Empty
	// means never verified. Additive field (id and schema unaffected: this is
	// metadata about the artifact, not about the witness).
	VerifyStatus VerifyStatus `json:"verifyStatus,omitempty"`
	// Note explains a non-confirmed status in one user-facing sentence
	// ("budget exceeded: 1/1 runs") or annotates a confirmed capsule whose
	// evidence is weaker than the status suggests ("not reproduced in the
	// race run"). Confirmed capsules with full evidence leave it empty.
	// Additive field: old readers ignore it, the schema version does not change.
	Note string `json:"note,omitempty"`
}

// ConfidenceInterval is a two-sided 95% Wilson score interval for the observed rate.
type ConfidenceInterval struct {
	Lower float64 `json:"lower"`
	Upper float64 `json:"upper"`
}

// Observe summarizes completed independent trials without treating a small sample as certainty.
func Observe(runs, failures int) Observation {
	if runs <= 0 || failures < 0 || failures > runs {
		return Observation{}
	}
	n := float64(runs)
	p := float64(failures) / n
	z := 1.959963984540054
	d := 1 + z*z/n
	center := (p + z*z/(2*n)) / d
	half := z * math.Sqrt(p*(1-p)/n+z*z/(4*n*n)) / d
	return Observation{Runs: runs, Failures: failures, Rate: p, Deterministic: failures == runs, Wilson95: &ConfidenceInterval{Lower: math.Max(0, center-half), Upper: math.Min(1, center+half)}}
}
