package store

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shinyvision/posthero/internal/postman"
	"github.com/shinyvision/posthero/internal/postman/pmtest"
	"github.com/shinyvision/posthero/internal/vars"
)

func TestCurrentValues(t *testing.T) {
	initial := []Initial{{Key: "base", Value: "http://x"}, {Key: "token", Value: "t", Secret: true}, {Key: "off", Value: "1", Off: true}}
	o := Overrides{"base": {Value: "http://y"}, "mine": {Value: "only here"}, "token": {Removed: true}}
	s := Scope(vars.Environment, "Local", initial, o)
	if len(s.Vars) != 2 || s.Vars[0].Value != "http://y" || s.Vars[1].Key != "mine" {
		t.Errorf("scope: %+v", s.Vars)
	}
	// Back to overrides: what differs from the team's values.
	s.Set("base", "http://x")
	s.SetTyped("count", "5", int64(5))
	got := Diff(initial, s)
	if _, ok := got["base"]; ok || got["mine"].Value != "only here" || !got["token"].Removed || got["count"].Typed != int64(5) {
		t.Errorf("diff: %+v", got)
	}
	dir := t.TempDir()
	v := NewValues(dir)
	if err := v.Set("env1", got); err != nil {
		t.Fatal(err)
	}
	again := NewValues(dir).Get("env1")
	if again["mine"].Value != "only here" || !again["token"].Removed {
		t.Errorf("saved: %+v", again)
	}
}

func TestRefreshAndScopes(t *testing.T) {
	srv := pmtest.New()
	defer srv.Close()
	api := postman.NewClient(pmtest.Key, srv.Start())
	colID := srv.AddCollection("ws1", &postman.Collection{Info: postman.Info{Name: "Shop"}, Variable: []postman.Variable{{Key: "id", Value: "1"}},
		Item: []*postman.Item{{Name: "List", Request: &postman.Request{Method: "GET", URL: &postman.URL{Raw: "{{base}}/items"}}}}})
	envID := srv.AddEnvironment("ws1", &postman.Environment{Name: "Local", Values: []postman.EnvValue{{Key: "base", Value: "http://localhost"}}})
	srv.SetGlobals("ws1", []postman.EnvValue{{Key: "g", Value: "1"}})
	cache := t.TempDir()
	s := Open(api, postman.Workspace{ID: "ws1"}, cache, t.TempDir())
	f, err := Fetch(context.Background(), s.Plan(false))
	if err != nil {
		t.Fatal(err)
	}
	ch := s.Apply(f)
	if len(ch.Cols) != 1 || len(ch.Envs) != 1 || !ch.Globals {
		t.Fatalf("changes: %+v", ch)
	}
	// Nothing changed: nothing is fetched again.
	before := srv.Calls["GET /collections/"+pmtest.Owner+"-"+colID]
	f, _ = Fetch(context.Background(), s.Plan(false))
	if ch := s.Apply(f); ch.Any() || srv.Calls["GET /collections/"+pmtest.Owner+"-"+colID] != before {
		t.Errorf("refetched without a change: %+v", ch)
	}
	// A teammate's change in the collection: Postman's list doesn't show
	// it (the collection's updatedAt stays), so it takes a recheck, which
	// goes first, while Postman lists what there is.
	srv.Edit(colID, func(c *postman.Collection) *postman.Item { c.Item[0].Name = "List items"; return c.Item[0] })
	f, _ = Fetch(context.Background(), s.Plan(false))
	if ch := s.Apply(f); ch.Any() {
		t.Errorf("the list showed a change in the collection: %+v", ch)
	}
	p := s.Plan(false)
	p.Recheck = []string{colID}
	f, err = Fetch(context.Background(), p)
	if ch := s.Apply(f); err != nil || len(f.Errors) > 0 || len(ch.Cols) != 1 || s.Col(colID).Data.Item[0].Name != "List items" {
		t.Errorf("teammate's change: %+v %v %v", ch, err, f.Errors)
	}
	// A change to the collection itself the list shows.
	srv.Edit(colID, func(c *postman.Collection) *postman.Item {
		c.Variable = append(c.Variable, postman.Variable{Key: "page", Value: "2"})
		return nil
	})
	f, _ = Fetch(context.Background(), s.Plan(false))
	if ch := s.Apply(f); len(ch.Cols) != 1 || len(s.Col(colID).Data.Variable) != 2 {
		t.Errorf("teammate's change to the collection: %+v", ch)
	}
	// The cache brings it all back.
	again := Open(api, postman.Workspace{ID: "ws1"}, cache, "")
	if c := again.Col(colID); c == nil || c.Data.Item[0].Name != "List items" || again.Env(envID) == nil || len(again.Globals()) != 1 {
		t.Errorf("cache: %+v", again.Collections())
	}

	// Scopes, and scripts' changes kept as current values.
	set := s.Scopes(colID, envID, nil, nil)
	if got := set.Resolve("{{base}}/{{id}}/{{g}}"); got != "http://localhost/1/1" {
		t.Errorf("resolve: %s", got)
	}
	start := set.Clone()
	set.Scope(vars.Environment).Set("base", "http://127.0.0.1")
	set.Scope(vars.Collection).Set("token", "abc")
	// Meanwhile, here: your value of id changes. The commit keeps it.
	now := s.Scopes(colID, envID, nil, nil)
	now.Scope(vars.Environment).Set("id", "2")
	if err := s.SetCurrent(vars.Environment, envID, now.Scope(vars.Environment)); err != nil {
		t.Fatal(err)
	}
	if err := s.CommitChanges(start, set, colID, envID); err != nil {
		t.Fatal(err)
	}
	set = s.Scopes(colID, envID, nil, nil)
	if got := set.Resolve("{{base}} {{token}} {{id}}"); got != "http://127.0.0.1 abc 2" {
		t.Errorf("after commit: %s", got)
	}
	// The team's values didn't change.
	if v, _ := srv.Environment(envID).Get("base"); v != "http://localhost" {
		t.Errorf("team value changed: %s", v)
	}
	// A removed collection goes.
	if err := api.DeleteCollection(context.Background(), pmtest.Owner+"-"+colID); err != nil {
		t.Fatal(err)
	}
	// (A recheck of it isn't a failure.)
	p = s.Plan(false)
	p.Recheck = []string{colID}
	f, err = Fetch(context.Background(), p)
	if ch := s.Apply(f); err != nil || len(f.Errors) > 0 || len(ch.Gone) != 1 || s.Col(colID) != nil {
		t.Errorf("gone: %+v %v %v", ch, err, f.Errors)
	}
}

func TestFiles(t *testing.T) {
	dir := t.TempDir()
	col := `{"info": {"name": "Local", "schema": "` + postman.SchemaV21 + `"}, "item": [{"name": "A", "request": {"method": "GET", "url": "http://x"}}]}`
	env := `{"name": "Dev", "values": [{"key": "base", "value": "http://x", "enabled": true}], "_postman_variable_scope": "environment"}`
	_ = os.WriteFile(filepath.Join(dir, "c.postman_collection.json"), []byte(col), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "e.postman_environment.json"), []byte(env), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "other.json"), []byte(`{"x": 1}`), 0o644)
	s, err := OpenFiles([]string{dir}, "")
	if err != nil {
		t.Fatal(err)
	}
	cols := s.Collections()
	if len(cols) != 1 || len(s.Environments()) != 1 {
		t.Fatalf("opened %d collections, %d environments", len(cols), len(s.Environments()))
	}
	it := cols[0].Data.Item[0]
	if !strings.HasPrefix(it.ID, generatedPrefix) {
		t.Errorf("no id: %q", it.ID)
	}
	// The same ids next time.
	s2, _ := OpenFiles([]string{dir}, "")
	if s2.Collections()[0].Data.Item[0].ID != it.ID {
		t.Error("ids change between runs")
	}
	it.Name = "B"
	data, _ := FileData(cols[0].Data)
	if err := WriteFile(cols[0].Path, data); err != nil {
		t.Fatal(err)
	}
	written, _ := os.ReadFile(cols[0].Path)
	if strings.Contains(string(written), generatedPrefix) || !strings.Contains(string(written), `"name":"B"`) {
		t.Errorf("written:\n%s", written)
	}
	if _, err := OpenFiles([]string{filepath.Join(dir, "other.json")}, ""); err == nil {
		t.Error("opened a file that isn't Postman's")
	}
}

// Two files with the same id (a copy renamed) both open, each with its own
// id and current values, and posthero says so.
func TestFilesSameID(t *testing.T) {
	dir := t.TempDir()
	env := func(name string) string {
		return `{"id": "e1", "name": "` + name + `", "values": [{"key": "base", "value": "http://` + name + `"}]}`
	}
	col := func(name string) string {
		return `{"info": {"_postman_id": "c1", "name": "` + name + `"}, "item": [{"name": "A", "request": {"method": "GET", "url": "http://x"}}]}`
	}
	_ = os.WriteFile(filepath.Join(dir, "dev.postman_environment.json"), []byte(env("dev")), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "staging.postman_environment.json"), []byte(env("staging")), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "a.postman_collection.json"), []byte(col("Shop")), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "b.postman_collection.json"), []byte(col("Shop copy")), 0o644)
	s, err := OpenFiles([]string{dir, filepath.Join(dir, "dev.postman_environment.json")}, t.TempDir())
	if s == nil {
		t.Fatal(err)
	}
	envs, cols := s.Environments(), s.Collections()
	if len(envs) != 2 || len(cols) != 2 {
		t.Fatalf("opened %d environments, %d collections: %v", len(envs), len(cols), err)
	}
	if envs[0].Summary.ID == envs[1].Summary.ID || cols[0].Summary.ID == cols[1].Summary.ID {
		t.Errorf("same ids: %s %s", envs[0].Summary.ID, cols[0].Summary.ID)
	}
	if cols[0].Data.Item[0].ID == cols[1].Data.Item[0].ID {
		t.Error("the copy's requests have the same ids")
	}
	if err == nil || !strings.Contains(err.Error(), "staging.postman_environment.json") || !strings.Contains(err.Error(), "b.postman_collection.json") {
		t.Errorf("no warning: %v", err)
	}
	// dev.postman_environment.json was named twice: that is no copy.
	if strings.Count(err.Error(), "\n") != 1 {
		t.Errorf("warnings: %v", err)
	}
	if err := s.Values.Set(envs[0].Summary.ID, Overrides{"token": {Value: "t"}}); err != nil {
		t.Fatal(err)
	}
	if len(s.Values.Get(envs[1].Summary.ID)) != 0 {
		t.Error("the two share current values")
	}
}

// Two posthero instances keep current values of the same scope: neither
// loses what the other set, and each sees the other's.
func TestValuesTwoInstances(t *testing.T) {
	dir := t.TempDir()
	a, b := NewValues(dir), NewValues(dir)
	_, _ = a.Get("env"), b.Get("env")
	if err := a.Set("env", Overrides{"token": {Value: "t"}}); err != nil {
		t.Fatal(err)
	}
	if err := b.Set("env", Overrides{"orderId": {Value: "42"}}); err != nil {
		t.Fatal(err)
	}
	got := NewValues(dir).Get("env")
	if got["token"].Value != "t" || got["orderId"].Value != "42" {
		t.Errorf("on disk: %+v", got)
	}
	if b.Get("env")["token"].Value != "t" || a.Get("env")["orderId"].Value != "42" {
		t.Errorf("not seen: %+v %+v", a.Get("env"), b.Get("env"))
	}
	// A removal by one stays removed.
	cur := a.Get("env")
	next := Overrides{}
	for k, v := range cur {
		if k != "token" {
			next[k] = v
		}
	}
	if err := a.Set("env", next); err != nil {
		t.Fatal(err)
	}
	if err := b.Set("env", Overrides{"orderId": {Value: "43"}}); err != nil {
		t.Fatal(err)
	}
	if got := NewValues(dir).Get("env"); len(got) != 1 || got["orderId"].Value != "43" {
		t.Errorf("after removal: %+v", got)
	}
	// Non-text values compare by value, whatever their Go type: b, which
	// read n as 5, doesn't put 5 back over a's 6.
	if err := a.Set("env2", Overrides{"n": {Value: "5", Typed: int64(5)}}); err != nil {
		t.Fatal(err)
	}
	_ = b.Get("env2")
	if err := a.Set("env2", Overrides{"n": {Value: "6", Typed: int64(6)}}); err != nil {
		t.Fatal(err)
	}
	if err := b.Set("env2", Overrides{"n": {Value: "5", Typed: int64(5)}, "m": {Value: "x"}}); err != nil {
		t.Fatal(err)
	}
	if got := NewValues(dir).Get("env2"); len(got) != 2 || got["n"].Value != "6" || got["n"].Typed != float64(6) {
		t.Errorf("typed: %+v", got)
	}
	// Resetting everything (after a look at what there is) clears the file.
	_ = a.Get("env")
	if err := a.Set("env", nil); err != nil {
		t.Fatal(err)
	}
	if got := NewValues(dir).Get("env"); len(got) != 0 {
		t.Errorf("after reset: %+v", got)
	}
	if _, err := os.Stat(a.path("env")); !os.IsNotExist(err) {
		t.Errorf("file left: %v", err)
	}
}

// Files are written through a temporary file of their own, so two writers
// (or a leftover) never get in each other's way.
func TestWritesUseTheirOwnTempFiles(t *testing.T) {
	dir := t.TempDir()
	v := NewValues(dir)
	// Something in the way where a fixed temporary name would go.
	_ = os.MkdirAll(v.path("env")+".tmp", 0o700)
	if err := v.Set("env", Overrides{"a": {Value: "1"}}); err != nil {
		t.Errorf("values: %v", err)
	}
	_ = os.MkdirAll(filepath.Join(dir, "globals.json.tmp"), 0o700)
	writeCacheFile(dir, "globals.json", []int{1})
	if data, _ := os.ReadFile(filepath.Join(dir, "globals.json")); string(data) != "[1]" {
		t.Errorf("cache: %q", data)
	}
	path := filepath.Join(dir, "c.json")
	_ = os.MkdirAll(path+".posthero-tmp", 0o700)
	if err := WriteFile(path, []byte("{}\n")); err != nil {
		t.Errorf("file: %v", err)
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if !e.IsDir() && strings.Contains(e.Name(), "tmp") {
			t.Errorf("left behind: %s", e.Name())
		}
	}
}

// exportedCol is a collection as Postman exports it.
const exportedCol = `{
	"info": {
		"_postman_id": "0b6f6c55-1111-4c3e-9e57-0123456789ab",
		"name": "Shop",
		"schema": "https://schema.getpostman.com/json/collection/v2.1.0/collection.json",
		"_exporter_id": "123"
	},
	"item": [
		{
			"name": "List",
			"event": [
				{
					"listen": "test",
					"script": {
						"exec": [
							"pm.test(\"ok\", () => {});"
						],
						"type": "text/javascript"
					}
				}
			],
			"request": {
				"method": "GET",
				"header": [],
				"url": {
					"raw": "{{base}}/items?limit={{limit}}",
					"host": [
						"{{base}}"
					],
					"path": [
						"items"
					],
					"query": [
						{
							"key": "limit",
							"value": "{{limit}}"
						}
					]
				}
			},
			"response": []
		},
		{
			"name": "Create",
			"request": {
				"method": "POST",
				"header": [],
				"body": {
					"mode": "raw",
					"raw": "{\"a\": \"<b> & \u00e9\"}",
					"options": {
						"raw": {
							"language": "json"
						}
					}
				},
				"url": "{{base}}/items"
			},
			"response": []
		}
	],
	"event": [
		{
			"listen": "prerequest",
			"script": {
				"type": "text/javascript",
				"exec": [
					""
				]
			}
		}
	],
	"variable": [
		{
			"key": "limit",
			"value": 25
		},
		{
			"key": "debug",
			"value": false,
			"type": "boolean"
		},
		{
			"key": "filter",
			"value": {
				"a": 1
			}
		}
	]
}
`

// exportedEnv is an environment as Postman exports it.
const exportedEnv = `{
	"id": "5c1e0000-2222-4d4d-8888-0123456789ab",
	"name": "Dev",
	"values": [
		{
			"key": "base",
			"value": "http://localhost",
			"type": "default",
			"enabled": true
		},
		{
			"key": "port",
			"value": 8080,
			"type": "default",
			"enabled": true
		},
		{
			"key": "trace",
			"value": "",
			"type": "default",
			"enabled": false
		}
	],
	"_postman_variable_scope": "environment",
	"_postman_exported_at": "2026-01-01T00:00:00.000Z",
	"_postman_exported_using": "Postman/11.0"
}
`

// openOne opens one file and returns the store.
func openOne(t *testing.T, name, text string) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := OpenFiles([]string{path}, "")
	if err != nil {
		t.Fatal(err)
	}
	return s, path
}

// Saving a file changes what was changed, and nothing else: its key order,
// empty lists, numbers and how strings are written stay.
func TestWriteBackKeepsTheFile(t *testing.T) {
	s, path := openOne(t, "shop.postman_collection.json", exportedCol)
	c := s.Collections()[0]
	c.Data.Item[1].Name = "Create item"
	data, err := FileData(c.Data)
	if err != nil {
		t.Fatal(err)
	}
	if err := WriteFile(path, data); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(path)
	want := strings.Replace(exportedCol, `"name": "Create"`, `"name": "Create item"`, 1)
	if string(got) != want {
		t.Errorf("renaming a request rewrote the file:\n%s", lineDiff(want, string(got)))
	}

	// A header added where there were none goes in the empty list's place.
	c.Data.Item[0].Request.Header = postman.Params{{Key: "Accept", Value: "application/json"}}
	data, _ = FileData(c.Data)
	if err := WriteFile(path, data); err != nil {
		t.Fatal(err)
	}
	got, _ = os.ReadFile(path)
	want = strings.Replace(want, `"header": [],`, `"header": [
					{
						"key": "Accept",
						"value": "application/json"
					}
				],`, 1)
	if string(got) != want {
		t.Errorf("adding a header:\n%s", lineDiff(want, string(got)))
	}

	// What is removed goes.
	c.Data.Variable = c.Data.Variable[:1]
	c.Data.Item = c.Data.Item[:1]
	data, _ = FileData(c.Data)
	if err := WriteFile(path, data); err != nil {
		t.Fatal(err)
	}
	again, _ := OpenFiles([]string{path}, "")
	ac := again.Collections()[0].Data
	if len(ac.Item) != 1 || len(ac.Variable) != 1 || ac.Variable[0].Value != "25" || len(ac.Item[0].Request.Header) != 1 {
		t.Errorf("after removing: %d items, variables %+v", len(ac.Item), ac.Variable)
	}
	got, _ = os.ReadFile(path)
	if !strings.Contains(string(got), `"value": 25`) || strings.Contains(string(got), "debug") || strings.Contains(string(got), "Create") {
		t.Errorf("after removing:\n%s", got)
	}

	// An environment: only the value changed is written differently.
	s, path = openOne(t, "dev.postman_environment.json", exportedEnv)
	e := s.Environments()[0]
	e.Data.Values[0].Value = "http://127.0.0.1"
	data, err = EnvFileData(e.Data)
	if err != nil {
		t.Fatal(err)
	}
	if err := WriteFile(path, data); err != nil {
		t.Fatal(err)
	}
	got, _ = os.ReadFile(path)
	want = strings.Replace(exportedEnv, `"http://localhost"`, `"http://127.0.0.1"`, 1)
	if string(got) != want {
		t.Errorf("changing a value rewrote the environment:\n%s", lineDiff(want, string(got)))
	}
	// A variable switched back on is on when read again.
	e.Data.Values[2].Enabled = nil
	data, _ = EnvFileData(e.Data)
	if err := WriteFile(path, data); err != nil {
		t.Fatal(err)
	}
	again, _ = OpenFiles([]string{path}, "")
	if v := again.Environments()[0].Data.Values[2]; !v.On() {
		t.Errorf("trace is still off: %+v", v)
	}
}

// A file holding an API response keeps its wrapper, and a minified file
// stays minified.
func TestWriteBackKeepsTheWrapper(t *testing.T) {
	text := `{"collection":{"info":{"_postman_id":"c9","name":"Shop"},"item":[{"name":"A","request":{"method":"GET","header":[],"url":"http://x"},"response":[]}]}}`
	s, path := openOne(t, "shop.json", text)
	c := s.Collections()[0]
	c.Data.Item[0].Name = "B"
	data, _ := FileData(c.Data)
	if err := WriteFile(path, data); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(path)
	want := strings.Replace(text, `"name":"A"`, `"name":"B"`, 1)
	// The schema posthero adds is all that is new.
	want = strings.Replace(want, `"name":"Shop"`, `"name":"Shop","schema":"`+postman.SchemaV21+`"`, 1)
	if string(got) != want {
		t.Errorf("got\n%s\nwant\n%s", got, want)
	}
}

// lineDiff shows the first line where two texts differ.
func lineDiff(want, got string) string {
	w, g := strings.Split(want, "\n"), strings.Split(got, "\n")
	for i := 0; i < len(w) || i < len(g); i++ {
		var a, b string
		if i < len(w) {
			a = w[i]
		}
		if i < len(g) {
			b = g[i]
		}
		if a != b {
			return fmt.Sprintf("line %d:\nwant %q\ngot  %q\n\n%s", i+1, a, b, got)
		}
	}
	return "(the same)"
}
