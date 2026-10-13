package auth_test

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/shinyvision/posthero/internal/auth"
	"github.com/shinyvision/posthero/internal/auth/idptest"
)

// stepIdP is a login page with what Keycloak may ask after the password:
// a new password, or a word that the account is disabled.
type stepIdP struct {
	mu  sync.Mutex
	srv *httptest.Server
	// password is rachel's; next is what follows it: "" logs in, "update"
	// asks for a new password, "disabled" says the account is disabled.
	password, next string
}

func newStepIdP(t *testing.T, password string) *stepIdP {
	s := &stepIdP{password: password}
	s.srv = httptest.NewServer(http.HandlerFunc(s.serve))
	t.Cleanup(s.srv.Close)
	return s
}

func (s *stepIdP) set(password, next string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.password, s.next = password, next
}

func (s *stepIdP) config() auth.Config {
	return auth.Config{Grant: auth.GrantCode, AuthURL: s.srv.URL + "/auth?login_hint=rachel", TokenURL: s.srv.URL + "/token", ClientID: "portal", RedirectURI: "http://localhost:3000/cb"}
}

func (s *stepIdP) serve(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_ = r.ParseForm()
	state := url.QueryEscape(r.Form.Get("state"))
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	login := func(msg string) {
		fmt.Fprintf(w, `<html><head><title>Sign in</title></head><body>%s
<form method="post" action="/login?state=%s"><input name="username" value="rachel"><input type="password" name="password">
<input type="submit" name="login" value="Sign In"></form></body></html>`, msg, state)
	}
	update := func(msg string) {
		fmt.Fprintf(w, `<html><head><title>Update password</title></head><body>%s
<form method="post" action="/update?state=%s"><label for="pn">New Password</label><input id="pn" type="password" name="password-new">
<label for="pc">Confirm password</label><input id="pc" type="password" name="password-confirm"><input type="submit" value="Submit"></form></body></html>`, msg, state)
	}
	back := func() {
		http.Redirect(w, r, "http://localhost:3000/cb?code=c0de&state="+state, http.StatusFound)
	}
	switch r.URL.Path {
	case "/auth":
		login("")
	case "/login":
		switch {
		case r.PostForm.Get("username") != "rachel" || r.PostForm.Get("password") != s.password:
			login(`<span id="input-error">Invalid username or password.</span>`)
		case s.next == "disabled":
			login(`<div class="alert-error">Account is disabled, contact your administrator.</div>`)
		case s.next == "update":
			update(`<div class="alert-warning">You need to change your password to activate your account.</div>`)
		default:
			back()
		}
	case "/update":
		pw := r.PostForm.Get("password-new")
		if pw == "" || pw != r.PostForm.Get("password-confirm") {
			update(`<span id="input-error-password-confirm">` + html.EscapeString("Passwords don't match.") + `</span>`)
			return
		}
		s.password, s.next = pw, ""
		back()
	case "/token":
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "at", "expires_in": 300})
	}
}

// Keycloak's "update password" step goes to the person as a form of its
// own, with what the page says; the saved password isn't thrown away for
// it, and the new one is what is saved from then on.
func TestUpdatePasswordStep(t *testing.T) {
	s := newStepIdP(t, "correct")
	m, _ := newManager(t)
	c := s.config()
	ctx := context.Background()
	if _, _, err := m.Token(ctx, c, &prompter{logins: []auth.LoginAnswer{{Username: "rachel", Password: "correct", Remember: true}}}, ""); err != nil {
		t.Fatal(err)
	}
	s.set("correct", "update")
	p := &prompter{forms: []map[string]string{{"password-new": "new-pass", "password-confirm": "new-pass"}}}
	if _, _, err := m.Token(ctx, c, p, auth.Fresh); err != nil {
		t.Fatalf("with a new password: %v (asked %+v)", err, p.asked)
	}
	if len(p.asked) != 0 {
		t.Errorf("asked to log in: %+v", p.asked)
	}
	if len(p.formsAsk) != 1 {
		t.Fatalf("forms asked: %+v", p.formsAsk)
	}
	q := p.formsAsk[0]
	if len(q.Fields) != 2 || !q.Fields[0].Secret || !q.Fields[1].Secret || q.Fields[0].Label != "New Password" || !strings.Contains(q.Message, "change your password") {
		t.Errorf("the form: %+v", q)
	}
	quiet := &prompter{}
	if _, _, err := m.Token(ctx, c, quiet, auth.Fresh); err != nil || len(quiet.asked)+len(quiet.formsAsk) != 0 {
		t.Errorf("with the new password saved: %v, asked %d", err, len(quiet.asked)+len(quiet.formsAsk))
	}
}

// The saved password is forgotten only when the login form comes back
// saying it is wrong: a disabled account keeps it.
func TestLoginErrorKeepsThePassword(t *testing.T) {
	s := newStepIdP(t, "correct")
	m, _ := newManager(t)
	c := s.config()
	ctx := context.Background()
	if _, _, err := m.Token(ctx, c, &prompter{logins: []auth.LoginAnswer{{Username: "rachel", Password: "correct", Remember: true}}}, ""); err != nil {
		t.Fatal(err)
	}
	s.set("correct", "disabled")
	p := &prompter{}
	if _, _, err := m.Token(ctx, c, p, auth.Fresh); err == nil || len(p.asked) != 1 || !strings.Contains(p.asked[0].Error, "Account is disabled") {
		t.Fatalf("disabled: %v, asked %+v", err, p.asked)
	}
	s.set("correct", "")
	quiet := &prompter{}
	if _, _, err := m.Token(ctx, c, quiet, auth.Fresh); err != nil || len(quiet.asked) != 0 {
		t.Errorf("enabled again: %v, asked %d", err, len(quiet.asked))
	}
	// A password changed elsewhere: posthero says the page's error and asks.
	s.set("changed", "")
	p = &prompter{logins: []auth.LoginAnswer{{Username: "rachel", Password: "changed"}}}
	if _, _, err := m.Token(ctx, c, p, auth.Fresh); err != nil || len(p.asked) != 1 || p.asked[0].Error != "Invalid username or password." {
		t.Errorf("changed: %v, asked %+v", err, p.asked)
	}
	// The old one is gone (the new one wasn't to be remembered).
	quiet = &prompter{}
	if _, _, err := m.Token(ctx, c, quiet, auth.Fresh); err == nil || len(quiet.asked) != 1 || quiet.asked[0].Error != "" {
		t.Errorf("after the change: %v, asked %+v", err, quiet.asked)
	}
}

// The password grant forgets the saved password only when the identity
// provider says the user name or password is wrong, not when the account
// needs setting up first.
func TestPasswordGrantRefusals(t *testing.T) {
	var mu sync.Mutex
	password, setUp := "correct", true
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		_ = r.ParseForm()
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.PostForm.Get("password") != password:
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = fmt.Fprint(w, `{"error":"invalid_grant","error_description":"Invalid user credentials"}`)
		case !setUp:
			w.WriteHeader(http.StatusBadRequest)
			_, _ = fmt.Fprint(w, `{"error":"invalid_grant","error_description":"Account is not fully set up"}`)
		default:
			_, _ = fmt.Fprint(w, `{"access_token":"at","expires_in":300}`)
		}
	}))
	defer srv.Close()
	set := func(pw string, ok bool) {
		mu.Lock()
		password, setUp = pw, ok
		mu.Unlock()
	}
	m, _ := newManager(t)
	ctx := context.Background()
	c := auth.Config{Grant: auth.GrantPassword, TokenURL: srv.URL + "/token", ClientID: "portal", Username: "rachel"}
	if _, _, err := m.Token(ctx, c, &prompter{logins: []auth.LoginAnswer{{Username: "rachel", Password: "correct", Remember: true}}}, ""); err != nil {
		t.Fatal(err)
	}
	set("correct", false)
	p := &prompter{}
	if _, _, err := m.Token(ctx, c, p, auth.Fresh); err == nil || !strings.Contains(err.Error(), "Account is not fully set up") || len(p.asked) != 0 {
		t.Errorf("not set up: %v, asked %+v", err, p.asked)
	}
	set("correct", true)
	quiet := &prompter{}
	if _, _, err := m.Token(ctx, c, quiet, auth.Fresh); err != nil || len(quiet.asked) != 0 {
		t.Errorf("set up: %v, asked %d", err, len(quiet.asked))
	}
	set("changed", true)
	p = &prompter{logins: []auth.LoginAnswer{{Username: "rachel", Password: "changed"}}}
	if _, _, err := m.Token(ctx, c, p, auth.Fresh); err != nil || len(p.asked) != 1 || p.asked[0].Error != "Invalid user credentials" {
		t.Errorf("changed: %v, asked %+v", err, p.asked)
	}
	quiet = &prompter{}
	if _, _, err := m.Token(ctx, c, quiet, auth.Fresh); err == nil || len(quiet.asked) != 1 {
		t.Errorf("the wrong password is still saved: %v, asked %d", err, len(quiet.asked))
	}
}

// A user name the identity provider doesn't know (a login_hint that isn't
// the account's name) is asked about once, with what the page says, rather
// than sent again and again.
func TestUnknownUserNameIsAsked(t *testing.T) {
	idp, _, m, _ := setup(t)
	idp.UsernameFirst = true
	idp.AddUser(idptest.User{Name: "alice@example.com", Password: "pw"})
	c := auth.Config{Grant: auth.GrantPKCE, AuthURL: idp.AuthURL() + "?login_hint=alice", TokenURL: idp.TokenURL(), ClientID: "portal", RedirectURI: "http://localhost:3000/cb", Challenge: "S256"}
	p := &prompter{logins: []auth.LoginAnswer{{Username: "alice@example.com", Password: "pw"}}}
	tok, _, err := m.Token(context.Background(), c, p, "")
	if err != nil {
		t.Fatalf("%v after %d form posts", err, idp.FormPosts)
	}
	if tok.Subject != "alice@example.com" || idp.FormPosts != 3 {
		t.Errorf("logged in as %q with %d form posts", tok.Subject, idp.FormPosts)
	}
	if len(p.asked) != 1 || p.asked[0].Error != "Invalid username." || p.asked[0].Username != "alice" {
		t.Errorf("asked %+v", p.asked)
	}
}
