// Package synctest is a fake of Postman's sync service that keeps its state
// in memory. It speaks the service's protocol (Sails requests over
// Socket.IO 2 over a websocket) and answers the routes posthero uses the
// way the service does: things kept flat with their order on their
// parents, changes merged into what is there, examples beside their
// requests, and changes told to the connections subscribed to them. It
// also answers the identity service's session endpoint.
package synctest

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/websocket"
)

// Token is the session token the fake takes.
const Token = "pm-session-test"

// Me is the signed-in user's id; Team is their team's.
const (
	Me   = "2000"
	Team = "500"
)

// Owner is the teammate whose the collections and environments the fake is
// given (Add…) are.
const Owner = "1000"

// Server is the fake.
type Server struct {
	mu          sync.Mutex
	srv         *httptest.Server
	workspaces  []map[string]any
	collections map[string]*entity
	folders     map[string]*entity
	requests    map[string]*entity
	responses   map[string]*entity
	envs        map[string]*entity
	globals     map[string]*entity
	clock       time.Time
	conns       map[*conn]bool
	// Calls counts requests by "method path" (without the query).
	Calls map[string]int
	// Silent makes the fake stop answering pings, as a dead link would.
	Silent bool
	// DropOn makes the fake drop the connection, once, instead of answering
	// a request ("method path", without the query).
	DropOn map[string]bool
	// PingInterval and PingTimeout are the heartbeat the fake asks for
	// (the service's: a minute, and two).
	PingInterval, PingTimeout time.Duration
	// BeforeRequest can delay an acknowledgement or observe requests in a
	// test. Configure it before Start; it runs outside the server lock.
	BeforeRequest func(method, path string)
	failNext      map[string]int
}

// entity is a collection, folder, request, example, environment or
// workspace's globals: its fields, as the service keeps them.
type entity struct {
	ws     string // for collections and environments
	fields map[string]any
}

func (e *entity) str(k string) string {
	s, _ := e.fields[k].(string)
	return s
}

func (e *entity) list(k string) []string {
	switch v := e.fields[k].(type) {
	case []string:
		return v
	case []any:
		var out []string
		for _, x := range v {
			if s, ok := x.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

// New returns an empty fake with one workspace, "Team" (id "ws1").
func New() *Server {
	return &Server{
		workspaces:   []map[string]any{{"id": "ws1", "name": "Team", "type": "team", "visibilityStatus": "team"}},
		collections:  map[string]*entity{},
		folders:      map[string]*entity{},
		requests:     map[string]*entity{},
		responses:    map[string]*entity{},
		envs:         map[string]*entity{},
		globals:      map[string]*entity{},
		clock:        time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC),
		conns:        map[*conn]bool{},
		Calls:        map[string]int{},
		DropOn:       map[string]bool{},
		PingInterval: time.Minute,
		PingTimeout:  2 * time.Minute,
	}
}

// Start serves the fake and returns its URL, for both the sync service and
// the identity service.
func (s *Server) Start() string {
	mux := http.NewServeMux()
	mux.Handle("/socket.io/", websocket.Server{Handler: s.serveSocket, Handshake: func(*websocket.Config, *http.Request) error { return nil }})
	mux.HandleFunc("/api/sessions/current", s.serveSession)
	s.srv = httptest.NewServer(mux)
	return s.srv.URL
}

// Close stops serving.
func (s *Server) Close() {
	if s.srv != nil {
		s.Drop()
		s.srv.Close()
	}
}

// Drop closes every connection, as a lost link would.
func (s *Server) Drop() {
	s.mu.Lock()
	list := make([]*conn, 0, len(s.conns))
	for c := range s.conns {
		list = append(list, c)
	}
	s.mu.Unlock()
	for _, c := range list {
		c.ws.Close()
	}
}

// Connections is how many connections are open.
func (s *Server) Connections() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.conns)
}

func (s *Server) CallCount(key string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.Calls[key]
}

func (s *Server) SetSilent(on bool) {
	s.mu.Lock()
	s.Silent = on
	s.mu.Unlock()
}

func (s *Server) DropNext(key string) {
	s.mu.Lock()
	s.DropOn[key] = true
	s.mu.Unlock()
}

func (s *Server) FailNext(key string, status int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failNext == nil {
		s.failNext = map[string]int{}
	}
	s.failNext[key] = status
}

func (s *Server) Subscriptions(workspace, kind string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for c := range s.conns {
		if c.subscribed(workspace + "/" + kind + "s") {
			n++
		}
	}
	return n
}

func (s *Server) serveSession(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Header.Get("x-access-token") != Token {
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, `{"error":{"name":"authenticationError","message":"Invalid session"}}`)
		return
	}
	me, _ := strconv.Atoi(Me)
	team, _ := strconv.Atoi(Team)
	_ = json.NewEncoder(w).Encode(map[string]any{"session": map[string]any{
		"status":   "active",
		"identity": map[string]any{"user": me, "team": team, "domain": "team"},
		"data": map[string]any{"user": map[string]any{
			"username": "rachel", "email": "rachel@example.com", "name": "Rachel Example", "teamName": "Team",
		}},
	}})
}

// now moves the clock on a second, so every change has its own time.
func (s *Server) now() string {
	s.clock = s.clock.Add(time.Second)
	return s.clock.Format("2006-01-02T15:04:05.000Z")
}

func newID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	h := hex.EncodeToString(b)
	return h[:8] + "-" + h[8:12] + "-4" + h[13:16] + "-8" + h[17:20] + "-" + h[20:]
}

// split is a uid's owner and id.
func split(ref string) (string, string) {
	i := strings.IndexByte(ref, '-')
	if i <= 0 || strings.Count(ref, "-") != 5 {
		return "", ref
	}
	return ref[:i], ref[i+1:]
}

// AddWorkspace adds a workspace.
func (s *Server) AddWorkspace(id, name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.workspaces = append(s.workspaces, map[string]any{"id": id, "name": name, "type": "team", "visibilityStatus": "team"})
}

// AddCollection stores a collection given in the service's format (with
// its folders and requests in lists, and examples in each request's
// "responses") in a workspace, as Owner's, and returns its uid. Its parts
// keep the ids they have.
func (s *Server) AddCollection(ws string, data []byte) (string, error) {
	var in map[string]any
	if err := json.Unmarshal(data, &in); err != nil {
		return "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	id, _ := in["id"].(string)
	if id == "" {
		id = newID()
		in["id"] = id
	}
	s.storeTree(ws, Owner, in, false)
	return Owner + "-" + id, nil
}

// AddEnvironment stores an environment ({name, values}) in a workspace, as
// Owner's, and returns its uid.
func (s *Server) AddEnvironment(ws, name string, values []map[string]any) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := newID()
	now := s.now()
	vs := make([]any, len(values))
	for i, v := range values {
		vs[i] = v
	}
	s.envs[id] = &entity{ws: ws, fields: map[string]any{"id": id, "owner": Owner, "name": name, "values": vs, "createdAt": now, "updatedAt": now}}
	return Owner + "-" + id
}

// Edit changes a request as a teammate would: what f sets on its fields.
func (s *Server) Edit(requestID string, f func(fields map[string]any)) {
	s.mu.Lock()
	r := s.requests[requestID]
	if r == nil {
		s.mu.Unlock()
		return
	}
	f(r.fields)
	r.fields["updatedAt"] = s.now()
	r.fields["lastUpdatedBy"] = Owner
	col := s.collections[r.str("collection")]
	s.mu.Unlock()
	if col != nil {
		s.broadcast(col.ws, "collections", col.str("owner")+"-"+col.str("id"), "update")
	}
}

// Collection returns a stored collection in the service's populated
// format, nil when there is none.
func (s *Server) Collection(id string) map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, id = split(id)
	c := s.collections[id]
	if c == nil {
		return nil
	}
	return s.populate(c, true)
}

// storeTree stores a collection and what is in it (as given), giving the
// parts new ids when fresh is set.
func (s *Server) storeTree(ws, owner string, in map[string]any, fresh bool) map[string]any {
	now := s.now()
	ids := map[string]string{}
	newOf := func(old string) string {
		if !fresh && old != "" {
			return old
		}
		if n, ok := ids[old]; ok {
			return n
		}
		n := newID()
		if old != "" {
			ids[old] = n
		}
		return n
	}
	colOld, _ := in["id"].(string)
	colID := newOf(colOld)
	mapIDs := func(v any) []any {
		out := []any{}
		for _, x := range asList(v) {
			if s, ok := x.(string); ok {
				out = append(out, newOf(s))
			}
		}
		return out
	}
	stamp := func(f map[string]any) {
		f["owner"], f["lastUpdatedBy"], f["createdAt"], f["updatedAt"] = owner, owner, now, now
	}
	folders := asList(in["folders"])
	requests := asList(in["requests"])
	col := copyMap(in, "folders", "requests")
	col["id"], col["order"], col["folders_order"] = colID, mapIDs(in["order"]), mapIDs(in["folders_order"])
	stamp(col)
	col["team"] = Team
	s.collections[colID] = &entity{ws: ws, fields: col}
	for _, x := range folders {
		f := copyMap(asMap(x))
		old, _ := f["id"].(string)
		f["id"], f["collection"] = newOf(old), colID
		if p, ok := f["folder"].(string); ok && p != "" {
			f["folder"] = newOf(p)
		} else {
			f["folder"] = nil
		}
		f["order"], f["folders_order"] = mapIDs(f["order"]), mapIDs(f["folders_order"])
		stamp(f)
		s.folders[f["id"].(string)] = &entity{fields: f}
	}
	for _, x := range requests {
		r := copyMap(asMap(x), "responses")
		old, _ := r["id"].(string)
		rid := newOf(old)
		r["id"], r["collection"] = rid, colID
		if p, ok := r["folder"].(string); ok && p != "" {
			r["folder"] = newOf(p)
		} else {
			r["folder"] = nil
		}
		r["responses_order"] = mapIDs(r["responses_order"])
		stamp(r)
		s.requests[rid] = &entity{fields: r}
		for _, y := range asList(asMap(x)["responses"]) {
			e := copyMap(asMap(y))
			eo, _ := e["id"].(string)
			e["id"], e["request"] = newOf(eo), rid
			e["requestObject"] = requestText(e["requestObject"])
			stamp(e)
			s.responses[e["id"].(string)] = &entity{fields: e}
		}
	}
	return s.populate(s.collections[colID], true)
}

// requestText is an example's request as the service keeps it: JSON text.
func requestText(v any) any {
	switch v := v.(type) {
	case nil, string:
		return v
	default:
		data, _ := json.Marshal(v)
		return string(data)
	}
}

func asList(v any) []any {
	l, _ := v.([]any)
	return l
}

func asMap(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}

// copyMap copies a map, leaving out some keys.
func copyMap(m map[string]any, without ...string) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		if !slices.Contains(without, k) {
			out[k] = v
		}
	}
	return out
}

// populate is a collection with its folders and requests (and their
// examples, with examples set). Its updatedAt is when anything in it last
// changed, as the service says it there.
func (s *Server) populate(c *entity, examples bool) map[string]any {
	out := copyMap(c.fields)
	id := c.str("id")
	latest := c.str("updatedAt")
	folders, requests := []any{}, []any{}
	for _, f := range sortedEntities(s.folders) {
		if f.str("collection") == id {
			folders = append(folders, copyMap(f.fields))
			latest = max(latest, f.str("updatedAt"))
		}
	}
	for _, r := range sortedEntities(s.requests) {
		if r.str("collection") != id {
			continue
		}
		latest = max(latest, r.str("updatedAt"))
		m := copyMap(r.fields)
		if examples {
			m["responses"] = s.examplesOf(r.str("id"))
		}
		requests = append(requests, m)
	}
	out["folders"], out["requests"], out["updatedAt"] = folders, requests, latest
	return out
}

func (s *Server) examplesOf(request string) []any {
	out := []any{}
	for _, e := range sortedEntities(s.responses) {
		if e.str("request") == request {
			out = append(out, copyMap(e.fields))
		}
	}
	return out
}

// sortedEntities lists entities in a stable order (by id).
func sortedEntities(m map[string]*entity) []*entity {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	out := make([]*entity, len(keys))
	for i, k := range keys {
		out[i] = m[k]
	}
	return out
}

// answer is a request's answer: a body and a status.
type answer struct {
	status int
	body   any
}

func ok(body any) answer { return answer{http.StatusOK, body} }

func notFound(model string) answer {
	return answer{http.StatusNotFound, map[string]any{"error": map[string]any{
		"name": "instanceNotFoundError", "message": "We could not find the " + model + " you are looking for",
	}}}
}

func bad(msg string) answer {
	return answer{http.StatusBadRequest, map[string]any{"error": map[string]any{"name": "badRequestError", "message": msg}}}
}

// one wraps one thing the way the service does.
func one(model, action string, data map[string]any) map[string]any {
	id, _ := data["id"].(string)
	return map[string]any{"model_id": id, "meta": map[string]any{"model": model, "action": action}, "data": data}
}

// handle answers a request; it returns what to broadcast afterwards.
func (s *Server) handle(c *conn, method, raw string, data map[string]any) (answer, []change) {
	u, err := url.Parse(raw)
	if err != nil {
		return bad("bad url"), nil
	}
	q := u.Query()
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	s.Calls[method+" "+u.Path]++
	switch {
	case method == "get" && u.Path == "/workspace":
		out := []any{}
		for _, w := range s.workspaces {
			out = append(out, one("workspace", "find", copyMap(w)))
		}
		return ok(out), nil
	case len(parts) == 3 && parts[0] == "workspace" && parts[2] == "globals":
		return s.globalsRoute(method, parts[1], data)
	case len(parts) == 2 && parts[0] == "list":
		return s.listRoute(c, parts[1], q, data), nil
	case method == "delete" && len(parts) == 3 && parts[0] == "list" && parts[2] == "subscribe":
		c.mu.Lock()
		delete(c.subs, q.Get("workspace")+"/"+parts[1]+"s")
		c.mu.Unlock()
		return ok(map[string]any{}), nil
	case parts[0] == "collection":
		return s.collectionRoute(method, parts[1:], q, data)
	case parts[0] == "folder" || parts[0] == "request" || parts[0] == "response":
		return s.itemRoute(method, parts[0], parts[1:], q, data)
	case parts[0] == "environment":
		return s.environmentRoute(method, parts[1:], q, data)
	}
	return notFound("endpoint"), nil
}

// change is something to broadcast: to whom (workspace, list) and what.
type change struct {
	ws, list, uid, action string
}

func (s *Server) globalsRoute(method, ws string, data map[string]any) (answer, []change) {
	g := s.globals[ws]
	if g == nil {
		now := s.now()
		g = &entity{fields: map[string]any{"id": newID(), "workspace": ws, "values": []any{}, "createdAt": now, "updatedAt": now}}
		s.globals[ws] = g
	}
	switch method {
	case "get":
		return ok(one("globals", "find", copyMap(g.fields))), nil
	case "put":
		if v, ok := data["values"]; ok {
			g.fields["values"] = v
		}
		g.fields["updatedAt"] = s.now()
		return ok(one("globals", "update", copyMap(g.fields))), nil
	}
	return notFound("endpoint"), nil
}

// listRoute answers /list/collection and /list/environment: the
// workspace's collections as trees (or environments), only those in ids
// when it is given, and subscribes the connection when asked to.
func (s *Server) listRoute(c *conn, kind string, q url.Values, data map[string]any) answer {
	ws := q.Get("workspace")
	var only []string
	for _, x := range asList(data["ids"]) {
		if id, ok := x.(string); ok {
			only = append(only, id)
		}
	}
	keep := func(uid string) bool { return only == nil || slices.Contains(only, uid) }
	out := []any{}
	list := "collections"
	switch kind {
	case "collection":
		for _, col := range sortedEntities(s.collections) {
			uid := col.str("owner") + "-" + col.str("id")
			if col.ws == ws && keep(uid) {
				m := copyMap(col.fields)
				m["id"] = uid
				out = append(out, m)
			}
		}
	case "environment":
		list = "environments"
		for _, e := range sortedEntities(s.envs) {
			uid := e.str("owner") + "-" + e.str("id")
			if e.ws == ws && keep(uid) {
				out = append(out, map[string]any{"id": uid, "name": e.fields["name"], "updatedAt": e.fields["updatedAt"]})
			}
		}
	default:
		return notFound("endpoint")
	}
	body := map[string]any{"data": out}
	if q.Get("subscribe") == "true" {
		c.subscribe(ws + "/" + list)
		body["subscription"] = map[string]any{"id": "subscription-" + ws + "-" + list}
	}
	return ok(body)
}

func (s *Server) collectionRoute(method string, parts []string, q url.Values, data map[string]any) (answer, []change) {
	switch {
	case method == "get" && len(parts) == 0:
		// The workspace's collections, without what is in them.
		out := []any{}
		for _, col := range sortedEntities(s.collections) {
			if col.ws == q.Get("workspace") {
				out = append(out, one("collection", "find", copyMap(col.fields)))
			}
		}
		return ok(out), nil
	case method == "post" && len(parts) == 0:
		owner, ws := q.Get("owner"), q.Get("workspace")
		if owner == "" || ws == "" {
			return bad("owner and workspace are required"), nil
		}
		id, _ := data["id"].(string)
		if id == "" {
			return bad("id is required"), nil
		}
		now := s.now()
		f := copyMap(data, "folders", "requests")
		f["owner"], f["lastUpdatedBy"], f["createdAt"], f["updatedAt"], f["team"] = owner, Me, now, now, Team
		for _, k := range []string{"order", "folders_order"} {
			if f[k] == nil {
				f[k] = []any{}
			}
		}
		s.collections[id] = &entity{ws: ws, fields: f}
		return ok(one("collection", "create", copyMap(f))), nil
	case method == "post" && len(parts) == 1 && parts[0] == "import":
		owner, ws := q.Get("owner"), q.Get("workspace")
		if owner == "" || ws == "" {
			return bad("owner and workspace are required"), nil
		}
		tree := s.storeTree(ws, owner, data, true)
		return ok(one("collection", "import", tree)), nil
	}
	if len(parts) != 1 {
		return notFound("endpoint"), nil
	}
	col := s.find(s.collections, parts[0], q.Get("owner"), method == "get")
	if col == nil {
		return notFound("collection"), nil
	}
	uid := col.str("owner") + "-" + col.str("id")
	switch method {
	case "get":
		if q.Get("populate") == "true" {
			return ok(one("collection", "find", s.populate(col, q.Get("responses") == "true"))), nil
		}
		return ok(one("collection", "find", copyMap(col.fields))), nil
	case "put":
		for k, v := range data {
			switch k {
			case "id", "owner", "collection", "createdAt", "updatedAt":
				continue
			}
			col.fields[k] = v
		}
		// Changing only the order isn't a change to the collection itself.
		if len(data) > 1 || (data["order"] == nil && data["folders_order"] == nil) {
			col.fields["updatedAt"] = s.now()
		}
		col.fields["lastUpdatedBy"] = Me
		return ok(one("collection", "update", copyMap(col.fields))), []change{{col.ws, "collections", uid, "update"}}
	case "delete":
		id := col.str("id")
		delete(s.collections, id)
		for k, f := range s.folders {
			if f.str("collection") == id {
				delete(s.folders, k)
			}
		}
		for k, r := range s.requests {
			if r.str("collection") == id {
				for x, e := range s.responses {
					if e.str("request") == k {
						delete(s.responses, x)
					}
				}
				delete(s.requests, k)
			}
		}
		return ok(one("collection", "destroy", map[string]any{"id": id, "owner": col.str("owner")})), []change{{col.ws, "collections", uid, "delete"}}
	}
	return notFound("endpoint"), nil
}

// find finds a thing by its uid (or, for writes, by its id and the owner
// the query says).
func (s *Server) find(m map[string]*entity, ref, owner string, byUID bool) *entity {
	o, id := split(ref)
	if !byUID {
		id, o = ref, owner
	}
	e := m[id]
	if e == nil || (o != "" && e.str("owner") != o) {
		return nil
	}
	return e
}

// itemRoute answers /folder, /request and /response.
func (s *Server) itemRoute(method, kind string, parts []string, q url.Values, data map[string]any) (answer, []change) {
	store := map[string]map[string]*entity{"folder": s.folders, "request": s.requests, "response": s.responses}[kind]
	owner := q.Get("owner")
	if method == "post" && len(parts) == 0 {
		return s.createItem(kind, store, owner, data)
	}
	if method == "put" && len(parts) == 2 && parts[0] == "transfer" && kind != "response" {
		return s.transfer(kind, store, parts[1], owner, data)
	}
	if len(parts) != 1 {
		return notFound("endpoint"), nil
	}
	it := s.find(store, parts[0], owner, method == "get")
	if it == nil {
		return notFound(kind), nil
	}
	col := s.collectionOf(kind, it)
	var changes []change
	if col != nil {
		changes = []change{{col.ws, "collections", col.str("owner") + "-" + col.str("id"), "update"}}
	}
	switch method {
	case "get":
		m := copyMap(it.fields)
		if kind == "request" && q.Get("populate") == "true" {
			m["responses"] = s.examplesOf(it.str("id"))
		}
		return ok(one(kind, "find", m)), nil
	case "put":
		for k, v := range data {
			switch k {
			case "id", "owner", "collection", "folder", "request", "createdAt", "updatedAt":
				continue
			}
			if k == "requestObject" {
				v = requestText(v)
			}
			it.fields[k] = v
		}
		it.fields["updatedAt"] = s.now()
		it.fields["lastUpdatedBy"] = Me
		return ok(one(kind, "update", copyMap(it.fields))), changes
	case "delete":
		s.removeItem(kind, it)
		return ok(one(kind, "destroy", map[string]any{"id": it.str("id"), "owner": it.str("owner")})), changes
	}
	return notFound("endpoint"), nil
}

// collectionOf is the collection an item is in.
func (s *Server) collectionOf(kind string, it *entity) *entity {
	if kind == "response" {
		r := s.requests[it.str("request")]
		if r == nil {
			return nil
		}
		it = r
	}
	return s.collections[it.str("collection")]
}

func (s *Server) createItem(kind string, store map[string]*entity, owner string, data map[string]any) (answer, []change) {
	id, _ := data["id"].(string)
	if id == "" || owner == "" {
		return bad("id and owner are required"), nil
	}
	f := copyMap(data)
	now := s.now()
	f["owner"], f["lastUpdatedBy"], f["createdAt"], f["updatedAt"] = owner, Me, now, now
	var col *entity
	switch kind {
	case "response":
		r := s.requests[str(f["request"])]
		if r == nil {
			return notFound("request"), nil
		}
		delete(f, "collection")
		f["requestObject"] = requestText(f["requestObject"])
		r.fields["responses_order"] = append(toAny(r.list("responses_order")), id)
		col = s.collections[r.str("collection")]
	default:
		col = s.collections[str(f["collection"])]
		if col == nil {
			return notFound("collection"), nil
		}
		parent := col
		if p := str(f["folder"]); p != "" {
			parent = s.folders[p]
			if parent == nil {
				return notFound("folder"), nil
			}
		} else {
			f["folder"] = nil
		}
		list := "order"
		if kind == "folder" {
			list = "folders_order"
			f["order"], f["folders_order"] = []any{}, []any{}
		} else {
			f["responses_order"] = []any{}
		}
		parent.fields[list] = append(toAny(parent.list(list)), id)
	}
	store[id] = &entity{fields: f}
	return ok(one(kind, "create", copyMap(f))), []change{{col.ws, "collections", col.str("owner") + "-" + col.str("id"), "update"}}
}

// transfer moves a request or folder to another parent, last there.
func (s *Server) transfer(kind string, store map[string]*entity, id, owner string, data map[string]any) (answer, []change) {
	it := s.find(store, id, owner, false)
	if it == nil {
		return notFound(kind), nil
	}
	to := asMap(data["to"])
	toModel, toID := str(to["model"]), str(to["model_id"])
	var parent, col *entity
	switch toModel {
	case "collection":
		col = s.collections[toID]
		parent = col
	case "folder":
		parent = s.folders[toID]
		if parent != nil {
			col = s.collections[parent.str("collection")]
		}
	}
	if parent == nil || col == nil {
		return notFound(toModel), nil
	}
	from := s.collections[it.str("collection")]
	s.detach(kind, it)
	list := "order"
	if kind == "folder" {
		list = "folders_order"
	}
	parent.fields[list] = append(toAny(parent.list(list)), it.str("id"))
	moved := []*entity{it}
	if kind == "folder" {
		moved = append(moved, s.inside(it.str("id"))...)
	}
	for _, x := range moved {
		x.fields["collection"] = col.str("id")
	}
	if toModel == "folder" {
		it.fields["folder"] = toID
	} else {
		it.fields["folder"] = nil
	}
	it.fields["updatedAt"] = s.now()
	changes := []change{{col.ws, "collections", col.str("owner") + "-" + col.str("id"), "update"}}
	if from != nil && from != col {
		changes = append(changes, change{from.ws, "collections", from.str("owner") + "-" + from.str("id"), "update"})
	}
	return ok(one(kind, "transfer", map[string]any{"id": it.str("id"), "from": data["from"], "to": data["to"]})), changes
}

// inside lists what is in a folder: its folders and requests, deeply.
func (s *Server) inside(folder string) []*entity {
	var out []*entity
	for _, f := range s.folders {
		if f.str("folder") == folder {
			out = append(out, f)
			out = append(out, s.inside(f.str("id"))...)
		}
	}
	for _, r := range s.requests {
		if r.str("folder") == folder {
			out = append(out, r)
		}
	}
	return out
}

// detach takes an item out of its parent's order.
func (s *Server) detach(kind string, it *entity) {
	id := it.str("id")
	var parent *entity
	if p := it.str("folder"); p != "" {
		parent = s.folders[p]
	} else {
		parent = s.collections[it.str("collection")]
	}
	if parent == nil {
		return
	}
	list := "order"
	if kind == "folder" {
		list = "folders_order"
	}
	parent.fields[list] = toAny(slices.DeleteFunc(slices.Clone(parent.list(list)), func(x string) bool { return x == id }))
}

// removeItem deletes an item and what is in it.
func (s *Server) removeItem(kind string, it *entity) {
	id := it.str("id")
	switch kind {
	case "response":
		if r := s.requests[it.str("request")]; r != nil {
			r.fields["responses_order"] = toAny(slices.DeleteFunc(slices.Clone(r.list("responses_order")), func(x string) bool { return x == id }))
		}
		delete(s.responses, id)
	case "request":
		s.detach(kind, it)
		for x, e := range s.responses {
			if e.str("request") == id {
				delete(s.responses, x)
			}
		}
		delete(s.requests, id)
	case "folder":
		s.detach(kind, it)
		for _, x := range s.inside(id) {
			if _, isFolder := s.folders[x.str("id")]; isFolder {
				delete(s.folders, x.str("id"))
				continue
			}
			s.removeItem("request", x)
		}
		delete(s.folders, id)
	}
}

func (s *Server) environmentRoute(method string, parts []string, q url.Values, data map[string]any) (answer, []change) {
	if method == "post" && len(parts) == 0 {
		owner, ws := q.Get("owner"), q.Get("workspace")
		id := str(data["id"])
		if owner == "" || ws == "" || id == "" {
			return bad("id, owner and workspace are required"), nil
		}
		now := s.now()
		f := map[string]any{"id": id, "owner": owner, "team": nil, "name": data["name"], "values": data["values"], "createdAt": now, "updatedAt": now}
		s.envs[id] = &entity{ws: ws, fields: f}
		return ok(one("environment", "create", copyMap(f))), []change{{ws, "environments", owner + "-" + id, "create"}}
	}
	if len(parts) != 1 {
		return notFound("endpoint"), nil
	}
	e := s.find(s.envs, parts[0], q.Get("owner"), method == "get")
	if e == nil {
		return notFound("environment"), nil
	}
	uid := e.str("owner") + "-" + e.str("id")
	switch method {
	case "get":
		return ok(one("environment", "find", copyMap(e.fields))), nil
	case "put":
		for _, k := range []string{"name", "values"} {
			if v, ok := data[k]; ok {
				e.fields[k] = v
			}
		}
		e.fields["updatedAt"] = s.now()
		return ok(one("environment", "update", copyMap(e.fields))), []change{{e.ws, "environments", uid, "update"}}
	case "delete":
		delete(s.envs, e.str("id"))
		return ok(one("environment", "destroy", map[string]any{"id": e.str("id"), "owner": e.str("owner")})), []change{{e.ws, "environments", uid, "delete"}}
	}
	return notFound("endpoint"), nil
}

func str(v any) string {
	s, _ := v.(string)
	return s
}

func toAny(l []string) []any {
	out := make([]any, len(l))
	for i, x := range l {
		out[i] = x
	}
	return out
}

// broadcast tells the connections subscribed to a workspace's list about a
// change.
func (s *Server) broadcast(ws, list, uid, action string) {
	s.mu.Lock()
	var to []*conn
	for c := range s.conns {
		if c.subscribed(ws + "/" + list) {
			to = append(to, c)
		}
	}
	s.mu.Unlock()
	payload, _ := json.Marshal([]any{"broadcast", map[string]any{
		"model_id": nil,
		"meta":     map[string]any{"model": "workspace", "action": "broadcast", "subscription": map[string]any{"id": "workspace/" + ws + "/list/" + list}},
		"data":     map[string]any{"subscription": map[string]any{"id": "subscription"}, "data": []any{map[string]any{"id": uid, "action": action}}},
		"type":     "workspace",
	}})
	for _, c := range to {
		c.send("42" + string(payload))
	}
}

// conn is one connection.
type conn struct {
	ws   *websocket.Conn
	wmu  sync.Mutex
	mu   sync.Mutex
	subs map[string]bool
}

func (c *conn) send(msg string) {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	_ = websocket.Message.Send(c.ws, msg)
}

func (c *conn) subscribe(key string) {
	c.mu.Lock()
	c.subs[key] = true
	c.mu.Unlock()
}

func (c *conn) subscribed(key string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.subs[key]
}

// serveSocket speaks Engine.IO 3 and Socket.IO 2 on a connection.
func (s *Server) serveSocket(ws *websocket.Conn) {
	ws.MaxPayloadBytes = 256 << 20
	c := &conn{ws: ws, subs: map[string]bool{}}
	s.mu.Lock()
	s.conns[c] = true
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.conns, c)
		s.mu.Unlock()
		ws.Close()
	}()
	q := ws.Request().URL.Query()
	if q.Get("EIO") != "3" || q.Get("transport") != "websocket" || q.Get("__sails_io_sdk_version") == "" {
		c.send(`44"unsupported"`)
		return
	}
	s.mu.Lock()
	open := fmt.Sprintf(`0{"sid":%q,"upgrades":[],"pingInterval":%d,"pingTimeout":%d}`, newID(), s.PingInterval.Milliseconds(), s.PingTimeout.Milliseconds())
	s.mu.Unlock()
	c.send(open)
	c.send("40")
	c.send(`42["pm-bootstrap",{"session":{"state":"INACTIVE"}}]`)
	for {
		var msg string
		if err := websocket.Message.Receive(ws, &msg); err != nil {
			return
		}
		switch {
		case msg == "2":
			s.mu.Lock()
			silent := s.Silent
			s.mu.Unlock()
			if !silent {
				c.send("3")
			}
		case strings.HasPrefix(msg, "42"):
			s.request(c, msg[2:])
		}
	}
}

// request answers a Sails request packet: "<id>[method, {…}]".
func (s *Server) request(c *conn, msg string) {
	i := strings.IndexByte(msg, '[')
	if i <= 0 {
		return
	}
	id := msg[:i]
	var list []json.RawMessage
	if json.Unmarshal([]byte(msg[i:]), &list) != nil || len(list) < 2 {
		return
	}
	var req struct {
		Method  string            `json:"method"`
		URL     string            `json:"url"`
		Data    json.RawMessage   `json:"data"`
		Headers map[string]string `json:"headers"`
	}
	if json.Unmarshal(list[1], &req) != nil {
		return
	}
	if s.BeforeRequest != nil {
		s.BeforeRequest(req.Method, strings.SplitN(req.URL, "?", 2)[0])
	}
	s.mu.Lock()
	key := req.Method + " " + strings.SplitN(req.URL, "?", 2)[0]
	drop := s.DropOn[key]
	failure := s.failNext[key]
	delete(s.failNext, key)
	delete(s.DropOn, key)
	if drop || failure != 0 {
		s.Calls[key]++
	}
	s.mu.Unlock()
	if drop {
		c.ws.Close()
		return
	}
	var a answer
	var changes []change
	if failure != 0 {
		a = answer{failure, map[string]any{"error": map[string]any{"name": "injectedError", "message": "injected test failure"}}}
	} else if req.Headers["x-access-token"] != Token {
		a = answer{http.StatusForbidden, map[string]any{"error": map[string]any{"name": "authenticationError", "message": "You are not authenticated by the server."}}}
	} else {
		var data map[string]any
		_ = json.Unmarshal(req.Data, &data)
		s.mu.Lock()
		a, changes = s.handle(c, req.Method, req.URL, data)
		s.mu.Unlock()
	}
	payload, _ := json.Marshal([]any{map[string]any{"body": a.body, "statusCode": a.status}})
	c.send("43" + id + string(payload))
	for _, ch := range changes {
		s.broadcast(ch.ws, ch.list, ch.uid, ch.action)
	}
}
