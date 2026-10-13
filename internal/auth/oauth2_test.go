package auth_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shinyvision/posthero/internal/auth"
	"github.com/shinyvision/posthero/internal/auth/idptest"
	"github.com/shinyvision/posthero/internal/postman"
	"github.com/shinyvision/posthero/internal/secret"
	"github.com/shinyvision/posthero/internal/send"
	"github.com/shinyvision/posthero/internal/vars"
)

// prompter answers like a person would, from a script.
type prompter struct {
	mu       sync.Mutex
	logins   []auth.LoginAnswer
	forms    []map[string]string
	asked    []auth.LoginPrompt
	formsAsk []auth.FormPrompt
	browser  func(q auth.BrowserPrompt) (string, error)
	statuses []string
}

func (p *prompter) Login(_ context.Context, q auth.LoginPrompt) (auth.LoginAnswer, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.asked = append(p.asked, q)
	if len(p.logins) == 0 {
		return auth.LoginAnswer{}, errors.New("cancelled")
	}
	a := p.logins[0]
	p.logins = p.logins[1:]
	return a, nil
}

func (p *prompter) Form(_ context.Context, q auth.FormPrompt) (auth.FormAnswer, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.formsAsk = append(p.formsAsk, q)
	if len(p.forms) == 0 {
		return auth.FormAnswer{}, errors.New("cancelled")
	}
	v := p.forms[0]
	p.forms = p.forms[1:]
	return auth.FormAnswer{Values: v}, nil
}

func (p *prompter) Browser(_ context.Context, q auth.BrowserPrompt) (string, error) {
	if p.browser == nil {
		return "", errors.New("no browser")
	}
	return p.browser(q)
}

func (p *prompter) Status(s string) {
	p.mu.Lock()
	p.statuses = append(p.statuses, s)
	p.mu.Unlock()
}

// setup starts a fake Keycloak with a user and a public client like the
// team's, and an API that wants its tokens.
func setup(t *testing.T) (*idptest.IdP, *httptest.Server, *auth.Manager, *time.Time) {
	t.Helper()
	secret.MockInit()
	idp := idptest.New()
	idp.Realm = "system"
	clock := time.Now()
	idp.Now = func() time.Time { return clock }
	idp.AddUser(idptest.User{Name: "rachel", Password: "hunter2"})
	idp.AddClient(idptest.Client{ID: "portal", Redirects: []string{"http://localhost:3000*"}})
	idp.AddClient(idptest.Client{ID: "backend", Secret: "s3cret", Redirects: []string{"http://localhost:3000*"}})
	idp.Start()
	t.Cleanup(idp.Close)
	api := httptest.NewServer(idp.Protect(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"me":"`+r.Header.Get("X-User")+`"}`)
	})))
	t.Cleanup(api.Close)
	m := auth.NewManager(t.TempDir())
	m.Now = func() time.Time { return clock }
	m.OpenBrowser = func(string) error { return nil }
	return idp, api, m, &clock
}

func oauth(idp *idptest.IdP, kv map[string]any) *postman.Auth {
	base := map[string]any{
		"grant_type": "authorization_code_with_pkce", "authUrl": idp.AuthURL() + "?login_hint={{DefaultUsername}}",
		"accessTokenUrl": idp.TokenURL(), "clientId": "portal", "redirect_uri": "{{AuthCallbackURL}}", "scope": "openid",
		"tokenName": "Shop Token", "addTokenTo": "header",
	}
	for k, v := range kv {
		base[k] = v
	}
	a := &postman.Auth{Type: "oauth2", Params: map[string][]postman.AuthParam{}}
	for k, v := range base {
		raw, _ := json.Marshal(v)
		a.Params["oauth2"] = append(a.Params["oauth2"], postman.AuthParam{Key: k, Value: raw, Type: "string"})
	}
	return a
}

var env = vars.New(&vars.Scope{Kind: vars.Environment, Vars: []vars.Var{{Key: "AuthCallbackURL", Value: "http://localhost:3000"}, {Key: "DefaultUsername", Value: ""}}})

// call sends a request to the API with the auth.
func call(t *testing.T, m *auth.Manager, api string, a *postman.Auth, p auth.Prompter) (*send.Response, *auth.Used) {
	t.Helper()
	req := &send.Request{Method: "GET", URL: &postman.URL{Raw: api + "/me"}, Settings: send.DefaultSettings()}
	prep, err := req.Prepare(env, send.Options{})
	if err != nil {
		t.Fatal(err)
	}
	resp, used, err := m.Do(context.Background(), send.NewClient(nil), prep, a, "collection", env.Resolve, p)
	if err != nil {
		t.Fatal(err)
	}
	return resp, used
}

func TestAutoLoginAndRefresh(t *testing.T) {
	idp, api, m, clock := setup(t)
	a := oauth(idp, nil)
	p := &prompter{logins: []auth.LoginAnswer{{Username: "rachel", Password: "hunter2", Remember: true}}}

	// The first time, posthero asks once and logs in through the form.
	resp, used := call(t, m, api.URL, a, p)
	if resp.Status != 200 || string(resp.Body) != `{"me":"rachel"}` {
		t.Fatalf("first call: %d %s", resp.Status, resp.Body)
	}
	if len(p.asked) != 1 || used.Fetch != auth.LoggedIn || used.Token.Subject != "rachel" || idp.Exchanges != 1 {
		t.Fatalf("first login: asked %d, fetch %v, subject %q, exchanges %d", len(p.asked), used.Fetch, used.Token.Subject, idp.Exchanges)
	}
	if !strings.Contains(p.asked[0].Issuer, "system") {
		t.Errorf("issuer: %q", p.asked[0].Issuer)
	}

	// Then the kept token is used.
	_, used = call(t, m, api.URL, a, p)
	if used.Fetch != auth.Cached || idp.Exchanges != 1 {
		t.Errorf("second call: %v", used.Fetch)
	}

	// After it runs out, it is refreshed without asking.
	*clock = clock.Add(6 * time.Minute)
	resp, used = call(t, m, api.URL, a, p)
	if resp.Status != 200 || used.Fetch != auth.Refreshed || idp.Refreshes != 1 || len(p.asked) != 1 {
		t.Errorf("refresh: %d %v refreshes %d", resp.Status, used.Fetch, idp.Refreshes)
	}

	// When the refresh token is gone too, the session cookie logs in again
	// without a form.
	*clock = clock.Add(31 * time.Minute)
	resp, used = call(t, m, api.URL, a, p)
	if resp.Status != 200 || used.Fetch != auth.LoggedIn || idp.SilentLogins != 1 || idp.FormPosts != 1 || len(p.asked) != 1 {
		t.Errorf("silent login: %d %v silent %d posts %d", resp.Status, used.Fetch, idp.SilentLogins, idp.FormPosts)
	}

	// When the session is gone as well, the saved password fills in the
	// form, still without asking.
	idp.EndSessions()
	*clock = clock.Add(time.Hour)
	resp, _ = call(t, m, api.URL, a, p)
	if resp.Status != 200 || idp.FormPosts != 2 || len(p.asked) != 1 {
		t.Errorf("form login with the saved password: %d posts %d asked %d", resp.Status, idp.FormPosts, len(p.asked))
	}

	// When the password was changed, posthero says why and asks again.
	idp.EndSessions()
	idp.SetPassword("rachel", "new-pass")
	*clock = clock.Add(time.Hour)
	p.logins = []auth.LoginAnswer{{Username: "rachel", Password: "new-pass", Remember: true}}
	resp, _ = call(t, m, api.URL, a, p)
	if resp.Status != 200 || len(p.asked) != 2 || p.asked[1].Error != "Invalid username or password." {
		t.Errorf("changed password: %d asked %+v", resp.Status, p.asked)
	}
	c := auth.ConfigFrom(auth.Resolve(a, env.Resolve))
	if _, pw, ok := m.Logins.Get(c.IdP(), c.TokenURL); !ok || pw != "new-pass" {
		t.Errorf("new password not saved: %q %v", pw, ok)
	}

	// A token the server stopped taking is replaced, and the request sent
	// again.
	idp.RevokeAccess()
	resp, _ = call(t, m, api.URL, a, p)
	if resp.Status != 200 {
		t.Errorf("after revocation: %d", resp.Status)
	}
}

func TestKeepFresh(t *testing.T) {
	idp, api, m, clock := setup(t)
	a := oauth(idp, nil)
	p := &prompter{logins: []auth.LoginAnswer{{Username: "rachel", Password: "hunter2", Remember: true}}}
	call(t, m, api.URL, a, p)
	c := auth.ConfigFrom(auth.Resolve(a, env.Resolve))
	configs := map[string]auth.Config{c.Key(): c}

	*clock = clock.Add(time.Minute)
	if got, _ := m.KeepFresh(context.Background(), configs); len(got) != 0 {
		t.Errorf("refreshed a fresh token")
	}
	// Every 4½ minutes for an hour: the token never runs out and the
	// session stays, though the refresh token alone lasts 30 minutes.
	for i := 0; i < 14; i++ {
		*clock = clock.Add(4*time.Minute + 30*time.Second)
		m.KeepFresh(context.Background(), configs)
	}
	if idp.Refreshes < 12 {
		t.Errorf("only %d refreshes", idp.Refreshes)
	}
	_, used := call(t, m, api.URL, a, p)
	if used.Fetch != auth.Cached {
		t.Errorf("after an hour of keeping fresh: %v", used.Fetch)
	}
	// Tokens not used for longer than KeepAlive are left to run out.
	m.KeepAlive = time.Minute
	before := idp.Refreshes
	*clock = clock.Add(10 * time.Minute)
	m.KeepFresh(context.Background(), configs)
	if idp.Refreshes != before {
		t.Error("kept an unused token fresh")
	}
}

func TestKeepFreshShortTokens(t *testing.T) {
	idp, api, m, clock := setup(t)
	idp.AccessTTL = time.Minute
	a := oauth(idp, nil)
	p := &prompter{logins: []auth.LoginAnswer{{Username: "rachel", Password: "hunter2", Remember: true}}}
	call(t, m, api.URL, a, p)
	c := auth.ConfigFrom(auth.Resolve(a, env.Resolve))
	configs := map[string]auth.Config{c.Key(): c}
	// Checked every 15 seconds for 5 minutes: refreshed in the second half
	// of each minute-long life, not on every check.
	for i := 0; i < 20; i++ {
		*clock = clock.Add(15 * time.Second)
		m.KeepFresh(context.Background(), configs)
	}
	if idp.Refreshes < 5 || idp.Refreshes > 12 {
		t.Errorf("%d refreshes in 5 minutes of one-minute tokens", idp.Refreshes)
	}
	if _, used := call(t, m, api.URL, a, p); used.Fetch != auth.Cached {
		t.Errorf("the token wasn't fresh: %v", used.Fetch)
	}
}

func TestOTPAndUsernameFirst(t *testing.T) {
	idp, api, m, _ := setup(t)
	idp.UsernameFirst = true
	idp.AddUser(idptest.User{Name: "ada", Password: "pw", OTP: "123456"})
	a := oauth(idp, map[string]any{"authUrl": idp.AuthURL() + "?login_hint=ada"})
	p := &prompter{logins: []auth.LoginAnswer{{Username: "ada", Password: "pw", Remember: true}}, forms: []map[string]string{{"otp": "123456"}}}
	resp, _ := call(t, m, api.URL, a, p)
	if resp.Status != 200 || string(resp.Body) != `{"me":"ada"}` {
		t.Fatalf("otp login: %d %s", resp.Status, resp.Body)
	}
	if len(p.asked) != 1 || p.asked[0].Username != "ada" {
		t.Errorf("login asked: %+v", p.asked)
	}
	if len(p.formsAsk) != 1 || len(p.formsAsk[0].Fields) != 1 || p.formsAsk[0].Fields[0].Label != "One-time code" || p.formsAsk[0].Title != "Sign in to system" {
		t.Errorf("form asked: %+v", p.formsAsk)
	}
}

func TestOtherGrants(t *testing.T) {
	idp, api, m, _ := setup(t)
	p := &prompter{}

	// Client credentials and a password in the settings need nobody.
	resp, used := call(t, m, api.URL, oauth(idp, map[string]any{"grant_type": "client_credentials", "clientId": "backend", "clientSecret": "s3cret"}), nil)
	if resp.Status != 200 || used.Fetch != auth.LoggedIn {
		t.Errorf("client credentials: %d", resp.Status)
	}
	resp, _ = call(t, m, api.URL, oauth(idp, map[string]any{"grant_type": "client_credentials", "clientId": "backend", "clientSecret": "s3cret", "client_authentication": "body"}), nil)
	if resp.Status != 200 {
		t.Errorf("client credentials in the body: %d", resp.Status)
	}
	resp, _ = call(t, m, api.URL, oauth(idp, map[string]any{"grant_type": "password_credentials", "username": "rachel", "password": "hunter2"}), nil)
	if resp.Status != 200 || idp.PasswordGrants != 1 {
		t.Errorf("password grant: %d", resp.Status)
	}
	// The implicit grant brings its token in the fragment.
	p.logins = []auth.LoginAnswer{{Username: "rachel", Password: "hunter2"}}
	resp, _ = call(t, m, api.URL, oauth(idp, map[string]any{"grant_type": "implicit"}), p)
	if resp.Status != 200 {
		t.Errorf("implicit: %d", resp.Status)
	}
	// A plain authorization code with a client secret.
	p.logins = []auth.LoginAnswer{{Username: "rachel", Password: "hunter2"}}
	resp, _ = call(t, m, api.URL, oauth(idp, map[string]any{"grant_type": "authorization_code", "clientId": "backend", "clientSecret": "s3cret"}), p)
	if resp.Status != 200 {
		t.Errorf("authorization code: %d", resp.Status)
	}
	// Without anybody to ask, a login that needs a password says so.
	m2 := auth.NewManager("")
	req := &send.Request{Method: "GET", URL: &postman.URL{Raw: api.URL + "/me"}, Settings: send.DefaultSettings()}
	prep, _ := req.Prepare(env, send.Options{})
	_, _, err := m2.Do(context.Background(), send.NewClient(nil), prep, oauth(idp, nil), "", env.Resolve, nil)
	if !auth.IsNoUser(err) {
		t.Errorf("no user: %v", err)
	}
}

// TestBrowserFallback logs in through "the browser" when the login page
// can't be filled in here: posthero listens on the redirect URI's port.
func TestBrowserFallback(t *testing.T) {
	idp, api, m, _ := setup(t)
	// A free port stands in for localhost:3000.
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	redirect := "http://localhost:" + strconv.Itoa(port) + "/callback"
	idp.AddClient(idptest.Client{ID: "spa", Redirects: []string{redirect}})
	// The login page is a JavaScript app: nothing to fill in here.
	a := oauth(idp, map[string]any{"clientId": "spa", "redirect_uri": redirect, "authUrl": idp.AuthURL() + "?kc_idp_hint=js"})
	js := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `<html><head><title>Sign in</title><script src="app.js"></script></head><body><div id="root"></div></body></html>`)
	}))
	defer js.Close()
	a = oauth(idp, map[string]any{"clientId": "spa", "redirect_uri": redirect, "authUrl": js.URL + "/auth"})
	// The "browser" logs in at the real IdP, which sends it to posthero.
	p := &prompter{browser: func(q auth.BrowserPrompt) (string, error) {
		if !q.Listening || !strings.Contains(q.Why, "JavaScript") {
			t.Errorf("browser prompt: %+v", q)
		}
		u, _ := url.Parse(q.URL)
		real, _ := url.Parse(idp.AuthURL())
		real.RawQuery = u.RawQuery
		jar := send.NewJar("")
		client := &http.Client{Jar: jar}
		resp, err := client.Get(real.String())
		if err != nil {
			return "", err
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		action := strings.Split(strings.Split(string(body), `action="`)[1], `"`)[0]
		action = strings.ReplaceAll(action, "&amp;", "&")
		resp, err = client.PostForm(action, url.Values{"username": {"rachel"}, "password": {"hunter2"}})
		if err != nil {
			return "", err
		}
		resp.Body.Close()
		select {
		case got := <-q.Done:
			return got, nil
		case <-time.After(5 * time.Second):
			return "", errors.New("posthero didn't catch the browser coming back")
		}
	}}
	resp, used := call(t, m, api.URL, a, p)
	if resp.Status != 200 || used.Fetch != auth.LoggedIn {
		t.Errorf("browser login: %d %v", resp.Status, used.Fetch)
	}
}

// TestPastedAddress logs in by pasting where the browser ended up, when
// the redirect URI's port is taken (the front end runs on it).
func TestPastedAddress(t *testing.T) {
	idp, api, m, _ := setup(t)
	busy, _ := net.Listen("tcp", "127.0.0.1:0")
	defer busy.Close()
	redirect := "http://localhost:" + strconv.Itoa(busy.Addr().(*net.TCPAddr).Port)
	idp.AddClient(idptest.Client{ID: "spa", Redirects: []string{redirect + "*"}})
	js := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `<html><script src="app.js"></script></html>`)
	}))
	defer js.Close()
	a := oauth(idp, map[string]any{"clientId": "spa", "redirect_uri": redirect, "authUrl": js.URL + "/auth"})
	p := &prompter{browser: func(q auth.BrowserPrompt) (string, error) {
		if q.Listening {
			t.Error("listening on a busy port")
		}
		u, _ := url.Parse(q.URL)
		real, _ := url.Parse(idp.AuthURL())
		real.RawQuery = u.RawQuery
		client := &http.Client{Jar: send.NewJar(""), CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
		resp, _ := client.Get(real.String())
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		action := strings.ReplaceAll(strings.Split(strings.Split(string(body), `action="`)[1], `"`)[0], "&amp;", "&")
		resp, _ = client.PostForm(action, url.Values{"username": {"rachel"}, "password": {"hunter2"}})
		resp.Body.Close()
		return resp.Header.Get("Location"), nil // what the address bar shows
	}}
	resp, _ := call(t, m, api.URL, a, p)
	if resp.Status != 200 {
		t.Errorf("pasted login: %d", resp.Status)
	}
}
