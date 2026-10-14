// Package syncapi reaches Postman the way Postman's own apps do: over a
// websocket to Postman's sync service, signed in as a user. It answers the
// same as the Postman API (postman.API) does for an API key: the same
// collections, environments and errors (TestParity and the scenario in
// postman's tests check that), with a connection kept open instead of a
// request each time, and changes told about as they happen (Watch).
//
// The service isn't documented; this follows what Postman's desktop app
// (12.32) does. It wants to hear from that app: requests say so in their
// user agent.
package syncapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/shinyvision/posthero/internal/postman"
)

// The addresses of Postman's services.
const (
	DefaultURL         = "https://bifrost-v10.getpostman.com"
	DefaultIdentityURL = "https://identity-api.getpostman.com"
)

// AppVersion is the version of Postman's desktop app this speaks as, and
// UserAgent what that app says it is.
const (
	AppVersion = "12.32.2"
	UserAgent  = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Postman/" + AppVersion + " Chrome/138.0.7204.251 Electron/37.10.3 Safari/605.1.15"
)

// Options say where the services are; empty means Postman's.
type Options struct {
	URL         string
	IdentityURL string
	TokenURL    string
	OnSession   func(Session) error
	// AppID names this installation to the service, as the desktop app
	// does; a random one is made when it is empty.
	AppID string
}

// Client talks to the sync service as a signed-in user. It is safe to use
// from many goroutines.
type Client struct {
	token      string
	session    *Session
	authGate   chan struct{}
	unsaved    bool
	renewErr   error
	renewAfter time.Time
	ctx        context.Context
	cancel     context.CancelFunc
	opts       Options
	sock       *Socket
	http       *http.Client
	agent      string
	me         postman.User
	meOK       bool
	mu         sync.Mutex
	wsOf       map[string]string // collection uid → its workspace
	listings   map[string]*listing
	watches    map[*watch]bool
	watchGate  chan struct{} // serializes subscription changes across reconnects
	watchWake  chan struct{}
	watchReset bool                   // protected by mu
	subscribed map[subscription]bool  // protected by watchGate
	reads      map[string]*readFlight // protected by mu; in-flight only
}

// New returns a client signed in with a session token.
func New(token string, opts Options) *Client {
	if opts.URL == "" {
		opts.URL = DefaultURL
	}
	if opts.IdentityURL == "" {
		opts.IdentityURL = DefaultIdentityURL
	}
	if opts.AppID == "" {
		opts.AppID = postman.NewID()
	}
	c := &Client{
		token:      token,
		opts:       opts,
		http:       &http.Client{Timeout: 30 * time.Second},
		agent:      UserAgent + " PostmanClient/" + AppVersion + " (AppId=" + opts.AppID + ")",
		wsOf:       map[string]string{},
		listings:   map[string]*listing{},
		watches:    map[*watch]bool{},
		watchGate:  make(chan struct{}, 1),
		watchWake:  make(chan struct{}, 1),
		subscribed: map[subscription]bool{},
		reads:      map[string]*readFlight{},
		authGate:   make(chan struct{}, 1),
	}
	c.ctx, c.cancel = context.WithCancel(context.Background())
	c.sock = &Socket{
		URL:       opts.URL,
		Header:    http.Header{"User-Agent": {UserAgent}},
		OnEvent:   c.event,
		OnConnect: c.reconnected,
	}
	go c.maintainWatches()
	return c
}

// Close closes the connection.
func (c *Client) Close() {
	c.cancel()
	c.sock.Close()
	// Wait for any renewal/persistence to finish before a new login can
	// replace it in the config.
	c.authGate <- struct{}{}
	<-c.authGate
}

var (
	_ postman.API     = (*Client)(nil)
	_ postman.Watcher = (*Client)(nil)
)

// call sends a request to the service and decodes the body of its answer
// into out (when not nil).
func (c *Client) call(ctx context.Context, method, path string, data, out any) error {
	token, err := c.sessionToken(ctx, "")
	if err != nil {
		return err
	}
	resp, err := c.request(ctx, method, path, data, token)
	if err != nil {
		return err
	}
	if resp.Status >= 400 && postman.IsUnauthorized(serviceError(resp)) && (method == "get" || strings.HasPrefix(path, "/list/")) {
		next, renewErr := c.sessionToken(ctx, token)
		if renewErr != nil {
			return renewErr
		}
		if next != token {
			resp, err = c.request(ctx, method, path, data, next)
			if err != nil {
				return err
			}
		}
	}
	if resp.Status >= 400 {
		return serviceError(resp)
	}
	if out == nil || len(resp.Body) == 0 {
		return nil
	}
	if err := decode(resp.Body, out); err != nil {
		return fmt.Errorf("Postman's answer to %s %s isn't what posthero expects: %w", strings.ToUpper(method), path, err)
	}
	return nil
}

// serviceError is an error answer as a *postman.Error. The service says a
// session it doesn't know is forbidden (403, authenticationError); the
// Postman API says such a key is unauthorized (401), and so does this.
func serviceError(resp Response) error {
	var e struct {
		Error struct {
			Name    string `json:"name"`
			Message string `json:"message"`
		} `json:"error"`
	}
	_ = json.Unmarshal(resp.Body, &e)
	out := &postman.Error{Status: resp.Status, Name: e.Error.Name, Message: e.Error.Message}
	if strings.EqualFold(e.Error.Name, "authenticationError") {
		out.Status = http.StatusUnauthorized
	}
	if out.Message == "" && out.Name == "" {
		out.Message = strings.TrimSpace(string(resp.Body))
		if len(out.Message) > 200 {
			out.Message = out.Message[:200] + "…"
		}
	}
	return out
}

// envelope is how the service wraps one thing.
type envelope[T any] struct {
	Data T `json:"data"`
}

// Me returns who is signed in, as Postman's identity service has it.
func (c *Client) Me(ctx context.Context) (postman.User, error) {
	token, err := c.sessionToken(ctx, "")
	if err != nil {
		return postman.User{}, err
	}
	u, err := c.meWithToken(ctx, token)
	if postman.IsUnauthorized(err) {
		next, renewErr := c.sessionToken(ctx, token)
		if renewErr != nil {
			return postman.User{}, renewErr
		}
		if next != token {
			return c.meWithToken(ctx, next)
		}
	}
	return u, err
}

func (c *Client) meWithToken(ctx context.Context, token string) (postman.User, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.opts.IdentityURL+"/api/sessions/current", nil)
	if err != nil {
		return postman.User{}, err
	}
	req.Header.Set("x-access-token", token)
	req.Header.Set("User-Agent", c.agent)
	req.Header.Set("Accept", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return postman.User{}, fmt.Errorf("couldn't reach Postman: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return postman.User{}, err
	}
	if resp.StatusCode >= 400 {
		e := serviceError(Response{Status: resp.StatusCode, Body: raw}).(*postman.Error)
		if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
			e.Status = http.StatusUnauthorized
		}
		return postman.User{}, e
	}
	var out struct {
		Session struct {
			Identity struct {
				User int64 `json:"user"`
				Team int64 `json:"team"`
			} `json:"identity"`
			Data struct {
				User struct {
					Username string `json:"username"`
					Email    string `json:"email"`
					Name     string `json:"name"`
					TeamName string `json:"teamName"`
				} `json:"user"`
			} `json:"data"`
		} `json:"session"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return postman.User{}, fmt.Errorf("Postman's session isn't what posthero expects: %w", err)
	}
	s := out.Session
	u := postman.User{ID: s.Identity.User, Username: s.Data.User.Username, Email: s.Data.User.Email,
		FullName: s.Data.User.Name, TeamID: s.Identity.Team, TeamName: s.Data.User.TeamName}
	c.mu.Lock()
	c.me, c.meOK = u, true
	c.mu.Unlock()
	return u, nil
}

// self is the signed-in user's id, for what they make.
func (c *Client) self(ctx context.Context) (string, error) {
	c.mu.Lock()
	me, ok := c.me, c.meOK
	c.mu.Unlock()
	if !ok {
		var err error
		if me, err = c.Me(ctx); err != nil {
			return "", err
		}
	}
	return fmt.Sprint(me.ID), nil
}

// visibility is what the Postman API calls a workspace's visibility.
var visibility = map[string]string{"only-me": "personal", "private-team": "private"}

// Workspaces lists the workspaces the user can see.
func (c *Client) Workspaces(ctx context.Context) ([]postman.Workspace, error) {
	var list []envelope[workspaceDTO]
	if err := c.call(ctx, "get", "/workspace", nil, &list); err != nil {
		return nil, err
	}
	return toWorkspaces(list), nil
}

func toWorkspaces(list []envelope[workspaceDTO]) []postman.Workspace {
	out := []postman.Workspace{}
	for _, w := range list {
		v := w.Data.VisibilityStatus
		if m, ok := visibility[v]; ok {
			v = m
		}
		out = append(out, postman.Workspace{ID: w.Data.ID, Name: w.Data.Name, Type: w.Data.Type, Visibility: v})
	}
	return out
}

// listing is a workspace's collections as last listed: what a collection's
// own updatedAt is (see Collection).
type listing struct {
	at      time.Time
	updated map[string]string // uid → updatedAt
	done    chan struct{}
	err     error
}

// Collections lists a workspace's collections, by name.
func (c *Client) Collections(ctx context.Context, workspace string) ([]postman.Summary, error) {
	var list []envelope[collectionDTO]
	if err := c.call(ctx, "get", "/collection?workspace="+url.QueryEscape(workspace), nil, &list); err != nil {
		return nil, err
	}
	out := toSummaries(list)
	updated := map[string]string{}
	c.mu.Lock()
	for _, s := range out {
		updated[s.UID] = s.UpdatedAt
		c.wsOf[s.UID] = workspace
	}
	done := make(chan struct{})
	close(done)
	c.listings[workspace] = &listing{at: time.Now(), updated: updated, done: done}
	c.mu.Unlock()
	return out, nil
}

// toSummaries lists collections the way the Postman API does: by name.
func toSummaries(list []envelope[collectionDTO]) []postman.Summary {
	out := []postman.Summary{}
	for _, x := range list {
		d := x.Data
		out = append(out, postman.Summary{ID: d.ID, UID: uid(d.Owner, d.ID), Name: d.Name, Owner: d.Owner, UpdatedAt: d.UpdatedAt})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		return out[i].UID < out[j].UID
	})
	return out
}

// updatedAt is a collection's own updatedAt, as its workspace's list says:
// what the Postman API's collection endpoint says too. The service's
// answer for the collection itself has when anything in it last changed.
// Lists younger than a few seconds are used again, and one is fetched at a
// time.
func (c *Client) updatedAt(ctx context.Context, col string) (string, bool) {
	c.mu.Lock()
	ws, ok := c.wsOf[col]
	if !ok {
		c.mu.Unlock()
		return "", false
	}
	l := c.listings[ws]
	if l == nil || time.Since(l.at) > 3*time.Second {
		l = &listing{at: time.Now(), done: make(chan struct{})}
		c.listings[ws] = l
		c.mu.Unlock()
		go func() {
			var list []envelope[collectionDTO]
			lctx, cancel := context.WithTimeout(c.ctx, 30*time.Second)
			defer cancel()
			err := c.call(lctx, "get", "/collection?workspace="+url.QueryEscape(ws), nil, &list)
			updated := map[string]string{}
			for _, x := range list {
				updated[uid(x.Data.Owner, x.Data.ID)] = x.Data.UpdatedAt
			}
			c.mu.Lock()
			l.updated, l.err = updated, err
			c.mu.Unlock()
			close(l.done)
		}()
	} else {
		c.mu.Unlock()
	}
	select {
	case <-l.done:
	case <-ctx.Done():
		return "", false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	at, ok := l.updated[col]
	return at, ok && l.err == nil
}

// Collection fetches a whole collection, with its examples.
func (c *Client) Collection(ctx context.Context, uidOrID string) (*postman.Collection, error) {
	type own struct {
		at string
		ok bool
	}
	ownCh := make(chan own, 1)
	go func() {
		at, ok := c.updatedAt(ctx, uidOrID)
		ownCh <- own{at, ok}
	}()
	var out envelope[*collectionDTO]
	if err := c.call(ctx, "get", "/collection/"+url.PathEscape(uidOrID)+"?populate=true&responses=true", nil, &out); err != nil {
		return nil, err
	}
	if out.Data == nil {
		return nil, fmt.Errorf("Postman sent no collection for %s", uidOrID)
	}
	col := toCollection(out.Data)
	if o := <-ownCh; o.ok {
		col.Info.UpdatedAt = o.at
	}
	return col, nil
}

// split is a uid's owner and id; a bare id is the owner's (when known).
func split(ref, owner string) (string, string) {
	if o, id := postman.SplitUID(ref); o != "" {
		return o, id
	}
	return owner, ref
}

// path is a thing's path on the service: its kind and id, with whose it
// is.
func path(kind, owner, id string) string {
	p := "/" + kind + "/" + url.PathEscape(id)
	if owner != "" {
		p += "?owner=" + url.QueryEscape(owner)
	}
	return p
}

// CreateCollection makes a collection, and everything in it, in a
// workspace. The service gives it and its items ids of its own.
func (c *Client) CreateCollection(ctx context.Context, workspace string, col *postman.Collection) (postman.Summary, error) {
	if col.Info.Schema == "" {
		col.Info.Schema = postman.SchemaV21
	}
	me, err := c.self(ctx)
	if err != nil {
		return postman.Summary{}, err
	}
	var out envelope[collectionDTO]
	q := "/collection/import?owner=" + url.QueryEscape(me) + "&workspace=" + url.QueryEscape(workspace)
	if err := c.call(ctx, "post", q, collectionPayload(col), &out); err != nil {
		return postman.Summary{}, err
	}
	owner := orDefault(out.Data.Owner, me)
	s := postman.Summary{ID: out.Data.ID, UID: uid(owner, out.Data.ID), Name: out.Data.Name}
	c.mu.Lock()
	c.wsOf[s.UID] = workspace
	delete(c.listings, workspace)
	c.mu.Unlock()
	return s, nil
}

// DeleteCollection deletes a collection.
func (c *Client) DeleteCollection(ctx context.Context, uid string) error {
	owner, id := split(uid, "")
	return c.call(ctx, "delete", path("collection", owner, id), nil, nil)
}

// PatchCollection changes the collection's own settings. Unlike the
// Postman API, the service takes scripts with packages too.
func (c *Client) PatchCollection(ctx context.Context, collection string, p postman.CollectionPatch) error {
	owner, id := split(collection, "")
	body := map[string]any{}
	if p.Name != nil {
		body["name"] = *p.Name
	}
	if p.Description != nil {
		body["description"] = *p.Description
	}
	if p.Auth != nil {
		body["auth"] = nil
		if a := *p.Auth; a != nil && a.Type != "" && a.Type != "noauth" && a.Type != "inherit" {
			body["auth"] = a
		}
	}
	if p.Event != nil {
		ev := make([]postman.Event, len(*p.Event))
		copy(ev, *p.Event)
		for i := range ev {
			if ev[i].Script.ID == "" {
				ev[i].Script.ID = postman.NewID()
			}
		}
		body["events"] = ev
	}
	if p.Variable != nil {
		v := *p.Variable
		if v == nil {
			v = []postman.Variable{}
		}
		body["variables"] = v
	}
	if len(body) == 0 {
		return nil
	}
	err := c.call(ctx, "put", path("collection", owner, id), body, nil)
	c.forget(collection)
	return err
}

// forget drops what is known of a collection's updatedAt: it changed.
func (c *Client) forget(collection string) {
	c.mu.Lock()
	if ws, ok := c.wsOf[collection]; ok {
		delete(c.listings, ws)
	}
	c.mu.Unlock()
}

// onItem sends a request about one item of a collection (a request,
// folder or response), trying whose it may be: the owner its uid says,
// then the collection's owner. What the service couldn't find it didn't
// change, so asking again is safe.
func (c *Client) onItem(ctx context.Context, method, kind, collection, ref string, body, out any) error {
	colOwner, _ := postman.SplitUID(collection)
	var owners []string
	if o, _ := postman.SplitUID(ref); o != "" {
		owners = append(owners, o)
	}
	if colOwner != "" && !slices.Contains(owners, colOwner) {
		owners = append(owners, colOwner)
	}
	if len(owners) == 0 {
		owners = []string{""}
	}
	_, id := postman.SplitUID(ref)
	var err error
	for _, o := range owners {
		p := "/" + kind + "/" + url.PathEscape(id)
		if method == "get" {
			p = "/" + kind + "/" + url.PathEscape(uid(o, id))
		} else if o != "" {
			p += "?owner=" + url.QueryEscape(o)
		}
		err = c.call(ctx, method, p, body, out)
		if !postman.IsNotFound(err) {
			return err
		}
	}
	return err
}

// made is what the service says it made.
type made struct {
	ID        string `json:"id"`
	Owner     string `json:"owner"`
	UpdatedAt string `json:"updatedAt"`
}

func (m made) result() postman.Made {
	out := postman.Made{ID: m.ID}
	if o, id := postman.SplitUID(uid(m.Owner, m.ID)); o != "" && id == m.ID {
		out.UID = o + "-" + m.ID
	}
	return out
}

// create makes a request, folder or response in a collection, with an id
// of posthero's.
func (c *Client) create(ctx context.Context, kind, collection string, body map[string]any) (postman.Made, error) {
	owner, colID := postman.SplitUID(collection)
	body["id"] = postman.NewID()
	body["collection"] = colID
	q := "/" + kind
	if owner != "" {
		q += "?owner=" + url.QueryEscape(owner)
	}
	var out envelope[made]
	if err := c.call(ctx, "post", q, body, &out); err != nil {
		return postman.Made{}, err
	}
	if out.Data.ID == "" {
		out.Data.ID = body["id"].(string)
	}
	if out.Data.Owner == "" {
		out.Data.Owner = owner
	}
	return out.Data.result(), nil
}

// bareID is the id of a uid or id.
func bareID(ref string) string {
	_, id := postman.SplitUID(ref)
	return id
}

// CreateRequest adds a request to a collection, in a folder when folder is
// not empty.
func (c *Client) CreateRequest(ctx context.Context, collection, folder string, it *postman.Item) (postman.Made, error) {
	body := requestPayload(it)
	body["folder"] = nil
	if folder != "" {
		body["folder"] = bareID(folder)
	}
	return c.create(ctx, "request", collection, body)
}

// UpdateRequest saves a request and returns when it was updated.
func (c *Client) UpdateRequest(ctx context.Context, collection string, it *postman.Item) (string, error) {
	var out envelope[made]
	err := c.onItem(ctx, "put", "request", collection, postman.Ref(it.ID, it.UID), requestPayload(it), &out)
	return out.Data.UpdatedAt, err
}

// DeleteRequest deletes a request.
func (c *Client) DeleteRequest(ctx context.Context, collection, ref string) error {
	return c.onItem(ctx, "delete", "request", collection, ref, nil, nil)
}

// RequestInfo fetches when a request last changed and by whom.
func (c *Client) RequestInfo(ctx context.Context, collection, ref string) (postman.RequestRevision, error) {
	var out envelope[postman.RequestRevision]
	err := c.onItem(ctx, "get", "request", collection, ref, nil, &out)
	return out.Data, err
}

// CreateFolder adds a folder to a collection, inside parent when it is not
// empty.
func (c *Client) CreateFolder(ctx context.Context, collection, parent string, it *postman.Item) (postman.Made, error) {
	body := folderPayload(it)
	body["folder"] = nil
	if parent != "" {
		body["folder"] = bareID(parent)
	}
	return c.create(ctx, "folder", collection, body)
}

// UpdateFolder saves a folder's own settings.
func (c *Client) UpdateFolder(ctx context.Context, collection string, it *postman.Item) error {
	return c.onItem(ctx, "put", "folder", collection, postman.Ref(it.ID, it.UID), folderPayload(it), nil)
}

// DeleteFolder deletes a folder and everything in it.
func (c *Client) DeleteFolder(ctx context.Context, collection, ref string) error {
	return c.onItem(ctx, "delete", "folder", collection, ref, nil, nil)
}

// CreateResponse saves an example for a request.
func (c *Client) CreateResponse(ctx context.Context, collection, request string, r *postman.Response) (postman.Made, error) {
	body := responsePayload(r)
	body["request"] = bareID(request)
	return c.create(ctx, "response", collection, body)
}

// UpdateResponse saves an example.
func (c *Client) UpdateResponse(ctx context.Context, collection string, r *postman.Response) error {
	return c.onItem(ctx, "put", "response", collection, postman.Ref(r.ID, r.UID), responsePayload(r), nil)
}

// DeleteResponse deletes an example.
func (c *Client) DeleteResponse(ctx context.Context, collection, ref string) error {
	return c.onItem(ctx, "delete", "response", collection, ref, nil, nil)
}

// Environments lists a workspace's environments.
func (c *Client) Environments(ctx context.Context, workspace string) ([]postman.Summary, error) {
	var out envelope[[]envSummaryDTO]
	if err := c.call(ctx, "post", "/list/environment?workspace="+url.QueryEscape(workspace), nil, &out); err != nil {
		return nil, err
	}
	return toEnvSummaries(out.Data), nil
}

// envSummaryDTO is an environment as a list has it: by uid.
type envSummaryDTO struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	UpdatedAt string `json:"updatedAt"`
}

func toEnvSummaries(list []envSummaryDTO) []postman.Summary {
	out := []postman.Summary{}
	for _, e := range list {
		owner, id := postman.SplitUID(e.ID)
		out = append(out, postman.Summary{ID: id, UID: e.ID, Name: e.Name, Owner: owner, UpdatedAt: e.UpdatedAt})
	}
	return out
}

// environmentDTO is an environment as the service has it: what the
// Postman API's says, with whose it is by id only.
type environmentDTO struct {
	ID        string             `json:"id"`
	Name      string             `json:"name"`
	Owner     json.RawMessage    `json:"owner"`
	Values    []postman.EnvValue `json:"values"`
	CreatedAt string             `json:"createdAt"`
	UpdatedAt string             `json:"updatedAt"`
}

// Environment fetches an environment.
func (c *Client) Environment(ctx context.Context, uidOrID string) (*postman.Environment, error) {
	var out envelope[*environmentDTO]
	if err := c.call(ctx, "get", "/environment/"+url.PathEscape(uidOrID), nil, &out); err != nil {
		return nil, err
	}
	if out.Data == nil {
		return nil, fmt.Errorf("Postman sent no environment for %s", uidOrID)
	}
	return toEnvironment(out.Data), nil
}

func toEnvironment(d *environmentDTO) *postman.Environment {
	owner := ownerID(d.Owner)
	return &postman.Environment{ID: d.ID, UID: uid(owner, d.ID), Name: d.Name, Owner: owner,
		CreatedAt: d.CreatedAt, UpdatedAt: d.UpdatedAt, Values: envValues(d.Values)}
}

// ownerID reads an owner: an id, or (in some answers) {id, name, …}.
func ownerID(raw json.RawMessage) string {
	var o struct {
		ID postman.Str `json:"id"`
	}
	if len(raw) > 0 && raw[0] == '{' && json.Unmarshal(raw, &o) == nil {
		return string(o.ID)
	}
	return text(raw)
}

// PutEnvironment replaces an environment's name and variables.
func (c *Client) PutEnvironment(ctx context.Context, uid string, e *postman.Environment) error {
	owner, id := split(uid, "")
	return c.call(ctx, "put", path("environment", owner, id), map[string]any{"name": e.Name, "values": envValues(e.Values)}, nil)
}

func envValues(v []postman.EnvValue) []postman.EnvValue {
	if v == nil {
		return []postman.EnvValue{}
	}
	return v
}

// CreateEnvironment makes an environment in a workspace.
func (c *Client) CreateEnvironment(ctx context.Context, workspace string, e *postman.Environment) (postman.Summary, error) {
	me, err := c.self(ctx)
	if err != nil {
		return postman.Summary{}, err
	}
	id := postman.NewID()
	var out envelope[environmentDTO]
	q := "/environment?owner=" + url.QueryEscape(me) + "&workspace=" + url.QueryEscape(workspace)
	if err := c.call(ctx, "post", q, map[string]any{"id": id, "name": e.Name, "values": envValues(e.Values)}, &out); err != nil {
		return postman.Summary{}, err
	}
	owner := orDefault(ownerID(out.Data.Owner), me)
	id = orDefault(out.Data.ID, id)
	return postman.Summary{ID: id, UID: uid(owner, id), Name: e.Name}, nil
}

// DeleteEnvironment deletes an environment.
func (c *Client) DeleteEnvironment(ctx context.Context, uid string) error {
	owner, id := split(uid, "")
	return c.call(ctx, "delete", path("environment", owner, id), nil, nil)
}

// Globals fetches a workspace's global variables.
func (c *Client) Globals(ctx context.Context, workspace string) ([]postman.EnvValue, error) {
	var out envelope[globalsDTO]
	if err := c.call(ctx, "get", "/workspace/"+url.PathEscape(workspace)+"/globals", nil, &out); err != nil {
		return nil, err
	}
	return envValues(out.Data.Values), nil
}

// PutGlobals replaces a workspace's global variables.
func (c *Client) PutGlobals(ctx context.Context, workspace string, values []postman.EnvValue) error {
	return c.call(ctx, "put", "/workspace/"+url.PathEscape(workspace)+"/globals", map[string]any{"values": envValues(values)}, nil)
}

// errNotFound is a 404 the way the service sends one.
func errNotFound(what string) error {
	return &postman.Error{Status: http.StatusNotFound, Name: "instanceNotFoundError", Message: "We could not find the " + what + " you are looking for"}
}
