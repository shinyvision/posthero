package run

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shinyvision/posthero/internal/auth"
	"github.com/shinyvision/posthero/internal/auth/idptest"
	"github.com/shinyvision/posthero/internal/postman"
	"github.com/shinyvision/posthero/internal/script"
	"github.com/shinyvision/posthero/internal/secret"
	"github.com/shinyvision/posthero/internal/send"
	"github.com/shinyvision/posthero/internal/vars"
)

const collection = `{
  "info": {"name": "Shop"},
  "auth": {"type": "oauth2", "oauth2": [
    {"key": "grant_type", "value": "password_credentials"},
    {"key": "accessTokenUrl", "value": "{{tokenUrl}}"},
    {"key": "clientId", "value": "portal"},
    {"key": "username", "value": "rachel"},
    {"key": "password", "value": "hunter2"}
  ]},
  "event": [{"listen": "prerequest", "script": {"exec": ["pm.variables.set('trace', 'run-' + pm.info.iteration);"]}},
            {"listen": "test", "script": {"exec": ["pm.test('collection: 2xx', () => pm.response.to.be.success);"]}}],
  "item": [
    {"name": "Users", "id": "f1", "event": [{"listen": "prerequest", "script": {"exec": ["pm.request.headers.add({key: 'X-Folder', value: 'users'});"]}}], "item": [
      {"name": "Create user", "id": "r1", "request": {"method": "POST", "url": "{{base}}/users", "header": [{"key": "X-Trace", "value": "{{trace}}"}],
        "body": {"mode": "raw", "raw": "{\"name\": \"{{name}}\"}", "options": {"raw": {"language": "json"}}}},
        "event": [{"listen": "test", "script": {"exec": ["const b = pm.response.json();", "pm.collectionVariables.set('userId', b.id);", "pm.test('named ' + pm.iterationData.get('name'), () => pm.expect(b.name).to.equal(pm.iterationData.get('name')));"]}}]},
      {"name": "Get user", "id": "r2", "request": {"method": "GET", "url": "{{base}}/users/{{userId}}"},
        "event": [{"listen": "test", "script": {"exec": ["pm.test('found', () => pm.expect(pm.response.json().id).to.equal(pm.collectionVariables.get('userId')));"]}}]},
      {"name": "Skipped", "id": "r3", "request": {"method": "DELETE", "url": "{{base}}/users/{{userId}}"},
        "event": [{"listen": "prerequest", "script": {"exec": ["pm.execution.skipRequest();"]}}]}
    ]},
    {"name": "Health", "id": "r4", "request": {"method": "GET", "url": "{{base}}/health"}, "auth": null,
      "event": [{"listen": "test", "script": {"exec": ["if (!pm.variables.get('jumped')) { pm.variables.set('jumped', true); pm.execution.setNextRequest('Get user'); } else if (pm.info.iteration === 1) pm.execution.setNextRequest(null);"]}}]}
  ]
}`

func setup(t *testing.T) (*Runner, *postman.Collection, func(map[string]any) *vars.Set, *vars.Scope, *[]http.Header) {
	t.Helper()
	secret.MockInit()
	idp := idptest.New()
	idp.AddUser(idptest.User{Name: "rachel", Password: "hunter2"})
	idp.AddClient(idptest.Client{ID: "portal"})
	idp.Start()
	t.Cleanup(idp.Close)
	var seen []http.Header
	users := map[string]string{}
	api := httptest.NewServer(idp.Protect(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Header.Clone())
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == "POST" && r.URL.Path == "/users":
			var in map[string]string
			body, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(body, &in)
			id := "u" + string(rune('0'+len(users)))
			users[id] = in["name"]
			_ = json.NewEncoder(w).Encode(map[string]string{"id": id, "name": in["name"]})
		case strings.HasPrefix(r.URL.Path, "/users/"):
			id := strings.TrimPrefix(r.URL.Path, "/users/")
			_ = json.NewEncoder(w).Encode(map[string]string{"id": id, "name": users[id]})
		default:
			_, _ = io.WriteString(w, `{"ok":true}`)
		}
	})))
	t.Cleanup(api.Close)
	var c postman.Collection
	if err := json.Unmarshal([]byte(collection), &c); err != nil {
		t.Fatal(err)
	}
	r := &Runner{Engine: script.NewEngine(), Client: send.NewClient(send.NewJar("")), Auth: auth.NewManager(""), Settings: send.DefaultSettings()}
	local := &vars.Scope{Kind: vars.Local}
	env := &vars.Scope{Kind: vars.Environment, Name: "Local", Vars: []vars.Var{{Key: "base", Value: api.URL}, {Key: "tokenUrl", Value: idp.TokenURL()}}}
	col := &vars.Scope{Kind: vars.Collection, Name: "Shop"}
	scopes := func(row map[string]any) *vars.Set { return vars.New(local, DataScope(row), env, col) }
	return r, &c, scopes, col, &seen
}

func TestExecute(t *testing.T) {
	r, c, scopes, col, seen := setup(t)
	it, parents := c.Find("r1")
	ex := r.Execute(context.Background(), Target{Collection: c, Item: it, Parents: parents}, scopes(map[string]any{"name": "Ada"}), Options{})
	if ex.Err != nil {
		t.Fatal(ex.Err)
	}
	if ex.Response.Status != 200 || ex.Auth == nil || ex.Auth.Type != "oauth2" || ex.Auth.From != "collection" {
		t.Fatalf("response %d auth %+v", ex.Response.Status, ex.Auth)
	}
	h := (*seen)[0]
	if h.Get("X-Trace") != "run-0" || h.Get("X-Folder") != "users" || !strings.HasPrefix(h.Get("Authorization"), "Bearer ") {
		t.Errorf("headers sent: %v", h)
	}
	if got := len(ex.Tests()); got != 2 || !ex.Tests()[0].Passed || !ex.Tests()[1].Passed {
		t.Errorf("tests: %+v %v", ex.Tests(), ex.Scripts.Errors)
	}
	if v, _ := col.Get("userId"); v.Value != "u0" {
		t.Errorf("userId: %+v", v)
	}
	if ex.Prepared.Get("Authorization") == "" {
		t.Error("the prepared request doesn't show the auth header")
	}
}

func TestRunCollection(t *testing.T) {
	r, c, scopes, _, _ := setup(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "data.csv")
	_ = os.WriteFile(path, []byte("name\nAda\nGrace\n"), 0o600)
	rows, err := LoadData(path)
	if err != nil || len(rows) != 2 {
		t.Fatalf("data: %v %v", rows, err)
	}
	var names []string
	sum := r.RunCollection(context.Background(), c, RunOptions{Data: rows}, scopes, func(s Step) {
		mark := ""
		if s.Skipped {
			mark = " (skipped)"
		}
		names = append(names, s.Target.Item.Name+mark)
	})
	// Health jumps back to Get user once; in the second iteration it stops
	// the run.
	want := "Create user, Get user, Skipped (skipped), Health, Get user, Skipped (skipped), Health, Create user, Get user, Skipped (skipped), Health"
	if got := strings.Join(names, ", "); got != want {
		t.Errorf("order:\n%s\nwant:\n%s", got, want)
	}
	if sum.TestsFailed != 0 || sum.Errors != 0 {
		t.Errorf("summary: %+v", sum)
	}
	if !sum.Stopped {
		t.Error("setNextRequest(null) didn't stop the run")
	}
	// Only the users folder, bail on failure.
	sum = r.RunCollection(context.Background(), c, RunOptions{Folder: "f1", Data: []map[string]any{{"name": "Ada"}}}, scopes, nil)
	if sum.Requests != 3 || sum.Skipped != 1 {
		t.Errorf("folder run: %+v", sum)
	}
	// A run that goes round in circles is stopped.
	loop := &postman.Collection{Info: postman.Info{Name: "Loop"}, Item: []*postman.Item{{Name: "A", ID: "a", Request: &postman.Request{Method: "GET", URL: &postman.URL{Raw: "{{base}}/health"}},
		Event: postman.SetScript(nil, "test", "pm.execution.setNextRequest('A')")}}}
	sum = r.RunCollection(context.Background(), loop, RunOptions{}, scopes, nil)
	if !sum.Loop || sum.Requests != 100 {
		t.Errorf("loop: %+v", sum)
	}
}

// A body cut short comes back with its status, headers and what arrived,
// and the error saying so; no test script runs on half a body.
func TestBodyCutShort(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Length", "1000")
		w.WriteHeader(200)
		_, _ = io.WriteString(w, `{"items":[1,2,3`)
		w.(http.Flusher).Flush()
		if hj, ok := w.(http.Hijacker); ok {
			if c, _, err := hj.Hijack(); err == nil {
				_ = c.Close()
			}
		}
	}))
	defer srv.Close()
	var c postman.Collection
	doc := `{"info": {"name": "Cut"}, "item": [{"name": "Items", "id": "r1", "request": {"method": "GET", "url": "` + srv.URL + `/items"},
	  "event": [{"listen": "test", "script": {"exec": ["pm.test('ran', () => {});"]}}]}]}`
	if err := json.Unmarshal([]byte(doc), &c); err != nil {
		t.Fatal(err)
	}
	r := &Runner{Engine: script.NewEngine(), Client: send.NewClient(send.NewJar("")), Auth: auth.NewManager(""), Settings: send.DefaultSettings()}
	it, parents := c.Find("r1")
	ex := r.Execute(context.Background(), Target{Collection: &c, Item: it, Parents: parents}, vars.New(&vars.Scope{Kind: vars.Local}), Options{})
	var inc *send.IncompleteError
	if !errors.As(ex.Err, &inc) {
		t.Fatalf("error %v, want the body cut short", ex.Err)
	}
	if ex.Response == nil || ex.Response.Status != 200 || string(ex.Response.Body) != `{"items":[1,2,3` {
		t.Fatalf("response %+v", ex.Response)
	}
	if len(ex.Tests()) != 0 {
		t.Fatalf("tests ran on half a body: %+v", ex.Tests())
	}
}
