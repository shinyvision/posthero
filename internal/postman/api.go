package postman

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/shinyvision/posthero/internal/jsonx"
)

// DefaultBaseURL is the Postman API.
const DefaultBaseURL = "https://api.getpostman.com"

// Client talks to the Postman API with an API key.
type Client struct {
	base string
	key  string
	http *http.Client
}

// NewClient returns a client. An empty base uses the Postman API.
func NewClient(key, base string) *Client {
	if base == "" {
		base = DefaultBaseURL
	}
	return &Client{base: strings.TrimRight(base, "/"), key: key, http: &http.Client{Timeout: 60 * time.Second}}
}

// NewClientWith returns a client that sends its requests through rt: an
// API answered in-process, like posthero's local copy of a workspace.
func NewClientWith(key, base string, rt http.RoundTripper) *Client {
	c := NewClient(key, base)
	c.http.Transport = rt
	return c
}

// Error is an error answer from the Postman API.
type Error struct {
	Status  int
	Name    string
	Message string
}

func (e *Error) Error() string {
	msg := e.Message
	if msg == "" {
		msg = e.Name
	}
	if msg == "" {
		msg = http.StatusText(e.Status)
	}
	switch e.Status {
	case http.StatusUnauthorized:
		return "Postman didn't accept the API key: " + msg
	case http.StatusForbidden:
		return "Postman says you may not do that: " + msg
	case http.StatusTooManyRequests:
		return "Postman's rate limit was reached; try again in a minute"
	}
	return fmt.Sprintf("Postman: %s (HTTP %d)", msg, e.Status)
}

// IsUnauthorized reports whether err is Postman refusing the API key.
func IsUnauthorized(err error) bool {
	var e *Error
	return errors.As(err, &e) && e.Status == http.StatusUnauthorized
}

// IsNotFound reports whether err is Postman not knowing a thing.
func IsNotFound(err error) bool {
	var e *Error
	return errors.As(err, &e) && e.Status == http.StatusNotFound
}

// do sends a request and decodes the JSON answer into out (when not nil).
// Rate-limited requests wait and try again a few times.
func (c *Client) do(ctx context.Context, method, path string, body, out any) error {
	var data []byte
	if body != nil {
		var err error
		if data, err = jsonx.Marshal(body); err != nil {
			return err
		}
	}
	for attempt := 0; ; attempt++ {
		req, err := http.NewRequestWithContext(ctx, method, c.base+path, bytes.NewReader(data))
		if err != nil {
			return err
		}
		req.Header.Set("X-Api-Key", c.key)
		req.Header.Set("Accept", "application/json")
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, err := c.http.Do(req)
		if err != nil {
			return fmt.Errorf("couldn't reach Postman: %w", err)
		}
		raw, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			return err
		}
		if resp.StatusCode == http.StatusTooManyRequests && attempt < 3 {
			wait := time.Duration(attempt+1) * 2 * time.Second
			if s, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil && s > 0 && s < 60 {
				wait = time.Duration(s) * time.Second
			}
			select {
			case <-time.After(wait):
				continue
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		if resp.StatusCode >= 400 {
			return apiError(resp.StatusCode, raw)
		}
		if out == nil || len(raw) == 0 {
			return nil
		}
		if err := json.Unmarshal(raw, out); err != nil {
			return fmt.Errorf("Postman's answer to %s %s isn't what posthero expects: %w", method, path, err)
		}
		return nil
	}
}

// apiError reads both error formats Postman uses.
func apiError(status int, raw []byte) error {
	var e struct {
		Error *struct {
			Name    string `json:"name"`
			Message string `json:"message"`
		} `json:"error"`
		Title  string `json:"title"`
		Detail string `json:"detail"`
	}
	_ = json.Unmarshal(raw, &e)
	out := &Error{Status: status}
	switch {
	case e.Error != nil:
		out.Name, out.Message = e.Error.Name, e.Error.Message
	case e.Detail != "" || e.Title != "":
		out.Name, out.Message = e.Title, e.Detail
	default:
		out.Message = strings.TrimSpace(string(raw))
		if len(out.Message) > 200 {
			out.Message = out.Message[:200] + "…"
		}
	}
	return out
}

// User is the owner of the API key.
type User struct {
	ID       int64  `json:"id"`
	Username string `json:"username"`
	Email    string `json:"email"`
	FullName string `json:"fullName"`
	TeamID   int64  `json:"teamId"`
	TeamName string `json:"teamName"`
}

// Me returns who the API key belongs to.
func (c *Client) Me(ctx context.Context) (User, error) {
	var out struct {
		User User `json:"user"`
	}
	err := c.do(ctx, http.MethodGet, "/me", nil, &out)
	return out.User, err
}

// Workspace is a workspace: a place collections and environments live.
type Workspace struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Type       string `json:"type"`
	Visibility string `json:"visibility"`
}

// Workspaces lists the workspaces the key can see.
func (c *Client) Workspaces(ctx context.Context) ([]Workspace, error) {
	var out struct {
		Workspaces []Workspace `json:"workspaces"`
	}
	err := c.do(ctx, http.MethodGet, "/workspaces", nil, &out)
	return out.Workspaces, err
}

// Summary is a collection or environment as lists show it.
type Summary struct {
	ID        string `json:"id"`
	UID       string `json:"uid"`
	Name      string `json:"name"`
	Owner     string `json:"owner"`
	UpdatedAt string `json:"updatedAt"`
}

// Collections lists a workspace's collections.
func (c *Client) Collections(ctx context.Context, workspace string) ([]Summary, error) {
	var out struct {
		Collections []Summary `json:"collections"`
	}
	err := c.do(ctx, http.MethodGet, "/collections?workspace="+url.QueryEscape(workspace), nil, &out)
	return out.Collections, err
}

// Collection fetches a whole collection by its uid or id.
func (c *Client) Collection(ctx context.Context, uid string) (*Collection, error) {
	var out struct {
		Collection *Collection `json:"collection"`
	}
	if err := c.do(ctx, http.MethodGet, "/collections/"+url.PathEscape(uid), nil, &out); err != nil {
		return nil, err
	}
	if out.Collection == nil {
		return nil, fmt.Errorf("Postman sent no collection for %s", uid)
	}
	return out.Collection, nil
}

// CreateCollection makes a collection in a workspace and returns its id
// and uid.
func (c *Client) CreateCollection(ctx context.Context, workspace string, col *Collection) (Summary, error) {
	var out struct {
		Collection Summary `json:"collection"`
	}
	if col.Info.Schema == "" {
		col.Info.Schema = SchemaV21
	}
	err := c.do(ctx, http.MethodPost, "/collections?workspace="+url.QueryEscape(workspace), map[string]any{"collection": col}, &out)
	return out.Collection, err
}

// DeleteCollection deletes a collection.
func (c *Client) DeleteCollection(ctx context.Context, uid string) error {
	return c.do(ctx, http.MethodDelete, "/collections/"+url.PathEscape(uid), nil, nil)
}

// CollectionPatch changes the collection itself: its name, description,
// auth, scripts or variables. Nil fields stay as they are.
type CollectionPatch struct {
	Name        *string
	Description *string
	Auth        **Auth // a pointer to nil removes the auth
	Event       *[]Event
	Variable    *[]Variable
}

// The API finds what belongs to a collection by uid: the owner's id, a
// dash and the id. A bare id is looked up in the API key's own account,
// where a teammate's collection and its requests aren't. Item paths name
// the collection itself by its bare id, though (a uid there reads as
// moving the item to another collection).

// SplitUID splits a uid into the owner's id and the id; a bare id has no
// owner.
func SplitUID(s string) (owner, id string) {
	i := strings.IndexByte(s, '-')
	if i <= 0 || strings.Count(s, "-") != 5 {
		return "", s
	}
	for _, r := range s[:i] {
		if r < '0' || r > '9' {
			return "", s
		}
	}
	return s[:i], s[i+1:]
}

// itemRef is how the API names an item of a collection (by its uid): by
// the item's uid, which has the collection owner's id.
func itemRef(collection, id string) string {
	owner, _ := SplitUID(collection)
	if o, _ := SplitUID(id); o != "" || owner == "" || id == "" {
		return id
	}
	return owner + "-" + id
}

// itemsPath is the path of a collection's items of a kind (requests,
// folders, responses).
func itemsPath(collection, kind string) string {
	_, id := SplitUID(collection)
	return "/collections/" + url.PathEscape(id) + "/" + kind
}

// itemPath is the path of one item of a collection.
func itemPath(collection, kind, id string) string {
	return itemsPath(collection, kind) + "/" + url.PathEscape(itemRef(collection, id))
}

// PatchCollection changes the collection's own settings; collection is
// its uid.
func (c *Client) PatchCollection(ctx context.Context, collection string, p CollectionPatch) error {
	col := map[string]any{}
	info := map[string]any{}
	if p.Name != nil {
		info["name"] = *p.Name
	}
	if p.Description != nil {
		info["description"] = *p.Description
	}
	if len(info) > 0 {
		col["info"] = info
	}
	if p.Auth != nil {
		if *p.Auth == nil {
			col["auth"] = map[string]any{"type": "noauth"}
		} else {
			col["auth"] = *p.Auth
		}
	}
	if p.Event != nil {
		// The PATCH endpoint calls them events, and wants every script to
		// have an id.
		ev := append([]Event{}, *p.Event...)
		for i := range ev {
			if ev[i].Script.ID == "" {
				ev[i].Script.ID = NewID()
			}
		}
		col["events"] = ev
	}
	if p.Variable != nil {
		v := *p.Variable
		if v == nil {
			v = []Variable{}
		}
		col["variable"] = v
	}
	return c.do(ctx, http.MethodPatch, "/collections/"+url.PathEscape(collection), map[string]any{"collection": col}, nil)
}

// itemAnswer is what the item endpoints answer.
type itemAnswer struct {
	Data struct {
		ID        string `json:"id"`
		UpdatedAt string `json:"updatedAt"`
		Folder    string `json:"folder"`
	} `json:"data"`
}

// CreateRequest adds a request to a collection (by its uid), in a folder
// when folder is not empty, and returns its id.
func (c *Client) CreateRequest(ctx context.Context, collection, folder string, it *Item) (string, error) {
	path := itemsPath(collection, "requests")
	if folder != "" {
		path += "?folder=" + url.QueryEscape(itemRef(collection, folder))
	}
	var out itemAnswer
	err := c.do(ctx, http.MethodPost, path, requestBody(it), &out)
	return out.Data.ID, err
}

// UpdateRequest saves a request of a collection (by its uid) and returns
// when it was updated.
func (c *Client) UpdateRequest(ctx context.Context, collection string, it *Item) (string, error) {
	var out itemAnswer
	err := c.do(ctx, http.MethodPut, itemPath(collection, "requests", it.ID), requestBody(it), &out)
	return out.Data.UpdatedAt, err
}

// DeleteRequest deletes a request of a collection (by its uid).
func (c *Client) DeleteRequest(ctx context.Context, collection, id string) error {
	return c.do(ctx, http.MethodDelete, itemPath(collection, "requests", id), nil, nil)
}

// RequestRevision is a request as the API has it now: when it changed.
type RequestRevision struct {
	UpdatedAt     string `json:"updatedAt"`
	LastUpdatedBy string `json:"lastUpdatedBy"`
}

// RequestInfo fetches when a request of a collection (by its uid) was
// last changed and by whom.
func (c *Client) RequestInfo(ctx context.Context, collection, id string) (RequestRevision, error) {
	var out struct {
		Data RequestRevision `json:"data"`
	}
	err := c.do(ctx, http.MethodGet, itemPath(collection, "requests", id), nil, &out)
	return out.Data, err
}

// CreateFolder adds a folder to a collection (by its uid), inside parent
// when it is not empty, and returns its id.
func (c *Client) CreateFolder(ctx context.Context, collection, parent string, it *Item) (string, error) {
	body := folderBody(it)
	if parent != "" {
		body["folder"] = itemRef(collection, parent)
	}
	var out itemAnswer
	err := c.do(ctx, http.MethodPost, itemsPath(collection, "folders"), body, &out)
	return out.Data.ID, err
}

// UpdateFolder saves a folder's own settings: name, description, auth and
// scripts. collection is the collection's uid.
func (c *Client) UpdateFolder(ctx context.Context, collection string, it *Item) error {
	return c.do(ctx, http.MethodPut, itemPath(collection, "folders", it.ID), folderBody(it), nil)
}

// DeleteFolder deletes a folder of a collection (by its uid) and
// everything in it.
func (c *Client) DeleteFolder(ctx context.Context, collection, id string) error {
	return c.do(ctx, http.MethodDelete, itemPath(collection, "folders", id), nil, nil)
}

// CreateResponse saves an example for a request of a collection (by its
// uid) and returns its id.
func (c *Client) CreateResponse(ctx context.Context, collection, request string, r *Response) (string, error) {
	var out itemAnswer
	err := c.do(ctx, http.MethodPost, itemsPath(collection, "responses")+"?request="+url.QueryEscape(itemRef(collection, request)), responseBody(r), &out)
	return out.Data.ID, err
}

// UpdateResponse saves an example of a collection (by its uid).
func (c *Client) UpdateResponse(ctx context.Context, collection string, r *Response) error {
	return c.do(ctx, http.MethodPut, itemPath(collection, "responses", r.ID), responseBody(r), nil)
}

// DeleteResponse deletes an example of a collection (by its uid).
func (c *Client) DeleteResponse(ctx context.Context, collection, id string) error {
	return c.do(ctx, http.MethodDelete, itemPath(collection, "responses", id), nil, nil)
}

// Place says where a moved or copied item goes: into a folder (or the
// collection, when Folder is empty), at the start or end, or before or
// after another item.
type Place struct {
	Collection string // uid of the collection
	Folder     string // uid of the folder, or empty
	// Position is "start", "end", "before" or "after".
	Position string
	// Next is the uid of the item to go before or after, and NextFolder
	// whether that item is a folder.
	Next       string
	NextFolder bool
}

// Transfer moves (or with copy set, copies) requests or folders to a place
// and returns the uids of what is there now (the copies, when copying).
func (c *Client) Transfer(ctx context.Context, folders bool, uids []string, to Place, copy bool) ([]string, error) {
	mode := "move"
	if copy {
		mode = "copy"
	}
	target := map[string]any{"id": to.Collection, "model": "collection"}
	if to.Folder != "" {
		target = map[string]any{"id": to.Folder, "model": "folder"}
	}
	loc := map[string]any{"position": to.Position}
	if to.Position == "before" || to.Position == "after" {
		model := "request"
		if to.NextFolder {
			model = "folder"
		}
		loc["id"], loc["model"] = to.Next, model
	}
	path := "/collection-requests-transfers"
	if folders {
		path = "/collection-folders-transfers"
	}
	var out struct {
		IDs []string `json:"ids"`
	}
	err := c.do(ctx, http.MethodPost, path, map[string]any{"ids": uids, "mode": mode, "target": target, "location": loc}, &out)
	return out.IDs, err
}

// Environments lists a workspace's environments.
func (c *Client) Environments(ctx context.Context, workspace string) ([]Summary, error) {
	var out struct {
		Environments []Summary `json:"environments"`
	}
	err := c.do(ctx, http.MethodGet, "/environments?workspace="+url.QueryEscape(workspace), nil, &out)
	return out.Environments, err
}

// Environment fetches an environment.
func (c *Client) Environment(ctx context.Context, uid string) (*Environment, error) {
	var out struct {
		Environment *Environment `json:"environment"`
	}
	if err := c.do(ctx, http.MethodGet, "/environments/"+url.PathEscape(uid), nil, &out); err != nil {
		return nil, err
	}
	if out.Environment == nil {
		return nil, fmt.Errorf("Postman sent no environment for %s", uid)
	}
	return out.Environment, nil
}

// PutEnvironment replaces an environment's name and variables.
func (c *Client) PutEnvironment(ctx context.Context, uid string, e *Environment) error {
	values := e.Values
	if values == nil {
		values = []EnvValue{}
	}
	return c.do(ctx, http.MethodPut, "/environments/"+url.PathEscape(uid),
		map[string]any{"environment": map[string]any{"name": e.Name, "values": values}}, nil)
}

// CreateEnvironment makes an environment in a workspace.
func (c *Client) CreateEnvironment(ctx context.Context, workspace string, e *Environment) (Summary, error) {
	var out struct {
		Environment Summary `json:"environment"`
	}
	values := e.Values
	if values == nil {
		values = []EnvValue{}
	}
	err := c.do(ctx, http.MethodPost, "/environments?workspace="+url.QueryEscape(workspace),
		map[string]any{"environment": map[string]any{"name": e.Name, "values": values}}, &out)
	return out.Environment, err
}

// DeleteEnvironment deletes an environment.
func (c *Client) DeleteEnvironment(ctx context.Context, uid string) error {
	return c.do(ctx, http.MethodDelete, "/environments/"+url.PathEscape(uid), nil, nil)
}

// Globals fetches a workspace's global variables.
func (c *Client) Globals(ctx context.Context, workspace string) ([]EnvValue, error) {
	var out struct {
		Values []EnvValue `json:"values"`
	}
	err := c.do(ctx, http.MethodGet, "/workspaces/"+url.PathEscape(workspace)+"/global-variables", nil, &out)
	return out.Values, err
}

// PutGlobals replaces a workspace's global variables.
func (c *Client) PutGlobals(ctx context.Context, workspace string, values []EnvValue) error {
	if values == nil {
		values = []EnvValue{}
	}
	return c.do(ctx, http.MethodPut, "/workspaces/"+url.PathEscape(workspace)+"/global-variables", map[string]any{"values": values}, nil)
}
