package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/shinyvision/posthero/internal/jsonx"
	"github.com/shinyvision/posthero/internal/postman"
)

// FilesWorkspace is the workspace of collections opened from files.
var FilesWorkspace = postman.Workspace{ID: "files", Name: "Files"}

// OpenFiles opens collection and environment exports (or folders of
// them) instead of a Postman workspace. Changes are written back to the
// files.
func OpenFiles(paths []string, valuesDir string) (*Store, error) {
	s := Open(nil, FilesWorkspace, "", valuesDir)
	var errs []error
	for _, p := range paths {
		fi, err := os.Stat(p)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if fi.IsDir() {
			matches, _ := filepath.Glob(filepath.Join(p, "*.json"))
			for _, m := range matches {
				if err := s.openFile(m); err != nil && !errors.Is(err, errNotPostman) {
					errs = append(errs, err)
				}
			}
			continue
		}
		if err := s.openFile(p); err != nil {
			errs = append(errs, err)
		}
	}
	if len(s.cols) == 0 && len(s.envs) == 0 {
		errs = append(errs, errors.New("no Postman collections or environments in "+strings.Join(paths, ", ")))
		return nil, errors.Join(errs...)
	}
	return s, errors.Join(errs...)
}

var errNotPostman = errors.New("not a Postman file")

func (s *Store) openFile(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(data, &probe); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	// An API response wraps the collection or environment.
	if raw, ok := probe["collection"]; ok {
		data = raw
		_ = json.Unmarshal(raw, &probe)
	} else if raw, ok := probe["environment"]; ok {
		data = raw
		_ = json.Unmarshal(raw, &probe)
	}
	abs, _ := filepath.Abs(path)
	if s.isOpen(abs) {
		return nil // named twice (a file and its folder)
	}
	switch {
	case probe["info"] != nil && probe["item"] != nil:
		var c postman.Collection
		if err := json.Unmarshal(data, &c); err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		id, warning := s.fileID(c.Info.PostmanID, abs)
		giveIDs(&c, id)
		s.cols[id] = &Col{Summary: postman.Summary{ID: id, UID: id, Name: c.Info.Name}, Data: &c, Path: abs}
		return warning
	case probe["values"] != nil && probe["name"] != nil:
		var e postman.Environment
		if err := json.Unmarshal(data, &e); err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		id, warning := s.fileID(e.ID, abs)
		s.envs[id] = &Env{Summary: postman.Summary{ID: id, UID: id, Name: e.Name}, Data: &e, Path: abs}
		return warning
	}
	return fmt.Errorf("%s: %w", path, errNotPostman)
}

// isOpen reports whether a file is open already.
func (s *Store) isOpen(path string) bool {
	for _, c := range s.cols {
		if c.Path == path {
			return true
		}
	}
	for _, e := range s.envs {
		if e.Path == path {
			return true
		}
	}
	return false
}

// fileID is the id a file's collection or environment goes by: its own, or
// one made from the file's path when it has none, or when another file has
// it already (a copy of that file). Both then open, each with current
// values of its own, and the warning says so.
func (s *Store) fileID(own, path string) (string, error) {
	if own == "" {
		return "file-" + hashOf(path), nil
	}
	other := ""
	if c, ok := s.cols[own]; ok {
		other = c.Path
	} else if e, ok := s.envs[own]; ok {
		other = e.Path
	}
	if other == "" {
		return own, nil
	}
	a, b := filepath.Base(path), filepath.Base(other)
	if a == b {
		a, b = path, other
	}
	return "file-" + hashOf(path), fmt.Errorf("%s has the same id as %s (a copy?): posthero keeps them apart, current values too", a, b)
}

func hashOf(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:8])
}

// generatedPrefix marks ids posthero made up for items without one; they
// are not written to the file.
const generatedPrefix = "ph-"

// giveIDs gives items without an id one that stays the same between runs:
// from where they are in the collection.
func giveIDs(c *postman.Collection, colID string) {
	var walk func(items []*postman.Item, path string)
	walk = func(items []*postman.Item, path string) {
		for i, it := range items {
			p := fmt.Sprintf("%s/%d:%s", path, i, it.Name)
			if it.ID == "" {
				it.ID = generatedPrefix + hashOf(colID+p)
			}
			walk(it.Item, p)
		}
	}
	walk(c.Item, "")
}

// NewID makes an id for an item made in a file collection.
func NewID() string { return generatedPrefix + hashOf(fmt.Sprint(Now().UnixNano())) }

// FileData is what a file collection's file should hold now: the
// collection without the ids posthero made up.
func FileData(c *postman.Collection) ([]byte, error) {
	clean := c.Clone()
	clean.Walk(func(it *postman.Item, _ []*postman.Item) bool {
		if strings.HasPrefix(it.ID, generatedPrefix) {
			it.ID = ""
		}
		return true
	})
	if clean.Info.Schema == "" {
		clean.Info.Schema = postman.SchemaV21
	}
	data, err := jsonx.MarshalIndent(clean, "", "\t")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

// EnvFileData is what an environment's file should hold now.
func EnvFileData(e *postman.Environment) ([]byte, error) {
	clean := e.Clone()
	if clean.Extra == nil {
		clean.Extra = postman.Extra{}
	}
	if _, ok := clean.Extra["_postman_variable_scope"]; !ok {
		clean.Extra["_postman_variable_scope"] = json.RawMessage(`"environment"`)
	}
	data, err := jsonx.MarshalIndent(clean, "", "\t")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

// WriteFile writes a collection's or environment's file (data from
// FileData or EnvFileData) atomically, keeping its mode. What the file
// holds already keeps its layout wherever it still means the same: the
// order of keys, empty lists, values written as numbers, how strings are
// escaped, the indentation and an API response's wrapper (see keepLayout).
func WriteFile(path string, data []byte) error {
	mode := os.FileMode(0o644)
	if fi, err := os.Stat(path); err == nil {
		mode = fi.Mode().Perm()
		if old, err := os.ReadFile(path); err == nil {
			data = keepLayout(old, data)
		}
	}
	return writeAtomic(path, data, mode)
}

// writeAtomic writes a file through a temporary file of its own next to it,
// so a reader never sees half of it and two writers never use the same
// temporary file.
func writeAtomic(path string, data []byte, mode os.FileMode) error {
	f, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	_, err = f.Write(data)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Chmod(f.Name(), mode)
	}
	if err == nil {
		err = os.Rename(f.Name(), path)
	}
	if err != nil {
		os.Remove(f.Name())
	}
	return err
}

// Conflict says a request changed in Postman since posthero fetched it.
type Conflict struct {
	At, By string
}

func (c *Conflict) Error() string {
	return "this request was changed in Postman since posthero fetched it (" + c.At + ")"
}

// CheckRequest asks Postman whether a request of a collection (by its uid)
// changed since known (its updatedAt as posthero has it). A nil Conflict
// means it didn't.
func CheckRequest(ctx context.Context, api *postman.Client, collection, itemID, known string) (*Conflict, error) {
	if api == nil || known == "" {
		return nil, nil
	}
	info, err := api.RequestInfo(ctx, collection, itemID)
	if err != nil {
		if postman.IsNotFound(err) {
			return nil, errors.New("this request was deleted in Postman")
		}
		return nil, err
	}
	if info.UpdatedAt != "" && info.UpdatedAt > known {
		return &Conflict{At: info.UpdatedAt, By: info.LastUpdatedBy}, nil
	}
	return nil, nil
}
