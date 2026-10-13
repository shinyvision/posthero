package codegen

import (
	"strings"
	"testing"

	"github.com/shinyvision/posthero/internal/send"
)

func TestParseCurl(t *testing.T) {
	cases := []struct {
		cmd, method, url, mode string
		headers                int
	}{
		{`curl https://api.example.com/users`, "GET", "https://api.example.com/users", "", 0},
		{`curl -X POST 'https://x.com/a?b=1' -H 'Content-Type: application/json' -d '{"a": 1}'`, "POST", "https://x.com/a?b=1", "raw", 1},
		{"curl 'https://x.com/graphql' \\\n  -H 'accept: */*' \\\n  -H 'content-type: application/json' \\\n  --data-raw $'{\"query\":\"query Q { a }\",\"variables\":{\"id\":\"1\"}}' \\\n  --compressed", "POST", "https://x.com/graphql", "graphql", 2},
		{`curl -d name=Ada -d 'job=math genius' https://x.com/people`, "POST", "https://x.com/people", "urlencoded", 0},
		{`curl -F name=Ada -F photo=@/tmp/a.png;type=image/png https://x.com/upload`, "POST", "https://x.com/upload", "formdata", 0},
		{`curl --data-binary @data.bin https://x.com/upload`, "POST", "https://x.com/upload", "file", 1}, // curl's form Content-Type
		{`curl -G -d q=1 https://x.com/search`, "GET", "https://x.com/search?q=1", "", 0},
		{`curl --json '{"a":1}' https://x.com/j`, "POST", "https://x.com/j", "raw", 2}, // Content-Type and Accept
		{`curl -u ada:secret -k -L https://x.com/me`, "GET", "https://x.com/me", "", 0},
	}
	for _, c := range cases {
		it, err := ParseCurl(c.cmd)
		if err != nil {
			t.Errorf("%s: %v", c.cmd, err)
			continue
		}
		r := it.Request
		mode := ""
		if r.Body != nil {
			mode = r.Body.Mode
		}
		if r.Method != c.method || r.URL.Raw != c.url || mode != c.mode || len(r.Header) != c.headers {
			t.Errorf("%s:\n got %s %s %q %d headers", c.cmd, r.Method, r.URL.Raw, mode, len(r.Header))
		}
	}
	it, _ := ParseCurl(cases[2].cmd)
	if g := it.Request.Body.GraphQL; string(g.Query) != "query Q { a }" || !strings.Contains(string(g.Variables), `"id": "1"`) {
		t.Errorf("graphql: %+v", g)
	}
	it, _ = ParseCurl(cases[8].cmd)
	if it.Request.Auth == nil || it.Request.Auth.Get("username") != "ada" || !strings.Contains(string(it.ProtocolProfileBehavior), `"strictSSL":false`) {
		t.Errorf("auth/settings: %+v %s", it.Request.Auth, it.ProtocolProfileBehavior)
	}
	if _, err := ParseCurl("wget x"); err == nil {
		t.Error("not curl")
	}
}

func TestSnippets(t *testing.T) {
	p := &send.Prepared{Method: "POST", URL: "https://x.com/a?b=1", Body: []byte(`{"it's": 1}`), Settings: send.DefaultSettings(),
		Header: []send.Header{{Name: "Content-Type", Value: "application/json", System: true}, {Name: "Authorization", Value: "Bearer t", Auth: true}, {Name: "User-Agent", Value: "posthero", System: true}}}
	got, _ := Snippet("curl", p)
	want := "curl -X POST 'https://x.com/a?b=1' \\\n  -H 'Content-Type: application/json' \\\n  -H 'Authorization: Bearer t' \\\n  --data-raw '{\"it'\\''s\": 1}' \\\n  -L"
	if got != want {
		t.Errorf("curl:\n%s\nwant:\n%s", got, want)
	}
	// What curl writes, curl reads back.
	it, err := ParseCurl(got)
	if err != nil || it.Request.Body.Raw != `{"it's": 1}` || len(it.Request.Header) != 2 {
		t.Errorf("round trip: %v %+v", err, it.Request)
	}
	for _, lang := range Languages {
		out, err := Snippet(lang, p)
		if err != nil || !strings.Contains(out, "x.com") || strings.Contains(out, "posthero") {
			t.Errorf("%s: %v\n%s", lang, err, out)
		}
	}
}
