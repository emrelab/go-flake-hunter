package capsule

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/emrelab/go-flake-hunter/internal/domain"
)

// Store is a directory of capsule JSON files (repo/.go-flake-hunter by default).
type Store struct{ Dir string }

// NewStore binds a store to a directory; it does not create anything.
func NewStore(dir string) Store { return Store{Dir: dir} }

var capsuleID = regexp.MustCompile(`^(gfh|fs)_[a-z0-9]+$`)

// Path is where a capsule with this id lives, or empty for an invalid id.
func (s Store) Path(id string) string {
	if filepath.Base(id) != id || !capsuleID.MatchString(id) {
		return ""
	}
	return filepath.Join(s.Dir, id+".json")
}

func checkStoreDir(dir string) error {
	info, err := os.Lstat(dir)
	if err == nil && !info.IsDir() {
		return domain.Wrap(domain.ErrUsage, "capsule store is not a real directory: %s", dir)
	}
	return err
}

func checkSchema(c domain.Capsule) error {
	if !domain.ReadableSchema(c.Schema) {
		return domain.Wrap(domain.ErrUsage, "capsule schema version not supported: %s (expected %s)", c.Schema, domain.SchemaVersion)
	}
	return nil
}

// Write persists the capsule, creating the directory lazily.
//
// The write goes through a temp file + rename so a killed process can never
// leave a half-written JSON that later fails to parse.
func (s Store) Write(c domain.Capsule) (string, error) {
	final := s.Path(c.ID)
	if final == "" {
		return "", domain.Wrap(domain.ErrUsage, "invalid capsule id %q", c.ID)
	}
	if err := checkSchema(c); err != nil {
		return "", err
	}
	if err := os.MkdirAll(s.Dir, 0o755); err != nil {
		return "", fmt.Errorf("%w: %v", domain.ErrInternal, err)
	}
	if err := checkStoreDir(s.Dir); err != nil {
		return "", err
	}
	if info, err := os.Lstat(final); err == nil && !info.Mode().IsRegular() {
		return "", domain.Wrap(domain.ErrUsage, "capsule is not a regular file: %s", final)
	} else if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return "", err
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return "", fmt.Errorf("%w: %v", domain.ErrInternal, err)
	}
	data = append(data, '\n')

	tmp, err := os.CreateTemp(s.Dir, ".tmp-*.json")
	if err != nil {
		return "", fmt.Errorf("%w: %v", domain.ErrInternal, err)
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return "", fmt.Errorf("%w: %v", domain.ErrInternal, err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return "", fmt.Errorf("%w: %v", domain.ErrInternal, err)
	}
	if err := os.Rename(tmpName, final); err != nil {
		_ = os.Remove(tmpName)
		return "", fmt.Errorf("%w: %v", domain.ErrInternal, err)
	}
	return final, nil
}

// Read loads one capsule by id.
func (s Store) Read(id string) (domain.Capsule, error) {
	var c domain.Capsule
	path := s.Path(id)
	if path == "" {
		return c, domain.Wrap(domain.ErrUsage, "invalid capsule id %q", id)
	}
	data, err := s.readFile(filepath.Base(path))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return c, fmt.Errorf("capsule not found: %s", id)
		}
		return c, err
	}
	if err := json.Unmarshal(data, &c); err != nil {
		return c, fmt.Errorf("%w: capsule is corrupt (%s): %v", domain.ErrInternal, id, err)
	}
	if err := checkSchema(c); err != nil {
		return c, err
	}
	if c.ID != id {
		return c, domain.Wrap(domain.ErrUsage, "capsule id %q does not match store key %q", c.ID, id)
	}
	// The class decides which regression body compiles and which report
	// sentence is true. A hand-edited or truncated file must be refused here,
	// as a usage error the user can act on — not as an "internal error: unknown
	// flake class" three layers down in the generator.
	if !c.Kind.Valid() {
		if c.Status == domain.StatusPartial {
			// Partial capsules predate the kind field being meaningful; they
			// are reported, never compiled.
			return c, nil
		}
		return c, fmt.Errorf("%w: capsule %s has unknown flake class %q", domain.ErrUsage, id, c.Kind)
	}
	if c.Status == domain.StatusConfirmed || c.Status == domain.StatusSuspected {
		if err := c.Witness.ValidateTestNames(); err != nil {
			return c, err
		}
	}
	return c, nil
}

func (s Store) readFile(name string) ([]byte, error) {
	if err := checkStoreDir(s.Dir); err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(s.Dir)
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	info, err := root.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, domain.Wrap(domain.ErrUsage, "capsule is not a regular file: %s", name)
	}
	f, err := root.Open(name)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	opened, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !os.SameFile(info, opened) {
		return nil, domain.Wrap(domain.ErrUsage, "capsule changed while opening: %s", name)
	}
	return io.ReadAll(f)
}

// List returns the ids in the store, newest file first.
func (s Store) List() ([]string, error) {
	if err := checkStoreDir(s.Dir); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	entries, err := os.ReadDir(s.Dir)
	if err != nil {
		return nil, err
	}
	type item struct {
		id  string
		mod int64
	}
	var items []item
	for _, e := range entries {
		id, ok := strings.CutSuffix(e.Name(), ".json")
		if !ok || s.Path(id) == "" || !e.Type().IsRegular() {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		items = append(items, item{
			id:  id,
			mod: info.ModTime().UnixNano(),
		})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].mod > items[j].mod })
	out := make([]string, len(items))
	for i, it := range items {
		out[i] = it.id
	}
	return out, nil
}
