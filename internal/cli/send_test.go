package cli

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shinyvision/posthero/internal/config"
	"github.com/shinyvision/posthero/internal/postman"
	"github.com/shinyvision/posthero/internal/postman/pmtest"
)

// api starts the server the requests go to: /orders/1 answers JSON, /pdf a
// PDF, /moved redirects to /pdf, and /echo the body it was sent.
func api(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/orders/1":
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"id":1,"status":"open"}`)
		case "/pdf":
			w.Header().Set("Content-Type", "application/pdf")
			w.Write([]byte("%PDF-1.7\x00\x01"))
		case "/moved":
			http.Redirect(w, r, "/pdf", http.StatusTemporaryRedirect)
		case "/echo":
			io.Copy(w, r.Body)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// sending starts a fake with a collection "Shop" whose requests go to
// {{base}}, and environments "Local" (base is the API) and "Nowhere".
func sending(t *testing.T) (*pmtest.Server, *Env, *bytes.Buffer, string) {
	t.Helper()
	base := api(t)
	srv := pmtest.New()
	t.Cleanup(srv.Close)
	tests := postman.Event{Listen: "test", Script: postman.Script{Exec: postman.Lines{
		"pm.test('ok', () => pm.response.to.have.status(200));",
		"pm.collectionVariables.set('orderId', pm.response.json().id);",
		"console.log('status', pm.response.json().status);",
	}}}
	colID := srv.AddCollection("ws1", &postman.Collection{
		Info: postman.Info{Name: "Shop"},
		Item: []*postman.Item{
			{Name: "Get order", Event: []postman.Event{tests}, Request: &postman.Request{Method: "GET", URL: postman.ParseURL("{{base}}/orders/1")}},
			{Name: "Get PDF", Request: &postman.Request{Method: "GET", URL: postman.ParseURL("{{base}}/moved")}},
			{Name: "Broken", Event: []postman.Event{{Listen: "test", Script: postman.Script{Exec: postman.Lines{"pm.test('created', () => pm.response.to.have.status(201));"}}}},
				Request: &postman.Request{Method: "GET", URL: postman.ParseURL("{{base}}/orders/1")}},
		},
	})
	local := srv.AddEnvironment("ws1", &postman.Environment{Name: "Local", Values: []postman.EnvValue{{Key: "base", Value: postman.Str(base)}}})
	srv.AddEnvironment("ws1", &postman.Environment{Name: "Nowhere", Values: []postman.EnvValue{{Key: "base", Value: "http://127.0.0.1:1"}}})
	out := &bytes.Buffer{}
	dir := t.TempDir()
	e := &Env{API: postman.NewClient(pmtest.Key, srv.Start()), Last: "ws1", Out: out, Err: io.Discard, Sending: &Sending{
		StateDir: filepath.Join(dir, "state"), CacheDir: filepath.Join(dir, "cache"),
		Settings: config.DefaultSettings(), Envs: map[string]string{colID: local}, Dir: dir,
	}}
	return srv, e, out, colID
}

func TestSend(t *testing.T) {
	srv, e, out, colID := sending(t)
	if err := cmd(t, e, "", "send", "Shop/Get order"); err != nil {
		t.Fatalf("send: %v\n%s", err, out)
	}
	for _, want := range []string{"GET http", "/orders/1 · Local\n", "✓ ok\n", "console.log: status open\n", "200 OK · ", "\"status\": \"open\""} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("the report lacks %q:\n%s", want, out)
		}
	}
	// What the test script set is kept, as the screen keeps it: here, not
	// in Postman.
	set := e.Sending
	if got := readValues(t, set.StateDir); !strings.Contains(got, "orderId") {
		t.Errorf("the variable the script set wasn't kept: %s", got)
	}
	if c := srv.Collection(colID); len(c.Variable) != 0 {
		t.Errorf("sending changed the collection in Postman: %+v", c.Variable)
	}
}

func TestSendFailing(t *testing.T) {
	_, e, out, _ := sending(t)
	err := cmd(t, e, "", "send", "Shop/Broken")
	if err == nil || !strings.Contains(err.Error(), "1 of 1 tests failed") || !strings.Contains(out.String(), "✗ created") {
		t.Errorf("a failing test: %v\n%s", err, out)
	}
	out.Reset()
	if err := cmd(t, e, "", "send", "-e", "nowhere", "Shop/Get order"); err == nil || !strings.Contains(out.String(), "· Nowhere") {
		t.Errorf("no server, in the environment named: %v\n%s", err, out)
	}
	out.Reset()
	if err := cmd(t, e, "", "send", "-e", "nowhere", "--var", "base="+e.Sending.Dir, "Shop/Get order"); err == nil {
		t.Error("send to a directory worked")
	}
	if err := cmd(t, e, "", "send", "--var", "nokey", "Shop/Get order"); err == nil || !strings.Contains(err.Error(), "KEY=VALUE") {
		t.Errorf("--var without a value: %v", err)
	}
	if err := cmd(t, e, "", "send", "-e", "Mars", "Shop/Get order"); err == nil || !strings.Contains(err.Error(), "Mars") {
		t.Errorf("an environment that isn't there: %v", err)
	}
	if err := cmd(t, e, "", "send", "Shop"); err == nil {
		t.Error("sending a collection worked")
	}
}

func TestSendVar(t *testing.T) {
	_, e, out, _ := sending(t)
	base := api(t)
	// The environment's base goes nowhere; --var wins over it.
	if err := cmd(t, e, "", "send", "-e", "Nowhere", "--var", "base="+base, "Shop/Get order"); err != nil {
		t.Fatalf("--var over the environment: %v\n%s", err, out)
	}
	if !strings.Contains(out.String(), "GET "+base+"/orders/1 · Nowhere") {
		t.Errorf("--var didn't fill the URL:\n%s", out)
	}
}

func TestSendText(t *testing.T) {
	_, e, out, _ := sending(t)
	text := "POST {{base}}/echo\nContent-Type: text/plain\n\nhello\n"
	if err := cmd(t, e, text, "send", "Shop/Get order", "-"); err != nil {
		t.Fatalf("send text: %v\n%s", err, out)
	}
	if !strings.Contains(out.String(), "POST http") || !strings.HasSuffix(out.String(), "\nhello\n") {
		t.Errorf("sending text instead of the saved request:\n%s", out)
	}
}

func TestSendBinary(t *testing.T) {
	_, e, out, _ := sending(t)
	if err := cmd(t, e, "", "send", "Shop/Get PDF"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "redirected: 307 → ") || !strings.Contains(out.String(), "(10 bytes of application/pdf; -o FILE writes it)") {
		t.Errorf("a PDF behind a redirect:\n%s", out)
	}
	file := filepath.Join(t.TempDir(), "out.pdf")
	if err := cmd(t, e, "", "send", "Shop/Get PDF", "-o", file); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(file); err != nil || !bytes.HasPrefix(data, []byte("%PDF")) {
		t.Errorf("-o: %v, %q", err, data)
	}
}

// readValues is everything kept in the values directory.
func readValues(t *testing.T, stateDir string) string {
	t.Helper()
	var b strings.Builder
	filepath.WalkDir(filepath.Join(stateDir, "values"), func(path string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			data, _ := os.ReadFile(path)
			b.Write(data)
		}
		return nil
	})
	return b.String()
}
