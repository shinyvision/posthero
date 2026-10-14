// Package store keeps the workspace posthero shows: its collections,
// environments and globals as Postman has them (cached on disk, so posthero
// starts at once), the current values on this machine, and the files of
// collections opened from disk. The model is changed only on the UI's
// goroutine; fetching and saving run in the background and hand back what
// to apply.
package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/shinyvision/posthero/internal/jsonx"
	"github.com/shinyvision/posthero/internal/postman"
	"github.com/shinyvision/posthero/internal/vars"
)

// Col is a collection.
type Col struct {
	Summary postman.Summary
	// Data is the whole collection; nil until it was fetched.
	Data *postman.Collection
	// Path is the file a collection opened from disk lives in.
	Path string
}

// Name is the collection's name.
func (c *Col) Name() string {
	if c.Data != nil && c.Data.Info.Name != "" {
		return c.Data.Info.Name
	}
	return c.Summary.Name
}

// Env is an environment.
type Env struct {
	Summary postman.Summary
	Data    *postman.Environment
	Path    string
}

// Name is the environment's name.
func (e *Env) Name() string {
	if e.Data != nil && e.Data.Name != "" {
		return e.Data.Name
	}
	return e.Summary.Name
}

// Store is the workspace.
type Store struct {
	API       postman.API // nil when working on files
	Workspace postman.Workspace
	Values    *Values

	cacheDir string
	versions map[string]uint64
	cols     map[string]*Col
	envs     map[string]*Env
	globals  []postman.EnvValue
	// globalsKnown says the globals were fetched (or there are none to fetch).
	globalsKnown bool
}

// Open makes the store of a workspace, with what was cached last time.
// cacheDir and valuesDir may be empty (nothing kept on disk).
func Open(api postman.API, ws postman.Workspace, cacheDir, valuesDir string) *Store {
	s := &Store{API: api, Workspace: ws, Values: NewValues(valuesDir), cols: map[string]*Col{}, envs: map[string]*Env{}}
	if cacheDir != "" {
		s.cacheDir = filepath.Join(cacheDir, safeName(ws.ID))
		s.loadCache()
	}
	return s
}

func safeName(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '/' || r == '\\' || r == ':' || r == 0 {
			return '_'
		}
		return r
	}, s)
}

// cached is what a cache file holds.
type cachedCol struct {
	Summary postman.Summary     `json:"summary"`
	Data    *postman.Collection `json:"collection"`
}

type cachedEnv struct {
	Summary postman.Summary      `json:"summary"`
	Data    *postman.Environment `json:"environment"`
}

func (s *Store) loadCache() {
	files, _ := filepath.Glob(filepath.Join(s.cacheDir, "collections", "*.json"))
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		var c cachedCol
		if json.Unmarshal(data, &c) == nil && c.Summary.ID != "" {
			s.cols[c.Summary.ID] = &Col{Summary: c.Summary, Data: c.Data}
		}
	}
	files, _ = filepath.Glob(filepath.Join(s.cacheDir, "environments", "*.json"))
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		var e cachedEnv
		if json.Unmarshal(data, &e) == nil && e.Summary.ID != "" {
			s.envs[e.Summary.ID] = &Env{Summary: e.Summary, Data: e.Data}
		}
	}
	if data, err := os.ReadFile(filepath.Join(s.cacheDir, "globals.json")); err == nil {
		if json.Unmarshal(data, &s.globals) == nil {
			s.globalsKnown = true
		}
	}
}

// Cached reports whether anything came from the cache.
func (s *Store) Cached() bool { return len(s.cols) > 0 || len(s.envs) > 0 }

// Collections lists the collections by name.
func (s *Store) Collections() []*Col {
	out := make([]*Col, 0, len(s.cols))
	for _, c := range s.cols {
		out = append(out, c)
	}
	sort.SliceStable(out, func(a, b int) bool {
		na, nb := strings.ToLower(out[a].Name()), strings.ToLower(out[b].Name())
		if na != nb {
			return na < nb
		}
		return out[a].Summary.ID < out[b].Summary.ID
	})
	return out
}

// Col returns a collection by id (or uid).
func (s *Store) Col(id string) *Col {
	if c, ok := s.cols[id]; ok {
		return c
	}
	for _, c := range s.cols {
		if c.Summary.UID == id {
			return c
		}
	}
	return nil
}

// Environments lists the environments by name.
func (s *Store) Environments() []*Env {
	out := make([]*Env, 0, len(s.envs))
	for _, e := range s.envs {
		out = append(out, e)
	}
	sort.SliceStable(out, func(a, b int) bool {
		na, nb := strings.ToLower(out[a].Name()), strings.ToLower(out[b].Name())
		if na != nb {
			return na < nb
		}
		return out[a].Summary.ID < out[b].Summary.ID
	})
	return out
}

// Env returns an environment by id (or uid).
func (s *Store) Env(id string) *Env {
	if e, ok := s.envs[id]; ok {
		return e
	}
	for _, e := range s.envs {
		if e.Summary.UID == id {
			return e
		}
	}
	return nil
}

// Globals returns the workspace's globals.
func (s *Store) Globals() []postman.EnvValue { return s.globals }

// globalsID is the id the globals' current values are kept under.
func (s *Store) globalsID() string { return "globals-" + s.Workspace.ID }

// Scopes makes the variables a request of a collection sees with an
// environment (envID may be empty): local and data (which may be nil),
// then the environment, the collection and the globals with this machine's
// current values.
func (s *Store) Scopes(colID, envID string, local, data *vars.Scope) *vars.Set {
	var env, col *vars.Scope
	if e := s.Env(envID); e != nil && e.Data != nil {
		env = Scope(vars.Environment, e.Name(), EnvInitial(e.Data.Values), s.Values.Get(e.Summary.ID))
	}
	if c := s.Col(colID); c != nil && c.Data != nil {
		col = Scope(vars.Collection, c.Name(), CollectionInitial(c.Data.Variable), s.Values.Get(c.Summary.ID))
	}
	glob := Scope(vars.Global, "Globals", EnvInitial(s.globals), s.Values.Get(s.globalsID()))
	if local == nil {
		local = &vars.Scope{Kind: vars.Local}
	}
	if data == nil {
		data = &vars.Scope{Kind: vars.Data}
	}
	return vars.New(local, data, env, col, glob)
}

// CommitChanges keeps what a send or run changed (the scopes as it
// started, before, and as it left them, after) as this machine's values,
// on top of the values as they are now, so changes made here meanwhile
// stay.
func (s *Store) CommitChanges(before, after *vars.Set, colID, envID string) error {
	now := s.Scopes(colID, envID, nil, nil)
	var errs []error
	for _, kind := range []string{vars.Environment, vars.Collection, vars.Global} {
		b, a, cur := before.Scope(kind), after.Scope(kind), now.Scope(kind)
		if a == nil || cur == nil {
			continue
		}
		changed := false
		for _, v := range a.Vars {
			if old, ok := b.Get(v.Key); !ok || old.Value != v.Value {
				cur.SetTyped(v.Key, v.Value, v.Typed)
				changed = true
			}
		}
		for _, v := range b.Vars {
			if _, ok := a.Get(v.Key); !ok {
				if _, had := cur.Get(v.Key); had {
					cur.Unset(v.Key)
					changed = true
				}
			}
		}
		if !changed {
			continue
		}
		id := colID
		if kind == vars.Environment {
			id = envID
		}
		errs = append(errs, s.SetCurrent(kind, id, cur))
	}
	return errors.Join(errs...)
}

// SetCurrent replaces this machine's values of a scope: kind is
// environment, collection or global; id the environment or collection.
func (s *Store) SetCurrent(kind, id string, now *vars.Scope) error {
	switch kind {
	case vars.Environment:
		if e := s.Env(id); e != nil && e.Data != nil {
			return s.Values.Set(e.Summary.ID, Diff(EnvInitial(e.Data.Values), now))
		}
	case vars.Collection:
		if c := s.Col(id); c != nil && c.Data != nil {
			return s.Values.Set(c.Summary.ID, Diff(CollectionInitial(c.Data.Variable), now))
		}
	case vars.Global:
		return s.Values.Set(s.globalsID(), Diff(EnvInitial(s.globals), now))
	}
	return nil
}

// Plan is what a refresh has to compare with: what the store has now.
type Plan struct {
	versions  map[string]uint64
	Workspace string
	API       postman.API
	Cols      map[string]string // id → updatedAt of what is here
	UIDs      map[string]string // id → uid of what is here
	Envs      map[string]string
	// Loaded says which collections were fetched whole.
	Loaded map[string]bool
	// Recheck are collections (ids) to fetch whatever their updatedAt says,
	// the first first: Postman moves a collection's updatedAt when the
	// collection itself changes, not when what is in it does.
	Recheck []string
	// RecheckEnvs forces an environment fetch after a live change, even if
	// its timestamp hasn't advanced. Keep Envs intact to detect deletions.
	RecheckEnvs []string
	// Globals says to fetch the globals too.
	Globals bool
	// Progress, when set, hears of each collection as it comes in, and
	// first of the list of them: the first load shows them as they come.
	// It is called from the fetch's goroutines.
	Progress func(Progress)
	// Changes restricts a live refresh to these invalidated entities. A nil
	// slice performs the ordinary workspace reconciliation, including lists
	// and timestamp checks. Reconnects always use ordinary reconciliation.
	Changes []postman.Change
}

// Progress is part of a fetch: the collections listed (without their
// requests yet), or one that came in.
type Progress struct {
	Listed   []*Col
	Loaded   *Col
	versions map[string]uint64
}

// ApplyProgress takes in part of a fetch (on the UI's goroutine). It
// reports whether the tree changed.
func (s *Store) ApplyProgress(p Progress) bool {
	changed := false
	for _, c := range p.Listed {
		if p.versions != nil && p.versions["collection/"+c.Summary.ID] != s.versions["collection/"+c.Summary.ID] {
			continue
		}
		if _, ok := s.cols[c.Summary.ID]; !ok {
			s.cols[c.Summary.ID] = c
			changed = true
		}
	}
	if c := p.Loaded; c != nil {
		if p.versions != nil && p.versions["collection/"+c.Summary.ID] != s.versions["collection/"+c.Summary.ID] {
			return changed
		}
		s.cols[c.Summary.ID] = c
		s.cacheCol(c)
		changed = true
	}
	return changed
}

// Plan returns what a refresh needs to know (on the UI's goroutine).
func (s *Store) Plan(globals bool) Plan {
	p := Plan{versions: maps.Clone(s.versions), Workspace: s.Workspace.ID, API: s.API, Cols: map[string]string{}, UIDs: map[string]string{}, Envs: map[string]string{}, Loaded: map[string]bool{}, Globals: globals || !s.globalsKnown}
	if p.versions == nil {
		p.versions = map[string]uint64{}
	}
	for id, c := range s.cols {
		p.Cols[id] = c.Summary.UpdatedAt
		p.UIDs[id] = c.Summary.UID
		p.Loaded[id] = c.Data != nil
	}
	for id, e := range s.envs {
		if e.Data != nil {
			p.Envs[id] = e.Summary.UpdatedAt
		}
	}
	return p
}

// Fetched is what a refresh found.
type Fetched struct {
	versions map[string]uint64
	Cols     []*Col // new or changed
	Envs     []*Env
	Gone     []string // ids of collections no longer there
	GoneEnvs []string
	Globals  []postman.EnvValue
	// HasGlobals says Globals were fetched.
	HasGlobals bool
	// Errors are collections or environments that failed to load.
	Errors []error
}

// Fetch asks Postman what changed since the plan, and fetches it. It runs
// in the background.
func Fetch(ctx context.Context, p Plan) (Fetched, error) {
	f := Fetched{versions: p.versions}
	if p.API == nil {
		return f, nil
	}
	if p.Changes != nil {
		return fetchChanges(ctx, p)
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	// Postman takes a few seconds to list what there is: both lists at
	// once, and meanwhile the collections to recheck.
	var cols, envs []postman.Summary
	var colsErr, envsErr error
	var lists sync.WaitGroup
	lists.Add(2)
	go func() {
		defer lists.Done()
		if cols, colsErr = p.API.Collections(ctx, p.Workspace); colsErr != nil {
			cancel()
		}
	}()
	go func() {
		defer lists.Done()
		if envs, envsErr = p.API.Environments(ctx, p.Workspace); envsErr != nil {
			cancel()
		}
	}()

	type got struct {
		col *postman.Collection
		err error
	}
	var mu sync.Mutex
	var wg sync.WaitGroup
	results := map[string]got{} // by collection id
	asked := map[string]bool{}
	sem := make(chan struct{}, 6) // well within the API's 300 a minute
	// get fetches a collection, in its turn: the ones asked for first go
	// first.
	get := func(id, uid string, sum *postman.Summary) {
		if asked[id] {
			return
		}
		asked[id] = true
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			return
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			c, err := p.API.Collection(ctx, uid)
			mu.Lock()
			results[id] = got{c, err}
			mu.Unlock()
			if err == nil && sum != nil && p.Progress != nil {
				p.Progress(Progress{Loaded: &Col{Summary: *sum, Data: c.Clone()}, versions: p.versions})
			}
		}()
	}
	for _, id := range p.Recheck {
		if uid := p.UIDs[id]; uid != "" && p.Loaded[id] {
			get(id, uid, nil)
		}
	}
	lists.Wait()
	// One failing cancels the other: its error is the one to tell.
	err := colsErr
	if envsErr != nil && (err == nil || errors.Is(err, context.Canceled)) {
		err = envsErr
	}
	if err != nil {
		wg.Wait()
		return f, err
	}
	if p.Progress != nil {
		var listed []*Col
		for _, sum := range cols {
			if !p.Loaded[sum.ID] {
				listed = append(listed, &Col{Summary: sum})
			}
		}
		p.Progress(Progress{Listed: listed, versions: p.versions})
	}
	present := map[string]bool{}
	for _, sum := range cols {
		present[sum.ID] = true
		if at, ok := p.Cols[sum.ID]; ok && at == sum.UpdatedAt && p.Loaded[sum.ID] {
			continue
		}
		get(sum.ID, sum.UID, &sum)
	}
	for id := range p.Cols {
		if !present[id] {
			f.Gone = append(f.Gone, id)
		}
	}
	presentEnv := map[string]bool{}
	for _, sum := range envs {
		presentEnv[sum.ID] = true
		if at, ok := p.Envs[sum.ID]; ok && at == sum.UpdatedAt && !slices.Contains(p.RecheckEnvs, sum.ID) {
			continue
		}
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			continue
		}
		wg.Add(1)
		go func(sum postman.Summary) {
			defer wg.Done()
			defer func() { <-sem }()
			e, err := p.API.Environment(ctx, sum.UID)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				f.Errors = append(f.Errors, fmt.Errorf("environment %s: %w", sum.Name, err))
				return
			}
			f.Envs = append(f.Envs, &Env{Summary: sum, Data: e})
		}(sum)
	}
	for id := range p.Envs {
		if !presentEnv[id] {
			f.GoneEnvs = append(f.GoneEnvs, id)
		}
	}
	if p.Globals {
		g, err := p.API.Globals(ctx, p.Workspace)
		if err == nil {
			f.Globals, f.HasGlobals = g, true
		} else {
			mu.Lock()
			f.Errors = append(f.Errors, fmt.Errorf("globals: %w", err))
			mu.Unlock()
		}
	}
	wg.Wait()
	// What came in, of what is still there, as Postman listed it.
	for _, sum := range cols {
		r, ok := results[sum.ID]
		switch {
		case !ok:
		case r.err != nil:
			f.Errors = append(f.Errors, fmt.Errorf("%s: %w", sum.Name, r.err))
		default:
			f.Cols = append(f.Cols, &Col{Summary: sum, Data: r.col})
		}
	}
	return f, ctx.Err()
}

func writeCacheFile(dir, name string, v any) {
	if dir == "" {
		return
	}
	data, err := jsonx.Marshal(v)
	if err != nil {
		return
	}
	path := filepath.Join(dir, name)
	if os.MkdirAll(filepath.Dir(path), 0o700) != nil {
		return
	}
	_ = writeAtomic(path, data, 0o600)
}

// Changes says what a refresh changed, for the UI to update.
type Changes struct {
	Cols, Envs []string // ids
	Gone       []string
	Globals    bool
}

// Any reports whether anything changed.
func (c Changes) Any() bool {
	return len(c.Cols) > 0 || len(c.Envs) > 0 || len(c.Gone) > 0 || c.Globals
}

// Apply takes in what a refresh fetched (on the UI's goroutine).
func (s *Store) Apply(f Fetched) Changes {
	var ch Changes
	current := func(key string) bool { return f.versions == nil || f.versions[key] == s.versions[key] }
	for _, c := range f.Cols {
		if !current("collection/" + c.Summary.ID) {
			continue
		}
		if reflect.DeepEqual(s.cols[c.Summary.ID], c) {
			continue
		}
		s.cols[c.Summary.ID] = c
		s.changed("collection/" + c.Summary.ID)
		s.cacheCol(c)
		ch.Cols = append(ch.Cols, c.Summary.ID)
	}
	for _, id := range f.Gone {
		if !current("collection/" + id) {
			continue
		}
		s.RemoveCol(id)
		ch.Gone = append(ch.Gone, id)
	}
	for _, e := range f.Envs {
		if !current("environment/" + e.Summary.ID) {
			continue
		}
		if reflect.DeepEqual(s.envs[e.Summary.ID], e) {
			continue
		}
		s.envs[e.Summary.ID] = e
		s.changed("environment/" + e.Summary.ID)
		s.cacheEnv(e)
		ch.Envs = append(ch.Envs, e.Summary.ID)
	}
	for _, id := range f.GoneEnvs {
		if !current("environment/" + id) {
			continue
		}
		s.RemoveEnv(id)
		ch.Gone = append(ch.Gone, id)
	}
	if f.HasGlobals && current("globals") {
		ch.Globals = !sameValues(s.globals, f.Globals)
		if ch.Globals || !s.globalsKnown {
			s.SetGlobals(f.Globals)
		}
	}
	return ch
}

func sameValues(a, b []postman.EnvValue) bool {
	da, _ := jsonx.Marshal(a)
	db, _ := jsonx.Marshal(b)
	return string(da) == string(db)
}

// Touched records that a collection changed through posthero, so the next
// refresh doesn't fetch it again for nothing, and updates the cache.
func (s *Store) Touched(colID, updatedAt string) {
	c := s.Col(colID)
	if c == nil {
		return
	}
	if updatedAt != "" {
		c.Summary.UpdatedAt = updatedAt
	}
	s.changed("collection/" + c.Summary.ID)
	s.cacheCol(c)
}

// PutCol adds or replaces a collection (one made or imported here).
func (s *Store) PutCol(c *Col) {
	s.cols[c.Summary.ID] = c
	s.Touched(c.Summary.ID, "")
}

// RemoveCol drops a collection.
func (s *Store) RemoveCol(id string) {
	if c := s.Col(id); c != nil {
		s.changed("collection/" + c.Summary.ID)
		queueCache(s.cacheDir, filepath.Join("collections", safeName(c.Summary.UID)+".json"), nil)
		delete(s.cols, c.Summary.ID)
	}
}

// PutEnv adds or replaces an environment.
func (s *Store) PutEnv(e *Env) {
	s.envs[e.Summary.ID] = e
	s.changed("environment/" + e.Summary.ID)
	s.cacheEnv(e)
}

// RemoveEnv drops an environment.
func (s *Store) RemoveEnv(id string) {
	if e := s.Env(id); e != nil {
		s.changed("environment/" + e.Summary.ID)
		queueCache(s.cacheDir, filepath.Join("environments", safeName(e.Summary.UID)+".json"), nil)
		delete(s.envs, e.Summary.ID)
	}
}

// SetGlobals replaces the globals.
func (s *Store) SetGlobals(values []postman.EnvValue) {
	s.globals, s.globalsKnown = values, true
	s.changed("globals")
	if s.cacheDir != "" {
		snapshot := (&postman.Environment{Values: values}).Clone()
		queueCache(s.cacheDir, "globals.json", snapshot.Values)
	}
}

// Find looks up an item by id in every collection.
func (s *Store) Find(id string) (*Col, *postman.Item, []*postman.Item) {
	for _, c := range s.cols {
		if c.Data == nil {
			continue
		}
		if it, parents := c.Data.Find(id); it != nil {
			return c, it, parents
		}
	}
	return nil, nil, nil
}

// Now is the clock for timestamps of local changes.
var Now = time.Now
