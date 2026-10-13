package codegen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shinyvision/posthero/internal/jsonx"
	"github.com/shinyvision/posthero/internal/postman"
	"github.com/shinyvision/posthero/internal/send"
	"github.com/shinyvision/posthero/internal/vars"
)

// prepare prepares an item the way posthero does before it sends it.
func prepare(t *testing.T, it *postman.Item, dir string) *send.Prepared {
	t.Helper()
	p, err := send.FromItem(it, nil, send.DefaultSettings()).Prepare(vars.New(), send.Options{Dir: dir, Token: "t"})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func fileSrc(path string) []byte {
	b, _ := jsonx.Marshal(path)
	return b
}

// formItem posts a form with the fields that the curl snippet used to
// get wrong.
func formItem(t *testing.T, base string) (*postman.Item, string) {
	dir := t.TempDir()
	shot := filepath.Join(dir, "Screenshot (1).png")
	_ = os.WriteFile(shot, []byte{0x89, 'P', 'N', 'G', 0, 0xff, '\r', '\n'}, 0o600)
	_ = os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("it's \"here\"\n"), 0o600)
	it := &postman.Item{Name: "form", Request: &postman.Request{Method: "POST", URL: &postman.URL{Raw: base + "/upload?x=1"},
		Header: postman.Params{{Key: "X-Note", Value: "it's"}},
		Body: &postman.Body{Mode: "formdata", FormData: postman.Params{
			{Key: "handle", Value: "@rachel", Type: "text"},
			{Key: "bio", Value: "two\nlines: yes", Type: "text"},
			{Key: "pair", Value: "a;b", Type: "text"},
			{Key: "lead", Value: "<not a file", Type: "text"},
			{Key: "photo", Type: "file", Src: fileSrc(shot)},
			{Key: "meta", Value: `{"a": "b;c"}`, Type: "text", ContentType: "application/json"},
			{Key: "notes", Type: "file", Src: fileSrc("notes.txt")},
		}}}}
	return it, dir
}

func TestCurlMultipart(t *testing.T) {
	it, dir := formItem(t, "https://x.com")
	p := prepare(t, it, dir)
	got, _ := Snippet("curl", p)
	for _, want := range []string{
		`--form-string handle=@rachel`,
		`--form-string 'bio=two` + "\n" + `lines: yes'`,
		`--form-string 'pair=a;b'`,
		`--form-string 'lead=<not a file'`,
		`-F 'photo=@"` + filepath.Join(dir, "Screenshot (1).png") + `"'`,
		`-F 'meta="{\"a\": \"b;c\"}";type=application/json'`,
		`-F 'notes=@"` + filepath.Join(dir, "notes.txt") + `";type=text/plain; charset=utf-8'`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("curl form: no %s in\n%s", want, got)
		}
	}
	if strings.Contains(strings.ToLower(got), "content-type: multipart") {
		t.Errorf("curl form: curl writes its own Content-Type:\n%s", got)
	}
	// What curl writes, curl reads back.
	back, err := ParseCurl(got)
	if err != nil {
		t.Fatal(err)
	}
	fd := back.Request.Body.FormData
	if len(fd) != 7 || fd[0].Value != "@rachel" || fd[1].Value != "two\nlines: yes" || fd[2].Value != "a;b" || fd[3].Value != "<not a file" ||
		fd[4].Files()[0] != filepath.Join(dir, "Screenshot (1).png") || fd[5].Value != `{"a": "b;c"}` || fd[5].ContentType != "application/json" {
		t.Errorf("round trip: %+v", fd)
	}
}

func TestHTTPieMultipart(t *testing.T) {
	it, dir := formItem(t, "https://x.com")
	p := prepare(t, it, dir)
	got, _ := Snippet("httpie", p)
	for _, want := range []string{
		"http --multipart ",
		" POST 'https://x.com/upload?x=1' ",
		`'handle=\@rachel'`,
		`'bio=two` + "\n" + `lines\: yes'`,
		`'pair=a\;b'`,
		`'photo@` + filepath.Join(dir, "Screenshot (1).png") + `'`,
		`'X-Note:it'\''s'`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("httpie form: no %s in\n%s", want, got)
		}
	}
	if strings.Contains(strings.ToLower(got), "content-type") || strings.Contains(got, "printf") {
		t.Errorf("httpie form: no Content-Type, no piped body:\n%s", got)
	}
}

func TestCurlHead(t *testing.T) {
	p := &send.Prepared{Method: "HEAD", URL: "https://x.com/a", Settings: send.DefaultSettings()}
	got, _ := Snippet("curl", p)
	if !strings.HasPrefix(got, "curl -I https://x.com/a") || strings.Contains(got, "-X") {
		t.Errorf("curl HEAD: %s", got)
	}
}

func TestSnippetBytes(t *testing.T) {
	p := &send.Prepared{Method: "POST", URL: "https://x.com/a", Body: []byte("a\r\nb`c\x00"), Settings: send.DefaultSettings(),
		Header: []send.Header{{Name: "Content-Type", Value: "text/plain", System: true}}}
	if got, _ := Snippet("go", p); !strings.Contains(got, `strings.NewReader("a\r\nb`+"`"+`c\x00")`) {
		t.Errorf("go CR/NUL:\n%s", got)
	}
	p.Body = []byte{'h', 'i', 0xff, 0xfe, 0x80, '"', '\\'}
	if got, _ := Snippet("go", p); !strings.Contains(got, `strings.NewReader("hi\xff\xfe\x80\"\\")`) {
		t.Errorf("go binary:\n%s", got)
	}
	if got, _ := Snippet("python", p); !strings.Contains(got, `data = b"hi\xff\xfe\x80\"\\"`) || strings.Contains(got, ".encode()") {
		t.Errorf("python binary:\n%s", got)
	}
	if got, _ := Snippet("fetch", p); !strings.Contains(got, `Uint8Array`) {
		t.Errorf("fetch binary:\n%s", got)
	}
	p.Body = []byte("bell\a é \U0001F600 \u2028")
	if got, _ := Snippet("fetch", p); !strings.Contains(got, `"bell\u0007 é 😀 \u2028"`) {
		t.Errorf("fetch text:\n%s", got)
	}
}

// The settings that change how a request is sent are in every snippet
// that can say them.
func TestSnippetSettings(t *testing.T) {
	s := send.DefaultSettings()
	s.FollowRedirects, s.StrictSSL = false, false
	p := &send.Prepared{Method: "GET", URL: "https://x.com/a?ids[]=1&f={x}", Settings: s,
		Header: []send.Header{{Name: "Cookie", Value: "a=1", System: true}}}
	want := map[string][]string{
		"curl":   {" -g", " -k"},
		"httpie": {"--verify=no"},
		"wget":   {"--no-check-certificate", "--max-redirect=0", "--method=GET"},
		"fetch":  {`redirect: "manual"`, `credentials: "include"`, "NODE_TLS_REJECT_UNAUTHORIZED"},
		"python": {"verify=False", "allow_redirects=False"},
		"go":     {"InsecureSkipVerify: true", "http.ErrUseLastResponse"},
	}
	for lang, ws := range want {
		got, _ := Snippet(lang, p)
		for _, w := range ws {
			if !strings.Contains(got, w) {
				t.Errorf("%s: no %s in\n%s", lang, w, got)
			}
		}
	}
	if got, _ := Snippet("curl", p); strings.Contains(got, " -L") {
		t.Errorf("curl follows redirects:\n%s", got)
	}
	if got, _ := Snippet("httpie", p); strings.Contains(got, "--follow") {
		t.Errorf("httpie follows redirects:\n%s", got)
	}
}
