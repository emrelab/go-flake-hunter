package capsule_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/emrelab/go-flake-hunter/internal/capsule"
	"github.com/emrelab/go-flake-hunter/internal/domain"
)

func TestAuditS2_RejectsTraversal(t *testing.T) {
	root := t.TempDir()
	store := capsule.NewStore(filepath.Join(root, "store"))
	if err := os.Mkdir(store.Dir, 0o755); err != nil {
		t.Fatal(err)
	}
	const id = "gfh_0123456789ab"
	const original = `{"schema":"go-flake-hunter/v2","id":"gfh_0123456789ab","kind":"order"}`
	outside := filepath.Join(root, id+".json")
	if err := os.WriteFile(outside, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := store.Read("../" + id); err == nil {
		t.Error("Read accepted a store key pointing outside the store")
	}
	if _, err := store.Write(domain.Capsule{Schema: domain.SchemaVersion, ID: "../" + id}); err == nil {
		t.Error("Write accepted a store key pointing outside the store")
	}
	if got, err := os.ReadFile(outside); err != nil || string(got) != original {
		t.Errorf("outside capsule changed: %s (%v)", got, err)
	}
}

func TestAuditS2_RejectsMismatchedEmbeddedID(t *testing.T) {
	for _, tc := range []struct{ key, embedded string }{
		{"gfh_0123456789ab", "gfh_abcdef012345"},
		{"fs_0123456789ab", "../fs_0123456789ab"},
	} {
		t.Run(tc.key, func(t *testing.T) {
			store := capsule.NewStore(t.TempDir())
			c := domain.Capsule{ID: tc.embedded, Schema: domain.LegacySchema, Kind: domain.KindOrder, Witness: witness()}
			data, err := json.Marshal(c)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(store.Dir, tc.key+".json"), data, 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := store.Read(tc.key); err == nil {
				t.Errorf("Read accepted %q as the ID for %q", tc.embedded, tc.key)
			}
		})
	}
}

func TestAuditS2_RejectsSymlink(t *testing.T) {
	const id = "gfh_0123456789ab"
	parent := t.TempDir()
	outside := filepath.Join(parent, "outside")
	if err := os.Mkdir(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	c := domain.Capsule{ID: id, Schema: domain.SchemaVersion, Kind: domain.KindOrder, Witness: witness()}
	outsideFile, err := capsule.NewStore(outside).Write(c)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	for _, tc := range []struct{ link, target, store string }{
		{filepath.Join(dir, id+".json"), outsideFile, dir},
		{filepath.Join(dir, "storelink"), outside, filepath.Join(dir, "storelink")},
	} {
		t.Run(filepath.Base(tc.link), func(t *testing.T) {
			if err := os.Symlink(tc.target, tc.link); err != nil {
				t.Skipf("symlink unavailable: %v", err)
			}
			store := capsule.NewStore(tc.store)
			if _, err := store.Read(id); err == nil {
				t.Error("Read followed a capsule symlink")
			}
			if _, err := store.Write(c); err == nil {
				t.Error("Write accepted a capsule symlink")
			}
		})
	}
}

func TestAuditS2_RejectsUnknownSchemaAndReadsLegacy(t *testing.T) {
	dir := t.TempDir()
	const id = "fs_0123456789ab"
	store := capsule.NewStore(dir)
	c := domain.Capsule{ID: id, Schema: "go-flake-hunter/v99", Kind: domain.KindOrder, Witness: witness()}
	if _, err := store.Write(c); err == nil {
		t.Error("Write accepted an unsupported schema")
	}
	path := filepath.Join(dir, id+".json")
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatalf("an unsupported schema reached disk: %v", err)
	}
	c.Schema = domain.LegacySchema
	if _, err := store.Write(c); err != nil {
		t.Fatal(err)
	}
	got, err := store.Read(id)
	if err != nil || got.ID != id || got.Schema != domain.LegacySchema {
		t.Fatalf("legacy capsule: got %q %q (%v)", got.ID, got.Schema, err)
	}
}
