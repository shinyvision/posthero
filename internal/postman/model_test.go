package postman

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

const sample = `{
  "info": {"_postman_id": "c1", "name": "Shop", "description": {"content": "# Shop", "type": "text/markdown"}, "schema": "` + SchemaV21 + `", "x-custom": 7},
  "item": [
    {"name": "Empty folder", "item": []},
    {"name": "Users", "id": "f1", "auth": {"type": "bearer", "bearer": {"token": "{{t}}"}}, "item": [
      {"name": "List", "id": "r1", "request": {
        "method": "GET",
        "header": "Accept: application/json\n// X-Off: 1",
        "url": {"raw": "{{base}}/users?page=1", "host": ["{{base}}"], "path": "users", "query": [{"key": "page", "value": 1}, {"key": "size", "value": "20", "disabled": "true"}]},
        "description": "Lists users"
      }, "response": [{"name": "ok", "code": 200, "status": "OK", "header": null, "body": "[]"}], "unknownThing": {"a": 1}},
      {"name": "Get", "request": "https://example.com/users/1"}
    ]},
    {"name": "Query", "request": {"method": "POST", "url": "{{base}}/graphql", "body": {"mode": "graphql", "graphql": {"query": "{ a }", "variables": {"x": 1}}}},
     "event": [{"listen": "test", "script": {"exec": "pm.test('a', () => {})\nconsole.log(1)", "type": "text/javascript"}}]}
  ],
  "variable": [{"key": "base", "value": "https://api.example.com"}, {"id": "legacy", "value": true, "type": "boolean"}]
}`

func TestReadVariants(t *testing.T) {
	var c Collection
	if err := json.Unmarshal([]byte(sample), &c); err != nil {
		t.Fatal(err)
	}
	if c.Info.Description == nil || c.Info.Description.Content != "# Shop" || string(c.Info.Extra["x-custom"]) != "7" {
		t.Errorf("info: %+v", c.Info)
	}
	if !c.Item[0].Folder || len(c.Item[0].Item) != 0 {
		t.Errorf("empty folder lost: %+v", c.Item[0])
	}
	users := c.Item[1]
	if users.Auth.Type != "bearer" || users.Auth.Get("token") != "{{t}}" {
		t.Errorf("v2.0 auth: %+v", users.Auth)
	}
	list := users.Item[0].Request
	if len(list.Header) != 2 || !list.Header[1].Disabled || list.Header[1].Key != "X-Off" {
		t.Errorf("string headers: %+v", list.Header)
	}
	if string(list.URL.Query[0].Value) != "1" || !list.URL.Query[1].Disabled || list.URL.Path[0] != "users" {
		t.Errorf("url: %+v", list.URL)
	}
	if list.Description.Content != "Lists users" {
		t.Errorf("description: %+v", list.Description)
	}
	if string(users.Item[0].Extra["unknownThing"]) != `{"a": 1}` {
		t.Errorf("unknown field lost: %s", users.Item[0].Extra)
	}
	if get := users.Item[1].Request; get.Method != "GET" || get.URL.Raw != "https://example.com/users/1" {
		t.Errorf("string request: %+v", get)
	}
	q := c.Item[2]
	if string(q.Request.Body.GraphQL.Variables) != `{"x": 1}` {
		t.Errorf("graphql variables: %q", q.Request.Body.GraphQL.Variables)
	}
	if Scripts(q.Event, "test") != "pm.test('a', () => {})\nconsole.log(1)" {
		t.Errorf("script: %q", Scripts(q.Event, "test"))
	}
	if c.Variable[1].Key != "legacy" || c.Variable[1].Value != "true" {
		t.Errorf("variable: %+v", c.Variable[1])
	}

	// Written out and read back, nothing changes.
	data, err := json.Marshal(&c)
	if err != nil {
		t.Fatal(err)
	}
	var again Collection
	if err := json.Unmarshal(data, &again); err != nil {
		t.Fatal(err)
	}
	data2, _ := json.Marshal(&again)
	if string(data) != string(data2) {
		t.Errorf("not stable:\n%s\n%s", data, data2)
	}
	if !strings.Contains(string(data), `"item":[]`) {
		t.Errorf("empty folder written without its items: %s", data)
	}
}

// TestCorpus round-trips real collections and environments (exports or API
// responses) from $POSTHERO_CORPUS, comparing the JSON before and after.
// Nothing posthero models may change, beyond the forms it writes them in.
func TestCorpus(t *testing.T) {
	dir := os.Getenv("POSTHERO_CORPUS")
	if dir == "" {
		t.Skip("POSTHERO_CORPUS is not set")
	}
	files, _ := filepath.Glob(filepath.Join(dir, "*", "*.json"))
	if len(files) == 0 {
		t.Fatalf("no JSON files under %s", dir)
	}
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		var wrap map[string]json.RawMessage
		if err := json.Unmarshal(data, &wrap); err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		var orig json.RawMessage
		var round []byte
		if raw, ok := wrap["collection"]; ok {
			var c Collection
			if err := json.Unmarshal(raw, &c); err != nil {
				t.Errorf("%s: %v", f, err)
				continue
			}
			orig, round = raw, mustJSON(t, &c)
		} else if raw, ok := wrap["environment"]; ok {
			var e Environment
			if err := json.Unmarshal(raw, &e); err != nil {
				t.Errorf("%s: %v", f, err)
				continue
			}
			orig, round = raw, mustJSON(t, &e)
		} else {
			continue
		}
		var a, b any
		_ = json.Unmarshal(orig, &a)
		_ = json.Unmarshal(round, &b)
		var diffs []string
		compare("", normalize(a), normalize(b), &diffs)
		if len(diffs) > 0 {
			sort.Strings(diffs)
			if len(diffs) > 10 {
				diffs = append(diffs[:10], fmt.Sprintf("… %d more", len(diffs)-10))
			}
			t.Errorf("%s changed:\n  %s", filepath.Base(f), strings.Join(diffs, "\n  "))
		}
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// normalize evens out what posthero writes differently but means the
// same: lines joined, scalars as strings, empty values dropped, enabled
// turned into disabled.
func normalize(v any) any {
	switch x := v.(type) {
	case map[string]any:
		out := map[string]any{}
		for k, val := range x {
			if k == "exec" {
				if list, ok := val.([]any); ok {
					parts := make([]string, len(list))
					for i, p := range list {
						parts[i] = fmt.Sprint(p)
					}
					val = strings.Join(parts, "\n")
				}
			}
			if k == "enabled" {
				if b, ok := val.(bool); ok {
					if _, isEnv := x["type"]; !isEnv || x["src"] != nil || x["description"] != nil {
						k, val = "disabled", !b
					}
				}
			}
			n := normalize(val)
			if empty(n) {
				continue
			}
			out[k] = n
		}
		return out
	case []any:
		out := make([]any, 0, len(x))
		for _, e := range x {
			out = append(out, normalize(e))
		}
		return out
	case float64:
		return formatNumber(x)
	case bool:
		if !x {
			return nil
		}
		return "true"
	}
	return v
}

func empty(v any) bool {
	switch x := v.(type) {
	case nil:
		return true
	case string:
		return x == ""
	case map[string]any:
		return len(x) == 0
	case []any:
		return len(x) == 0
	}
	return false
}

func compare(path string, a, b any, diffs *[]string) {
	if reflect.DeepEqual(a, b) {
		return
	}
	am, aok := a.(map[string]any)
	bm, bok := b.(map[string]any)
	if aok && bok {
		keys := map[string]bool{}
		for k := range am {
			keys[k] = true
		}
		for k := range bm {
			keys[k] = true
		}
		for k := range keys {
			compare(path+"."+k, am[k], bm[k], diffs)
		}
		return
	}
	al, aok := a.([]any)
	bl, bok := b.([]any)
	if aok && bok && len(al) == len(bl) {
		for i := range al {
			compare(fmt.Sprintf("%s[%d]", path, i), al[i], bl[i], diffs)
		}
		return
	}
	*diffs = append(*diffs, fmt.Sprintf("%s: %.80v → %.80v", path, a, b))
}

func TestSetScriptKeepsDisabled(t *testing.T) {
	events := []Event{
		{Listen: "test", Script: Script{Exec: Lines{"now()"}}},
		{Listen: "test", Disabled: true, Script: Script{Exec: Lines{"old()"}}},
		{Listen: "prerequest", Disabled: true, Script: Script{Exec: Lines{"before()"}}},
	}
	out := SetScript(events, "test", "changed()")
	if len(out) != 3 || out[0].Script.Exec.Text() != "changed()" || out[1].Script.Exec.Text() != "old()" || !out[1].Disabled {
		t.Errorf("changed test: %+v", out)
	}
	out = SetScript(events, "prerequest", "fresh()")
	if len(out) != 4 || !out[2].Disabled || out[2].Script.Exec.Text() != "before()" || out[3].Disabled || out[3].Script.Exec.Text() != "fresh()" {
		t.Errorf("added pre-request: %+v", out)
	}
	out = SetScript(events, "test", "")
	if len(out) != 2 || out[0].Script.Exec.Text() != "old()" {
		t.Errorf("removed test: %+v", out)
	}
}

func TestRebuildKeepsUnknownFields(t *testing.T) {
	var old URL
	if err := json.Unmarshal([]byte(`{"raw": "x?a=1&b=2", "query": [{"key": "a", "value": "1", "type": "text", "uuid": "u1", "description": "d"}, {"key": "b", "value": "2", "disabled": true}], "foo": "bar"}`), &old); err != nil {
		t.Fatal(err)
	}
	u := Rebuild(&old, "x?a=3", Params{{Key: "a", Value: "3"}, {Key: "b", Value: "2"}}, nil)
	data, _ := json.Marshal(u)
	for _, want := range []string{`"uuid":"u1"`, `"type":"text"`, `"foo":"bar"`, `"description":"d"`, `"value":"3"`} {
		if !strings.Contains(string(data), want) {
			t.Errorf("%s lost: %s", want, data)
		}
	}
	if u.Query[1].Disabled {
		t.Errorf("b still disabled: %s", data)
	}
}

func TestResponseHeaderFlags(t *testing.T) {
	r := &Response{Name: "ok", Header: Params{{Key: "X", Value: "1", Disabled: true, Description: &Description{Content: "why"}}}}
	h := ResponseFields(r)["headers"].([]map[string]any)
	if len(h) != 1 || h[0]["enabled"] != false || h[0]["description"] != "why" {
		t.Errorf("headers: %+v", h)
	}
}

func TestQueryStringEncodesSeparators(t *testing.T) {
	cases := []struct {
		ps   Params
		want string
	}{
		{Params{{Key: "x", Value: "1"}, {Key: "q", Value: "a&b=c"}}, "x=1&q=a%26b=c"},
		{Params{{Key: "a=b&c", Value: "1"}, {Key: "d#", NoValue: true}}, "a%3Db%26c=1&d%23"},
		{Params{{Key: "q", Value: "a%26b"}, {Key: "h", Value: "x#y"}}, "q=a%26b&h=x%23y"},
		{Params{{Key: "{{k&}}", Value: "{{a&b}}&"}, {Key: "s", Value: "a b"}}, "{{k&}}={{a&b}}%26&s=a b"},
	}
	for _, c := range cases {
		if got := QueryString(c.ps); got != c.want {
			t.Errorf("QueryString(%+v) = %q, want %q", c.ps, got, c.want)
		}
	}
	u := &URL{Raw: "http://x/?x=1", Query: Params{{Key: "x", Value: "1"}, {Key: "q", Value: "a&b=c"}}}
	if got := u.SendRaw(); got != "http://x/?x=1&q=a%26b=c" {
		t.Errorf("SendRaw: %q", got)
	}
}

// TestParseURLHostWithVariables: like Postman, a host splits at the dots
// outside {{variables}}.
func TestParseURLHostWithVariables(t *testing.T) {
	cases := map[string][]string{
		"https://api{{suffix}}.toegang.org/a":         {"api{{suffix}}", "toegang", "org"},
		"https://{{ProjectID}}-db.europe-west1.app/x": {"{{ProjectID}}-db", "europe-west1", "app"},
		"{{base}}/users":              {"{{base}}"},
		"{{a.b}}.example.com":         {"{{a.b}}", "example", "com"},
		"http://localhost:8080/x":     {"localhost"},
		"https://{{host}}:{{port}}/x": {"{{host}}:{{port}}"},
	}
	for raw, want := range cases {
		if got := ParseURL(raw).Host; strings.Join(got, "|") != strings.Join(want, "|") {
			t.Errorf("ParseURL(%q).Host = %q, want %q", raw, got, want)
		}
	}
}
