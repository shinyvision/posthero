package syncapi

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/shinyvision/posthero/internal/jsonx"
	"github.com/shinyvision/posthero/internal/postman"
)

// The rules below are what TestParity found the Postman API does when it
// turns the service's format into a collection; these hold posthero to
// them without real data.

func request(t *testing.T, data string) *postman.Item {
	t.Helper()
	var r requestDTO
	if err := decode([]byte(data), &r); err != nil {
		t.Fatal(err)
	}
	return toItem(&r)
}

func asJSON(t *testing.T, v any) string {
	t.Helper()
	data, err := jsonx.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestConvertURL(t *testing.T) {
	cases := []struct {
		name, in, want string
	}{
		{"the query is the enabled parameters, in their order",
			`{"url": "{{base}}/incidents?pageStart=50&pageSize=50#top", "queryParams": [
				{"key": "pageSize", "value": "50", "equals": true, "enabled": true},
				{"key": "sort", "value": "x", "enabled": false, "description": "how"},
				{"key": "pageStart", "value": "50", "equals": true}]}`,
			`{"raw":"{{base}}/incidents?pageSize=50&pageStart=50#top","host":["{{base}}"],"path":["incidents"],"query":[{"key":"pageSize","value":"50"},{"key":"sort","value":"x","disabled":true,"description":"how"},{"key":"pageStart","value":"50"}],"hash":"top"}`},
		{"a parameter without =",
			`{"url": "http://x/a?debug", "queryParams": [{"key": "debug", "value": "", "equals": false}]}`,
			`{"raw":"http://x/a?debug","protocol":"http","host":["x"],"path":["a"],"query":[{"key":"debug","value":null}]}`},
		{"the host splits outside {{variables}}",
			`{"url": "https://api{{suffix}}.toegang.org/a", "queryParams": []}`,
			`{"raw":"https://api{{suffix}}.toegang.org/a","protocol":"https","host":["api{{suffix}}","toegang","org"],"path":["a"]}`},
		{"path variables with their ids",
			`{"url": "{{base}}/users/:id", "pathVariableData": [{"id": "v1", "key": "id", "value": "7", "description": "who"}]}`,
			`{"raw":"{{base}}/users/:id","host":["{{base}}"],"path":["users",":id"],"variable":[{"id":"v1","key":"id","value":"7","description":"who"}]}`},
		{"older path variables, in the path's order",
			`{"url": "{{base}}/:team/users/:id", "pathVariables": {"id": "7", "team": "t"}}`,
			`{"raw":"{{base}}/:team/users/:id","host":["{{base}}"],"path":[":team","users",":id"],"variable":[{"key":"team","value":"t"},{"key":"id","value":"7"}]}`},
		{"path variables nothing says the values of",
			`{"url": "{{base}}/profiles/:title/:version", "pathVariableData": [], "pathVariables": {}}`,
			`{"raw":"{{base}}/profiles/:title/:version","host":["{{base}}"],"path":["profiles",":title",":version"],"variable":[{"key":"title","value":""},{"key":"version","value":""}]}`},
	}
	for _, c := range cases {
		it := request(t, c.in)
		if got := asJSON(t, it.Request.URL); got != c.want {
			t.Errorf("%s:\n got %s\nwant %s", c.name, got, c.want)
		}
	}
	if it := request(t, `{"name": "No address", "url": ""}`); it.Request.URL != nil {
		t.Errorf("a request without an address: %+v", it.Request.URL)
	}
}

func TestConvertBody(t *testing.T) {
	cases := []struct {
		name, in, want string
	}{
		{"options without the empty ones",
			`{"dataMode": "raw", "rawModeData": "{}", "dataOptions": {"raw": {"language": "json"}, "graphql": {}, "params": null}}`,
			`{"mode":"raw","raw":"{}","options":{"raw":{"language":"json"}}}`},
		{"no options at all",
			`{"dataMode": "graphql", "graphqlModeData": {"query": "{ me }", "variables": ""}, "dataOptions": {"graphql": {}}}`,
			`{"mode":"graphql","graphql":{"query":"{ me }"}}`},
		{"an older raw body in data",
			`{"dataMode": "raw", "rawModeData": "", "data": "{\"a\": 1}"}`,
			`{"mode":"raw","raw":"{\"a\": 1}"}`},
		{"a GraphQL body only in data is none",
			`{"dataMode": "graphql", "data": {"query": "{ me }"}}`,
			`{"mode":"graphql"}`},
		{"form fields: a file's value is its src, other fields pass on",
			`{"dataMode": "params", "data": [
				{"key": "f", "type": "file", "value": "/tmp/a.pdf", "uuid": "u1", "enabled": true},
				{"key": "g", "type": "file", "value": null},
				{"key": "h", "type": "file", "value": []},
				{"key": "t", "type": "text", "value": "x", "enabled": false, "description": ""}]}`,
			`{"mode":"formdata","formdata":[{"key":"f","type":"file","src":"/tmp/a.pdf","value":null,"uuid":"u1"},{"key":"g","type":"file","value":null},{"key":"h","type":"file","src":[],"value":null},{"key":"t","value":"x","disabled":true,"type":"text"}]}`},
		{"formdata is params too",
			`{"dataMode": "formdata", "data": [{"key": "a", "value": "1", "type": "text"}]}`,
			`{"mode":"formdata","formdata":[{"key":"a","value":"1","type":"text"}]}`},
		{"a binary body",
			`{"dataMode": "binary", "rawModeData": "/tmp/data.bin", "dataDisabled": true}`,
			`{"mode":"file","file":{"src":"/tmp/data.bin"},"disabled":true}`},
	}
	for _, c := range cases {
		it := request(t, c.in)
		if got := asJSON(t, it.Request.Body); got != c.want {
			t.Errorf("%s:\n got %s\nwant %s", c.name, got, c.want)
		}
	}
	if it := request(t, `{"dataMode": null, "data": null}`); it.Request.Body != nil {
		t.Errorf("no body: %+v", it.Request.Body)
	}
}

func TestConvertParamsAndAuth(t *testing.T) {
	it := request(t, `{"method": "post", "url": "x",
		"headerData": [{"key": "A", "value": "1", "type": "text", "warning": "w", "description": {"content": "about A", "type": "text/plain"}}, {"key": "B", "value": "2", "enabled": false, "description": ""}],
		"auth": {"type": "oauth2", "oauth2": [{"key": "a", "value": "x"}, {"key": "b", "value": true}, {"key": "c", "value": [1]}, {"key": "d", "value": 3}, {"key": "e", "value": "y", "type": "string"}]},
		"protocolProfileBehavior": {}, "description": "Does a thing"}`)
	r := it.Request
	if r.Method != "POST" {
		t.Errorf("method %q", r.Method)
	}
	if got := asJSON(t, r.Header); got != `[{"key":"A","value":"1","description":{"content":"about A","type":"text/plain"},"type":"text","warning":"w"},{"key":"B","value":"2","disabled":true}]` {
		t.Errorf("headers: %s", got)
	}
	if got := asJSON(t, r.Auth); got != `{"type":"oauth2","oauth2":[{"key":"a","value":"x","type":"string"},{"key":"b","value":true,"type":"boolean"},{"key":"c","value":[1],"type":"any"},{"key":"d","value":3,"type":"number"},{"key":"e","value":"y","type":"string"}]}` {
		t.Errorf("auth: %s", got)
	}
	if r.Description == nil || r.Description.Content != "Does a thing" || it.ProtocolProfileBehavior != nil {
		t.Errorf("description %+v, settings %s", r.Description, it.ProtocolProfileBehavior)
	}
}

func TestConvertExamples(t *testing.T) {
	it := request(t, `{"id": "r1", "owner": "7", "url": "x", "responses_order": ["e2", "e1"], "responses": [
		{"id": "e1", "owner": "7", "name": "Empty", "responseCode": "", "status": null, "cookies": null, "text": null, "createdAt": "2026-01-01T00:00:00.000Z", "updatedAt": "2026-01-02T00:00:00.000Z",
		 "requestObject": "{\"method\":\"PUT\",\"url\":\"{{base}}/users/:id\",\"data\":\"{\\\"a\\\":1}\",\"dataMode\":\"raw\",\"headers\":\"A: 1\\n\",\"headerData\":[{\"key\":\"A\",\"value\":\"1\"}],\"pathVariableData\":[],\"pathVariables\":{\"id\":\"9\"}}"},
		{"id": "e2", "owner": "7", "name": "Created", "responseCode": {"code": 201, "name": "Created", "detail": ""}, "status": "Created", "language": "json",
		 "headers": [{"key": "Content-Type", "value": "application/json"}], "cookies": [{"domain": "", "path": ""}, {"name": "sid", "value": "1"}], "text": "{}"}]}`)
	if len(it.Response) != 2 || it.Response[0].Name != "Created" || it.Response[1].Name != "Empty" {
		t.Fatalf("examples: %+v", it.Response)
	}
	created, empty := it.Response[0], it.Response[1]
	if created.UID != "7-e2" || created.Code != 201 || created.Status != "Created" || created.PreviewLanguage != "json" || created.Body != "{}" {
		t.Errorf("an example: %+v", created)
	}
	if string(created.Cookie) != `[{"name":"sid","value":"1"}]` || string(empty.Cookie) != `[]` {
		t.Errorf("cookies: %s %s", created.Cookie, empty.Cookie)
	}
	if empty.Code != 0 || empty.Status != "" || empty.Body != "" {
		t.Errorf("an example without a code: %+v", empty)
	}
	if string(empty.Extra["createdAt"]) != `"2026-01-01T00:00:00.000Z"` || string(empty.Extra["updatedAt"]) != `"2026-01-02T00:00:00.000Z"` {
		t.Errorf("an example's times: %v", empty.Extra)
	}
	o := empty.OriginalRequest
	if o == nil || o.Method != "PUT" || o.Body == nil || o.Body.Raw != `{"a":1}` || len(o.URL.Variable) != 1 || o.URL.Variable[0].Value != "9" || len(o.Header) != 1 {
		t.Errorf("an example's request: %s", asJSON(t, o))
	}
}

func TestConvertTree(t *testing.T) {
	var c collectionDTO
	err := decode([]byte(`{"id": "c1", "owner": "7", "name": "C", "description": "", "order": ["r2", "gone"], "folders_order": ["f1"],
		"folders": [{"id": "f1", "owner": "7", "name": "F", "folder": null, "order": ["r1"], "folders_order": ["f2"], "createdAt": "2026-01-01T00:00:00.000Z", "updatedAt": "2026-01-03T00:00:00.000Z"},
		            {"id": "f2", "owner": "7", "name": "Inner", "folder": "f1", "order": [], "folders_order": []}],
		"requests": [{"id": "r1", "owner": "8", "name": "One", "url": "a"}, {"id": "r2", "owner": "7", "name": "Two", "url": "b"}]}`), &c)
	if err != nil {
		t.Fatal(err)
	}
	col := toCollection(&c)
	if col.Info.UID != "7-c1" || col.Info.Schema != postman.SchemaV21 || col.Info.Description != nil {
		t.Errorf("info: %+v", col.Info)
	}
	var shape []string
	col.Walk(func(it *postman.Item, parents []*postman.Item) bool {
		shape = append(shape, strings.Repeat(">", len(parents))+it.Name+"("+it.UID+")")
		return true
	})
	if got := strings.Join(shape, " "); got != "F(7-f1) >Inner(7-f2) >One(8-r1) Two(7-r2)" {
		t.Errorf("tree: %s", got)
	}
	if f := col.Item[0]; !f.Folder || f.CreatedAt != "2026-01-01T00:00:00.000Z" || f.UpdatedAt != "2026-01-03T00:00:00.000Z" {
		t.Errorf("a folder's times: %+v", f)
	}
}

// TestPayloadFiles: a form's file goes in value, where the service keeps
// it, in a request and in an example's request.
func TestPayloadFiles(t *testing.T) {
	it := &postman.Item{Name: "Upload", Request: &postman.Request{Method: "POST", URL: postman.ParseURL("x"),
		Body: &postman.Body{Mode: "formdata", FormData: postman.Params{{Key: "f", Type: "file", Src: json.RawMessage(`"/tmp/a.png"`)}, {Key: "t", Value: "1"}}}}}
	body := requestPayload(it)
	fields := body["data"].([]map[string]any)
	if string(fields[0]["value"].(json.RawMessage)) != `"/tmp/a.png"` || fields[0]["src"] != nil || fields[1]["value"] != "1" {
		t.Errorf("request fields: %+v", fields)
	}
	ex := responsePayload(&postman.Response{Name: "OK", OriginalRequest: it.Request})
	inner := ex["requestObject"].(map[string]any)["data"].([]map[string]any)
	if string(inner[0]["value"].(json.RawMessage)) != `"/tmp/a.png"` || inner[0]["src"] != nil {
		t.Errorf("example's request fields: %+v", inner)
	}
	// Read back, it is what was sent.
	data, _ := json.Marshal(body)
	back := request(t, string(data))
	if f := back.Request.Body.FormData[0]; f.Files()[0] != "/tmp/a.png" || f.Type != "file" {
		t.Errorf("read back: %+v", f)
	}
}
