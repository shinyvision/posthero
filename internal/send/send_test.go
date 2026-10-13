package send

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/andybalholm/brotli"

	"github.com/shinyvision/posthero/internal/postman"
	"github.com/shinyvision/posthero/internal/vars"
)

// echo answers with what it got.
func echo(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"method": r.Method, "path": r.URL.EscapedPath(), "query": r.URL.RawQuery,
		"headers": r.Header, "body": string(body), "host": r.Host,
	})
}

type echoed struct {
	Method  string              `json:"method"`
	Path    string              `json:"path"`
	Query   string              `json:"query"`
	Headers map[string][]string `json:"headers"`
	Body    string              `json:"body"`
}

func varsFor(base string) *vars.Set {
	return vars.New(&vars.Scope{Kind: vars.Environment, Vars: []vars.Var{
		{Key: "base", Value: base}, {Key: "id", Value: "42"}, {Key: "name", Value: "Ada Lovelace"}, {Key: "tok", Value: "s3cret"},
	}})
}

func send(t *testing.T, c *Client, r *Request, v *vars.Set) (*Response, echoed) {
	t.Helper()
	p, err := r.Prepare(v, Options{Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := c.Do(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	var e echoed
	_ = json.Unmarshal(resp.Body, &e)
	return resp, e
}

func TestPrepareAndSend(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(echo))
	defer srv.Close()
	c := NewClient(NewJar(""))
	v := varsFor(srv.URL)

	r := &Request{
		Method: "post",
		URL: postman.Rebuild(nil, "{{base}}/users/:id/notes?q={{name}}&x=a b", postman.Params{
			{Key: "q", Value: "{{name}}"}, {Key: "x", Value: "a b"}, {Key: "off", Value: "1", Disabled: true},
		}, map[string]string{"id": "{{id}}"}),
		Header:   postman.Params{{Key: "X-Who", Value: "{{name}}"}, {Key: "X-Off", Value: "1", Disabled: true}, {Key: "X-Missing", Value: "{{nope}}"}},
		Body:     &postman.Body{Mode: "raw", Raw: `{"id": {{id}}}`},
		Settings: DefaultSettings(),
	}
	r.Body.SetLanguage("json")
	p, err := r.Prepare(v, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if p.Method != "POST" || !strings.HasSuffix(p.URL, "/users/42/notes?q=Ada%20Lovelace&x=a%20b") {
		t.Errorf("url: %s %s", p.Method, p.URL)
	}
	if strings.Join(p.Missing, ",") != "nope" {
		t.Errorf("missing: %v", p.Missing)
	}
	resp, e := send(t, c, r, v)
	if resp.Status != 200 || resp.StatusText != "OK" || e.Method != "POST" || e.Path != "/users/42/notes" || e.Query != "q=Ada%20Lovelace&x=a%20b" {
		t.Errorf("sent: %d %+v", resp.Status, e)
	}
	if e.Body != `{"id": 42}` || e.Headers["Content-Type"][0] != "application/json" || e.Headers["X-Who"][0] != "Ada Lovelace" {
		t.Errorf("body/headers: %+v", e)
	}
	if _, ok := e.Headers["X-Off"]; ok {
		t.Error("a disabled header was sent")
	}
	for _, h := range []string{"User-Agent", "Postman-Token", "Cache-Control", "Accept"} {
		if len(e.Headers[h]) == 0 {
			t.Errorf("no %s header", h)
		}
	}
	if resp.Size() <= int64(len(resp.Body)) || resp.Time <= 0 || resp.Sent == nil {
		t.Errorf("size/time: %d %v", resp.Size(), resp.Time)
	}

	// GET drops its body unless told not to.
	r.Method = "GET"
	_, e = send(t, c, r, v)
	if e.Body != "" {
		t.Errorf("GET sent a body: %q", e.Body)
	}
	r.Settings.DisableBodyPruning = true
	_, e = send(t, c, r, v)
	if e.Body == "" {
		t.Error("disableBodyPruning didn't keep the body")
	}
}

func TestBodies(t *testing.T) {
	var got *http.Request
	var raw []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ = io.ReadAll(r.Body)
		got = r
	}))
	defer srv.Close()
	c := NewClient(nil)
	v := varsFor(srv.URL)
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "a.png"), []byte("PNGDATA"), 0o600)
	_ = os.WriteFile(filepath.Join(dir, "data.bin"), []byte{1, 2, 3}, 0o600)

	do := func(b *postman.Body, headers ...postman.Param) {
		t.Helper()
		r := &Request{Method: "POST", URL: &postman.URL{Raw: "{{base}}/x"}, Body: b, Header: headers, Settings: DefaultSettings()}
		p, err := r.Prepare(v, Options{Dir: dir})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := c.Do(context.Background(), p); err != nil {
			t.Fatal(err)
		}
	}

	do(&postman.Body{Mode: "graphql", GraphQL: &postman.GraphQL{Query: "query Q($id: ID!) { user(id: $id) { name } }", Variables: `{"id": "{{id}}", "n": 12345678901234567890}`}})
	if got.Header.Get("Content-Type") != "application/json" || string(raw) != `{"query":"query Q($id: ID!) { user(id: $id) { name } }","variables":{"id":"42","n":12345678901234567890}}` {
		t.Errorf("graphql: %s %s", got.Header.Get("Content-Type"), raw)
	}
	do(&postman.Body{Mode: "graphql", GraphQL: &postman.GraphQL{Query: "{ a }"}})
	if string(raw) != `{"query":"{ a }","variables":{}}` {
		t.Errorf("graphql without variables: %s", raw)
	}

	do(&postman.Body{Mode: "urlencoded", URLEncoded: postman.Params{{Key: "name", Value: "{{name}}"}, {Key: "a&b", Value: "1=2"}, {Key: "off", Value: "x", Disabled: true}}})
	if got.Header.Get("Content-Type") != "application/x-www-form-urlencoded" || string(raw) != "name=Ada+Lovelace&a%26b=1%3D2" {
		t.Errorf("urlencoded: %s", raw)
	}

	do(&postman.Body{Mode: "formdata", FormData: postman.Params{
		{Key: "name", Value: "{{name}}", Type: "text"},
		{Key: "photo", Type: "file", Src: json.RawMessage(`"a.png"`)},
		{Key: "meta", Value: `{"a":1}`, Type: "text", ContentType: "application/json"},
	}})
	mt, params, _ := mime.ParseMediaType(got.Header.Get("Content-Type"))
	if mt != "multipart/form-data" {
		t.Fatalf("formdata content type: %s", mt)
	}
	mr := multipart.NewReader(strings.NewReader(string(raw)), params["boundary"])
	var parts []string
	for {
		part, err := mr.NextPart()
		if err != nil {
			break
		}
		data, _ := io.ReadAll(part)
		parts = append(parts, part.FormName()+"|"+part.FileName()+"|"+part.Header.Get("Content-Type")+"|"+string(data))
	}
	want := []string{"name|||Ada Lovelace", "photo|a.png|image/png|PNGDATA", "meta||application/json|{\"a\":1}"}
	if strings.Join(parts, "\n") != strings.Join(want, "\n") {
		t.Errorf("formdata parts:\n%s", strings.Join(parts, "\n"))
	}

	do(&postman.Body{Mode: "file", File: &postman.BodyFile{Src: "data.bin"}})
	if string(raw) != "\x01\x02\x03" || got.Header.Get("Content-Type") != "application/octet-stream" {
		t.Errorf("file: %q %s", raw, got.Header.Get("Content-Type"))
	}

	// A Content-Type of one's own wins.
	do(&postman.Body{Mode: "raw", Raw: "<a/>"}, postman.Param{Key: "content-type", Value: "application/soap+xml"})
	if got.Header.Get("Content-Type") != "application/soap+xml" {
		t.Errorf("own content type: %s", got.Header.Get("Content-Type"))
	}

	// Bad GraphQL variables say so.
	r := &Request{Method: "POST", URL: &postman.URL{Raw: "{{base}}/x"}, Body: &postman.Body{Mode: "graphql", GraphQL: &postman.GraphQL{Query: "{a}", Variables: "{nope"}}}
	if _, err := r.Prepare(v, Options{}); err == nil || !strings.Contains(err.Error(), "GraphQL variables") {
		t.Errorf("bad variables: %v", err)
	}
}

func TestRedirectsCookiesAndEncodings(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/login", func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: "session", Value: "abc", Path: "/", HttpOnly: true})
		http.Redirect(w, r, "/home", http.StatusFound)
	})
	mux.HandleFunc("/home", echo)
	mux.HandleFunc("/gzip", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Encoding", "gzip")
		gz := gzip.NewWriter(w)
		_, _ = gz.Write([]byte(strings.Repeat("hello ", 100)))
		_ = gz.Close()
	})
	mux.HandleFunc("/br", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Encoding", "br")
		bw := brotli.NewWriter(w)
		_, _ = bw.Write([]byte("brotli!"))
		_ = bw.Close()
	})
	mux.HandleFunc("/big", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(strings.Repeat("x", 1000))) })
	mux.HandleFunc("/slow", func(w http.ResponseWriter, r *http.Request) { time.Sleep(300 * time.Millisecond) })
	srv := httptest.NewServer(mux)
	defer srv.Close()
	jar := NewJar(filepath.Join(t.TempDir(), "cookies.json"))
	c := NewClient(jar)
	v := varsFor(srv.URL)

	r := &Request{Method: "POST", URL: &postman.URL{Raw: "{{base}}/login"}, Body: &postman.Body{Mode: "raw", Raw: "x"}, Header: postman.Params{{Key: "Authorization", Value: "Bearer t"}}, Settings: DefaultSettings()}
	resp, e := send(t, c, r, v)
	if len(resp.Redirects) != 1 || e.Method != "GET" || e.Path != "/home" || e.Headers["Cookie"][0] != "session=abc" {
		t.Errorf("redirect: %+v %+v", resp.Redirects, e)
	}
	if e.Headers["Authorization"][0] != "Bearer t" {
		t.Error("same-host redirect dropped Authorization")
	}
	if all := jar.All(); len(all) != 1 || !all[0].HostOnly || !all[0].HTTPOnly {
		t.Errorf("jar: %+v", all)
	}
	if err := jar.Save(); err != nil {
		t.Fatal(err)
	}
	if again := NewJar(jar.file).All(); len(again) != 1 {
		t.Errorf("jar not saved: %+v", again)
	}

	r.Settings.FollowRedirects = false
	resp, _ = send(t, c, r, v)
	if resp.Status != 302 || len(resp.Redirects) != 0 {
		t.Errorf("not following: %d", resp.Status)
	}

	r = &Request{Method: "GET", URL: &postman.URL{Raw: "{{base}}/gzip"}, Settings: DefaultSettings()}
	resp, _ = send(t, c, r, v)
	if string(resp.Body) != strings.Repeat("hello ", 100) || resp.WireSize >= int64(len(resp.Body)) {
		t.Errorf("gzip: %d bytes, wire %d", len(resp.Body), resp.WireSize)
	}
	r.URL.Raw = "{{base}}/br"
	resp, _ = send(t, c, r, v)
	if string(resp.Body) != "brotli!" {
		t.Errorf("br: %q", resp.Body)
	}
	r.URL.Raw = "{{base}}/big"
	r.Settings.MaxResponseSize = 100
	resp, _ = send(t, c, r, v)
	if len(resp.Body) != 100 || !resp.Truncated {
		t.Errorf("max size: %d %v", len(resp.Body), resp.Truncated)
	}
	r.URL.Raw = "{{base}}/slow"
	r.Settings.Timeout = 50 * time.Millisecond
	p, _ := r.Prepare(v, Options{})
	if _, err := c.Do(context.Background(), p); err == nil || !strings.Contains(err.Error(), "no response within") {
		t.Errorf("timeout: %v", err)
	}
}

func TestJarRules(t *testing.T) {
	j := NewJar("")
	u, _ := url.Parse("https://api.example.com/v1/users")
	j.SetCookies(u, []*http.Cookie{
		{Name: "a", Value: "1"},
		{Name: "b", Value: "2", Domain: ".example.com", Path: "/"},
		{Name: "c", Value: "3", Domain: "other.com"},
		{Name: "d", Value: "4", Domain: "com"},
		{Name: "s", Value: "5", Secure: true, Path: "/"},
		{Name: "gone", Value: "6", MaxAge: -1},
	})
	names := func(raw string) string {
		u, _ := url.Parse(raw)
		var out []string
		for _, c := range j.Cookies(u) {
			out = append(out, c.Name)
		}
		return strings.Join(out, ",")
	}
	if got := names("https://api.example.com/v1/users/7"); got != "a,b,s" {
		t.Errorf("same host: %s", got)
	}
	if got := names("http://www.example.com/"); got != "b" {
		t.Errorf("other host, no TLS: %s", got)
	}
	if got := names("https://api.example.com/v2"); got != "b,s" {
		t.Errorf("other path: %s", got)
	}
	j.Delete("example.com", "b")
	if got := names("https://www.example.com/"); got != "" {
		t.Errorf("after delete: %s", got)
	}
}

func TestRawPathOnTheWire(t *testing.T) {
	var uri string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { uri = r.RequestURI }))
	defer srv.Close()
	r := &Request{Method: "GET", URL: &postman.URL{Raw: srv.URL + "/a|b/c%2Fd/[x]?q[]=1&r=a|b"}, Settings: DefaultSettings()}
	send(t, NewClient(nil), r, vars.New())
	if uri != "/a|b/c%2Fd/[x]?q[]=1&r=a|b" {
		t.Errorf("request line: %s", uri)
	}
}

func TestEncodeURL(t *testing.T) {
	cases := map[string]string{
		"http://x/a b?q=a b&r=ü#frag ment": "http://x/a%20b?q=a%20b&r=%C3%BC#frag%20ment",
		"http://x/already%20done?q=%2F":    "http://x/already%20done?q=%2F",
		"http://x/p?a[]=1&b={2}|'":         "http://x/p?a[]=1&b={2}|%27",
		"http://x/{id}/a|b":                "http://x/%7Bid%7D/a|b",
		"http://x/100%":                    "http://x/100%25",
	}
	for in, want := range cases {
		if got := EncodeURL(in); got != want {
			t.Errorf("EncodeURL(%q) = %q, want %q", in, got, want)
		}
	}
}

// A server address with a variable nobody set says which one, instead of
// failing to read the URL.
func TestHostVariableNotSet(t *testing.T) {
	set := vars.New(&vars.Scope{Kind: vars.Environment, Vars: []vars.Var{{Key: "path", Value: "graphql"}}})
	for raw, want := range map[string]string{
		"{{BackendEndpoint}}/graphql":         "{{BackendEndpoint}} isn't set, so there's no server to send to",
		"https://{{sub}}.{{domain}}/{{path}}": "{{sub}} and {{domain}} aren't set",
	} {
		r := &Request{Method: "POST", URL: postman.Rebuild(nil, raw, nil, nil)}
		_, err := r.Prepare(set, Options{})
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v, want %q", raw, err, want)
		}
	}
	// A variable later in the URL is the server's business.
	r := &Request{Method: "GET", URL: postman.Rebuild(nil, "https://example.com/{{missing}}", nil, nil)}
	if p, err := r.Prepare(set, Options{}); err != nil || len(p.Missing) != 1 {
		t.Errorf("a variable in the path: %v %v", p, err)
	}
}
