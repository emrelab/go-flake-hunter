package capsule_test

import (
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/emrelab/go-flake-hunter/internal/capsule"
	"github.com/emrelab/go-flake-hunter/internal/domain"
)

// FuzzStoreRead hammers the capsule load path (Store.Read) with arbitrary file
// bytes.
//
// The contract in store.go is "data, not panic": a half-written or hostile JSON
// must come back as a typed error (corrupt / unsupported schema), never as a
// crash, and Read must never accept a foreign schema version. The file lives in
// t.TempDir only; the fuzz body also calls List to prove directory walking
// survives whatever content a file holds.
func FuzzStoreRead(f *testing.F) {
	// A real store-shaped capsule, marshalled exactly like Store.Write does.
	full := domain.Capsule{
		Schema:    domain.SchemaVersion,
		ID:        "gfh_fuzz",
		Kind:      domain.KindRepeat,
		CreatedAt: time.Date(2026, 11, 2, 1, 0, 0, 0, time.UTC),
		Repo:      domain.Repo{Module: "example.com/app", Commit: "abc1234"},
		Target:    domain.Target{Package: "example.com/app/internal/x", Victim: "TestFlake"},
		Observation: domain.Observation{
			Runs: 10, Failures: 7, Deterministic: false, Rate: 0.7,
		},
		Status: domain.StatusProbabilistic,
		Note:   "budget exceeded: 7/10 runs",
	}
	if data, err := json.Marshal(full); err == nil {
		f.Add(data)
		// Truncation: the classic artifact of a process killed mid-write.
		f.Add(data[:len(data)/2])
	}
	seeds := []string{
		`{"schema":"flakesmith/v1","id":"gfh_fuzz","kind":"repeat"}`, // pre-rename, still readable
		`{"schema":"go-flake-hunter/v3","id":"gfh_fuzz"}`,            // foreign version must be rejected
		"{",
		"null",
		"[]",
		`"just a string"`,
		`{"schema":"go-flake-hunter/v2","id":123}`,        // wrong id type
		`{"schema":"go-flake-hunter/v2","createdAt":"x"}`, // unparseable time
		`{"schema":"go-flake-hunter/v2","observation":{"failureRate":"fast"}}`,
	}
	for _, s := range seeds {
		f.Add([]byte(s))
	}

	f.Fuzz(func(t *testing.T, data []byte) {
		dir := t.TempDir()
		store := capsule.NewStore(dir)
		if err := os.WriteFile(store.Path("gfh_fuzz"), data, 0o644); err != nil {
			t.Fatal(err)
		}
		c, err := store.Read("gfh_fuzz")
		if err != nil {
			return // corrupt input rejected as data — that is the contract
		}
		if !domain.ReadableSchema(c.Schema) {
			t.Errorf("Read accepted foreign schema %q with nil error", c.Schema)
		}
		// Listing must survive the same file no matter what it contains.
		ids, err := store.List()
		if err != nil {
			t.Fatalf("List on a corrupt store: %v", err)
		}
		if len(ids) != 1 || ids[0] != "gfh_fuzz" {
			t.Errorf("List = %v, want [gfh_fuzz]", ids)
		}
	})
}
