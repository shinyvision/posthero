package reqdoc

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/shinyvision/posthero/internal/postman"
)

func item(t *testing.T, js string) *postman.Item {
	t.Helper()
	var it postman.Item
	if err := json.Unmarshal([]byte(js), &it); err != nil {
		t.Fatal(err)
	}
	return &it
}

func TestRenderGraphQL(t *testing.T) {
	it := item(t, `{"name": "Create installer", "request": {
		"method": "POST",
		"header": [{"key": "Origin", "value": "{{Origin}}"}, {"key": "X-Debug", "value": "1", "disabled": true}],
		"body": {"mode": "graphql", "graphql": {"query": "mutation CreateInstaller($input: CreateInstallerInput!) {\n  createInstaller(input: $input) { id }\n}", "variables": "{\n  \"input\": {\"name\": \"x\"}\n}"}},
		"url": {"raw": "{{BackendEndpoint}}/graphql", "host": ["{{BackendEndpoint}}"], "path": ["graphql"]}
	}, "event": [{"listen": "test", "script": {"exec": ["const body = pm.response.json();", "pm.test('ok', () => {});"], "type": "text/javascript"}}]}`)
	want := `POST {{BackendEndpoint}}/graphql
Origin: {{Origin}}
# X-Debug: 1

mutation CreateInstaller($input: CreateInstallerInput!) {
  createInstaller(input: $input) { id }
}

### variables
{
  "input": {"name": "x"}
}

### tests
const body = pm.response.json();
pm.test('ok', () => {});
`
	if got := Render(it); got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
	back, err := Apply(it, want)
	if err != nil {
		t.Fatal(err)
	}
	if Render(back) != want {
		t.Errorf("not stable:\n%s", Render(back))
	}
}

func TestEditing(t *testing.T) {
	old := item(t, `{"name": "Users", "request": {
		"method": "GET",
		"header": [{"key": "Accept", "value": "application/json", "description": "what we want"}],
		"url": {"raw": "{{base}}/teams/:team/users?page=1", "query": [{"key": "page", "value": "1", "description": "from 0"}], "variable": [{"key": "team", "value": "7", "description": "the team"}]}
	}, "response": [{"name": "example", "code": 200}]}`)
	doc := `POST {{base}}/teams/:team/users
    ?page=2
    &size=20
    # &debug
    :team = 8
Accept: application/json
X-New: yes please

[1, 2]

### auth bearer
token: {{token}}

### pre-request
pm.variables.set("x", 1)

### settings
followRedirects: false
`
	it, err := Apply(old, doc)
	if err != nil {
		t.Fatal(err)
	}
	r := it.Request
	if r.Method != "POST" || r.URL.Raw != "{{base}}/teams/:team/users?page=2&size=20" {
		t.Errorf("method/url: %s %s", r.Method, r.URL.Raw)
	}
	if len(r.URL.Query) != 3 || r.URL.Query[0].Description == nil || !r.URL.Query[2].Disabled || !r.URL.Query[2].NoValue {
		t.Errorf("query: %+v", r.URL.Query)
	}
	if v := r.URL.Variable; len(v) != 1 || v[0].Value != "8" || v[0].Description == nil {
		t.Errorf("path variables: %+v", v)
	}
	if len(r.Header) != 2 || r.Header[0].Description == nil || r.Header[1].Value != "yes please" {
		t.Errorf("headers: %+v", r.Header)
	}
	if r.Body.Mode != "raw" || r.Body.Raw != "[1, 2]" || r.Body.Language() != "json" {
		t.Errorf("body: %+v", r.Body)
	}
	if r.Auth.Type != "bearer" || r.Auth.Get("token") != "{{token}}" {
		t.Errorf("auth: %+v", r.Auth)
	}
	if postman.Scripts(it.Event, "prerequest") != `pm.variables.set("x", 1)` {
		t.Errorf("events: %+v", it.Event)
	}
	if string(it.ProtocolProfileBehavior) != `{"followRedirects":false}` {
		t.Errorf("settings: %s", it.ProtocolProfileBehavior)
	}
	if len(it.Response) != 1 {
		t.Error("examples lost")
	}
	if Render(it) != doc {
		t.Errorf("render:\n%s\nwant:\n%s", Render(it), doc)
	}

	// Taking things out takes them out.
	it2, err := Apply(it, "GET {{base}}/teams/:team/users\n    :team = 8\n")
	if err != nil {
		t.Fatal(err)
	}
	if it2.Request.Body != nil || it2.Request.Auth != nil || len(it2.Event) != 0 || len(it2.Request.Header) != 0 || it2.Request.URL.Query != nil {
		t.Errorf("not removed: %+v", it2.Request)
	}
	if !strings.Contains(string(it2.ProtocolProfileBehavior), `"followRedirects":true`) {
		t.Errorf("setting not reset: %s", it2.ProtocolProfileBehavior)
	}
}

func TestBodies(t *testing.T) {
	cases := []struct{ doc, mode, lang string }{
		{"POST x\n\n{\"a\": 1}\n", "raw", "json"},
		{"POST x\n\n{\n  \"a\": {{n}}\n}\n", "raw", "json"},
		{"POST x\n\n{ users { id } }\n", "graphql", ""},
		{"POST x\n\nquery { a }\n", "graphql", ""},
		{"POST x\n\n<a>1</a>\n", "raw", "xml"},
		{"POST x\n\nhello there\n", "raw", "text"},
		{"POST x\n\n### xml\nnot really xml\n", "raw", "xml"},
		{"POST x\n\n### graphql\n{{query}}\n\n### variables\n{}\n", "graphql", ""},
		{"POST x\n\n### form\na: 1\n# b: 2\n", "urlencoded", ""},
		{"POST x\n\n### multipart\nname: Ada\nphoto: @/tmp/a.png;type=image/png\nphoto: @/tmp/b.png;type=image/png\nmeta: {\"x\":1};type=application/json\n", "formdata", ""},
		{"POST x\n\n### file\n@/tmp/data.bin\n", "file", ""},
	}
	for _, c := range cases {
		it, err := Apply(&postman.Item{}, c.doc)
		if err != nil {
			t.Errorf("%q: %v", c.doc, err)
			continue
		}
		b := it.Request.Body
		if b == nil || b.Mode != c.mode || b.Language() != c.lang {
			t.Errorf("%q: body %+v", c.doc, b)
			continue
		}
		if got := Render(it); got != c.doc {
			t.Errorf("render of %q:\n%s", c.doc, got)
		}
	}
	it, _ := Apply(&postman.Item{}, cases[9].doc)
	fd := it.Request.Body.FormData
	if len(fd) != 3 || fd[1].Type != "file" || len(fd[1].Files()) != 2 || fd[1].ContentType != "image/png" || fd[2].ContentType != "application/json" {
		t.Errorf("formdata: %+v", fd)
	}
}

func TestErrors(t *testing.T) {
	cases := map[string]int{
		"":                                   1,
		"GET x\nnot a header\n":              2,
		"GET x\n\n### nonsense\n":            3,
		"GET x\n    nonsense\n":              2,
		"GET x\n    :id = 1\n":               2,
		"GET x\n\n{}\n\n### variables\n{}\n": 5,
		"GET x\n\n### auth\n":                3,
		"GET x\n\n### auth fancy\n":          3,
		"GET x\n\n### form\n@nope\n":         4,
	}
	for doc, line := range cases {
		_, err := Apply(&postman.Item{}, doc)
		var e *Error
		if !errors.As(err, &e) || e.Line != line {
			t.Errorf("%q: %v, want an error on line %d", doc, err, line)
		}
	}
}

func TestSectionEscaping(t *testing.T) {
	it := &postman.Item{Request: &postman.Request{Method: "POST", URL: &postman.URL{Raw: "x"}, Body: &postman.Body{Mode: "raw", Raw: "# Title\n### not a section\nend"}}}
	it.Request.Body.SetLanguage("text")
	doc := Render(it)
	back, err := Apply(it, doc)
	if err != nil {
		t.Fatal(err)
	}
	if back.Request.Body.Raw != it.Request.Body.Raw {
		t.Errorf("escaped body: %q from\n%s", back.Request.Body.Raw, doc)
	}
}

func TestCollectionAndFolder(t *testing.T) {
	c := &postman.Collection{Info: postman.Info{Name: "Shop"}, Variable: []postman.Variable{{Key: "base", Value: "http://x", Description: &postman.Description{Content: "kept"}}}}
	c.Auth = &postman.Auth{Type: "oauth2", Params: map[string][]postman.AuthParam{"oauth2": {
		{Key: "grant_type", Value: json.RawMessage(`"authorization_code_with_pkce"`), Type: "string"},
		{Key: "useBrowser", Value: json.RawMessage(`false`), Type: "boolean"},
		{Key: "authRequestParams", Value: json.RawMessage(`[{"key":"audience","value":"x","enabled":true}]`), Type: "any"},
	}}}
	doc := RenderCollection(c)
	if !strings.Contains(doc, "### auth oauth2\ngrant_type: authorization_code_with_pkce\nuseBrowser: false\n") || !strings.Contains(doc, "### variables\nbase: http://x\n") {
		t.Errorf("collection doc:\n%s", doc)
	}
	doc = strings.Replace(doc, "base: http://x", "base: http://y\nnew: 1", 1)
	got, err := ApplyCollection(c, doc)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Variable) != 2 || got.Variable[0].Value != "http://y" || got.Variable[0].Description == nil {
		t.Errorf("variables: %+v", got.Variable)
	}
	if string(got.Auth.Raw("useBrowser")) != "false" || !strings.HasPrefix(string(got.Auth.Raw("authRequestParams")), "[") {
		t.Errorf("auth types: %s %s", got.Auth.Raw("useBrowser"), got.Auth.Raw("authRequestParams"))
	}

	f := &postman.Item{Name: "Users", Folder: true}
	fd, err := ApplyFolder(f, RenderFolder(f)+"\n### auth none\n\n### tests\npm.test('x', () => {})\n")
	if err != nil {
		t.Fatal(err)
	}
	if fd.Auth.Type != "noauth" || postman.Scripts(fd.Event, "test") == "" {
		t.Errorf("folder: %+v", fd)
	}
	if _, err := ApplyFolder(f, "### variables\na: 1\n"); err == nil {
		t.Error("a folder took variables")
	}
}

// TestCorpus renders and reads back every request of real collections in
// $POSTHERO_CORPUS: nothing may change.
func TestCorpus(t *testing.T) {
	dir := os.Getenv("POSTHERO_CORPUS")
	if dir == "" {
		t.Skip("POSTHERO_CORPUS is not set")
	}
	files, _ := filepath.Glob(filepath.Join(dir, "col", "*.json"))
	n := 0
	for _, f := range files {
		data, _ := os.ReadFile(f)
		var w struct {
			Collection postman.Collection `json:"collection"`
		}
		if err := json.Unmarshal(data, &w); err != nil {
			t.Fatal(err)
		}
		c := &w.Collection
		for _, loc := range c.Requests() {
			n++
			it := loc.Item
			doc := Render(it)
			back, err := Apply(it, doc)
			if err != nil {
				t.Errorf("%s / %s: %v\n%s", c.Info.Name, loc.Path(), err, doc)
				continue
			}
			if again := Render(back); again != doc {
				if d := os.Getenv("DUMP"); d != "" {
					_ = os.WriteFile(filepath.Join(d, "a.txt"), []byte(doc), 0o600)
					_ = os.WriteFile(filepath.Join(d, "b.txt"), []byte(again), 0o600)
				}
				t.Errorf("%s / %s: render not stable:\n%s\n---\n%s", c.Info.Name, loc.Path(), doc, again)
				continue
			}
			if diff := sameRequest(it, back); diff != "" {
				t.Errorf("%s / %s: %s\n%s", c.Info.Name, loc.Path(), diff, doc)
			}
		}
		folders := 0
		c.Walk(func(it *postman.Item, _ []*postman.Item) bool {
			if it.Folder {
				folders++
				back, err := ApplyFolder(it, RenderFolder(it))
				if err != nil || RenderFolder(back) != RenderFolder(it) || !reflect.DeepEqual(norm(back.Auth), norm(it.Auth)) {
					t.Errorf("%s / folder %s: %v", c.Info.Name, it.Name, err)
				}
			}
			return true
		})
		back, err := ApplyCollection(c, RenderCollection(c))
		if err != nil || RenderCollection(back) != RenderCollection(c) {
			t.Errorf("%s: collection doc: %v", c.Info.Name, err)
		}
	}
	t.Logf("%d requests", n)
}

// sameRequest compares what a document covers.
func sameRequest(a, b *postman.Item) string {
	ra, rb := a.Request, b.Request
	if ra.URL == nil {
		ra.URL = &postman.URL{}
	}
	checks := []struct {
		what string
		x, y any
	}{
		{"method", strings.ToUpper(orGET(ra.Method)), rb.Method},
		{"url", ra.URL.SendRaw(), rb.URL.SendRaw()},
		{"query", norm(ra.URL.Query), norm(rb.URL.Query)},
		{"path variables", norm(ra.URL.Variable), norm(rb.URL.Variable)},
		{"headers", norm(ra.Header), norm(rb.Header)},
		{"body", norm(cleanBody(ra.Body)), norm(cleanBody(rb.Body))},
		{"auth", norm(ra.Auth), norm(rb.Auth)},
		{"pre-request", trimEnd(postman.Scripts(a.Event, "prerequest")), trimEnd(postman.Scripts(b.Event, "prerequest"))},
		{"tests", trimEnd(postman.Scripts(a.Event, "test")), trimEnd(postman.Scripts(b.Event, "test"))},
	}
	for _, c := range checks {
		if !reflect.DeepEqual(c.x, c.y) {
			return c.what + " changed: " + jsonOf(c.x) + " → " + jsonOf(c.y)
		}
	}
	return ""
}

func cleanBody(b *postman.Body) *postman.Body {
	if b == nil {
		return nil
	}
	c := *b
	c.URLEncoded = nonBlank(b.URLEncoded)
	c.FormData = nonBlank(b.FormData)
	return &c
}

func orGET(m string) string {
	if m == "" {
		return "GET"
	}
	return m
}

func nonBlank(ps postman.Params) postman.Params {
	var keep postman.Params
	for _, p := range ps {
		if !blank(p) {
			keep = append(keep, p)
		}
	}
	return keep
}

func norm(v any) any {
	if ps, ok := v.(postman.Params); ok {
		v = nonBlank(ps)
	}
	data, _ := json.Marshal(v)
	var out any
	_ = json.Unmarshal(data, &out)
	return dropEmpty(out)
}

func dropEmpty(v any) any {
	switch x := v.(type) {
	case map[string]any:
		out := map[string]any{}
		for k, val := range x {
			// Ids are given by the server, and "text" is the default type
			// of a form field.
			if k == "id" || (k == "type" && val == "text") {
				continue
			}
			if d := dropEmpty(val); d != nil && d != "" && d != false {
				out[k] = d
			}
		}
		if len(out) == 0 {
			return nil
		}
		return out
	case []any:
		if len(x) == 0 {
			return nil
		}
		for i := range x {
			x[i] = dropEmpty(x[i])
		}
	}
	return v
}

func jsonOf(v any) string {
	data, _ := json.Marshal(v)
	if len(data) > 300 {
		return string(data[:300]) + "…"
	}
	return string(data)
}

func TestGraphQLQuery(t *testing.T) {
	doc := "POST {{base}}/graphql\nOrigin: x\n\nquery Q {\n  me { id }\n}\n\n### variables\n{}"
	q, line, ok := GraphQLQuery(doc)
	if !ok || line != 3 || q != "query Q {\n  me { id }\n}\n" {
		t.Fatalf("inline: %q %d %v", q, line, ok)
	}
	doc = "POST {{base}}/graphql\n\n### graphql\n{ me { id } }\n### variables\n{}"
	if q, line, ok = GraphQLQuery(doc); !ok || line != 3 || q != "{ me { id } }" {
		t.Fatalf("section: %q %d %v", q, line, ok)
	}
	// Blank lines and trailing spaces stay, so places map 1:1.
	doc = "POST {{base}}/graphql\n\n### graphql\n\n{ me { id }  \n}"
	if q, line, ok = GraphQLQuery(doc); !ok || line != 3 || q != "\n{ me { id }  \n}" {
		t.Fatalf("blank line after the section: %q %d %v", q, line, ok)
	}
	if _, _, ok = GraphQLQuery("POST x\n\n{\"a\": 1}"); ok {
		t.Fatal("JSON isn't GraphQL")
	}
}

// roundTrip renders an item, reads the document back and checks that a
// second render is the same.
func roundTrip(t *testing.T, it *postman.Item) *postman.Item {
	t.Helper()
	doc := Render(it)
	back, err := Apply(it, doc)
	if err != nil {
		t.Fatalf("%v\n%s", err, doc)
	}
	if again := Render(back); again != doc {
		t.Fatalf("render not stable:\n%s\n---\n%s", doc, again)
	}
	return back
}

func TestUnusualHeaderNames(t *testing.T) {
	it := item(t, `{"request": {"method": "GET", "url": "x", "header": [
		{"key": "{{hdrName}}", "value": "1", "disabled": true},
		{"key": "X Custom", "value": "2", "disabled": true},
		{"key": "", "value": "3", "disabled": true},
		{"key": "Token ", "value": "4", "description": "kept"},
		{"key": "{{other}}", "value": "5"},
		{"key": "#hash", "value": "6"},
		{"key": "a:b", "value": "7"},
		{"key": "\"q\"", "value": "8"}]}}`)
	back := roundTrip(t, it)
	if got, want := jsonOf(back.Request.Header), jsonOf(it.Request.Header); got != want {
		t.Errorf("headers:\n%s\nwant:\n%s\n%s", got, want, Render(it))
	}
	// A comment in the headers stays a comment.
	back, err := Apply(it, "GET x\n# just a note, see: the wiki\nA: 1\n")
	if err != nil || len(back.Request.Header) != 1 {
		t.Errorf("comment: %v %+v", err, back)
	}
}

func TestMultipartTextLooksLikeFile(t *testing.T) {
	it := item(t, `{"request": {"method": "POST", "url": "x", "body": {"mode": "formdata", "formdata": [
		{"key": "handle", "value": "@ada", "type": "text"},
		{"key": "odd", "value": "x;type=y", "type": "text"},
		{"key": "quoted", "value": "\"a\";type=b", "type": "text", "contentType": "text/plain"},
		{"key": "f", "src": "/tmp/a;type=b.png", "type": "file"}]}}}`)
	back := roundTrip(t, it)
	if got, want := jsonOf(back.Request.Body.FormData), jsonOf(it.Request.Body.FormData); got != want {
		t.Errorf("form-data:\n%s\nwant:\n%s\n%s", got, want, Render(it))
	}
}

func TestKeysWithColons(t *testing.T) {
	it, err := Apply(&postman.Item{}, "POST x\n\n### form\nfilter[created:gte]: 2024\nfilter[updated:lte]:\ntime: 12:30\nurl = http://x\n")
	if err != nil {
		t.Fatal(err)
	}
	f := it.Request.Body.URLEncoded
	if len(f) != 4 || f[0].Key != "filter[created:gte]" || f[0].Value != "2024" || f[1].Key != "filter[updated:lte]" || f[1].Value != "" ||
		f[2].Key != "time" || f[2].Value != "12:30" || f[3].Key != "url" || f[3].Value != "http://x" {
		t.Errorf("form: %+v", f)
	}
	it = item(t, `{"request": {"method": "POST", "url": "x", "body": {"mode": "urlencoded", "urlencoded": [
		{"key": "a: b", "value": "1"}, {"key": "#tag", "value": "2"}, {"key": "//path", "value": "3"}, {"key": "x:", "value": ""}, {"key": " y", "value": "4"}]}}}`)
	back := roundTrip(t, it)
	if got, want := jsonOf(back.Request.Body.URLEncoded), jsonOf(it.Request.Body.URLEncoded); got != want {
		t.Errorf("urlencoded:\n%s\nwant:\n%s", got, want)
	}

	c := &postman.Collection{Variable: []postman.Variable{{Key: "#tag", Value: "1"}, {Key: "//path", Value: "2"}, {Key: "a:b", Value: "3"}, {Key: "c: d", Value: "4", Disabled: true}}}
	got, err := ApplyCollection(c, RenderCollection(c))
	if err != nil {
		t.Fatal(err)
	}
	if jsonOf(got.Variable) != jsonOf(c.Variable) {
		t.Errorf("variables: %s\n%s", jsonOf(got.Variable), RenderCollection(c))
	}
}

func TestDuplicateCollectionVariables(t *testing.T) {
	c := &postman.Collection{Variable: []postman.Variable{
		{ID: "v1", Key: "token", Value: "a", Description: &postman.Description{Content: "prod token"}},
		{ID: "v2", Key: "token", Value: "b", Disabled: true, Description: &postman.Description{Content: "dev token"}},
	}}
	got, err := ApplyCollection(c, RenderCollection(c))
	if err != nil {
		t.Fatal(err)
	}
	if jsonOf(got.Variable) != jsonOf(c.Variable) {
		t.Errorf("variables: %s", jsonOf(got.Variable))
	}
}

func TestDisabledScriptsStay(t *testing.T) {
	it := item(t, `{"request": {"method": "GET", "url": "x"}, "event": [
		{"listen": "test", "script": {"exec": ["now()"]}},
		{"listen": "test", "disabled": true, "script": {"exec": ["old()"]}},
		{"listen": "prerequest", "disabled": true, "script": {"exec": ["before()"]}}]}`)
	doc := strings.Replace(Render(it), "now()", "changed()", 1) + "\n### pre-request\nfresh()\n"
	back, err := Apply(it, doc)
	if err != nil {
		t.Fatal(err)
	}
	if postman.Scripts(back.Event, "test") != "changed()" || postman.Scripts(back.Event, "prerequest") != "fresh()" {
		t.Errorf("scripts: %s", jsonOf(back.Event))
	}
	disabled := 0
	for _, e := range back.Event {
		if e.Disabled {
			disabled++
			if text := e.Script.Exec.Text(); text != "old()" && text != "before()" {
				t.Errorf("disabled script changed: %q", text)
			}
		}
	}
	if disabled != 2 || len(back.Event) != 4 {
		t.Errorf("events: %s", jsonOf(back.Event))
	}
}

func TestBlankSpaceKept(t *testing.T) {
	it := item(t, `{"request": {"method": "POST",
		"url": {"raw": "x/:id/:note?q=a b &r=\"x\"&s=\"  \"", "query": [{"key": "q", "value": "a b "}, {"key": "r", "value": "\"x\""}, {"key": "s", "value": "\"  \""}],
			"variable": [{"key": "id", "value": " 7 "}, {"key": "note", "value": "a\nb"}]},
		"header": [{"key": "A", "value": "tab\t"}, {"key": "B", "value": " lead"}],
		"body": {"mode": "urlencoded", "urlencoded": [{"key": "f", "value": "tab\t"}]}}}`)
	back := roundTrip(t, it)
	r, b := it.Request, back.Request
	for _, c := range []struct {
		what string
		x, y any
	}{
		{"query", r.URL.Query, b.URL.Query},
		{"path variables", r.URL.Variable, b.URL.Variable},
		{"headers", r.Header, b.Header},
		{"form", r.Body.URLEncoded, b.Body.URLEncoded},
	} {
		if jsonOf(c.x) != jsonOf(c.y) {
			t.Errorf("%s: %s\nwant %s\n%s", c.what, jsonOf(c.y), jsonOf(c.x), Render(it))
		}
	}
	// A value in quotes that doesn't need them stays as written.
	it2, err := Apply(&postman.Item{}, "GET x/:id\n    ?q=\"x\"\n    :id = \"7\"\n")
	if err != nil {
		t.Fatal(err)
	}
	if it2.Request.URL.Query[0].Value != `"x"` || it2.Request.URL.Variable[0].Value != `"7"` {
		t.Errorf("quoted: %+v %+v", it2.Request.URL.Query, it2.Request.URL.Variable)
	}
	c := &postman.Collection{Variable: []postman.Variable{{Key: "v", Value: "tab\t"}}}
	if got, err := ApplyCollection(c, RenderCollection(c)); err != nil || got.Variable[0].Value != "tab\t" {
		t.Errorf("variable: %v %+v", err, got)
	}
}

func TestQueryAmpersand(t *testing.T) {
	it := item(t, `{"request": {"method": "GET", "url": {"raw": "x?q=a&b&k=1", "query": [{"key": "q", "value": "a&b"}, {"key": "k", "value": "1"}]}}}`)
	back := roundTrip(t, it)
	if q := back.Request.URL.Query; len(q) != 2 || q[0].Value != "a&b" || back.Request.URL.Raw != "x?q=a%26b&k=1" {
		t.Errorf("query: %s %+v", back.Request.URL.Raw, q)
	}
}

func TestUnknownFieldsKept(t *testing.T) {
	it := item(t, `{"request": {"method": "GET", "url": {"raw": "x?a=1", "query": [{"key": "a", "value": "1", "type": "text", "uuid": "u1"}], "foo": "bar"}}}`)
	back := roundTrip(t, it)
	data, _ := json.Marshal(back.Request.URL)
	for _, want := range []string{`"uuid":"u1"`, `"type":"text"`, `"foo":"bar"`} {
		if !strings.Contains(string(data), want) {
			t.Errorf("%s lost: %s", want, data)
		}
	}
}

func TestFileFieldsWithOneKey(t *testing.T) {
	it := item(t, `{"request": {"method": "POST", "url": "x", "body": {"mode": "formdata", "formdata": [
		{"key": "files", "src": "/a", "type": "file", "description": "first"},
		{"key": "files", "src": "/b", "type": "file", "description": "second"},
		{"key": "many", "src": ["/c", "/d"], "type": "file", "description": "third"},
		{"key": "many", "src": "/e", "type": "file", "description": "fourth"},
		{"key": "one", "src": ["/f"], "type": "file"}]}}}`)
	back := roundTrip(t, it)
	if got, want := jsonOf(back.Request.Body.FormData), jsonOf(it.Request.Body.FormData); got != want {
		t.Errorf("form-data:\n%s\nwant:\n%s", got, want)
	}
	// A file added to a field with several files joins it, when no other
	// field with the key is left to take it.
	it.Request.Body.FormData = it.Request.Body.FormData[:3]
	doc := strings.Replace(Render(it), "many: @/d\n", "many: @/d\nmany: @/new\n", 1)
	back, err := Apply(it, doc)
	if err != nil {
		t.Fatal(err)
	}
	if fd := back.Request.Body.FormData; len(fd) != 3 || len(fd[2].Files()) != 3 || fd[2].Description == nil {
		t.Errorf("added file: %s", jsonOf(fd))
	}
	// Taken out, it leaves the field and the others.
	doc = strings.Replace(Render(it), "many: @/c\n", "", 1)
	if back, err = Apply(it, doc); err != nil {
		t.Fatal(err)
	}
	if fd := back.Request.Body.FormData; len(fd) != 3 || string(fd[2].Src) != `["/d"]` {
		t.Errorf("removed file: %s", jsonOf(fd))
	}
}

func TestOwnDocumentsApply(t *testing.T) {
	// A path variable the URL doesn't use isn't shown, and stays.
	it := item(t, `{"request": {"method": "GET", "url": {"raw": "{{base}}/users/:team", "variable": [{"key": "id", "value": "7"}, {"key": "team", "value": "1"}]}}}`)
	back := roundTrip(t, it)
	if v := back.Request.URL.Variable; len(v) != 2 || v[0].Key != "team" || v[1].Key != "id" || v[1].Value != "7" {
		t.Errorf("path variables: %+v", v)
	}
	if strings.Contains(Render(it), ":id") {
		t.Errorf("unused path variable shown:\n%s", Render(it))
	}
	// Used again, it starts empty, as the document shows.
	back, err := Apply(it, "GET {{base}}/users/:team/:id\n    :team = 1\n")
	if err != nil {
		t.Fatal(err)
	}
	if v := back.Request.URL.Variable; len(v) != 2 || v[1].Key != "id" || v[1].Value != "" {
		t.Errorf("path variables: %+v", v)
	}
	// A URL with a space, as the item has it.
	it = item(t, `{"request": {"method": "GET", "url": "http://x/a b?c d=1"}}`)
	if back := roundTrip(t, it); back.Request.URL.Raw != "http://x/a b?c d=1" {
		t.Errorf("url: %q", back.Request.URL.Raw)
	}
	if _, err := Apply(&postman.Item{}, "GET http://x/a b"); err == nil {
		t.Error("a typed URL with a space was taken")
	}
}

func TestErrorLinesAfterBlankLines(t *testing.T) {
	cases := map[string]int{
		"POST x\n\n### form\n\nbad line\n":                                     5,
		"POST x\n\n### multipart\n\n\nbad line\n":                              6,
		"GET x\n\n### auth bearer\n\n\nbad\n":                                  6,
		"GET x\n\n### settings\n\nbad\n":                                       5,
		"GET x\n\n### tests\na()\n\n### tests\nb()\n":                          6,
		"GET x\n\n### description\na\n### docs\nb\n":                           5,
		"POST x\n\n### graphql\n{ a }\n### variables\n{}\n### variables\n{}\n": 7,
	}
	for doc, line := range cases {
		_, err := Apply(&postman.Item{}, doc)
		var e *Error
		if !errors.As(err, &e) || e.Line != line {
			t.Errorf("%q: %v, want an error on line %d", doc, err, line)
		}
	}
	c := &postman.Collection{}
	for doc, line := range map[string]int{
		"### variables\n\nbad\n":                     3,
		"### auth none\n\n### auth bearer\n":         3,
		"### pre-request\na()\n### pre-request\nb()": 3,
	} {
		_, err := ApplyCollection(c, doc)
		var e *Error
		if !errors.As(err, &e) || e.Line != line {
			t.Errorf("collection %q: %v, want an error on line %d", doc, err, line)
		}
	}
	if _, err := ApplyFolder(&postman.Item{Folder: true}, "### tests\na()\n### tests\nb()\n"); err == nil {
		t.Error("a folder took two ### tests")
	}
}

func TestBackslashedSectionLines(t *testing.T) {
	raw := "# Title\n### not a section\n\\### escaped already\n\\\\### twice\n###\nend"
	it := &postman.Item{Request: &postman.Request{Method: "POST", URL: &postman.URL{Raw: "x"}, Body: &postman.Body{Mode: "raw", Raw: raw}}}
	it.Request.Body.SetLanguage("text")
	if back := roundTrip(t, it); back.Request.Body.Raw != raw {
		t.Errorf("body: %q\n%s", back.Request.Body.Raw, Render(it))
	}
	it.Event = postman.SetScript(nil, "test", "// \\### x\n\\### y")
	if back := roundTrip(t, it); postman.Scripts(back.Event, "test") != "// \\### x\n\\### y" {
		t.Errorf("script: %q", postman.Scripts(back.Event, "test"))
	}
}

func TestDetailsKept(t *testing.T) {
	// A binary body's other fields.
	it := item(t, `{"request": {"method": "POST", "url": "x", "body": {"mode": "file", "file": {"src": "/a", "content": "xyz", "foo": 1}}}}`)
	if f := roundTrip(t, it).Request.Body.File; f == nil || f.Content != "xyz" || string(f.Extra["foo"]) != "1" {
		t.Errorf("file body: %+v", f)
	}
	// A raw body in a language posthero doesn't know.
	it = item(t, `{"request": {"method": "POST", "url": "x", "body": {"mode": "raw", "raw": "a: 1", "options": {"raw": {"language": "yaml"}}}}}`)
	if b := roundTrip(t, it).Request.Body; b.Language() != "yaml" {
		t.Errorf("language: %s", b.Options)
	}
	// No auth keeps the settings of the other types.
	it = item(t, `{"request": {"method": "GET", "url": "x", "auth": {"type": "bearer", "bearer": [{"key": "token", "value": "t", "type": "string"}]}}}`)
	doc := strings.Replace(Render(it), "### auth bearer\ntoken: t", "### auth none", 1)
	back, err := Apply(it, doc)
	if err != nil {
		t.Fatal(err)
	}
	if a := back.Request.Auth; a.Type != "noauth" || len(a.Params["bearer"]) != 1 {
		t.Errorf("auth none: %s", jsonOf(a))
	}
	// Settings without a type keep their JSON type.
	it = item(t, `{"request": {"method": "GET", "url": "x", "auth": {"type": "apikey", "apikey": [
		{"key": "a", "value": "true"}, {"key": "b", "value": "{\"a\":1}"}, {"key": "c", "value": 5}, {"key": "d", "value": true, "type": "string"}, {"key": "e", "value": "x"}]}}}`)
	back = roundTrip(t, it)
	if got, want := jsonOf(back.Request.Auth), jsonOf(it.Request.Auth); got != want {
		t.Errorf("auth:\n%s\nwant:\n%s", got, want)
	}
	back, err = Apply(it, strings.Replace(strings.Replace(Render(it), "a: true", "a: false", 1), "e: x", "e: true", 1))
	if err != nil {
		t.Fatal(err)
	}
	if string(back.Request.Auth.Raw("a")) != `"false"` || string(back.Request.Auth.Raw("e")) != `"true"` {
		t.Errorf("changed auth: %s", jsonOf(back.Request.Auth))
	}
	// A request setting that is the text "true".
	it = item(t, `{"request": {"method": "GET", "url": "x"}, "protocolProfileBehavior": {"custom": "true", "n": 3}}`)
	if s := roundTrip(t, it).ProtocolProfileBehavior; string(s) != `{"custom":"true","n":3}` {
		t.Errorf("settings: %s", s)
	}
}

func TestDisabledBody(t *testing.T) {
	it := item(t, `{"request": {"method": "POST", "url": "x", "body": {"mode": "raw", "raw": "{\"a\": 1}", "disabled": true, "options": {"raw": {"language": "json"}}}}}`)
	doc := Render(it)
	if !strings.Contains(doc, "### json off\n") {
		t.Errorf("disabled body shown as on:\n%s", doc)
	}
	if b := roundTrip(t, it).Request.Body; !b.Disabled {
		t.Error("body switched on")
	}
	back, err := Apply(it, strings.Replace(doc, "### json off", "### json", 1))
	if err != nil {
		t.Fatal(err)
	}
	if b := back.Request.Body; b.Disabled || b.Raw != `{"a": 1}` {
		t.Errorf("body not switched on: %+v", b)
	}
	for _, body := range []string{
		`{"mode": "graphql", "graphql": {"query": "{ a }", "variables": "{}"}, "disabled": true}`,
		`{"mode": "urlencoded", "urlencoded": [{"key": "a", "value": "1"}], "disabled": true}`,
		`{"mode": "formdata", "formdata": [{"key": "a", "value": "1"}], "disabled": true}`,
		`{"mode": "file", "file": {"src": "/a"}, "disabled": true}`,
	} {
		it := item(t, `{"request": {"method": "POST", "url": "x", "body": `+body+`}}`)
		if b := roundTrip(t, it).Request.Body; !b.Disabled || !strings.Contains(Render(it), " off\n") {
			t.Errorf("%s:\n%s", body, Render(it))
		}
	}
	if _, err := Apply(it, strings.Replace(doc, "### json off", "### json of", 1)); err == nil {
		t.Error("### json of was taken")
	}
}
