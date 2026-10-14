package pmtest

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/shinyvision/posthero/internal/jsonx"
	"github.com/shinyvision/posthero/internal/postman"
)

// The fake also holds posthero's local mode: a workspace's copy on this
// machine, which posthero changes as it would change Postman. It then lives
// in a directory, written as it changes (only what changed: a workspace can
// be big), and answers in-process, without a port.

// savedState is what isn't a collection or an environment.
type savedState struct {
	Clock      time.Time                     `json:"clock"`
	Workspaces []postman.Workspace           `json:"workspaces"`
	Globals    map[string][]postman.EnvValue `json:"globals"`
}

type savedCol struct {
	Workspace  string              `json:"workspace"`
	Collection *postman.Collection `json:"collection"`
}

type savedEnv struct {
	Workspace   string               `json:"workspace"`
	Environment *postman.Environment `json:"environment"`
}

// Open loads the fake kept in dir (an empty one when there is none yet) and
// keeps it there from now on. Its clock is the real one.
func Open(dir string) (*Server, error) {
	s := New()
	s.workspaces = nil
	s.dir, s.realClock, s.itemsMoveTime = dir, true, true
	if data, err := os.ReadFile(filepath.Join(dir, "state.json")); err == nil {
		var st savedState
		if err := json.Unmarshal(data, &st); err != nil {
			return nil, fmt.Errorf("the local copy in %s can't be read: %w", dir, err)
		}
		s.clock, s.workspaces = st.Clock, st.Workspaces
		if st.Globals != nil {
			s.globals = st.Globals
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	files, _ := filepath.Glob(filepath.Join(dir, "collections", "*.json"))
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			return nil, err
		}
		var c savedCol
		if err := json.Unmarshal(data, &c); err != nil || c.Collection == nil {
			return nil, fmt.Errorf("the local copy %s can't be read: %v", f, err)
		}
		s.collections[c.Collection.Info.PostmanID] = &collection{ws: c.Workspace, col: c.Collection}
	}
	files, _ = filepath.Glob(filepath.Join(dir, "environments", "*.json"))
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			return nil, err
		}
		var e savedEnv
		if err := json.Unmarshal(data, &e); err != nil || e.Environment == nil {
			return nil, fmt.Errorf("the local copy %s can't be read: %v", f, err)
		}
		s.envs[e.Environment.ID] = &environment{ws: e.Workspace, env: e.Environment}
	}
	return s, nil
}

// RoundTrip answers a request in-process: a postman.Client can talk to
// the fake without a server.
func (s *Server) RoundTrip(r *http.Request) (*http.Response, error) {
	rec := httptest.NewRecorder()
	s.serve(rec, r)
	resp := rec.Result()
	resp.Request = r
	return resp, nil
}

// SaveErr is the last error writing the fake to its directory, if any.
func (s *Server) SaveErr() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.saveErr
}

// Replace makes a workspace hold exactly these collections, environments
// and globals (copies; collections and items keep their ids).
func (s *Server) Replace(ws postman.Workspace, cols []*postman.Collection, envs []*postman.Environment, globals []postman.EnvValue) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	before := s.versions()
	known := false
	for i, w := range s.workspaces {
		if w.ID == ws.ID {
			s.workspaces[i], known = ws, true
		}
	}
	if !known {
		s.workspaces = append(s.workspaces, ws)
	}
	for id, c := range s.collections {
		if c.ws == ws.ID {
			delete(s.collections, id)
		}
	}
	for id, e := range s.envs {
		if e.ws == ws.ID {
			delete(s.envs, id)
		}
	}
	for _, c := range cols {
		s.putCollection(ws.ID, c.Clone())
	}
	for _, e := range envs {
		s.putEnv(ws.ID, e.Clone())
	}
	s.globals[ws.ID] = append([]postman.EnvValue(nil), globals...)
	s.save(before, true)
	s.changed()
	return s.saveErr
}

// PutCollection stores a collection as it is (a copy, keeping its ids),
// replacing the one with its id.
func (s *Server) PutCollection(ws string, c *postman.Collection) {
	s.mu.Lock()
	defer s.mu.Unlock()
	before := s.versions()
	s.putCollection(ws, c.Clone())
	s.save(before, false)
	s.changed()
}

func (s *Server) putCollection(ws string, c *postman.Collection) {
	id := c.Info.PostmanID
	c.Info.UID = Owner + "-" + id
	c.Info.Schema = postman.SchemaV21
	now := s.now()
	c.Walk(func(it *postman.Item, _ []*postman.Item) bool {
		s.stamp(it, now)
		return true
	})
	// Put is a new version: whoever reads the fake fetches it again.
	c.Info.UpdatedAt = now
	s.collections[id] = &collection{ws: ws, col: c}
}

// RemoveCollection drops a collection by id.
func (s *Server) RemoveCollection(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	before := s.versions()
	delete(s.collections, id)
	s.save(before, false)
	s.changed()
}

// PutEnvironment stores an environment as it is (a copy, keeping its id).
func (s *Server) PutEnvironment(ws string, e *postman.Environment) {
	s.mu.Lock()
	defer s.mu.Unlock()
	before := s.versions()
	s.putEnv(ws, e.Clone())
	s.save(before, false)
	s.changed()
}

func (s *Server) putEnv(ws string, e *postman.Environment) {
	e.UID = Owner + "-" + e.ID
	e.Owner = Owner
	e.UpdatedAt = s.now()
	s.envs[e.ID] = &environment{ws: ws, env: e}
}

// RemoveEnvironment drops an environment by id.
func (s *Server) RemoveEnvironment(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	before := s.versions()
	delete(s.envs, id)
	s.save(before, false)
	s.changed()
}

// PutGlobals replaces a workspace's globals.
func (s *Server) PutGlobals(ws string, values []postman.EnvValue) {
	s.mu.Lock()
	defer s.mu.Unlock()
	before := s.versions()
	s.globals[ws] = append([]postman.EnvValue(nil), values...)
	s.save(before, true)
	s.changed()
}

// Contents returns copies of what a workspace holds.
func (s *Server) Contents(ws string) (cols []*postman.Collection, envs []*postman.Environment, globals []postman.EnvValue) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, c := range s.collections {
		if c.ws == ws {
			cols = append(cols, c.col.Clone())
		}
	}
	for _, e := range s.envs {
		if e.ws == ws {
			envs = append(envs, e.env.Clone())
		}
	}
	return cols, envs, append([]postman.EnvValue(nil), s.globals[ws]...)
}

// versions are when each collection and environment last changed, to tell
// what a request changed.
type versions struct {
	cols, envs map[string]string
	globals    string
}

func (s *Server) versions() versions {
	v := versions{cols: map[string]string{}, envs: map[string]string{}}
	if s.dir == "" {
		return v
	}
	for id, c := range s.collections {
		v.cols[id] = c.col.Info.UpdatedAt
	}
	for id, e := range s.envs {
		v.envs[id] = e.env.UpdatedAt
	}
	data, _ := jsonx.Marshal(s.globals)
	v.globals = string(data)
	return v
}

// save writes what changed since before to the directory: the collections
// and environments that are new, changed or gone, and the rest (the clock,
// the workspaces, the globals) when state says so or the globals changed.
func (s *Server) save(before versions, state bool) {
	if s.dir == "" {
		return
	}
	var errs []error
	write := func(name string, v any) {
		data, err := jsonx.Marshal(v)
		if err == nil {
			err = writeFile(filepath.Join(s.dir, name), data)
		}
		errs = append(errs, err)
	}
	for id, c := range s.collections {
		if at, ok := before.cols[id]; !ok || at != c.col.Info.UpdatedAt {
			write(filepath.Join("collections", fileName(id)), savedCol{Workspace: c.ws, Collection: c.col})
		}
	}
	for id := range before.cols {
		if _, ok := s.collections[id]; !ok {
			errs = append(errs, removeFile(filepath.Join(s.dir, "collections", fileName(id))))
		}
	}
	for id, e := range s.envs {
		if at, ok := before.envs[id]; !ok || at != e.env.UpdatedAt {
			write(filepath.Join("environments", fileName(id)), savedEnv{Workspace: e.ws, Environment: e.env})
		}
	}
	for id := range before.envs {
		if _, ok := s.envs[id]; !ok {
			errs = append(errs, removeFile(filepath.Join(s.dir, "environments", fileName(id))))
		}
	}
	if data, _ := jsonx.Marshal(s.globals); state || string(data) != before.globals {
		write("state.json", savedState{Clock: s.clock, Workspaces: s.workspaces, Globals: s.globals})
	}
	s.saveErr = errors.Join(errs...)
}

func fileName(id string) string {
	return strings.Map(func(r rune) rune {
		if r == '/' || r == '\\' || r == ':' || r == 0 {
			return '_'
		}
		return r
	}, id) + ".json"
}

// writeFile writes a file whole or not at all.
func writeFile(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	_, err = f.Write(data)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(f.Name())
		return err
	}
	return os.Rename(f.Name(), path)
}

func removeFile(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// changed tells OnChange.
func (s *Server) changed() {
	if f := s.OnChange; f != nil {
		go f()
	}
}

// isWrite says a request may change something.
func isWrite(r *http.Request) bool { return r.Method != http.MethodGet && r.Method != http.MethodHead }
