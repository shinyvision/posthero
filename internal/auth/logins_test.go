package auth_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shinyvision/posthero/internal/auth"
	"github.com/shinyvision/posthero/internal/auth/idptest"
	"github.com/shinyvision/posthero/internal/secret"
)

// tokenThief is a server a config sends its token requests to, which keeps
// what it gets.
type tokenThief struct {
	*httptest.Server
	mu   sync.Mutex
	got  []url.Values
	host string
}

func newTokenThief(t *testing.T) *tokenThief {
	s := &tokenThief{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		s.mu.Lock()
		s.got = append(s.got, r.PostForm)
		s.mu.Unlock()
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(s.Close)
	s.host = strings.TrimPrefix(s.URL, "http://")
	return s
}

// stole returns what the server got that it shouldn't have.
func (s *tokenThief) stole() []url.Values {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []url.Values
	for _, f := range s.got {
		if f.Get("code") != "" || f.Get("password") != "" || f.Get("refresh_token") != "" {
			out = append(out, f)
		}
	}
	return out
}

// A saved password logs in by itself only where the login sends what it
// gets (the code, or the password itself) to the server it was saved for:
// a config with the identity provider's Auth URL and a token URL elsewhere
// is asked about, and told why.
func TestSavedPasswordOnlyForItsTokenURL(t *testing.T) {
	idp, _, m, _ := setup(t)
	thief := newTokenThief(t)
	ctx := context.Background()
	real := auth.Config{Grant: auth.GrantPKCE, AuthURL: idp.AuthURL() + "?login_hint=rachel", TokenURL: idp.TokenURL(), ClientID: "portal", RedirectURI: "http://localhost:3000/cb", Challenge: "S256"}
	if _, _, err := m.Token(ctx, real, &prompter{logins: []auth.LoginAnswer{{Username: "rachel", Password: "hunter2", Remember: true}}}, ""); err != nil {
		t.Fatal(err)
	}

	p := &prompter{}
	bad := real
	bad.TokenURL, bad.RefreshURL = thief.URL+"/token", thief.URL+"/token"
	if _, _, err := m.Token(ctx, bad, p, ""); err == nil {
		t.Error("logged in through another token URL")
	}
	// The password grant would send the password itself.
	grant := auth.Config{Grant: auth.GrantPassword, AuthURL: real.AuthURL, TokenURL: thief.URL + "/token", ClientID: "portal", Username: "rachel"}
	if _, _, err := m.Token(ctx, grant, p, ""); err == nil {
		t.Error("logged in through another token URL with the password grant")
	}
	if got := thief.stole(); len(got) != 0 {
		t.Fatalf("%s got %v", thief.URL, got)
	}
	if len(p.asked) != 2 {
		t.Fatalf("asked %d times", len(p.asked))
	}
	for _, q := range p.asked {
		if !strings.Contains(q.Error, thief.host) {
			t.Errorf("the prompt doesn't say where the token comes from: %q", q.Error)
		}
	}

	// The config it was saved with still logs in by itself.
	m.Forget(real, false)
	quiet := &prompter{}
	if _, how, err := m.Token(ctx, real, quiet, ""); err != nil || how != auth.LoggedIn || len(quiet.asked) != 0 {
		t.Errorf("again: %v %v, asked %d", how, err, len(quiet.asked))
	}
}

// Passwords saved before posthero kept where their logins' tokens come from
// still log in by themselves with the identity provider's own token URL,
// and nowhere else.
func TestPasswordSavedBefore(t *testing.T) {
	idp, _, _, _ := setup(t)
	thief := newTokenThief(t)
	dir := t.TempDir()
	base := idp.AuthURL()
	at := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	old, _ := json.Marshal(map[string]any{
		base + "\x00rachel": map[string]any{"username": "rachel", "password": "hunter2", "Saved": at},
		base:                map[string]any{"username": "rachel", "Saved": at},
	})
	if err := writeFile(dir+"/logins.json", string(old)); err != nil {
		t.Fatal(err)
	}
	m := auth.NewManager(dir)
	m.OpenBrowser = func(string) error { return nil }
	ctx := context.Background()
	real := auth.Config{Grant: auth.GrantPKCE, AuthURL: base + "?login_hint=rachel", TokenURL: idp.TokenURL(), ClientID: "portal", RedirectURI: "http://localhost:3000/cb", Challenge: "S256"}
	quiet := &prompter{}
	if _, _, err := m.Token(ctx, real, quiet, ""); err != nil || len(quiet.asked) != 0 {
		t.Errorf("with the saved password: %v, asked %d", err, len(quiet.asked))
	}
	bad := real
	bad.TokenURL = thief.URL + "/token"
	p := &prompter{}
	if _, _, err := m.Token(ctx, bad, p, ""); err == nil || len(p.asked) != 1 {
		t.Errorf("another token URL: %v, asked %d", err, len(p.asked))
	}
	if got := thief.stole(); len(got) != 0 {
		t.Fatalf("%s got %v", thief.URL, got)
	}
}

// Two posthero instances share the logins file: neither drops a login the
// other saved, and one that either forgot stays forgotten, whether the
// password is in the keyring or (without one) in the file.
func TestTwoInstancesShareLogins(t *testing.T) {
	for _, keyring := range []bool{true, false} {
		name := "keyring"
		if !keyring {
			name = "file"
		}
		t.Run(name, func(t *testing.T) {
			idp, _, _, _ := setup(t)
			if !keyring {
				secret.MockUnavailable(errors.New("no keyring here"))
				t.Cleanup(secret.MockInit)
			}
			idp.AddUser(idptest.User{Name: "bob", Password: "pw-b"})
			dir := t.TempDir()
			instance := func() *auth.Manager {
				m := auth.NewManager(dir)
				m.OpenBrowser = func(string) error { return nil }
				return m
			}
			as := func(user string) auth.Config {
				return auth.Config{Grant: auth.GrantPKCE, AuthURL: idp.AuthURL() + "?login_hint=" + user, TokenURL: idp.TokenURL(), ClientID: "portal", RedirectURI: "http://localhost:3000/cb", Challenge: "S256"}
			}
			ctx := context.Background()
			one, two := instance(), instance()
			if _, _, err := one.Token(ctx, as("rachel"), &prompter{logins: []auth.LoginAnswer{{Username: "rachel", Password: "hunter2", Remember: true}}}, ""); err != nil {
				t.Fatal(err)
			}
			if _, _, err := two.Token(ctx, as("bob"), &prompter{logins: []auth.LoginAnswer{{Username: "bob", Password: "pw-b", Remember: true}}}, ""); err != nil {
				t.Fatal(err)
			}
			// Without their tokens and sessions, a third instance logs in as
			// either, asking nobody.
			if err := os.Remove(dir + "/tokens.json"); err != nil {
				t.Fatal(err)
			}
			idp.EndSessions()
			three := instance()
			for _, user := range []string{"rachel", "bob"} {
				quiet := &prompter{}
				if _, _, err := three.Token(ctx, as(user), quiet, ""); err != nil || len(quiet.asked) != 0 {
					t.Errorf("as %s: %v, asked %d", user, err, len(quiet.asked))
				}
			}
			// The third logs out and forgets rachel's password; a fourth, which
			// knew it, then saves bob's again.
			four := instance()
			three.Forget(as("rachel"), true)
			if _, err := four.ForceLogin(ctx, as("bob"), &prompter{logins: []auth.LoginAnswer{{Username: "bob", Password: "pw-b", Remember: true}}}, ""); err != nil {
				t.Fatal(err)
			}
			idp.EndSessions()
			p := &prompter{}
			if _, _, err := instance().Token(ctx, as("rachel"), p, ""); err == nil || len(p.asked) != 1 {
				t.Errorf("rachel's forgotten password came back: %v, asked %d", err, len(p.asked))
			}
		})
	}
}

// Two posthero instances share the identity provider's session: one logs
// in through the session the other got, and doesn't use one the other
// ended.
func TestTwoInstancesShareSessions(t *testing.T) {
	idp, _, _, _ := setup(t)
	dir := t.TempDir()
	one, two := auth.NewManager(dir), auth.NewManager(dir)
	one.OpenBrowser = func(string) error { return nil }
	two.OpenBrowser = one.OpenBrowser
	c := auth.Config{Grant: auth.GrantPKCE, AuthURL: idp.AuthURL() + "?login_hint=rachel", TokenURL: idp.TokenURL(), ClientID: "portal", RedirectURI: "http://localhost:3000/cb", Challenge: "S256"}
	ctx := context.Background()
	// The second looks first, while there is no session: it can't log in.
	if _, _, err := two.Token(ctx, c, &prompter{}, ""); err == nil {
		t.Fatal("logged in without a password")
	}
	if _, _, err := one.Token(ctx, c, &prompter{logins: []auth.LoginAnswer{{Username: "rachel", Password: "hunter2"}}}, ""); err != nil {
		t.Fatal(err)
	}
	// (Not through the token the first keeps.)
	two.Tokens.Delete(c.Key())
	q := &prompter{}
	if _, _, err := two.Token(ctx, c, q, ""); err != nil || len(q.asked) != 0 || idp.SilentLogins != 1 {
		t.Errorf("through the first's session: %v, asked %d, %d silent logins", err, len(q.asked), idp.SilentLogins)
	}
	// The first ends the session here: the second asks again.
	one.EndSession(c)
	two.Tokens.Delete(c.Key())
	q = &prompter{}
	if _, _, err := two.Token(ctx, c, q, ""); err == nil || len(q.asked) != 1 || idp.SilentLogins != 1 {
		t.Errorf("after the session ended: %v, asked %d, %d silent logins", err, len(q.asked), idp.SilentLogins)
	}
}

// Logging out after logging in as someone else forgets that someone: their
// password, when asked to, and every copy of their token.
func TestLogoutForgetsWhoLoggedIn(t *testing.T) {
	idp, _, _, _ := setup(t)
	idp.AddUser(idptest.User{Name: "alice", Password: "pw-a"})
	idp.AddUser(idptest.User{Name: "bob", Password: "pw-b"})
	m, dir := newManager(t)
	ctx := context.Background()
	as := func(user string) auth.Config {
		return auth.Config{Grant: auth.GrantPKCE, AuthURL: idp.AuthURL() + "?login_hint=" + user, TokenURL: idp.TokenURL(), ClientID: "portal", RedirectURI: "http://localhost:3000/cb", Challenge: "S256"}
	}
	c := as("alice")
	if _, _, err := m.Token(ctx, c, &prompter{logins: []auth.LoginAnswer{{Username: "alice", Password: "pw-a", Remember: true}}}, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := m.ForceLogin(ctx, c, &prompter{logins: []auth.LoginAnswer{{Username: "bob", Password: "pw-b", Remember: true}}}, "bob"); err != nil {
		t.Fatal(err)
	}
	m.Forget(c, true)
	q := &prompter{}
	if _, _, err := m.Token(ctx, as("bob"), q, ""); err == nil || len(q.asked) != 1 {
		t.Errorf("bob's password is still saved: %v, asked %d", err, len(q.asked))
	}
	q = &prompter{}
	if _, _, err := m.Token(ctx, c, q, ""); err != nil || len(q.asked) != 0 {
		t.Errorf("alice's password went too: %v, asked %d", err, len(q.asked))
	}

	// The password grant, logged in as bob, leaves no token of his behind.
	pc := auth.Config{Grant: auth.GrantPassword, TokenURL: idp.TokenURL(), ClientID: "portal", Username: "alice"}
	if _, err := m.ForceLogin(ctx, pc, &prompter{logins: []auth.LoginAnswer{{Username: "bob", Password: "pw-b"}}}, "bob"); err != nil {
		t.Fatal(err)
	}
	m.Forget(pc, false)
	for _, s := range []*auth.Store{m.Tokens, auth.NewStore(dir + "/tokens.json")} {
		for _, tok := range s.All() {
			if tok.Login == "bob" {
				t.Errorf("bob's token is left under %s", tok.Key)
			}
		}
	}

	// Nor does it leave the copy posthero used to keep under bob's own key.
	asBob := pc
	asBob.Username = "bob"
	at := time.Now()
	for _, key := range []string{pc.Key(), asBob.Key()} {
		m.Tokens.Put(&auth.Token{Key: key, AccessToken: "x", RefreshToken: "r", Login: "bob", Grant: auth.GrantPassword, TokenURL: pc.TokenURL, ObtainedAt: at, IssuedAt: at})
	}
	m.Forget(pc, false)
	if m.Tokens.Get(asBob.Key()) != nil {
		t.Error("the copy under bob's key is left")
	}
}
