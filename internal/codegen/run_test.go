package codegen

import (
	"bytes"
	"context"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shinyvision/posthero/internal/postman"
	"github.com/shinyvision/posthero/internal/send"
)

type received struct {
	method, uri string
	header      http.Header
	body        []byte
}

// runner runs a snippet: the tool it needs, how its file is named and
// the command that runs the file.
type runner struct {
	tool, file string
	run        []string
}

var runners = map[string]runner{
	"curl":   {"curl", "s.sh", []string{"bash"}},
	"httpie": {"http", "s.sh", []string{"bash"}},
	"wget":   {"wget", "s.sh", []string{"bash"}},
	"fetch":  {"node", "s.mjs", []string{"node"}},
	"python": {"python3", "s.py", []string{"python3"}},
	"go":     {"go", "main.go", []string{"go", "run"}},
}

// TestSnippetsRun runs each snippet against a server and checks that it
// sends what posthero sends: method, URL, headers and body, byte for byte.
func TestSnippetsRun(t *testing.T) {
	if testing.Short() {
		t.Skip("runs curl, python, node and go")
	}
	var mu sync.Mutex
	var last *received
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		last = &received{r.Method, r.RequestURI, r.Header.Clone(), body}
		mu.Unlock()
		_, _ = io.WriteString(w, "hello") // HEAD gets a Content-Length and no body
	}))
	defer srv.Close()

	form, formDir := formItem(t, srv.URL)
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "blob.bin"), []byte{0xff, 0xfe, 0x80, 0, '\r', '\n', 'x'}, 0o600)
	raw := func(name, method, url, body string, headers ...postman.Param) *postman.Item {
		it := &postman.Item{Name: name, Request: &postman.Request{Method: method, URL: &postman.URL{Raw: url}, Header: headers}}
		if body != "" {
			it.Request.Body = &postman.Body{Mode: "raw", Raw: body}
		}
		return it
	}
	cases := []struct {
		it  *postman.Item
		dir string
	}{
		{raw("json", "POST", srv.URL+"/a?ids[]=1&q={x}&s=it's", `{"it's": "ok", "tab": "	"}`,
			postman.Param{Key: "Content-Type", Value: "application/json"}, postman.Param{Key: "X-Quote", Value: `say "hi" $HOME`}), dir},
		{raw("crlf", "PUT", srv.URL+"/b", "line one\r\nline two\r\n\a`end`\u2028é"), dir},
		{raw("head", "HEAD", srv.URL+"/c", ""), dir},
		{raw("get", "GET", srv.URL+"/d?x=%20y", ""), dir},
		{&postman.Item{Name: "file", Request: &postman.Request{Method: "POST", URL: &postman.URL{Raw: srv.URL + "/e"},
			Body: &postman.Body{Mode: "file", File: &postman.BodyFile{Src: "blob.bin"}}}}, dir},
		{&postman.Item{Name: "urlencoded", Request: &postman.Request{Method: "PATCH", URL: &postman.URL{Raw: srv.URL + "/f"},
			Body: &postman.Body{Mode: "urlencoded", URLEncoded: postman.Params{{Key: "a b", Value: "c&d=é"}}}}}, dir},
		{form, formDir},
		{&postman.Item{Name: "settings", Request: &postman.Request{Method: "DELETE", URL: &postman.URL{Raw: srv.URL + "/g"},
			Header: postman.Params{{Key: "Cookie", Value: "a=1; b=2"}, {Key: "X-Empty", Value: ""}}},
			ProtocolProfileBehavior: []byte(`{"strictSSL": false, "followRedirects": false}`)}, dir},
	}
	for lang, rn := range runners {
		if _, err := exec.LookPath(rn.tool); err != nil {
			t.Logf("%s: no %s here, not run", lang, rn.tool)
			continue
		}
		for _, c := range cases {
			p := prepare(t, c.it, c.dir)
			code, err := Snippet(lang, p)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(t.TempDir(), rn.file)
			_ = os.WriteFile(path, []byte(code), 0o600)
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			cmd := exec.CommandContext(ctx, rn.run[0], append(rn.run[1:], path)...)
			cmd.Dir = filepath.Dir(path)
			cmd.WaitDelay = time.Second // a shell's children may outlive it
			mu.Lock()
			last = nil
			mu.Unlock()
			out, err := cmd.CombinedOutput()
			cancel()
			if err != nil {
				t.Errorf("%s %s: %v\n%s\n%s", lang, c.it.Name, err, out, code)
				continue
			}
			mu.Lock()
			got := last
			mu.Unlock()
			if got == nil {
				t.Errorf("%s %s: nothing arrived\n%s", lang, c.it.Name, code)
				continue
			}
			if lang == "python" || lang == "httpie" {
				// urllib3 encodes [ ] { } in the query itself.
				got.uri = strings.NewReplacer("%5B", "[", "%5D", "]", "%7B", "{", "%7D", "}").Replace(got.uri)
			}
			compare(t, lang, c.it.Name, p, got, code)
		}
	}
}

func compare(t *testing.T, lang, name string, p *send.Prepared, got *received, code string) {
	t.Helper()
	what := lang + " " + name
	uri := p.URL[strings.Index(p.URL[len("http://"):], "/")+len("http://"):]
	if got.method != p.Method || got.uri != uri {
		t.Errorf("%s: got %s %s, want %s %s\n%s", what, got.method, got.uri, p.Method, uri, code)
	}
	body := p.Body
	if p.BodyFile != "" {
		body, _ = os.ReadFile(p.BodyFile)
	}
	_, params, _ := mime.ParseMediaType(p.Get("Content-Type"))
	_, gotParams, _ := mime.ParseMediaType(got.header.Get("Content-Type"))
	multi := isMultipart(p) && params["boundary"] != gotParams["boundary"]
	for _, h := range userHeaders(p) {
		v := got.header.Values(h.Name)
		switch {
		case multi && strings.EqualFold(h.Name, "Content-Type"):
			if !strings.HasPrefix(got.header.Get("Content-Type"), "multipart/form-data; boundary=") {
				t.Errorf("%s: Content-Type %q", what, got.header.Get("Content-Type"))
			}
		case len(v) != 1 || v[0] != h.Value:
			t.Errorf("%s: header %s: got %q, want %q\n%s", what, h.Name, v, h.Value, code)
		}
	}
	if !multi {
		if !bytes.Equal(got.body, body) {
			t.Errorf("%s: body\n got %q\nwant %q\n%s", what, got.body, body, code)
		}
		return
	}
	// HTTPie can't give a text field a Content-Type.
	textTypes := lang != "httpie"
	want, gotParts := parts(t, body, params["boundary"], textTypes), parts(t, got.body, gotParams["boundary"], textTypes)
	if strings.Join(want, "\n---\n") != strings.Join(gotParts, "\n---\n") {
		t.Errorf("%s: form\n got %q\nwant %q\n%s", what, gotParts, want, code)
	}
}

// parts writes out the parts of a multipart body, boundary left out.
func parts(t *testing.T, body []byte, boundary string, textTypes bool) []string {
	t.Helper()
	r := multipart.NewReader(bytes.NewReader(body), boundary)
	var out []string
	for {
		part, err := r.NextRawPart()
		if err == io.EOF {
			return out
		}
		if err != nil {
			t.Errorf("multipart: %v", err)
			return out
		}
		data, _ := io.ReadAll(part)
		ct := part.Header.Get("Content-Type")
		if part.FileName() == "" && !textTypes {
			ct = ""
		}
		out = append(out, part.FormName()+"|"+part.FileName()+"|"+ct+"|"+string(data))
	}
}
