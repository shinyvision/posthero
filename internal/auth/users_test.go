package auth_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shinyvision/posthero/internal/auth"
	"github.com/shinyvision/posthero/internal/auth/idptest"
	"github.com/shinyvision/posthero/internal/secret"
)

// A login page whose form posts somewhere else never gets the saved
// password: posthero hands the login to the browser.
func TestPasswordStaysWithTheIdP(t *testing.T) {
	secret.MockInit()
	var mu sync.Mutex
	var stolen []string
	evil := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		stolen = append(stolen, string(body))
		mu.Unlock()
	}))
	defer evil.Close()
	idp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = io.WriteString(w, `<html><body><form method="post" action="`+evil.URL+`/login">
<input name="username"><input type="password" name="password"><button type="submit">Sign in</button></form></body></html>`)
	}))
	defer idp.Close()

	m := auth.NewManager("")
	m.OpenBrowser = func(string) error { return nil }
	c := auth.Config{Grant: auth.GrantPKCE, AuthURL: idp.URL + "/auth", TokenURL: idp.URL + "/token", ClientID: "portal", RedirectURI: "http://localhost:3000/cb", Challenge: "S256"}
	if _, err := m.Logins.Save(c.IdP(), c.TokenURL, "rachel", "hunter2"); err != nil {
		t.Fatal(err)
	}
	var why string
	p := &prompter{browser: func(q auth.BrowserPrompt) (string, error) {
		why = q.Why
		return "", context.Canceled
	}}
	if _, _, err := m.Token(context.Background(), c, p, ""); err == nil {
		t.Fatal("logged in through a foreign form")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(stolen) != 0 {
		t.Fatalf("the other server got %q", stolen)
	}
	if !strings.Contains(why, strings.TrimPrefix(evil.URL, "http://")) {
		t.Errorf("the browser prompt doesn't say where the login goes: %q", why)
	}
}

// Configs that log in as different users (a login_hint per environment)
// get tokens, saved logins and sessions of their own.
func TestUsersByLoginHint(t *testing.T) {
	secret.MockInit()
	idp := idptest.New()
	idp.Realm = "system"
	idp.AddUser(idptest.User{Name: "alice", Password: "pw-a"})
	idp.AddUser(idptest.User{Name: "bob", Password: "pw-b"})
	idp.AddClient(idptest.Client{ID: "portal", Redirects: []string{"http://localhost:3000*"}})
	idp.Start()
	defer idp.Close()
	m := auth.NewManager("")
	config := func(user string) auth.Config {
		return auth.Config{Grant: auth.GrantPKCE, AuthURL: idp.AuthURL() + "?login_hint=" + user, TokenURL: idp.TokenURL(), ClientID: "portal", RedirectURI: "http://localhost:3000/cb", Challenge: "S256", Scope: "openid"}
	}
	a, b := config("alice"), config("bob")
	if a.Key() == b.Key() {
		t.Fatal("alice and bob share a token")
	}
	p := &prompter{logins: []auth.LoginAnswer{{Username: "alice", Password: "pw-a", Remember: true}, {Username: "bob", Password: "pw-b", Remember: true}}}
	ta, _, err := m.Token(context.Background(), a, p, "")
	if err != nil {
		t.Fatal(err)
	}
	tb, _, err := m.Token(context.Background(), b, p, "")
	if err != nil {
		t.Fatal(err)
	}
	if ta.Subject != "alice" || tb.Subject != "bob" {
		t.Fatalf("tokens for %q and %q", ta.Subject, tb.Subject)
	}
	// Both passwords are kept, and bob logged in last.
	if pw, ok := m.Logins.Password(a.IdP(), a.TokenURL, "alice"); !ok || pw != "pw-a" {
		t.Errorf("alice's password: %q %v", pw, ok)
	}
	if u, pw, ok := m.Logins.Get(a.IdP(), a.TokenURL); !ok || u != "bob" || pw != "pw-b" {
		t.Errorf("last login: %q %q %v", u, pw, ok)
	}
	// The sessions end and the tokens are refused: each logs in again as
	// itself, with its saved password, asking nobody.
	idp.EndSessions()
	idp.RevokeAccess()
	posts := idp.FormPosts
	quiet := &prompter{}
	for _, c := range []auth.Config{a, b} {
		cur := m.Tokens.Get(c.Key())
		if _, _, err := m.Token(context.Background(), c, quiet, auth.Fresh); err != nil {
			t.Fatalf("again as %s: %v", cur.Subject, err)
		}
		if got := m.Tokens.Get(c.Key()); got.Subject != cur.Subject {
			t.Errorf("logged in again as %q, not %q", got.Subject, cur.Subject)
		}
	}
	if len(quiet.asked) != 0 || idp.FormPosts != posts+2 {
		t.Errorf("asked %d times, %d form posts", len(quiet.asked), idp.FormPosts-posts)
	}
}

// A logins file from before logins were kept per user still works.
func TestOldLoginsFile(t *testing.T) {
	secret.MockInit()
	dir := t.TempDir()
	idp := "https://kc.example.com/realms/x/protocol/openid-connect/auth"
	old := `{"` + idp + `":{"username":"control@example.invalid","password":"ctrl-pass","Saved":"2026-10-12T18:52:00Z"}}`
	if err := writeFile(dir+"/logins.json", old); err != nil {
		t.Fatal(err)
	}
	l := auth.NewLogins(dir + "/logins.json")
	token := "https://kc.example.com/realms/x/protocol/openid-connect/token"
	if u, pw, ok := l.Get(idp, token); !ok || u != "control@example.invalid" || pw != "ctrl-pass" {
		t.Fatalf("old login: %q %q %v", u, pw, ok)
	}
	if pw, ok := l.Password(idp, token, "Control@Example.invalid"); !ok || pw != "ctrl-pass" {
		t.Fatalf("by user: %q %v", pw, ok)
	}
	// It kept no token URL: it is filled in for the identity provider's own.
	if _, ok := l.Password(idp, "https://elsewhere.example.com/token", "control@example.invalid"); ok {
		t.Fatal("filled in for another server")
	}
}

func writeFile(path, text string) error { return os.WriteFile(path, []byte(text), 0o600) }

// Two posthero instances share the token file: neither undoes the other's
// refresh (a refresh token can often be used only once).
func TestTwoInstancesShareTokens(t *testing.T) {
	path := t.TempDir() + "/tokens.json"
	now := time.Now()
	one := auth.NewStore(path)
	one.Put(&auth.Token{Key: "a", AccessToken: "a1", RefreshToken: "r1", IssuedAt: now})
	two := auth.NewStore(path)
	if got := two.Get("a"); got == nil || got.RefreshToken != "r1" {
		t.Fatalf("the second instance has %+v", got)
	}
	// The first refreshes: the refresh token rotates.
	one.Put(&auth.Token{Key: "a", AccessToken: "a2", RefreshToken: "r2", IssuedAt: now.Add(time.Minute)})
	// The second saves another token: the rotated one stays in the file.
	two.Put(&auth.Token{Key: "b", AccessToken: "b1", IssuedAt: now})
	three := auth.NewStore(path)
	if got := three.Get("a"); got == nil || got.RefreshToken != "r2" {
		t.Fatalf("the file has %+v", got)
	}
	if three.Get("b") == nil {
		t.Fatal("b is lost")
	}
	// And before refreshing, the second takes the newer token.
	two.Sync("a")
	if got := two.Get("a"); got.RefreshToken != "r2" {
		t.Fatalf("after sync: %+v", got)
	}
	// Forgetting a token here takes it out of the file too.
	two.Delete("b")
	if auth.NewStore(path).Get("b") != nil {
		t.Fatal("b came back")
	}
}

// Logging out in one instance lets go of the token in the others too.
func TestLogoutReachesOtherInstances(t *testing.T) {
	path := t.TempDir() + "/tokens.json"
	one := auth.NewStore(path)
	one.Put(&auth.Token{Key: "a", AccessToken: "a1", IssuedAt: time.Now().Add(-time.Minute)})
	two := auth.NewStore(path)
	two.Delete("a")
	// The first saves something else: a stays gone.
	one.Put(&auth.Token{Key: "b", AccessToken: "b1"})
	if auth.NewStore(path).Get("a") != nil {
		t.Fatal("a came back in the file")
	}
	if one.Get("a") != nil {
		t.Fatal("the first instance still has a")
	}
	two.Sync("a")
	if two.Get("a") != nil {
		t.Fatal("a came back in the second instance")
	}
	// Logging in again afterwards works as ever.
	two.Put(&auth.Token{Key: "a", AccessToken: "a2"})
	one.Sync("a")
	if got := one.Get("a"); got == nil || got.AccessToken != "a2" {
		t.Fatalf("after logging in again: %+v", got)
	}
}

// After logging in again as someone else, the token's later logins are
// that someone's too.
func TestForceLoginSticks(t *testing.T) {
	secret.MockInit()
	idp := idptest.New()
	idp.Realm = "system"
	idp.AddUser(idptest.User{Name: "alice", Password: "pw-a"})
	idp.AddUser(idptest.User{Name: "bob", Password: "pw-b"})
	idp.AddClient(idptest.Client{ID: "portal", Redirects: []string{"http://localhost:3000*"}})
	idp.Start()
	defer idp.Close()
	m := auth.NewManager("")
	c := auth.Config{Grant: auth.GrantPKCE, AuthURL: idp.AuthURL(), TokenURL: idp.TokenURL(), ClientID: "portal", RedirectURI: "http://localhost:3000/cb", Challenge: "S256", Scope: "openid"}
	p := &prompter{logins: []auth.LoginAnswer{{Username: "alice", Password: "pw-a", Remember: true}, {Username: "bob", Password: "pw-b", Remember: true}}}
	if _, _, err := m.Token(context.Background(), c, p, ""); err != nil {
		t.Fatal(err)
	}
	tb, err := m.ForceLogin(context.Background(), c, p, "bob")
	if err != nil || tb.Subject != "bob" {
		t.Fatalf("as bob: %+v %v", tb, err)
	}
	// The token runs out and can't be refreshed: the session logs in again,
	// as bob.
	tok := m.Tokens.Get(c.Key())
	tok.ExpiresAt, tok.RefreshToken = time.Now().Add(-time.Minute), ""
	m.Tokens.Put(tok)
	got, _, err := m.Token(context.Background(), c, &prompter{}, "")
	if err != nil || got.Subject != "bob" {
		t.Fatalf("again: %+v %v", got, err)
	}
}

// NoLogin uses a token that is there, and never logs in for one.
func TestNoLogin(t *testing.T) {
	idp, _, m, _ := setup(t)
	c := auth.Config{Grant: auth.GrantPKCE, AuthURL: idp.AuthURL(), TokenURL: idp.TokenURL(), ClientID: "portal", RedirectURI: "http://localhost:3000/cb", Challenge: "S256", Scope: "openid"}
	if _, err := m.Logins.Save(c.IdP(), c.TokenURL, "rachel", "hunter2"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := m.Token(context.Background(), c, auth.NoLogin, ""); !errors.Is(err, auth.ErrNotLoggedIn) {
		t.Fatalf("without a token: %v", err)
	}
	if idp.FormPosts != 0 || idp.SilentLogins != 0 {
		t.Fatalf("it logged in: %d posts, %d silent", idp.FormPosts, idp.SilentLogins)
	}
	// With one, it is used.
	if _, _, err := m.Token(context.Background(), c, &prompter{}, ""); err != nil {
		t.Fatal(err)
	}
	if _, how, err := m.Token(context.Background(), c, auth.NoLogin, ""); err != nil || how != auth.Cached {
		t.Fatalf("with a token: %v %v", how, err)
	}
}
