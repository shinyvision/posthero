package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/shinyvision/posthero/internal/auth"
	"github.com/shinyvision/posthero/internal/config"
	"github.com/shinyvision/posthero/internal/postman"
	"github.com/shinyvision/posthero/internal/reqdoc"
	"github.com/shinyvision/posthero/internal/run"
	"github.com/shinyvision/posthero/internal/script"
	"github.com/shinyvision/posthero/internal/send"
	"github.com/shinyvision/posthero/internal/store"
	"github.com/shinyvision/posthero/internal/vars"
)

// Sending is what send needs besides the API: where posthero keeps its
// state and cache, the options (:set) and the environment picked per
// collection (collection id → environment id), as the screen has them.
type Sending struct {
	StateDir, CacheDir string
	Settings           config.Settings
	Certs              []send.ClientCert
	Envs               map[string]string
	// Dir is where relative file paths in requests start.
	Dir string
}

func sendRequest(ctx context.Context, e *Env, args []string) error {
	var envName, output string
	local := &vars.Scope{Kind: vars.Local, Name: "--var"}
	args, err := e.flags("send", args, func(fs *flag.FlagSet) {
		fs.StringVar(&envName, "e", "", "")
		fs.StringVar(&envName, "environment", "", "")
		fs.StringVar(&output, "o", "", "")
		fs.Func("var", "", func(kv string) error {
			k, v, ok := strings.Cut(kv, "=")
			if !ok || k == "" {
				return fmt.Errorf("--var %q: give KEY=VALUE", kv)
			}
			local.Set(k, v)
			return nil
		})
	})
	if err != nil {
		return err
	}
	if len(args) < 1 || len(args) > 2 {
		return errors.New("send: give the request's path, and a file (or -) to send that text instead")
	}
	if e.Sending == nil {
		return errors.New("send: not available here")
	}
	s := e.Sending
	ws, err := e.workspace(ctx)
	if err != nil {
		return err
	}
	// The workspace as the screen has it: from the cache, brought up to
	// date, with the values of variables kept on this machine.
	cache := ""
	if s.CacheDir != "" {
		cache = filepath.Join(s.CacheDir, "workspaces")
	}
	st := store.Open(e.API, ws, cache, stateFile(s.StateDir, "values"))
	fetched, err := store.Fetch(ctx, st.Plan(false))
	if err != nil {
		return err
	}
	st.Apply(fetched)
	var sums []postman.Summary
	for _, c := range st.Collections() {
		sums = append(sums, c.Summary)
	}
	f, err := follow(args[0], ws.Name, sums, func(sum postman.Summary) (*postman.Collection, error) {
		if c := st.Col(sum.ID); c != nil && c.Data != nil {
			return c.Data, nil
		}
		return nil, fmt.Errorf("couldn't load %s", sum.Name)
	})
	if err != nil {
		return err
	}
	if f.item == nil || f.item.Folder {
		return fmt.Errorf("send: %s is not a request", args[0])
	}
	it := f.item
	if len(args) == 2 {
		text, err := e.read(args[1:])
		if err != nil {
			return err
		}
		if it, err = reqdoc.Apply(f.item, text); err != nil {
			return err
		}
	}
	envID, err := pickEnv(st, s.Envs, f.sum.ID, envName)
	if err != nil {
		return err
	}

	r, jar := s.runner()
	defer jar.Save()
	r.Packages, _ = e.API.(postman.PackageProvider)
	// --var values come first, as local variables do in Postman.
	set := st.Scopes(f.sum.ID, envID, local, nil)
	before := set.Clone()
	ex := r.Execute(ctx, run.Snapshot(f.col, it, f.parents), set, run.Options{Prompt: prompter{}})
	if err := st.CommitChanges(before, set, f.sum.ID, envID); err != nil {
		fmt.Fprintln(e.errOut(), "posthero: couldn't keep the variables scripts set:", err)
	}
	envLabel := "no environment"
	if env := st.Env(envID); env != nil {
		envLabel = env.Name()
	}
	return report(e.Out, ex, envLabel, output)
}

// pickEnv is the environment to send with: the one named, else the one
// picked for the collection in posthero, else none.
func pickEnv(st *store.Store, picked map[string]string, colID, name string) (string, error) {
	if name == "" {
		return picked[colID], nil
	}
	var hits []*store.Env
	for _, env := range st.Environments() {
		if env.Summary.ID == name || env.Summary.UID == name {
			return env.Summary.ID, nil
		}
		if strings.EqualFold(env.Name(), name) {
			hits = append(hits, env)
		}
	}
	switch len(hits) {
	case 1:
		return hits[0].Summary.ID, nil
	case 0:
		return "", fmt.Errorf("no environment %q", name)
	}
	return "", fmt.Errorf("%d environments are called %q; give the id", len(hits), name)
}

// runner sends the way the screen does: its logins, cookies and options.
func (s *Sending) runner() (*run.Runner, *send.Jar) {
	jar := send.NewJar(stateFile(s.StateDir, "cookies.json"))
	client := send.NewClient(jar)
	settings := s.Settings.Send()
	settings.Certs = s.Certs
	m := auth.NewManager(stateFile(s.StateDir, "auth"))
	m.Transport = func() http.RoundTripper {
		t, err := client.Transport(settings)
		if err != nil {
			return nil
		}
		return t
	}
	m.Configure(time.Duration(s.Settings.KeepAlive)*time.Hour, !s.Settings.Verify)
	return &run.Runner{Engine: script.NewEngine(), Client: client, Auth: m, Settings: settings, Dir: s.Dir}, jar
}

func stateFile(dir, name string) string {
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, name)
}

// report prints how a send went: what was sent and what came back, the
// tests and the console, then the body (to output instead, when given).
// Failing tests, or no response, make it an error.
func report(w io.Writer, ex *run.Execution, env, output string) error {
	if p := ex.Prepared; p != nil {
		fmt.Fprintf(w, "%s %s · %s\n", p.Method, p.URL, env)
	}
	if a := ex.Auth; a != nil && a.Type != "" {
		line := "auth: " + a.Type
		if a.From != "" {
			line += " from " + a.From
		}
		if a.Token != nil {
			if a.Token.Name != "" {
				line += " as " + a.Token.Name
			}
			line += " (" + [...]string{"kept token", "refreshed", "logged in", "token from the settings"}[a.Fetch] + ")"
		}
		fmt.Fprintln(w, line)
	}
	if len(ex.Missing) > 0 {
		fmt.Fprintf(w, "not set: %s\n", strings.Join(ex.Missing, ", "))
	}
	failed := 0
	for _, t := range ex.Tests() {
		switch {
		case t.Skipped:
			fmt.Fprintf(w, "- %s (skipped)\n", t.Name)
		case t.Passed:
			fmt.Fprintf(w, "✓ %s\n", t.Name)
		default:
			failed++
			fmt.Fprintf(w, "✗ %s: %s\n", t.Name, t.Error)
		}
	}
	for _, l := range ex.Scripts.Logs {
		fmt.Fprintf(w, "console.%s: %s\n", l.Level, l.Text)
	}
	if ex.Err != nil {
		return ex.Err
	}
	if ex.Skipped {
		fmt.Fprintln(w, "skipped by a pre-request script")
		return nil
	}
	res := ex.Response
	fmt.Fprintf(w, "%d %s · %d ms · %d B\n", res.Status, res.StatusText, res.Time.Milliseconds(), len(res.Body))
	for _, rd := range res.Redirects {
		fmt.Fprintf(w, "redirected: %d → %s\n", rd.Status, rd.To)
	}
	if output != "" {
		if err := os.WriteFile(output, res.Body, 0o644); err != nil {
			return err
		}
		fmt.Fprintf(w, "body written to %s\n", output)
	} else if len(res.Body) > 0 {
		fmt.Fprintln(w)
		w.Write(showBody(res))
	}
	if failed > 0 {
		return fmt.Errorf("%d of %d tests failed", failed, len(ex.Tests()))
	}
	return nil
}

// showBody is a body to print: JSON indented, text as it is, anything else
// (a PDF, an image) described instead (-o writes it to a file).
func showBody(res *send.Response) []byte {
	ctype := ""
	for _, h := range res.Header {
		if strings.EqualFold(h.Name, "Content-Type") {
			ctype = strings.ToLower(h.Value)
		}
	}
	var out bytes.Buffer
	if json.Valid(res.Body) {
		if json.Indent(&out, res.Body, "", "  ") == nil {
			out.WriteByte('\n')
			return out.Bytes()
		}
	}
	textual := ctype == "" || strings.HasPrefix(ctype, "text/") || strings.Contains(ctype, "json") || strings.Contains(ctype, "xml") || strings.Contains(ctype, "javascript")
	if !textual || bytes.IndexByte(res.Body, 0) >= 0 {
		what := ctype
		if what == "" {
			what = "binary"
		}
		return fmt.Appendf(nil, "(%d bytes of %s; -o FILE writes it)\n", len(res.Body), what)
	}
	body := res.Body
	if !bytes.HasSuffix(body, []byte("\n")) {
		body = append(body, '\n')
	}
	return body
}

// prompter answers a login that needs someone at the keyboard: it can't
// here. Saved passwords and tokens are used without asking.
type prompter struct{}

var errLogin = errors.New("the login needs you: send this request once in posthero, then try again")

func (prompter) Login(context.Context, auth.LoginPrompt) (auth.LoginAnswer, error) {
	return auth.LoginAnswer{}, errLogin
}
func (prompter) Form(context.Context, auth.FormPrompt) (auth.FormAnswer, error) {
	return auth.FormAnswer{}, errLogin
}
func (prompter) Browser(context.Context, auth.BrowserPrompt) (string, error) { return "", errLogin }
func (prompter) Status(string)                                               {}
