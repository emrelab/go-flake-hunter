package capsule_test

import (
	"os"
	"testing"
	"time"

	"github.com/emrelab/go-flake-hunter/internal/capsule"
	"github.com/emrelab/go-flake-hunter/internal/domain"
)

// K5: a fresh clone writes every file with one checkout timestamp, so "newest
// capsule" by mtime alone fell back to directory order. The id breaks the tie,
// and both clones must agree.
func TestAuditK5_ListIsDeterministicOnEqualMtime(t *testing.T) {
	store := capsule.NewStore(t.TempDir())
	for _, id := range []string{"gfh_aaa", "gfh_bbb"} {
		if _, err := store.Write(domain.Capsule{ID: id, Schema: domain.SchemaVersion, Status: domain.StatusPartial, Kind: domain.KindOrder}); err != nil {
			t.Fatal(err)
		}
	}
	stamp := time.Unix(1700000000, 0)
	for _, id := range []string{"gfh_aaa", "gfh_bbb"} {
		if err := os.Chtimes(store.Path(id), stamp, stamp); err != nil {
			t.Fatal(err)
		}
	}
	for i := range 3 {
		ids, err := store.List()
		if err != nil {
			t.Fatal(err)
		}
		if len(ids) != 2 || ids[0] != "gfh_bbb" || ids[1] != "gfh_aaa" {
			t.Fatalf("run %d: order is not deterministic: %v", i+1, ids)
		}
	}
}
