package reqdoc

import (
	"encoding/json"
	"errors"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/shinyvision/posthero/internal/postman"
)

func example(t *testing.T, js string) *postman.Response {
	t.Helper()
	var r postman.Response
	if err := json.Unmarshal([]byte(js), &r); err != nil {
		t.Fatal(err)
	}
	return &r
}

var getProduct = `{"name": "Get product", "request": {"method": "GET", "header": [{"key": "Accept", "value": "application/json"}],
	"url": {"raw": "{{baseUrl}}/api/products/:id", "variable": [{"key": "id", "value": "{{productId}}"}]}},
	"event": [{"listen": "test", "script": {"exec": ["pm.test('ok', () => {});"]}}]}`

// An example reads as its request, then its response; read back as it is,
// it is the example it was, to the byte.
func TestExampleRoundTrip(t *testing.T) {
	it := item(t, getProduct)
	r := example(t, `{"id": "e1", "name": "A product",
		"originalRequest": {"method": "GET", "header": [{"key": "Accept", "value": "application/json", "description": "what comes back"}], "url": {"raw": "{{baseUrl}}/api/products/p-1001"}},
		"status": "OK", "code": 200, "_postman_previewlanguage": "json",
		"header": [{"key": "Content-Type", "value": "application/json", "description": "always JSON"}],
		"cookie": [], "responseTime": 12,
		"body": "{\n  \"id\": \"p-1001\",\n  \"price\": 129.0\n}\n"}`)
	want := `GET {{baseUrl}}/api/products/p-1001
Accept: application/json

### response 200 OK
Content-Type: application/json

{
  "id": "p-1001",
  "price": 129.0
}
`
	got := RenderExample(it, r)
	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
	back, err := ApplyExample(it, r, got)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(back, r) {
		a, _ := json.Marshal(back)
		b, _ := json.Marshal(r)
		t.Errorf("changed:\n%s\nwas:\n%s", a, b)
	}
}

// JSON on one line is spread out to read. Unchanged, it stays as Postman
// has it; changed, it is saved as written, with its language.
func TestExampleJSONBody(t *testing.T) {
	it := item(t, getProduct)
	r := example(t, `{"name": "Minified", "code": 200, "status": "OK", "body": "{\"id\":\"p-1\",\"tags\":[\"a\",\"b\"]}"}`)
	doc := RenderExample(it, r)
	if !strings.Contains(doc, "{\n  \"id\": \"p-1\",\n  \"tags\": [\n    \"a\",\n    \"b\"\n  ]\n}") {
		t.Fatalf("not spread out:\n%s", doc)
	}
	back, err := ApplyExample(it, r, doc)
	if err != nil {
		t.Fatal(err)
	}
	if back.Body != r.Body || back.PreviewLanguage != "" {
		t.Errorf("unchanged body changed: %q (%q)", back.Body, back.PreviewLanguage)
	}
	edited := strings.Replace(doc, `"p-1"`, `"p-2"`, 1)
	back, err = ApplyExample(it, r, edited)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(back.Body, "\"id\": \"p-2\"") || back.PreviewLanguage != "json" {
		t.Errorf("edited body: %q (%q)", back.Body, back.PreviewLanguage)
	}
	// An example saved without its request shows the request's; it keeps
	// none until its request is changed.
	if back.OriginalRequest != nil {
		t.Errorf("request: %+v", back.OriginalRequest)
	}
	back, err = ApplyExample(it, r, strings.Replace(edited, "Accept: application/json", "Accept: text/plain", 1))
	if err != nil {
		t.Fatal(err)
	}
	if o := back.OriginalRequest; o == nil || o.URL.Raw != "{{baseUrl}}/api/products/:id" || string(o.Header[0].Value) != "text/plain" {
		t.Errorf("request: %+v", back.OriginalRequest)
	}
}

// The status line takes a code and its text; without the text, the usual
// one for the code.
func TestExampleStatus(t *testing.T) {
	it := item(t, getProduct)
	r := example(t, `{"name": "Teapot", "code": 418, "status": "I'm a teapot", "body": "short and stout"}`)
	doc := RenderExample(it, r)
	if !strings.Contains(doc, "### response 418 I'm a teapot\n\nshort and stout\n") {
		t.Fatalf("doc:\n%s", doc)
	}
	cases := map[string][2]string{
		"### response 404":            {"404", "Not Found"},
		"### response 201 Made it":    {"201", "Made it"},
		"### response 418":            {"418", "I'm a teapot"},
		"###   Response   503   Busy": {"503", "Busy"},
	}
	for line, want := range cases {
		back, err := ApplyExample(it, r, strings.Replace(doc, "### response 418 I'm a teapot", line, 1))
		if err != nil {
			t.Errorf("%s: %v", line, err)
			continue
		}
		if got := [2]string{strconv.Itoa(back.Code), back.Status}; got != want {
			t.Errorf("%s: %v, want %v", line, got, want)
		}
	}
}

// A response without headers has its body right under the status, or
// after a blank line; one without a body ends with its headers.
func TestExampleWithoutHeadersOrBody(t *testing.T) {
	it := item(t, getProduct)
	r := example(t, `{"name": "Bare", "code": 200, "status": "OK", "body": "old"}`)
	for _, doc := range []string{
		"GET http://x\n\n### response 200 OK\n{\"a\": 1}\n",
		"GET http://x\n\n### response 200 OK\n\n{\"a\": 1}",
	} {
		back, err := ApplyExample(it, r, doc)
		if err != nil {
			t.Fatal(err)
		}
		if len(back.Header) != 0 || back.Body != `{"a": 1}` {
			t.Errorf("%q: headers %v, body %q", doc, back.Header, back.Body)
		}
	}
	back, err := ApplyExample(it, r, "GET http://x\n\n### response 204\nX-Trace: 1\n")
	if err != nil {
		t.Fatal(err)
	}
	if back.Code != 204 || back.Status != "No Content" || back.Body != "" || len(back.Header) != 1 || back.Header[0].Key != "X-Trace" {
		t.Errorf("204: %+v", back)
	}
	if doc := RenderExample(it, back); !strings.HasSuffix(doc, "### response 204 No Content\nX-Trace: 1\n") {
		t.Errorf("rendered:\n%s", doc)
	}
}

// Lines of the body that would read as a section stay the body's.
func TestExampleBodyWithSections(t *testing.T) {
	it := item(t, getProduct)
	r := example(t, `{"name": "Markdown", "code": 200, "status": "OK", "body": "# Notes\n### response 500\n### tests\ntext"}`)
	doc := RenderExample(it, r)
	back, err := ApplyExample(it, r, doc)
	if err != nil {
		t.Fatalf("%v in:\n%s", err, doc)
	}
	if back.Body != r.Body {
		t.Errorf("body %q from:\n%s", back.Body, doc)
	}
}

// Mistakes say what an example is.
func TestExampleErrors(t *testing.T) {
	it := item(t, getProduct)
	r := example(t, `{"name": "A", "code": 200, "status": "OK", "body": "{}"}`)
	cases := map[string]string{
		"GET http://x\n": "### response",
		"GET http://x\n\n### tests\npm.test()\n\n### response 200": "no ### tests of its own",
		"GET http://x\n\n### response\n":                           "say the status",
		"GET http://x\n\n### response OK\n":                        "starts with its code",
		"GET http://x\n\n### response 200\nA: 1\nnot a header\n":   "a response header is a name and a value",
		"### response 200\n":                                       "method and URL",
	}
	for doc, want := range cases {
		_, err := ApplyExample(it, r, doc)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: %v, want %q", doc, err, want)
		}
	}
	var de *Error
	_, err := ApplyExample(it, r, "GET http://x\nAccept: json\n\n### response 200\nA: 1\nnot a header\n")
	if !errors.As(err, &de) || de.Line != 6 {
		t.Errorf("line of the mistake: %v", err)
	}
	// A request's document has no response.
	if _, err := Apply(it, "GET http://x\n\n### response 200 OK\n{}"); err == nil || !strings.Contains(err.Error(), "saved example") {
		t.Errorf("a request with a response: %v", err)
	}
}

// The request part takes what a request's does: a GraphQL body and its
// variables, auth; completion finds the query.
func TestExampleGraphQLRequest(t *testing.T) {
	it := item(t, `{"name": "Product", "request": {"method": "POST", "url": "{{gql}}", "body": {"mode": "graphql", "graphql": {"query": "query { a }"}}}}`)
	r := example(t, `{"name": "Found", "code": 200, "status": "OK", "body": "{\"data\":{\"a\":1}}"}`)
	doc := RenderExample(it, r)
	edited := strings.Replace(doc, "query { a }", "query P($id: ID!) { product(id: $id) { name } }\n\n### variables\n{\"id\": \"p-1\"}\n\n### auth bearer\ntoken: {{token}}", 1)
	back, err := ApplyExample(it, r, edited)
	if err != nil {
		t.Fatalf("%v in:\n%s", err, edited)
	}
	b := back.OriginalRequest.Body
	if b == nil || b.Mode != "graphql" || !strings.Contains(string(b.GraphQL.Query), "product(id: $id)") || string(b.GraphQL.Variables) != `{"id": "p-1"}` {
		t.Errorf("body: %+v", b)
	}
	if a := back.OriginalRequest.Auth; a == nil || a.Type != "bearer" || a.Get("token") != "{{token}}" {
		t.Errorf("auth: %+v", a)
	}
	q, line, ok := GraphQLQuery(RenderExample(it, back))
	if !ok || line != 2 || !strings.Contains(q, "product(id: $id)") || strings.Contains(q, "response") {
		t.Errorf("query %q at %d (%v)", q, line, ok)
	}
	if got := RequestPart(edited); strings.Contains(got, "### response") || !strings.HasPrefix(got, "POST {{gql}}") {
		t.Errorf("request part:\n%s", got)
	}
}
