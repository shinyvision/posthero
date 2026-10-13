package codegen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shinyvision/posthero/internal/postman"
)

func mustParse(t *testing.T, cmd string) *postman.Request {
	t.Helper()
	it, err := ParseCurl(cmd)
	if err != nil {
		t.Fatalf("%s: %v", cmd, err)
	}
	return it.Request
}

func header(r *postman.Request, name string) (string, bool) {
	for _, h := range r.Header {
		if strings.EqualFold(h.Key, name) {
			return string(h.Value), true
		}
	}
	return "", false
}

// $'…' words have all of bash's escapes: browsers write ! as \u0021
// (Chrome) and é as \xe9 (Firefox).
func TestParseCurlANSIC(t *testing.T) {
	cases := []struct{ word, want string }{
		{`$'{"password":"hunter2\u0021"}'`, `{"password":"hunter2!"}`},
		{`$'{"name":"Jos\xe9"}'`, `{"name":"José"}`},
		{`$'Jos\xc3\xa9'`, "José"},
		{`$'\U0001F600\u00e9'`, "😀é"},
		{`$'\101\0102\x41\x4'`, "A\b2A\x04"},
		{`$'\a\b\e\E\f\v\n\r\t'`, "\a\b\x1b\x1b\f\v\n\r\t"},
		{`$'\\ \' \" \?'`, `\ ' " ?`},
		{`$'\z\cA'`, "\\z\x01"},
		{`$'it\'s'`, "it's"},
	}
	for _, c := range cases {
		r := mustParse(t, "curl https://x.com/a -H 'Content-Type: text/plain' --data-raw "+c.word)
		if r.Body == nil || r.Body.Raw != c.want {
			t.Errorf("%s: got %q, want %q", c.word, r.Body.Raw, c.want)
		}
	}
	if _, err := ParseCurl(`curl https://x.com -d $'abc`); err == nil {
		t.Error("an open $' quote: no error")
	}
}

// Copies for Windows cmd.exe: ^ escapes and ^ continuations, \" and ""
// for quotes.
func TestParseCurlCmd(t *testing.T) {
	chrome := "curl ^\"https://x.com/search?q=a^&page=2^\" ^\n" +
		"  -H ^\"accept: application/json^\" ^\n" +
		"  -H ^\"content-type: application/json^\" ^\n" +
		"  --data-raw ^\"^{^\\^\"a^\\^\":1,^\\^\"b^\\^\":^\\^\"x^^y ^%^PATH^% 100^%^\\^\"^}^\" ^\n" +
		"  --compressed"
	r := mustParse(t, chrome)
	if r.URL.Raw != "https://x.com/search?q=a&page=2" {
		t.Errorf("chrome url: %q", r.URL.Raw)
	}
	if v, ok := header(r, "accept"); !ok || v != "application/json" || len(r.Header) != 2 {
		t.Errorf("chrome headers: %+v", r.Header)
	}
	if r.Body == nil || r.Body.Raw != `{"a":1,"b":"x^y %PATH% 100%"}` {
		t.Errorf("chrome body: %+v", r.Body)
	}

	// A newline in the data: ^, then two newlines.
	r = mustParse(t, "curl ^\"https://x.com/a^\" ^\n  -H ^\"Content-Type: text/plain^\" ^\n  --data-raw ^\"one^\n\ntwo^\"")
	if r.Body == nil || r.Body.Raw != "one\ntwo" {
		t.Errorf("chrome newline: %+v", r.Body)
	}

	firefox := "curl \"https://x.com/j?a=1&b=2\" ^\r\n  -X POST ^\r\n  -H \"Content-Type: application/json\" ^\r\n  --data-raw \"{\"\"a\"\":1}\""
	r = mustParse(t, firefox)
	if r.URL.Raw != "https://x.com/j?a=1&b=2" || r.Method != "POST" {
		t.Errorf("firefox: %s %q", r.Method, r.URL.Raw)
	}
	if r.Body == nil || r.Body.Raw != `{"a":1}` {
		t.Errorf("firefox body: %+v", r.Body)
	}
	// A ^" inside a bash word isn't cmd.exe's.
	r = mustParse(t, `curl https://x.com/a -H 'Content-Type: application/json' --data-raw '{"re":"^"}'`)
	if r.Body == nil || r.Body.Raw != `{"re":"^"}` {
		t.Errorf("bash ^\": %+v", r.Body)
	}
	// Backslashes count only before a quote.
	r = mustParse(t, `curl ^"https://x.com/a^" -H ^"Content-Type: text/plain^" --data-raw ^"a\b\\^"`)
	if r.Body == nil || r.Body.Raw != `a\b\` {
		t.Errorf("backslashes: %+v", r.Body)
	}
}

// -d @file sends a file; -F name=<file sends the file's text as a value.
func TestParseCurlFiles(t *testing.T) {
	r := mustParse(t, `curl -d @payload.json https://x.com/a`)
	if r.Body == nil || r.Body.Mode != "file" || r.Body.File == nil || r.Body.File.Src != "payload.json" {
		t.Errorf("-d @file: %+v", r.Body)
	}
	if v, _ := header(r, "Content-Type"); v != "application/x-www-form-urlencoded" {
		t.Errorf("-d @file: curl sends a form Content-Type, got %q", v)
	}
	for _, flag := range []string{"--data", "--data-ascii", "--data-binary"} {
		r := mustParse(t, "curl "+flag+" @p.bin https://x.com/a")
		if r.Body == nil || r.Body.Mode != "file" || r.Body.File.Src != "p.bin" {
			t.Errorf("%s @file: %+v", flag, r.Body)
		}
	}
	r = mustParse(t, `curl --data-raw @handle https://x.com/a`)
	if r.Body == nil || r.Body.Mode != "raw" || r.Body.Raw != "@handle" {
		t.Errorf("--data-raw @: %+v", r.Body)
	}
	r = mustParse(t, `curl --json @q.json https://x.com/a`)
	if r.Body == nil || r.Body.Mode != "file" || r.Body.File.Src != "q.json" {
		t.Errorf("--json @file: %+v", r.Body)
	}
	if v, _ := header(r, "Content-Type"); v != "application/json" {
		t.Errorf("--json @file: Content-Type %q", v)
	}

	// Several -d: the files are read, as curl does (-d drops newlines).
	dir := t.TempDir()
	part := filepath.Join(dir, "part.txt")
	_ = os.WriteFile(part, []byte("b=2\r\n"), 0o600)
	r = mustParse(t, "curl -d a=1 -d @"+part+" https://x.com/a")
	if r.Body == nil || r.Body.Mode != "urlencoded" || len(r.Body.URLEncoded) != 2 || r.Body.URLEncoded[1].Value != "2" {
		t.Errorf("-d a=1 -d @file: %+v", r.Body)
	}

	notes := filepath.Join(dir, "notes.txt")
	_ = os.WriteFile(notes, []byte("line one\nline two"), 0o600)
	r = mustParse(t, "curl -F 'notes=<"+notes+"' -F 'kind=<"+notes+";type=text/markdown' https://x.com/a")
	if r.Body == nil || r.Body.Mode != "formdata" || len(r.Body.FormData) != 2 {
		t.Fatalf("-F <file: %+v", r.Body)
	}
	if f := r.Body.FormData[0]; f.Type != "text" || f.Value != "line one\nline two" {
		t.Errorf("-F <file: %+v", f)
	}
	if f := r.Body.FormData[1]; f.Type != "text" || f.Value != "line one\nline two" || f.ContentType != "text/markdown" {
		t.Errorf("-F <file;type=: %+v", f)
	}
	// A file that isn't there is still sent from the file.
	r = mustParse(t, `curl -F 'notes=<missing.txt' https://x.com/a`)
	if f := r.Body.FormData[0]; f.Type != "file" || len(f.Files()) != 1 || f.Files()[0] != "missing.txt" {
		t.Errorf("-F <missing: %+v", f)
	}
	// curl's -F syntax: quoted file names, ;type= and ;filename=.
	r = mustParse(t, `curl -F 'photo=@"/tmp/Screenshot (1).png";type=image/png' -F 'doc=@a.txt;filename=b.txt' -F 'q="@x;y";type=text/foo' https://x.com/a`)
	if f := r.Body.FormData[0]; f.Type != "file" || f.Files()[0] != "/tmp/Screenshot (1).png" || f.ContentType != "image/png" {
		t.Errorf("-F quoted file: %+v", f)
	}
	if f := r.Body.FormData[1]; f.Type != "file" || f.Files()[0] != "a.txt" {
		t.Errorf("-F ;filename: %+v", f)
	}
	if f := r.Body.FormData[2]; f.Type != "text" || f.Value != "@x;y" || f.ContentType != "text/foo" {
		t.Errorf("-F quoted text: %+v", f)
	}
	r = mustParse(t, `curl --form-string 'handle=@rachel;x' https://x.com/a`)
	if f := r.Body.FormData[0]; f.Type != "text" || f.Value != "@rachel;x" {
		t.Errorf("--form-string: %+v", f)
	}
}

// Options that take a value don't give it as the URL.
func TestParseCurlOptions(t *testing.T) {
	cases := []struct{ cmd, url string }{
		{`curl https://x.com/a --oauth2-bearer abc123`, "https://x.com/a"},
		{`curl -D headers.txt https://x.com/a`, "https://x.com/a"},
		{`curl https://x.com/a -D headers.txt`, "https://x.com/a"},
		{`curl --cert-type PEM --proxy-header 'X: y' --resolve x.com:443:127.0.0.1 https://x.com/a`, "https://x.com/a"},
		{`curl https://x.com/a https://x.com/b`, "https://x.com/a"},
		{`curl --url https://x.com/a https://x.com/b`, "https://x.com/a"},
		{`curl -sSo out.json https://x.com/a`, "https://x.com/a"},
		{`curl -sY 1 -y5 https://x.com/a`, "https://x.com/a"},
	}
	for _, c := range cases {
		r := mustParse(t, c.cmd)
		if r.URL.Raw != c.url {
			t.Errorf("%s: url %q", c.cmd, r.URL.Raw)
		}
	}
	r := mustParse(t, `curl https://x.com/a --oauth2-bearer abc123`)
	if r.Auth == nil || r.Auth.Type != "bearer" || r.Auth.Get("token") != "abc123" {
		t.Errorf("--oauth2-bearer: %+v", r.Auth)
	}
	// Short options run together.
	it, err := ParseCurl(`curl -sSLk -XPUT https://x.com/a`)
	if err != nil || it.Request.Method != "PUT" || !strings.Contains(string(it.ProtocolProfileBehavior), `"followRedirects":true`) || !strings.Contains(string(it.ProtocolProfileBehavior), `"strictSSL":false`) {
		t.Errorf("-sSLk: %v %+v %s", err, it.Request, it.ProtocolProfileBehavior)
	}
	// -b with no = is a cookie file, not a cookie.
	if r := mustParse(t, `curl -b cookies.txt https://x.com/a`); len(r.Header) != 0 {
		t.Errorf("-b file: %+v", r.Header)
	}
	// Name; is an empty header; Name: alone keeps curl from sending one.
	if r := mustParse(t, `curl -H 'X-Empty;' -H 'Expect:' https://x.com/a`); len(r.Header) != 1 || r.Header[0].Key != "X-Empty" || r.Header[0].Value != "" {
		t.Errorf("empty headers: %+v", r.Header)
	}
	if _, err := ParseCurl(`curl https://x.com/a -H`); err == nil {
		t.Error("-H with no value: no error")
	}
}

// -d sends its bytes as they are; only name=value pairs become form fields.
func TestParseCurlData(t *testing.T) {
	cases := []struct{ data, mode, raw string }{
		{`'[1,2]'`, "raw", "[1,2]"},
		{`'hello world'`, "raw", "hello world"},
		{`'a=1&b'`, "raw", "a=1&b"},
		{`'<a b="c"/>'`, "raw", `<a b="c"/>`},
		{`'{"a":"b=c"}'`, "raw", `{"a":"b=c"}`},
		{`'a=1%ZZ'`, "raw", "a=1%ZZ"},
		{`'a=1&ids[]=2&c=x+y'`, "urlencoded", ""},
	}
	for _, c := range cases {
		r := mustParse(t, "curl -d "+c.data+" https://x.com/a")
		if r.Body == nil || r.Body.Mode != c.mode || r.Body.Raw != c.raw {
			t.Errorf("-d %s: %+v", c.data, r.Body)
			continue
		}
		v, ok := header(r, "Content-Type")
		switch {
		case c.mode == "raw" && v != "application/x-www-form-urlencoded":
			t.Errorf("-d %s: curl sends a form Content-Type, got %q", c.data, v)
		case c.mode == "urlencoded" && ok:
			t.Errorf("-d %s: a urlencoded body has its own Content-Type, got %q", c.data, v)
		}
	}
	r := mustParse(t, `curl -d 'a=1&ids[]=2&c=x+y' https://x.com/a`)
	if u := r.Body.URLEncoded; len(u) != 3 || u[1].Key != "ids[]" || u[2].Value != "x y" {
		t.Errorf("urlencoded: %+v", u)
	}
	// A JSON Content-Type keeps the body raw.
	r = mustParse(t, `curl -H 'Content-Type: application/json' -d 'a=1' https://x.com/a`)
	if r.Body.Mode != "raw" || r.Body.Raw != "a=1" || len(r.Header) != 1 {
		t.Errorf("json a=1: %+v %+v", r.Body, r.Header)
	}
}
