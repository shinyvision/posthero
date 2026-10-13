// Package pmtest is a fake Postman API that keeps its state in memory. It
// answers the endpoints posthero uses, the way the real API does, so tests
// (and posthero --demo) can read and change collections and environments
// without touching a real workspace.
package pmtest

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/shinyvision/posthero/internal/postman"
)

// Owner is the user id the fake gives everything it makes: a teammate's.
// The API key is another user's, Caller's, so as in the real API a bare id
// (looked up in the caller's own account) finds nothing: everything has to
// be named by its uid.
const Owner = "1000"

// Caller is the user the API key belongs to.
const Caller = "2000"

// Key is the API key the fake accepts.
const Key = "PMAK-test"

// Server is the fake.
type Server struct {
	mu          sync.Mutex
	srv         *httptest.Server
	workspaces  []postman.Workspace
	collections map[string]*collection // by id
	envs        map[string]*environment
	globals     map[string][]postman.EnvValue // by workspace id
	clock       time.Time
	// Calls counts requests by "METHOD /path" (without the query), for tests
	// that check what posthero asked for.
	Calls map[string]int
	// Fail makes the next request matching "METHOD /path" fail with this
	// status, once.
	Fail map[string]int

	// dir keeps the fake on disk (see Open), with the real clock.
	dir       string
	realClock bool
	saveErr   error
	// OnChange hears (on a goroutine of its own) that a request changed
	// something.
	OnChange func()
}

type collection struct {
	ws  string
	col *postman.Collection
}

type environment struct {
	ws  string
	env *postman.Environment
}

// New returns an empty fake with one team workspace, "Team" (id "ws1").
func New() *Server {
	return &Server{
		workspaces:  []postman.Workspace{{ID: "ws1", Name: "Team", Type: "team", Visibility: "team"}},
		collections: map[string]*collection{},
		envs:        map[string]*environment{},
		globals:     map[string][]postman.EnvValue{},
		clock:       time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC),
		Calls:       map[string]int{},
		Fail:        map[string]int{},
	}
}

// Start serves the fake and returns its URL.
func (s *Server) Start() string {
	s.srv = httptest.NewServer(http.HandlerFunc(s.serve))
	return s.srv.URL
}

// Close stops serving.
func (s *Server) Close() {
	if s.srv != nil {
		s.srv.Close()
	}
}

// AddWorkspace adds a workspace.
func (s *Server) AddWorkspace(id, name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.workspaces = append(s.workspaces, postman.Workspace{ID: id, Name: name, Type: "team", Visibility: "team"})
}

// AddCollection stores a collection in a workspace, giving it and its items
// ids where they have none, and returns its id.
func (s *Server) AddCollection(ws string, c *postman.Collection) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.addCollection(ws, c.Clone())
}

func (s *Server) addCollection(ws string, c *postman.Collection) string {
	if c.Info.PostmanID == "" {
		c.Info.PostmanID = newID()
	}
	id := c.Info.PostmanID
	c.Info.UID = Owner + "-" + id
	c.Info.Schema = postman.SchemaV21
	now := s.now()
	if c.Info.CreatedAt == "" {
		c.Info.CreatedAt = now
	}
	c.Info.UpdatedAt = now
	c.Walk(func(it *postman.Item, _ []*postman.Item) bool {
		s.stamp(it, now)
		return true
	})
	s.collections[id] = &collection{ws: ws, col: c}
	return id
}

func (s *Server) stamp(it *postman.Item, now string) {
	if it.ID == "" {
		it.ID = newID()
	}
	it.UID = Owner + "-" + it.ID
	if it.CreatedAt == "" {
		it.CreatedAt = now
	}
	if it.UpdatedAt == "" {
		it.UpdatedAt = now
	}
	for _, r := range it.Response {
		if r.ID == "" {
			r.ID = newID()
		}
		r.UID = Owner + "-" + r.ID
	}
}

// AddEnvironment stores an environment in a workspace and returns its id.
func (s *Server) AddEnvironment(ws string, e *postman.Environment) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	e = e.Clone()
	if e.ID == "" {
		e.ID = newID()
	}
	e.UID = Owner + "-" + e.ID
	e.Owner = Owner
	e.UpdatedAt = s.now()
	s.envs[e.ID] = &environment{ws: ws, env: e}
	return e.ID
}

// SetGlobals replaces a workspace's globals.
func (s *Server) SetGlobals(ws string, values []postman.EnvValue) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.globals[ws] = values
}

// Collection returns a copy of a stored collection, nil when there is none.
func (s *Server) Collection(id string) *postman.Collection {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := s.anyCollection(id)
	if c == nil {
		return nil
	}
	return c.col.Clone()
}

// Environment returns a copy of a stored environment.
func (s *Server) Environment(id string) *postman.Environment {
	s.mu.Lock()
	defer s.mu.Unlock()
	e := s.anyEnv(id)
	if e == nil {
		return nil
	}
	return e.env.Clone()
}

// Globals returns a workspace's globals.
func (s *Server) Globals(ws string) []postman.EnvValue {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]postman.EnvValue(nil), s.globals[ws]...)
}

// Edit changes a stored collection as a teammate would, marking it and the
// item it returns true for as updated.
func (s *Server) Edit(id string, f func(c *postman.Collection) *postman.Item) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := s.anyCollection(id)
	if c == nil {
		return
	}
	it := f(c.col)
	now := s.now()
	c.col.Info.UpdatedAt = now
	if it != nil {
		it.UpdatedAt = now
	}
}

// now moves the clock on a second, so every change has its own time (and
// to now, with the real clock).
func (s *Server) now() string {
	s.clock = s.clock.Add(time.Second)
	if t := time.Now().UTC(); s.realClock && t.After(s.clock) {
		s.clock = t
	}
	return s.clock.Format("2006-01-02T15:04:05.000Z")
}

func newID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	h := hex.EncodeToString(b)
	return h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
}

// own turns a uid of the fake's into an id. A bare id is looked up in the
// caller's account, where nothing is: ok is false, as the real API answers
// 404.
func own(uid string) (string, bool) {
	owner, id := postman.SplitUID(uid)
	if owner != Owner {
		return "", false
	}
	return id, true
}

// anyCollection finds a collection by id or uid, for tests (not the API).
func (s *Server) anyCollection(ref string) *collection {
	if c := s.findCollection(ref); c != nil {
		return c
	}
	return s.collections[ref]
}

// anyEnv finds an environment by id or uid, for tests.
func (s *Server) anyEnv(ref string) *environment {
	if e := s.findEnv(ref); e != nil {
		return e
	}
	return s.envs[ref]
}

func (s *Server) findCollection(uid string) *collection {
	id, ok := own(uid)
	if !ok {
		return nil
	}
	return s.collections[id]
}

func (s *Server) findEnv(uid string) *environment {
	id, ok := own(uid)
	if !ok {
		return nil
	}
	return s.envs[id]
}

type apiErr struct {
	status        int
	name, message string
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := r.Method + " " + r.URL.Path
	s.Calls[key]++
	if status, ok := s.Fail[key]; ok {
		delete(s.Fail, key)
		writeJSON(w, status, map[string]any{"error": map[string]any{"name": "testFailure", "message": "made to fail"}})
		return
	}
	if r.Header.Get("X-Api-Key") != Key {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": map[string]any{"name": "AuthenticationError", "message": "Invalid API Key. Every request requires a valid API Key to be sent."}})
		return
	}
	body, _ := io.ReadAll(r.Body)
	if isWrite(r) && s.dir != "" {
		defer s.save(s.versions(), false)
	}
	if f := s.OnChange; isWrite(r) && f != nil {
		defer func() { go f() }()
	}
	out, err := s.route(r, strings.Split(strings.Trim(r.URL.Path, "/"), "/"), body)
	if err != nil {
		writeJSON(w, err.status, map[string]any{"error": map[string]any{"name": err.name, "message": err.message}})
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func notFound(what string) *apiErr {
	return &apiErr{http.StatusNotFound, "instanceNotFoundError", "We could not find the " + what + " you are looking for"}
}

func badRequest(msg string) *apiErr {
	return &apiErr{http.StatusBadRequest, "malformedRequestError", msg}
}

func (s *Server) route(r *http.Request, parts []string, body []byte) (any, *apiErr) {
	q := r.URL.Query()
	switch {
	case r.Method == http.MethodGet && len(parts) == 1 && parts[0] == "me":
		return map[string]any{"user": map[string]any{"id": 2000, "username": "rachel", "email": "rachel@example.com", "fullName": "Rachel Example", "teamName": "Team"}}, nil
	case r.Method == http.MethodGet && len(parts) == 1 && parts[0] == "workspaces":
		return map[string]any{"workspaces": s.workspaces}, nil
	case len(parts) == 3 && parts[0] == "workspaces" && parts[2] == "global-variables":
		return s.globalsRoute(r, parts[1], body)
	case len(parts) >= 1 && parts[0] == "collections":
		return s.collectionsRoute(r, parts[1:], q.Get("workspace"), body)
	case len(parts) == 1 && (parts[0] == "collection-requests-transfers" || parts[0] == "collection-folders-transfers"):
		return s.transfer(parts[0] == "collection-folders-transfers", body)
	case len(parts) >= 1 && parts[0] == "environments":
		return s.environmentsRoute(r, parts[1:], q.Get("workspace"), body)
	}
	return nil, notFound("endpoint")
}

func (s *Server) globalsRoute(r *http.Request, ws string, body []byte) (any, *apiErr) {
	switch r.Method {
	case http.MethodGet:
		values := s.globals[ws]
		if values == nil {
			values = []postman.EnvValue{}
		}
		return map[string]any{"values": values}, nil
	case http.MethodPut:
		var in struct {
			Values []postman.EnvValue `json:"values"`
		}
		if json.Unmarshal(body, &in) != nil {
			return nil, badRequest("values must be a list")
		}
		s.globals[ws] = in.Values
		return map[string]any{"values": in.Values}, nil
	}
	return nil, notFound("endpoint")
}

func (s *Server) collectionsRoute(r *http.Request, parts []string, ws string, body []byte) (any, *apiErr) {
	if len(parts) == 0 {
		switch r.Method {
		case http.MethodGet:
			list := []postman.Summary{}
			for id, c := range s.collections {
				if ws == "" || c.ws == ws {
					list = append(list, postman.Summary{ID: id, UID: c.col.Info.UID, Name: c.col.Info.Name, Owner: Owner, UpdatedAt: c.col.Info.UpdatedAt})
				}
			}
			sortSummaries(list)
			return map[string]any{"collections": list}, nil
		case http.MethodPost:
			var in struct {
				Collection *postman.Collection `json:"collection"`
			}
			if json.Unmarshal(body, &in) != nil || in.Collection == nil {
				return nil, badRequest("collection is required")
			}
			in.Collection.Info.PostmanID = ""
			in.Collection.Walk(func(it *postman.Item, _ []*postman.Item) bool {
				it.ID, it.UID = "", ""
				return true
			})
			if ws == "" {
				ws = s.workspaces[0].ID
			}
			id := s.addCollection(ws, in.Collection)
			c := s.collections[id].col
			return map[string]any{"collection": map[string]any{"id": id, "name": c.Info.Name, "uid": c.Info.UID}}, nil
		}
		return nil, notFound("endpoint")
	}
	c := s.findCollection(parts[0])
	if len(parts) >= 2 {
		// Item paths name the collection by its bare id.
		if cc := s.collections[parts[0]]; cc != nil {
			c = cc
		}
	}
	if c == nil {
		return nil, notFound("collection")
	}
	if len(parts) == 1 {
		switch r.Method {
		case http.MethodGet:
			return map[string]any{"collection": c.col}, nil
		case http.MethodPatch:
			return s.patchCollection(c, body)
		case http.MethodDelete:
			delete(s.collections, c.col.Info.PostmanID)
			return map[string]any{"collection": map[string]any{"id": c.col.Info.PostmanID, "uid": c.col.Info.UID}}, nil
		}
		return nil, notFound("endpoint")
	}
	if parts[0] != c.col.Info.PostmanID && len(parts) >= 2 {
		// As the real API: item endpoints want the id, not the uid.
		return nil, &apiErr{http.StatusBadRequest, "changeParentError", "You can't update the parent. Use transfer instead."}
	}
	kind := parts[1]
	var id string
	if len(parts) >= 3 {
		var ok bool
		if id, ok = own(parts[2]); !ok {
			return nil, notFound(strings.TrimSuffix(kind, "s"))
		}
	}
	switch kind {
	case "requests":
		return s.requestRoute(r, c, id, body)
	case "folders":
		return s.folderRoute(r, c, id, body)
	case "responses":
		return s.responseRoute(r, c, id, body)
	}
	return nil, notFound("endpoint")
}

func (s *Server) patchCollection(c *collection, body []byte) (any, *apiErr) {
	var in struct {
		Collection map[string]json.RawMessage `json:"collection"`
	}
	if json.Unmarshal(body, &in) != nil || in.Collection == nil {
		return nil, badRequest("collection is required")
	}
	col := c.col
	if raw, ok := in.Collection["info"]; ok {
		var info struct {
			Name        *string `json:"name"`
			Description *string `json:"description"`
		}
		_ = json.Unmarshal(raw, &info)
		if info.Name != nil {
			col.Info.Name = *info.Name
		}
		if info.Description != nil {
			col.Info.Description = &postman.Description{Content: *info.Description}
		}
	}
	if raw, ok := in.Collection["auth"]; ok {
		var a postman.Auth
		_ = json.Unmarshal(raw, &a)
		col.Auth = &a
	}
	if _, ok := in.Collection["event"]; ok {
		return nil, badRequest("Invalid parameters: ('event') for key: 'collection'")
	}
	if raw, ok := in.Collection["events"]; ok {
		var events []postman.Event
		_ = json.Unmarshal(raw, &events)
		for _, e := range events {
			if e.Script.ID == "" {
				return nil, badRequest("Parameters required: ('id') for key: 'collection.events.script'")
			}
		}
		col.Event = events
	}
	if raw, ok := in.Collection["variable"]; ok {
		col.Variable = nil
		_ = json.Unmarshal(raw, &col.Variable)
	}
	col.Info.UpdatedAt = s.now()
	return map[string]any{"collection": map[string]any{"id": col.Info.PostmanID, "name": col.Info.Name}}, nil
}

func (s *Server) touched(c *collection, it *postman.Item) {
	now := s.now()
	c.col.Info.UpdatedAt = now
	if it != nil {
		it.UpdatedAt = now
	}
}

func (s *Server) requestRoute(r *http.Request, c *collection, id string, body []byte) (any, *apiErr) {
	switch {
	case r.Method == http.MethodPost && id == "":
		it := &postman.Item{}
		if err := applyRequest(it, body); err != nil {
			return nil, badRequest(err.Error())
		}
		var parent *postman.Item
		if f := r.URL.Query().Get("folder"); f != "" {
			f, _ = own(f)
			parent, _ = c.col.Find(f)
			if parent == nil || !parent.Folder {
				return nil, notFound("folder")
			}
		}
		s.stamp(it, s.now())
		list := c.col.Container(parent)
		c.col.Insert(parent, region(*list, len(*list), false), it)
		s.touched(c, it)
		return itemData(c, it), nil
	}
	it, _ := c.col.Find(id)
	if it == nil || it.Folder {
		return nil, notFound("request")
	}
	switch r.Method {
	case http.MethodGet:
		return itemData(c, it), nil
	case http.MethodPut:
		if err := applyRequest(it, body); err != nil {
			return nil, badRequest(err.Error())
		}
		s.touched(c, it)
		return itemData(c, it), nil
	case http.MethodDelete:
		c.col.Remove(id)
		s.touched(c, nil)
		return map[string]any{"model_id": id, "meta": map[string]any{"model": "request", "action": "destroy"}, "data": map[string]any{"id": id, "owner": Owner}}, nil
	}
	return nil, notFound("endpoint")
}

func (s *Server) folderRoute(r *http.Request, c *collection, id string, body []byte) (any, *apiErr) {
	if r.Method == http.MethodPost && id == "" {
		it := &postman.Item{Folder: true}
		var in struct {
			Folder string `json:"folder"`
		}
		_ = json.Unmarshal(body, &in)
		if err := applyFolder(it, body); err != nil {
			return nil, badRequest(err.Error())
		}
		var parent *postman.Item
		if in.Folder != "" {
			f, _ := own(in.Folder)
			parent, _ = c.col.Find(f)
			if parent == nil || !parent.Folder {
				return nil, notFound("folder")
			}
		}
		s.stamp(it, s.now())
		// Folders come before requests, as Postman lists them.
		list := c.col.Container(parent)
		at := 0
		for at < len(*list) && (*list)[at].Folder {
			at++
		}
		c.col.Insert(parent, at, it)
		s.touched(c, it)
		return map[string]any{"data": map[string]any{"id": it.ID, "name": it.Name, "folder": in.Folder, "collection": c.col.Info.PostmanID}}, nil
	}
	it, _ := c.col.Find(id)
	if it == nil || !it.Folder {
		return nil, notFound("folder")
	}
	switch r.Method {
	case http.MethodGet:
		return map[string]any{"data": map[string]any{"id": it.ID, "name": it.Name}}, nil
	case http.MethodPut:
		if err := applyFolder(it, body); err != nil {
			return nil, badRequest(err.Error())
		}
		s.touched(c, it)
		return map[string]any{"data": map[string]any{"id": it.ID, "name": it.Name}}, nil
	case http.MethodDelete:
		c.col.Remove(id)
		s.touched(c, nil)
		return map[string]any{"model_id": id, "meta": map[string]any{"model": "folder", "action": "destroy"}, "data": map[string]any{"id": id, "owner": Owner}}, nil
	}
	return nil, notFound("endpoint")
}

func (s *Server) responseRoute(r *http.Request, c *collection, id string, body []byte) (any, *apiErr) {
	if r.Method == http.MethodPost && id == "" {
		rid, _ := own(r.URL.Query().Get("request"))
		req, _ := c.col.Find(rid)
		if req == nil || req.Folder {
			return nil, notFound("request")
		}
		resp := &postman.Response{ID: newID()}
		resp.UID = Owner + "-" + resp.ID
		applyResponse(resp, body)
		req.Response = append(req.Response, resp)
		s.touched(c, req)
		return map[string]any{"data": map[string]any{"id": resp.ID, "name": resp.Name, "request": req.ID}}, nil
	}
	var owner *postman.Item
	var found *postman.Response
	c.col.Walk(func(it *postman.Item, _ []*postman.Item) bool {
		for _, resp := range it.Response {
			if resp.ID == id {
				owner, found = it, resp
			}
		}
		return true
	})
	if found == nil {
		return nil, notFound("response")
	}
	switch r.Method {
	case http.MethodPut:
		applyResponse(found, body)
		s.touched(c, owner)
		return map[string]any{"data": map[string]any{"id": found.ID, "name": found.Name}}, nil
	case http.MethodDelete:
		for i, resp := range owner.Response {
			if resp.ID == id {
				owner.Response = append(owner.Response[:i], owner.Response[i+1:]...)
				break
			}
		}
		s.touched(c, owner)
		return map[string]any{"model_id": id, "meta": map[string]any{"model": "response", "action": "destroy"}}, nil
	}
	return nil, notFound("endpoint")
}

func (s *Server) transfer(folders bool, body []byte) (any, *apiErr) {
	var in struct {
		IDs    []string `json:"ids"`
		Mode   string   `json:"mode"`
		Target struct {
			ID    string `json:"id"`
			Model string `json:"model"`
		} `json:"target"`
		Location struct {
			ID       string `json:"id"`
			Model    string `json:"model"`
			Position string `json:"position"`
		} `json:"location"`
	}
	if json.Unmarshal(body, &in) != nil || len(in.IDs) == 0 {
		return nil, badRequest("ids are required")
	}
	var out []string
	for _, uid := range in.IDs {
		id, _ := own(uid)
		var src *collection
		var it *postman.Item
		for _, c := range s.collections {
			if found, _ := c.col.Find(id); found != nil {
				src, it = c, found
				break
			}
		}
		if it == nil || it.Folder != folders {
			return nil, notFound("item")
		}
		// As in Postman: a request goes before or after a request, a folder
		// before or after a folder.
		if p := in.Location.Position; (p == "before" || p == "after") && (in.Location.Model == "folder") != folders {
			return nil, badRequest("Incompatible location of type " + in.Location.Model + " provided")
		}
		// The target is a collection or a folder in some collection.
		var dst *collection
		var parent *postman.Item
		if in.Target.Model == "collection" {
			dst = s.findCollection(in.Target.ID)
		} else {
			tid, _ := own(in.Target.ID)
			for _, c := range s.collections {
				if f, _ := c.col.Find(tid); tid != "" && f != nil && f.Folder {
					dst, parent = c, f
					break
				}
			}
		}
		if dst == nil {
			return nil, notFound("target")
		}
		moving := it
		if in.Mode == "copy" {
			moving = it.Clone()
			moving.Name += " Copy"
			var reset func(x *postman.Item)
			reset = func(x *postman.Item) {
				x.ID, x.UID, x.CreatedAt, x.UpdatedAt = "", "", "", ""
				for _, r := range x.Response {
					r.ID, r.UID = "", ""
				}
				for _, sub := range x.Item {
					reset(sub)
				}
			}
			reset(moving)
			now := s.now()
			var stampAll func(x *postman.Item)
			stampAll = func(x *postman.Item) {
				s.stamp(x, now)
				for _, sub := range x.Item {
					stampAll(sub)
				}
			}
			stampAll(moving)
		} else {
			src.col.Remove(id)
			s.touched(src, nil)
		}
		list := dst.col.Container(parent)
		at := len(*list)
		switch in.Location.Position {
		case "start":
			at = 0
		case "before", "after":
			for i, x := range *list {
				if lid, _ := own(in.Location.ID); x.ID == lid {
					at = i
					if in.Location.Position == "after" {
						at++
					}
				}
			}
		}
		dst.col.Insert(parent, region(*list, at, moving.Folder), moving)
		s.touched(dst, moving)
		out = append(out, Owner+"-"+moving.ID)
	}
	return map[string]any{"ids": out}, nil
}

func (s *Server) environmentsRoute(r *http.Request, parts []string, ws string, body []byte) (any, *apiErr) {
	if len(parts) == 0 {
		switch r.Method {
		case http.MethodGet:
			list := []postman.Summary{}
			for id, e := range s.envs {
				if ws == "" || e.ws == ws {
					list = append(list, postman.Summary{ID: id, UID: e.env.UID, Name: e.env.Name, Owner: Owner, UpdatedAt: e.env.UpdatedAt})
				}
			}
			sortSummaries(list)
			return map[string]any{"environments": list}, nil
		case http.MethodPost:
			var in struct {
				Environment *postman.Environment `json:"environment"`
			}
			if json.Unmarshal(body, &in) != nil || in.Environment == nil {
				return nil, badRequest("environment is required")
			}
			if ws == "" {
				ws = s.workspaces[0].ID
			}
			in.Environment.ID = newID()
			in.Environment.UID = Owner + "-" + in.Environment.ID
			in.Environment.UpdatedAt = s.now()
			s.envs[in.Environment.ID] = &environment{ws: ws, env: in.Environment}
			return map[string]any{"environment": map[string]any{"id": in.Environment.ID, "name": in.Environment.Name, "uid": in.Environment.UID}}, nil
		}
		return nil, notFound("endpoint")
	}
	e := s.findEnv(parts[0])
	if e == nil {
		return nil, notFound("environment")
	}
	switch r.Method {
	case http.MethodGet:
		return map[string]any{"environment": e.env}, nil
	case http.MethodPut:
		var in struct {
			Environment struct {
				Name   string             `json:"name"`
				Values []postman.EnvValue `json:"values"`
			} `json:"environment"`
		}
		if err := json.Unmarshal(body, &in); err != nil {
			return nil, badRequest(err.Error())
		}
		if in.Environment.Name != "" {
			e.env.Name = in.Environment.Name
		}
		e.env.Values = in.Environment.Values
		e.env.UpdatedAt = s.now()
		return map[string]any{"environment": map[string]any{"id": e.env.ID, "name": e.env.Name, "uid": e.env.UID}}, nil
	case http.MethodDelete:
		delete(s.envs, e.env.ID)
		return map[string]any{"environment": map[string]any{"id": e.env.ID, "uid": e.env.UID}}, nil
	}
	return nil, notFound("endpoint")
}

// region keeps an index among the folders (which Postman lists first) or
// among the requests.
func region(list []*postman.Item, at int, folder bool) int {
	folders := 0
	for folders < len(list) && list[folders].Folder {
		folders++
	}
	if folder {
		return min(at, folders)
	}
	return max(at, folders)
}

func sortSummaries(list []postman.Summary) {
	for i := 1; i < len(list); i++ {
		for j := i; j > 0 && strings.ToLower(list[j].Name) < strings.ToLower(list[j-1].Name); j-- {
			list[j], list[j-1] = list[j-1], list[j]
		}
	}
}

// itemData is a request as the item endpoints answer it.
func itemData(c *collection, it *postman.Item) map[string]any {
	return map[string]any{"data": map[string]any{
		"id": it.ID, "name": it.Name, "collection": c.col.Info.PostmanID,
		"updatedAt": it.UpdatedAt, "createdAt": it.CreatedAt, "lastUpdatedBy": Owner, "owner": Owner,
	}}
}

// String describes the fake's state, for test failures.
func (s *Server) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return fmt.Sprintf("pmtest: %d collections, %d environments", len(s.collections), len(s.envs))
}

// CollectionNames lists a workspace's collections by name, for tests.
func (s *Server) CollectionNames(ws string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for _, c := range s.collections {
		if c.ws == ws {
			out = append(out, c.col.Info.Name)
		}
	}
	sort.Strings(out)
	return out
}

// EnvironmentNames lists a workspace's environments by name, for tests.
func (s *Server) EnvironmentNames(ws string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for _, e := range s.envs {
		if e.ws == ws {
			out = append(out, e.env.Name)
		}
	}
	sort.Strings(out)
	return out
}

// EditEnvironment changes an environment as a teammate would, for tests.
func (s *Server) EditEnvironment(id string, f func(e *postman.Environment)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e := s.anyEnv(id); e != nil {
		f(e.env)
		e.env.UpdatedAt = s.now()
	}
}
