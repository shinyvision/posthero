package postman_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/shinyvision/posthero/internal/postman"
	"github.com/shinyvision/posthero/internal/postman/pmtest"
)

func TestClientAgainstFake(t *testing.T) {
	srv := pmtest.New()
	defer srv.Close()
	c := postman.NewClient(pmtest.Key, srv.Start())
	scenario(t, c, "ws1")

	bad := postman.NewClient("wrong", srv.Start())
	if _, err := bad.Me(context.Background()); !postman.IsUnauthorized(err) {
		t.Errorf("a wrong key: %v", err)
	}
}

// TestCreateInTeammatesCollection: the API looks a bare collection id up
// in the API key's own account, so making something in a teammate's
// collection takes its uid.
func TestCreateInTeammatesCollection(t *testing.T) {
	const uid = "2000-c0ffee00-0000-4000-8000-000000000001"
	var calls []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.Method+" "+r.URL.Path)
		if !strings.HasPrefix(r.URL.Path, "/collections/"+uid+"/") {
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{"error":{"name":"instanceNotFoundError","message":"We could not find the collection you are looking for"}}`)
			return
		}
		_, _ = io.WriteString(w, `{"data":{"id":"made"}}`)
	}))
	defer srv.Close()
	c := postman.NewClient("key", srv.URL)
	ctx := context.Background()

	req, err := c.CreateRequest(ctx, uid, "f1", &postman.Item{Name: "New", Request: &postman.Request{Method: "GET", URL: postman.ParseURL("http://x")}})
	if err != nil || req != "made" {
		t.Fatalf("request: %q %v", req, err)
	}
	folder, err := c.CreateFolder(ctx, uid, "", &postman.Item{Name: "New", Folder: true})
	if err != nil || folder != "made" {
		t.Fatalf("folder: %q %v", folder, err)
	}
	ex, err := c.CreateResponse(ctx, uid, "r1", &postman.Response{Name: "OK", Code: 200})
	if err != nil || ex != "made" {
		t.Fatalf("example: %q %v", ex, err)
	}
	want := []string{
		"POST /collections/c0ffee00-0000-4000-8000-000000000001/requests", "POST /collections/" + uid + "/requests",
		"POST /collections/c0ffee00-0000-4000-8000-000000000001/folders", "POST /collections/" + uid + "/folders",
		"POST /collections/c0ffee00-0000-4000-8000-000000000001/responses", "POST /collections/" + uid + "/responses",
	}
	if strings.Join(calls, "\n") != strings.Join(want, "\n") {
		t.Errorf("calls:\n%s", strings.Join(calls, "\n"))
	}
}

// TestClientLive runs the same scenario against the real Postman API, in a
// collection it makes and deletes again. It needs POSTMAN_API_KEY and
// POSTHERO_LIVE_WORKSPACE (the id of a workspace to make it in).
func TestClientLive(t *testing.T) {
	key, ws := os.Getenv("POSTMAN_API_KEY"), os.Getenv("POSTHERO_LIVE_WORKSPACE")
	if key == "" || ws == "" {
		t.Skip("POSTMAN_API_KEY and POSTHERO_LIVE_WORKSPACE are not set")
	}
	scenario(t, postman.NewClient(key, ""), ws)
}

func scenario(t *testing.T, c *postman.Client, ws string) {
	ctx := context.Background()
	me, err := c.Me(ctx)
	if err != nil || me.ID == 0 {
		t.Fatalf("me: %+v %v", me, err)
	}
	created, err := c.CreateCollection(ctx, ws, &postman.Collection{
		Info: postman.Info{Name: "posthero test (safe to delete)"},
		Item: []*postman.Item{{Name: "Folder", Folder: true}},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := c.DeleteCollection(ctx, created.UID); err != nil {
			t.Errorf("delete: %v", err)
		}
	}()
	col, err := c.Collection(ctx, created.UID)
	if err != nil {
		t.Fatal(err)
	}
	folder := col.Item[0]

	// A request with everything posthero edits.
	it := &postman.Item{Name: "Create user", Request: &postman.Request{
		Method: "POST",
		URL: postman.Rebuild(nil, "{{base}}/users/:team?notify=true#top", postman.Params{
			{Key: "notify", Value: "true"},
			{Key: "dry", Value: "1", Disabled: true, Description: &postman.Description{Content: "try it"}},
		}, map[string]string{"team": "7"}),
		Header: postman.Params{{Key: "Origin", Value: "{{origin}}"}, {Key: "X-Debug", Value: "1", Disabled: true}},
		Body:   &postman.Body{Mode: "raw", Raw: `{"name": "Ada"}`},
		Auth:   &postman.Auth{Type: "bearer", Params: map[string][]postman.AuthParam{"bearer": {{Key: "token", Value: json.RawMessage(`"{{token}}"`), Type: "string"}}}},
	}, Event: postman.SetScript(nil, "test", "pm.test('ok', () => pm.response.to.have.status(201))")}
	it.Request.Body.SetLanguage("json")
	it.Request.Description = &postman.Description{Content: "Makes a user"}
	it.ProtocolProfileBehavior = json.RawMessage(`{"followRedirects":false}`)
	id, err := c.CreateRequest(ctx, created.UID, folder.ID, it)
	if err != nil || id == "" {
		t.Fatalf("create request: %q %v", id, err)
	}
	// What the check before a save asks: when it last changed.
	if info, err := c.RequestInfo(ctx, created.UID, id); err != nil || info.UpdatedAt == "" {
		t.Fatalf("request info: %+v %v", info, err)
	}
	col, _ = c.Collection(ctx, created.UID)
	got, parents := col.Find(id)
	if got == nil || len(parents) != 1 {
		t.Fatalf("created request not in the folder: %+v", col.Item)
	}
	r := got.Request
	if r.Method != "POST" || r.URL.Raw != "{{base}}/users/:team?notify=true#top" {
		t.Errorf("method/url: %s %s", r.Method, r.URL.Raw)
	}
	if len(r.URL.Query) != 2 || !r.URL.Query[1].Disabled || r.URL.Query[1].Description == nil || r.URL.Query[1].Description.Content != "try it" {
		t.Errorf("query: %+v", r.URL.Query)
	}
	if len(r.URL.Variable) != 1 || r.URL.Variable[0].Key != "team" || r.URL.Variable[0].Value != "7" {
		t.Errorf("path variables: %+v", r.URL.Variable)
	}
	if len(r.Header) != 2 || !r.Header[1].Disabled {
		t.Errorf("headers: %+v", r.Header)
	}
	if r.Body == nil || r.Body.Mode != "raw" || r.Body.Raw != `{"name": "Ada"}` || r.Body.Language() != "json" {
		t.Errorf("body: %+v", r.Body)
	}
	if r.Auth == nil || r.Auth.Type != "bearer" || r.Auth.Get("token") != "{{token}}" {
		t.Errorf("auth: %+v", r.Auth)
	}
	if postman.Scripts(got.Event, "test") != "pm.test('ok', () => pm.response.to.have.status(201))" {
		t.Errorf("events: %+v", got.Event)
	}
	if r.Description == nil || r.Description.Content != "Makes a user" {
		t.Errorf("description: %+v", r.Description)
	}
	if !strings.Contains(string(got.ProtocolProfileBehavior), `"followRedirects":false`) {
		t.Errorf("settings: %s", got.ProtocolProfileBehavior)
	}

	// Every body mode survives a save.
	bodies := []*postman.Body{
		{Mode: "urlencoded", URLEncoded: postman.Params{{Key: "a", Value: "1"}, {Key: "b", Value: "2", Disabled: true}}},
		{Mode: "formdata", FormData: postman.Params{{Key: "t", Value: "x", Type: "text"}, {Key: "f", Type: "file", Src: json.RawMessage(`"/tmp/a.png"`)}}},
		{Mode: "file", File: &postman.BodyFile{Src: "/tmp/data.bin"}},
		{Mode: "graphql", GraphQL: &postman.GraphQL{Query: "query Q($id: ID!) { user(id: $id) { name } }", Variables: `{"id": "1"}`}},
	}
	for _, b := range bodies {
		got.Request.Body = b
		if _, err := c.UpdateRequest(ctx, created.UID, got); err != nil {
			t.Fatalf("update with %s body: %v", b.Mode, err)
		}
		col, _ = c.Collection(ctx, created.UID)
		after, _ := col.Find(id)
		ab := after.Request.Body
		switch {
		case ab == nil || ab.Mode != b.Mode:
			t.Errorf("%s body came back as %+v", b.Mode, ab)
		case b.Mode == "urlencoded" && (len(ab.URLEncoded) != 2 || !ab.URLEncoded[1].Disabled):
			t.Errorf("urlencoded: %+v", ab.URLEncoded)
		case b.Mode == "formdata" && (len(ab.FormData) != 2 || ab.FormData[1].Type != "file" || ab.FormData[1].Files()[0] != "/tmp/a.png"):
			t.Errorf("formdata: %+v", ab.FormData)
		case b.Mode == "file" && (ab.File == nil || ab.File.Src != "/tmp/data.bin"):
			t.Errorf("file: %+v", ab.File)
		case b.Mode == "graphql" && (ab.GraphQL == nil || ab.GraphQL.Variables != `{"id": "1"}`):
			t.Errorf("graphql: %+v", ab.GraphQL)
		}
	}

	// Removing things removes them.
	got.Request.Body, got.Request.Auth, got.Request.Description, got.Event = nil, nil, nil, nil
	got.ProtocolProfileBehavior = postman.RemovedSettings(got.ProtocolProfileBehavior, nil)
	got.Name = "Create a user"
	if _, err := c.UpdateRequest(ctx, created.UID, got); err != nil {
		t.Fatal(err)
	}
	col, _ = c.Collection(ctx, created.UID)
	after, _ := col.Find(id)
	if after.Name != "Create a user" || after.Request.Body != nil || after.Request.Auth != nil || after.Request.Description != nil || len(after.Event) != 0 {
		t.Errorf("not removed: %+v / %+v", after, after.Request)
	}

	// An example.
	exID, err := c.CreateResponse(ctx, created.UID, id, &postman.Response{Name: "Created", Code: 201, Status: "Created", Header: postman.Params{{Key: "Content-Type", Value: "application/json"}}, Body: `{"id": 1}`, PreviewLanguage: "json"})
	if err != nil {
		t.Fatal(err)
	}
	col, _ = c.Collection(ctx, created.UID)
	after, _ = col.Find(id)
	if len(after.Response) != 1 || after.Response[0].Code != 201 || after.Response[0].Body != `{"id": 1}` {
		t.Errorf("example: %+v", after.Response)
	}
	// It changes: its name, status and body, and the request it keeps.
	ex := after.Response[0].Clone()
	ex.Name, ex.Code, ex.Status, ex.Body = "Made", 200, "OK", `{"id": 2}`
	ex.OriginalRequest = &postman.Request{Method: "PUT", URL: postman.ParseURL("{{base}}/users/2")}
	if err := c.UpdateResponse(ctx, created.UID, ex); err != nil {
		t.Fatal(err)
	}
	col, _ = c.Collection(ctx, created.UID)
	after, _ = col.Find(id)
	if len(after.Response) != 1 || after.Response[0].Name != "Made" || after.Response[0].Code != 200 || after.Response[0].Body != `{"id": 2}` {
		t.Errorf("changed example: %+v", after.Response)
	} else if o := after.Response[0].OriginalRequest; o == nil || o.Method != "PUT" || o.URL.Raw != "{{base}}/users/2" {
		t.Errorf("changed example's request: %+v", o)
	}
	if err := c.DeleteResponse(ctx, created.UID, exID); err != nil {
		t.Fatal(err)
	}

	// Folders, moving and copying.
	sub, err := c.CreateFolder(ctx, created.UID, "", &postman.Item{Name: "Second", Folder: true, Description: &postman.Description{Content: "two"}})
	if err != nil {
		t.Fatal(err)
	}
	owner := strings.SplitN(created.UID, "-", 2)[0]
	if _, err := c.Transfer(ctx, false, []string{owner + "-" + id}, postman.Place{Collection: created.UID, Folder: owner + "-" + sub, Position: "end"}, false); err != nil {
		t.Fatal(err)
	}
	copies, err := c.Transfer(ctx, false, []string{owner + "-" + id}, postman.Place{Collection: created.UID, Position: "start"}, true)
	if err != nil || len(copies) != 1 {
		t.Fatalf("copy: %v %v", copies, err)
	}
	col, _ = c.Collection(ctx, created.UID)
	if _, p := col.Find(id); len(p) != 1 || p[0].ID != sub {
		t.Errorf("not moved into the second folder: %+v", p)
	}
	// Postman lists folders first, so the start for a request is after them.
	if n := len(col.Item); n != 3 || col.Item[2].Folder || !strings.HasSuffix(col.Item[2].Name, "Copy") {
		t.Errorf("copy not first among the requests: %d items, last %+v", n, col.Item[n-1])
	}
	if err := c.UpdateFolder(ctx, created.UID, &postman.Item{ID: sub, Name: "Renamed", Folder: true}); err != nil {
		t.Fatal(err)
	}
	if err := c.DeleteRequest(ctx, created.UID, strings.SplitN(copies[0], "-", 2)[1]); err != nil {
		t.Fatal(err)
	}
	if err := c.DeleteFolder(ctx, created.UID, folder.ID); err != nil {
		t.Fatal(err)
	}

	// The collection's own settings.
	name := "posthero test (renamed, safe to delete)"
	vars := []postman.Variable{{Key: "base", Value: "https://example.com"}}
	if err := c.PatchCollection(ctx, created.UID, postman.CollectionPatch{Name: &name, Variable: &vars}); err != nil {
		t.Fatal(err)
	}
	col, _ = c.Collection(ctx, created.UID)
	if col.Info.Name != name || len(col.Variable) != 1 || len(col.Item) != 1 || col.Item[0].Name != "Renamed" || len(col.Item[0].Item) != 1 {
		t.Errorf("after patch: %s %+v %d items", col.Info.Name, col.Variable, len(col.Item))
	}
	// Scripts, and taking them away again. Postman's scripts have packages
	// and requests, which this endpoint refuses: empty, they stay out; one
	// that holds something can't be saved here.
	events := postman.SetScript(nil, "prerequest", "pm.variables.set('x', 1);")
	events[0].Script.Extra = postman.Extra{"packages": json.RawMessage(`{}`), "requests": json.RawMessage(`{}`)}
	if err := c.PatchCollection(ctx, created.UID, postman.CollectionPatch{Event: &events}); err != nil {
		t.Fatal(err)
	}
	used := append([]postman.Event(nil), events...)
	used[0].Script.Extra = postman.Extra{"packages": json.RawMessage(`{"@team/utils":{"id":"p1"}}`)}
	if err := c.PatchCollection(ctx, created.UID, postman.CollectionPatch{Event: &used}); err == nil || err.Error() != "the collection's pre-request script has packages, which Postman's API can't save; change that script in Postman" {
		t.Errorf("a script with packages: %v", err)
	}
	col, _ = c.Collection(ctx, created.UID)
	if postman.Scripts(col.Event, "prerequest") != "pm.variables.set('x', 1);" {
		t.Errorf("collection scripts: %+v", col.Event)
	}
	var none []postman.Event
	if err := c.PatchCollection(ctx, created.UID, postman.CollectionPatch{Event: &none}); err != nil {
		t.Fatal(err)
	}
	col, _ = c.Collection(ctx, created.UID)
	if len(col.Event) != 0 {
		t.Errorf("scripts not taken away: %+v", col.Event)
	}

	// Environments.
	env, err := c.CreateEnvironment(ctx, ws, &postman.Environment{Name: "posthero test env", Values: []postman.EnvValue{{Key: "base", Value: "http://localhost"}}})
	if err != nil {
		t.Fatal(err)
	}
	defer c.DeleteEnvironment(ctx, env.UID)
	off := false
	if err := c.PutEnvironment(ctx, env.UID, &postman.Environment{Name: "posthero test env", Values: []postman.EnvValue{
		{Key: "base", Value: "http://localhost:8080"}, {Key: "pw", Value: "s3cret", Type: "secret"}, {Key: "off", Value: "1", Enabled: &off},
	}}); err != nil {
		t.Fatal(err)
	}
	e, err := c.Environment(ctx, env.UID)
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := e.Get("base"); v != "http://localhost:8080" || len(e.Values) != 3 || !e.Values[1].Secret() || e.Values[2].On() {
		t.Errorf("environment: %+v", e.Values)
	}
	list, err := c.Environments(ctx, ws)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, s := range list {
		found = found || s.UID == env.UID
	}
	if !found {
		t.Errorf("environment not listed")
	}
}
