package facts_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/emrelab/go-flake-hunter/internal/facts"
)

// synthPkg writes a package of `files` generated _test.go files into a temp
// dir. Each file carries the signals Scan hunts for — a package-level global
// mutated by tests, an os.Getenv comparison, filesystem calls, t.Run subtests
// and a t.Parallel — so the benchmark measures the real AST walk, not a parse
// of empty bodies. Everything lives under b.TempDir; nothing touches the repo.
func synthPkg(b *testing.B, files int) string {
	b.Helper()
	dir := b.TempDir()
	var sb strings.Builder
	for i := 0; i < files; i++ {
		sb.Reset()
		fmt.Fprintf(&sb, "package bench\n\nimport (\n\t\"os\"\n\t\"testing\"\n)\n\nvar counter%03d int\n", i)
		for j := 0; j < 3; j++ {
			fmt.Fprintf(&sb, `
func TestGen_%03d_%d(t *testing.T) {
	counter%03d++
	if os.Getenv("GO_FLAKE_HUNTER_BENCH") == "on" {
		t.Parallel()
	}
	_, _ = os.CreateTemp(t.TempDir(), "state")
	t.Run("case", func(t *testing.T) {})
}
`, i, j, i)
		}
		name := filepath.Join(dir, fmt.Sprintf("gen%03d_test.go", i))
		if err := os.WriteFile(name, []byte(sb.String()), 0o644); err != nil {
			b.Fatal(err)
		}
	}
	return dir
}

// BenchmarkScan scans generated packages of increasing size (25, 100, 400 test
// files). Generation happens once per size outside the timed region; the loop
// measures readdir + parse + the global/env/FS analysis walk.
func BenchmarkScan(b *testing.B) {
	for _, n := range []int{25, 100, 400} {
		dir := synthPkg(b, n)
		b.Run(fmt.Sprintf("files=%d", n), func(b *testing.B) {
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				f, err := facts.Scan(dir)
				if err != nil {
					b.Fatal(err)
				}
				if len(f.Files) != n || len(f.Clause) != n*3 {
					b.Fatalf("scanned %d files / %d tests, want %d / %d",
						len(f.Files), len(f.Clause), n, n*3)
				}
			}
		})
	}
}
