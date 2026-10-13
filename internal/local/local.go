// Package local keeps a workspace on this machine: posthero's local mode.
// The copy lives in a fake of the Postman API (pmtest, kept on disk), so
// everything posthero does to a workspace works on it unchanged. Next to it
// is the base: the team's version the copy started from, as of the last
// pull or push. With the base, a pull takes in what the team changed and a
// push sends what changed here, each item three ways: changed here, changed
// in Postman, or both (a conflict).
package local

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/shinyvision/posthero/internal/jsonx"
	"github.com/shinyvision/posthero/internal/postman"
	"github.com/shinyvision/posthero/internal/postman/pmtest"
)

// Copy is a workspace kept locally.
type Copy struct {
	dir string
	ws  postman.Workspace
	srv *pmtest.Server

	mu   sync.Mutex // guards base, and a pull or push from another
	base *Base
}

// Dir is where a workspace's local copy lives under posthero's state
// directory.
func Dir(stateDir, wsID string) string {
	return filepath.Join(stateDir, "local", safeName(wsID))
}

// Exists says a local copy was made in dir.
func Exists(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, "base", "workspace.json"))
	return err == nil
}

// Start makes a local copy of a workspace in dir from the team's version:
// its collections (whole), environments and globals.
func Start(dir string, ws postman.Workspace, cols []Remote, envs []RemoteEnv, globals []postman.EnvValue) (*Copy, error) {
	if err := os.RemoveAll(dir); err != nil {
		return nil, err
	}
	srv, err := pmtest.Open(filepath.Join(dir, "copy"))
	if err != nil {
		return nil, err
	}
	c := &Copy{dir: dir, ws: ws, srv: srv, base: newBase()}
	var lc []*postman.Collection
	for _, r := range cols {
		c.base.setCol(r)
		lc = append(lc, r.Data)
	}
	var le []*postman.Environment
	for _, e := range envs {
		c.base.Envs[e.Data.ID] = &EnvBase{UID: e.UID, UpdatedAt: e.UpdatedAt, Env: e.Data.Clone()}
		le = append(le, e.Data)
	}
	c.base.Globals = append([]postman.EnvValue(nil), globals...)
	if err := srv.Replace(ws, lc, le, globals); err != nil {
		return nil, err
	}
	if err := c.saveBase(); err != nil {
		return nil, err
	}
	return c, nil
}

// Open opens the local copy in dir.
func Open(dir string) (*Copy, error) {
	b, ws, err := loadBase(filepath.Join(dir, "base"))
	if err != nil {
		return nil, err
	}
	srv, err := pmtest.Open(filepath.Join(dir, "copy"))
	if err != nil {
		return nil, err
	}
	return &Copy{dir: dir, ws: ws, srv: srv, base: b}, nil
}

// Client talks to the local copy as to Postman.
func (c *Copy) Client() *postman.Client {
	return postman.NewClientWith(pmtest.Key, "http://local.posthero", c.srv)
}

// OnChange hears (on a goroutine of its own) that posthero changed the
// copy.
func (c *Copy) OnChange(f func()) { c.srv.OnChange = f }

// Workspace is the workspace this is a copy of.
func (c *Copy) Workspace() postman.Workspace { return c.ws }

// SaveErr is the last error keeping the copy on disk, if any.
func (c *Copy) SaveErr() error { return c.srv.SaveErr() }

// Remove deletes the local copy.
func (c *Copy) Remove() error { return os.RemoveAll(c.dir) }

// Remote is a collection as Postman has it.
type Remote struct {
	UID, UpdatedAt string
	Data           *postman.Collection
}

// RemoteEnv is an environment as Postman has it.
type RemoteEnv struct {
	UID, UpdatedAt string
	Data           *postman.Environment
}

// Base is the team's version a local copy started from.
type Base struct {
	Cols    map[string]*ColBase `json:"-"`
	Envs    map[string]*EnvBase `json:"-"`
	Globals []postman.EnvValue  `json:"-"`
}

// ColBase is a collection of the base: the collection itself (Props, its
// items left out) and each item, flat, with where it is.
type ColBase struct {
	UID       string               `json:"uid"`
	UpdatedAt string               `json:"updatedAt"`
	Props     *postman.Collection  `json:"props"`
	Items     map[string]*ItemBase `json:"items"`
}

// ItemBase is an item of the base: its content (without what is in it, if
// a folder) and its place.
type ItemBase struct {
	Item   *postman.Item `json:"item"`
	Folder bool          `json:"folder"`
	Parent string        `json:"parent,omitempty"`
	// Order is the item's position among its parent's items.
	Order int `json:"order"`
}

// EnvBase is an environment of the base.
type EnvBase struct {
	UID       string               `json:"uid"`
	UpdatedAt string               `json:"updatedAt"`
	Env       *postman.Environment `json:"environment"`
}

func newBase() *Base {
	return &Base{Cols: map[string]*ColBase{}, Envs: map[string]*EnvBase{}}
}

func (b *Base) setCol(r Remote) {
	b.Cols[r.Data.Info.PostmanID] = &ColBase{UID: r.UID, UpdatedAt: r.UpdatedAt, Props: props(r.Data), Items: flat(r.Data)}
}

// props is a collection without its items.
func props(c *postman.Collection) *postman.Collection {
	p := c.Clone()
	p.Item = nil
	return p
}

// flat lists a collection's items by id, with where each is.
func flat(c *postman.Collection) map[string]*ItemBase {
	out := map[string]*ItemBase{}
	var walk func(items []*postman.Item, parent string)
	walk = func(items []*postman.Item, parent string) {
		for i, it := range items {
			out[it.ID] = &ItemBase{Item: content(it), Folder: it.Folder, Parent: parent, Order: i}
			if it.Folder {
				walk(it.Item, it.ID)
			}
		}
	}
	walk(c.Item, "")
	return out
}

// content is an item without what is in it.
func content(it *postman.Item) *postman.Item {
	kids := it.Item
	it.Item = nil
	c := it.Clone()
	it.Item = kids
	c.Folder = it.Folder
	return c
}

// volatile are fields that change with every save, whatever changed.
var volatile = []string{"id", "uid", "_postman_id", "owner", "createdAt", "updatedAt", "lastUpdatedBy", "lastRevision", "fork", "_exporter_id"}

// fingerprint is an item's (or a collection's props', or an environment's)
// content to compare: its JSON without ids and times.
func fingerprint(v any) string {
	data, err := jsonx.Marshal(v)
	if err != nil {
		return ""
	}
	var x any
	if json.Unmarshal(data, &x) != nil {
		return string(data)
	}
	var strip func(x any) any
	strip = func(x any) any {
		switch t := x.(type) {
		case map[string]any:
			for _, k := range volatile {
				delete(t, k)
			}
			for k, v := range t {
				t[k] = strip(v)
			}
		case []any:
			for i, v := range t {
				t[i] = strip(v)
			}
		}
		return x
	}
	out, _ := jsonx.Marshal(strip(x))
	return string(out)
}

func sameItem(a, b *postman.Item) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.Folder == b.Folder && fingerprint(a) == fingerprint(b)
}

func sameProps(a, b *postman.Collection) bool {
	if a == nil || b == nil {
		return a == b
	}
	pa, pb := props(a), props(b)
	return fingerprint(pa.Info) == fingerprint(pb.Info) && fingerprint(pa.Auth) == fingerprint(pb.Auth) &&
		fingerprint(pa.Event) == fingerprint(pb.Event) && fingerprint(pa.Variable) == fingerprint(pb.Variable)
}

func sameEnv(a, b *postman.Environment) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.Name == b.Name && sameValues(a.Values, b.Values)
}

// sameValues compares variables; none is none, whether null or [].
func sameValues(a, b []postman.EnvValue) bool {
	if len(a) == 0 || len(b) == 0 {
		return len(a) == len(b)
	}
	return fingerprint(a) == fingerprint(b)
}

// place is where an item is: its parent and the item before it, counting
// only items the two versions compared both have (so adding or removing
// a neighbour doesn't move it).
func place(items map[string]*ItemBase, id string, both func(string) bool) string {
	it := items[id]
	prev, at := "", -1
	for oid, o := range items {
		if oid != id && o.Parent == it.Parent && o.Order < it.Order && o.Order > at && both(oid) {
			prev, at = oid, o.Order
		}
	}
	return it.Parent + "/" + prev
}

// Change is something changed in the local copy that Postman doesn't have
// yet.
type Change struct {
	// Kind is "collection", "folder", "request", "environment" or "globals".
	Kind string
	// What is "new", "changed", "moved" or "deleted".
	What string
	// Col, Item and Env are ids (Col for collections and their items).
	Col, Item, Env string
	Name           string
}

// Changes lists what changed locally since the base.
func (c *Copy) Changes() []Change {
	c.mu.Lock()
	defer c.mu.Unlock()
	cols, envs, globals := c.srv.Contents(c.ws.ID)
	var out []Change
	local := map[string]*postman.Collection{}
	for _, l := range cols {
		local[l.Info.PostmanID] = l
		b := c.base.Cols[l.Info.PostmanID]
		if b == nil {
			out = append(out, Change{Kind: "collection", What: "new", Col: l.Info.PostmanID, Name: l.Info.Name})
			continue
		}
		out = append(out, colChanges(l, b)...)
	}
	for id, b := range c.base.Cols {
		if local[id] == nil {
			out = append(out, Change{Kind: "collection", What: "deleted", Col: id, Name: b.Props.Info.Name})
		}
	}
	have := map[string]bool{}
	for _, e := range envs {
		have[e.ID] = true
		switch b := c.base.Envs[e.ID]; {
		case b == nil:
			out = append(out, Change{Kind: "environment", What: "new", Env: e.ID, Name: e.Name})
		case !sameEnv(e, b.Env):
			out = append(out, Change{Kind: "environment", What: "changed", Env: e.ID, Name: e.Name})
		}
	}
	for id, b := range c.base.Envs {
		if !have[id] {
			out = append(out, Change{Kind: "environment", What: "deleted", Env: id, Name: b.Env.Name})
		}
	}
	if !sameValues(globals, c.base.Globals) {
		out = append(out, Change{Kind: "globals", What: "changed", Name: "Globals"})
	}
	return out
}

// colChanges lists what changed in a collection since its base.
func colChanges(l *postman.Collection, b *ColBase) []Change {
	var out []Change
	id := l.Info.PostmanID
	if !sameProps(l, b.Props) {
		out = append(out, Change{Kind: "collection", What: "changed", Col: id, Name: l.Info.Name})
	}
	li := flat(l)
	both := func(x string) bool { return li[x] != nil && b.Items[x] != nil }
	kind := func(f bool) string {
		if f {
			return "folder"
		}
		return "request"
	}
	for iid, it := range li {
		bi := b.Items[iid]
		switch {
		case bi == nil:
			out = append(out, Change{Kind: kind(it.Folder), What: "new", Col: id, Item: iid, Name: it.Item.Name})
		case !sameItem(it.Item, bi.Item):
			out = append(out, Change{Kind: kind(it.Folder), What: "changed", Col: id, Item: iid, Name: it.Item.Name})
		case place(li, iid, both) != place(b.Items, iid, both):
			out = append(out, Change{Kind: kind(it.Folder), What: "moved", Col: id, Item: iid, Name: it.Item.Name})
		}
	}
	for iid, bi := range b.Items {
		if li[iid] == nil {
			out = append(out, Change{Kind: kind(bi.Folder), What: "deleted", Col: id, Item: iid, Name: bi.Item.Name})
		}
	}
	return out
}

// The base on disk: a file per collection and environment, so a pull or
// push writes only what it changed.

type savedWorkspace struct {
	Workspace postman.Workspace  `json:"workspace"`
	Globals   []postman.EnvValue `json:"globals"`
}

func (c *Copy) saveBase() error {
	dir := filepath.Join(c.dir, "base")
	for _, sub := range []string{"collections", "environments"} {
		if err := os.RemoveAll(filepath.Join(dir, sub)); err != nil {
			return err
		}
	}
	var errs []error
	for id, b := range c.base.Cols {
		errs = append(errs, writeJSON(filepath.Join(dir, "collections", safeName(id)+".json"), b))
	}
	for id, e := range c.base.Envs {
		errs = append(errs, writeJSON(filepath.Join(dir, "environments", safeName(id)+".json"), e))
	}
	errs = append(errs, writeJSON(filepath.Join(dir, "workspace.json"), savedWorkspace{Workspace: c.ws, Globals: c.base.Globals}))
	return errors.Join(errs...)
}

func loadBase(dir string) (*Base, postman.Workspace, error) {
	b := newBase()
	var w savedWorkspace
	if err := readJSON(filepath.Join(dir, "workspace.json"), &w); err != nil {
		return nil, w.Workspace, fmt.Errorf("the local copy's base can't be read: %w", err)
	}
	b.Globals = w.Globals
	files, _ := filepath.Glob(filepath.Join(dir, "collections", "*.json"))
	for _, f := range files {
		var cb ColBase
		if err := readJSON(f, &cb); err != nil || cb.Props == nil {
			return nil, w.Workspace, fmt.Errorf("the local copy's base %s can't be read: %v", f, err)
		}
		b.Cols[cb.Props.Info.PostmanID] = &cb
	}
	files, _ = filepath.Glob(filepath.Join(dir, "environments", "*.json"))
	for _, f := range files {
		var eb EnvBase
		if err := readJSON(f, &eb); err != nil || eb.Env == nil {
			return nil, w.Workspace, fmt.Errorf("the local copy's base %s can't be read: %v", f, err)
		}
		b.Envs[eb.Env.ID] = &eb
	}
	return b, w.Workspace, nil
}

func writeJSON(path string, v any) error {
	data, err := jsonx.Marshal(v)
	if err != nil {
		return err
	}
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

func readJSON(path string, v any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, v)
}

func safeName(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '/' || r == '\\' || r == ':' || r == 0 {
			return '_'
		}
		return r
	}, s)
}
