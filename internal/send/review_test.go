package send

import (
	"bytes"
	"compress/zlib"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/shinyvision/posthero/internal/postman"
	"github.com/shinyvision/posthero/internal/vars"
)

// Credentials stay with their origin on redirects; 303 turns into a GET
// without the body; 307 keeps both.
func TestRedirectsKeepCredentialsHome(t *testing.T) {
	var got []*http.Request
	var bodies []string
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got, bodies = append(got, r), append(bodies, string(b))
	}))
	defer other.Close()
	home := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/away":
			http.Redirect(w, r, other.URL+"/landed", http.StatusFound)
		case "/see-other":
			http.Redirect(w, r, other.URL+"/result", http.StatusSeeOther)
		case "/temporary":
			http.Redirect(w, r, other.URL+"/again", http.StatusTemporaryRedirect)
		}
	}))
	defer home.Close()
	c := NewClient(nil)
	do := func(method, path string, s Settings) *http.Request {
		t.Helper()
		got, bodies = nil, nil
		p := &Prepared{Method: method, URL: home.URL + path + "?access_token=SECRET", Settings: s,
			Header: []Header{{Name: "X-Api-Key", Value: "k", Auth: true}, {Name: "Authorization", Value: "Bearer t", Auth: true}, {Name: "Content-Type", Value: "text/plain"}, {Name: "X-Mine", Value: "yes"}}}
		if method == "POST" {
			p.Body = []byte("payload")
		}
		if _, err := c.Do(context.Background(), p); err != nil {
			t.Fatal(err)
		}
		if len(got) != 1 {
			t.Fatalf("%s %s: the other server got %d requests", method, path, len(got))
		}
		return got[0]
	}
	r := do("GET", "/away", DefaultSettings())
	if r.Header.Get("X-Api-Key") != "" || r.Header.Get("Authorization") != "" {
		t.Errorf("credentials went along: %v", r.Header)
	}
	if r.Header.Get("X-Mine") != "yes" {
		t.Error("an ordinary header was dropped")
	}
	if ref := r.Header.Values("Referer"); len(ref) != 1 || ref[0] != home.URL+"/" {
		t.Errorf("referer %q: the origin only, once", ref)
	}
	settings := DefaultSettings()
	settings.FollowOriginalMethod = true
	r = do("POST", "/see-other", settings)
	if r.Method != "GET" || bodies[0] != "" || r.Header.Get("Content-Type") != "" {
		t.Errorf("303: %s with %q, content type %q", r.Method, bodies[0], r.Header.Get("Content-Type"))
	}
	r = do("POST", "/temporary", DefaultSettings())
	if r.Method != "POST" || bodies[0] != "payload" || r.Header.Get("Content-Type") != "text/plain" {
		t.Errorf("307: %s with %q", r.Method, bodies[0])
	}
}

func TestReferer(t *testing.T) {
	u := func(s string) *url.URL { x, _ := url.Parse(s); return x }
	if got := referer(u("https://a.example/x?q=1#f"), u("https://a.example/y")); got != "https://a.example/x?q=1" {
		t.Errorf("same origin: %q", got)
	}
	if got := referer(u("https://a.example/x?token=1"), u("https://b.example/y")); got != "https://a.example/" {
		t.Errorf("another origin: %q", got)
	}
	if got := referer(u("https://a.example/x"), u("http://a.example/y")); got != "" {
		t.Errorf("https to http: %q", got)
	}
}

// Bodies come out whole: zlib "deflate", a gzip header on a plain body,
// and HEAD answers with an encoding but no body.
func TestDecoding(t *testing.T) {
	var zbuf bytes.Buffer
	zw := zlib.NewWriter(&zbuf)
	_, _ = zw.Write([]byte("zlib deflate"))
	_ = zw.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/deflate":
			w.Header().Set("Content-Encoding", "deflate")
			_, _ = w.Write(zbuf.Bytes())
		case "/lying":
			w.Header().Set("Content-Encoding", "gzip")
			_, _ = w.Write([]byte("not gzip at all"))
		case "/lying-deflate":
			w.Header().Set("Content-Encoding", "deflate")
			_, _ = w.Write([]byte("plain text, said to be deflate"))
		case "/lying-br":
			w.Header().Set("Content-Encoding", "br")
			_, _ = w.Write([]byte("plain text, said to be brotli"))
		case "/head":
			w.Header().Set("Content-Encoding", "br")
		}
	}))
	defer srv.Close()
	c := NewClient(nil)
	for path, want := range map[string]string{"/deflate": "zlib deflate", "/lying": "not gzip at all", "/lying-deflate": "plain text, said to be deflate", "/lying-br": "plain text, said to be brotli"} {
		resp, err := c.Do(context.Background(), &Prepared{Method: "GET", URL: srv.URL + path, Settings: DefaultSettings()})
		if err != nil || string(resp.Body) != want {
			t.Errorf("%s: %q %v", path, resp.Body, err)
		}
	}
	if _, err := c.Do(context.Background(), &Prepared{Method: "HEAD", URL: srv.URL + "/head", Settings: DefaultSettings()}); err != nil {
		t.Errorf("HEAD: %v", err)
	}
}

// Through a proxy, a plain-http request names the whole address; a path
// starting with // goes to the right host.
func TestProxyAndDoubleSlash(t *testing.T) {
	var lines []string
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		lines = append(lines, r.RequestURI)
	}))
	defer proxy.Close()
	settings := DefaultSettings()
	settings.Proxy = proxy.URL
	p := &Prepared{Method: "GET", URL: "http://api.example.invalid/a/b?x=1", Settings: settings}
	if _, err := NewClient(nil).Do(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	if len(lines) != 1 || lines[0] != "http://api.example.invalid/a/b?x=1" {
		t.Errorf("the proxy got %q", lines)
	}
	var uri, host string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { uri, host = r.RequestURI, r.Host }))
	defer srv.Close()
	r := &Request{Method: "GET", URL: &postman.URL{Raw: srv.URL + "//files/x"}, Settings: DefaultSettings()}
	send(t, NewClient(nil), r, vars.New())
	if !strings.HasSuffix(uri, "//files/x") || host != strings.TrimPrefix(srv.URL, "http://") {
		t.Errorf("request line %q to %q", uri, host)
	}
}

func TestAddQueryEncodesValues(t *testing.T) {
	p := &Prepared{URL: "https://a.example/x?y=1#top"}
	p.AddQuery("api_key", "ab%41cd e+f")
	if p.URL != "https://a.example/x?y=1&api_key=ab%2541cd%20e%2Bf#top" {
		t.Errorf("url %s", p.URL)
	}
}

// A cookie you set yourself goes along on a redirect within the origin.
func TestOwnCookieOnRedirect(t *testing.T) {
	var cookie string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/items" {
			http.Redirect(w, r, "/api/items/", http.StatusMovedPermanently)
			return
		}
		cookie = r.Header.Get("Cookie")
	}))
	defer srv.Close()
	p := &Prepared{Method: "GET", URL: srv.URL + "/api/items", Settings: DefaultSettings(), Header: []Header{{Name: "Cookie", Value: "session=abc"}}}
	if _, err := NewClient(nil).Do(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	if cookie != "session=abc" {
		t.Errorf("the redirect sent cookie %q", cookie)
	}
}
