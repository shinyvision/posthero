// Package idptest is a fake OpenID Connect identity provider that behaves
// like Keycloak: a realm with users, an HTML login form, a session cookie,
// authorization codes with PKCE, implicit tokens, short-lived access tokens
// and rotating refresh tokens. Tests and posthero --demo log in to it.
package idptest

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/shinyvision/posthero/internal/jsonx"
)

// User is someone who can log in.
type User struct {
	Name, Password string
	// OTP is a one-time code the login asks for after the password.
	OTP string
}

// Client is an OAuth client of the realm.
type Client struct {
	ID, Secret string
	// Redirects are the allowed redirect URIs (a trailing * matches any
	// rest).
	Redirects []string
}

// IdP is the fake.
type IdP struct {
	mu  sync.Mutex
	srv *httptest.Server
	// Realm is the realm's name; its endpoints are under /realms/<Realm>.
	Realm string
	// AccessTTL and RefreshTTL are how long tokens last.
	AccessTTL, RefreshTTL time.Duration
	// SessionTTL is how long the login session (cookie) lasts.
	SessionTTL time.Duration
	// UsernameFirst asks for the user name on a page of its own.
	UsernameFirst bool
	Now           func() time.Time

	users    map[string]*User
	clients  map[string]*Client
	codes    map[string]codeInfo
	sessions map[string]session // by session cookie
	pending  map[string]pending // logins under way, by session_code
	refresh  map[string]tokenInfo
	access   map[string]tokenInfo
	// Counts of what happened, for tests.
	FormPosts, Exchanges, Refreshes, SilentLogins, PasswordGrants int
}

type codeInfo struct {
	client, redirect, challenge, method, user string
	at                                        time.Time
}

type session struct {
	user string
	ends time.Time
}

type pending struct {
	client, redirect, state, challenge, method, responseType, nonce string
	user                                                            string // set once the password is right and an OTP is due
}

type tokenInfo struct {
	user, client string
	ends         time.Time
}

// New returns a realm "test" with no users or clients.
func New() *IdP {
	return &IdP{
		Realm: "test", AccessTTL: 5 * time.Minute, RefreshTTL: 30 * time.Minute, SessionTTL: 10 * time.Hour,
		Now:   time.Now,
		users: map[string]*User{}, clients: map[string]*Client{}, codes: map[string]codeInfo{},
		sessions: map[string]session{}, pending: map[string]pending{}, refresh: map[string]tokenInfo{}, access: map[string]tokenInfo{},
	}
}

// Start serves the fake and returns its base URL.
func (p *IdP) Start() string {
	p.srv = httptest.NewServer(http.HandlerFunc(p.serve))
	return p.srv.URL
}

// Close stops serving.
func (p *IdP) Close() {
	if p.srv != nil {
		p.srv.Close()
	}
}

// URL is the base URL.
func (p *IdP) URL() string { return p.srv.URL }

// AuthURL and TokenURL are the realm's endpoints.
func (p *IdP) AuthURL() string {
	return p.srv.URL + "/realms/" + p.Realm + "/protocol/openid-connect/auth"
}

// TokenURL is the token endpoint.
func (p *IdP) TokenURL() string {
	return p.srv.URL + "/realms/" + p.Realm + "/protocol/openid-connect/token"
}

// AddUser adds a user.
func (p *IdP) AddUser(u User) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.users[u.Name] = &u
}

// SetPassword changes a user's password.
func (p *IdP) SetPassword(name, password string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.users[name].Password = password
}

// AddClient adds a client.
func (p *IdP) AddClient(c Client) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.clients[c.ID] = &c
}

// EndSessions ends every login session, as a server restart or the session
// limit would.
func (p *IdP) EndSessions() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.sessions = map[string]session{}
	p.refresh = map[string]tokenInfo{}
}

// RevokeAccess makes every access token invalid (refresh tokens stay).
func (p *IdP) RevokeAccess() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.access = map[string]tokenInfo{}
}

// Valid reports whether an access token is good now, and whose it is.
func (p *IdP) Valid(token string) (string, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	t, ok := p.access[token]
	if !ok || !t.ends.After(p.Now()) {
		return "", false
	}
	return t.user, true
}

// Protect wraps a handler so it answers 401 unless the request carries a
// good bearer token; the user's name goes in the X-User header.
func (p *IdP) Protect(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		user, ok := p.Valid(tok)
		if !ok {
			w.Header().Set("WWW-Authenticate", `Bearer realm="`+p.Realm+`"`)
			http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
			return
		}
		r.Header.Set("X-User", user)
		h.ServeHTTP(w, r)
	})
}

func random() string {
	b := make([]byte, 18)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

func (p *IdP) serve(w http.ResponseWriter, r *http.Request) {
	p.mu.Lock()
	defer p.mu.Unlock()
	base := "/realms/" + p.Realm
	switch {
	case r.URL.Path == base+"/protocol/openid-connect/auth":
		p.authorize(w, r)
	case r.URL.Path == base+"/login-actions/authenticate":
		p.authenticate(w, r)
	case r.URL.Path == base+"/protocol/openid-connect/token":
		p.token(w, r)
	case r.URL.Path == base+"/protocol/openid-connect/logout":
		_ = r.ParseForm()
		delete(p.refresh, r.Form.Get("refresh_token"))
		w.WriteHeader(http.StatusNoContent)
	case r.URL.Path == base+"/protocol/openid-connect/userinfo":
		tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		t, ok := p.access[tok]
		if !ok || !t.ends.After(p.Now()) {
			http.Error(w, "", http.StatusUnauthorized)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"sub": t.user, "preferred_username": t.user})
	default:
		http.NotFound(w, r)
	}
}

func redirectAllowed(c *Client, redirect string) bool {
	for _, allowed := range c.Redirects {
		if allowed == redirect || strings.HasSuffix(allowed, "*") && strings.HasPrefix(redirect, strings.TrimSuffix(allowed, "*")) {
			return true
		}
	}
	return false
}

func (p *IdP) authorize(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	c := p.clients[q.Get("client_id")]
	if c == nil {
		p.errorPage(w, "Client not found.")
		return
	}
	redirect := q.Get("redirect_uri")
	if !redirectAllowed(c, redirect) {
		p.errorPage(w, "Invalid parameter: redirect_uri")
		return
	}
	pd := pending{client: c.ID, redirect: redirect, state: q.Get("state"), challenge: q.Get("code_challenge"),
		method: q.Get("code_challenge_method"), responseType: q.Get("response_type"), nonce: q.Get("nonce")}
	// A live session logs in without a form, unless prompt=login.
	if ck, err := r.Cookie("KEYCLOAK_IDENTITY"); err == nil && q.Get("prompt") != "login" {
		if s, ok := p.sessions[ck.Value]; ok && s.ends.After(p.Now()) {
			p.SilentLogins++
			p.finish(w, r, pd, s.user)
			return
		}
	}
	code := random()
	p.pending[code] = pd
	http.SetCookie(w, &http.Cookie{Name: "AUTH_SESSION_ID", Value: code, Path: "/realms/" + p.Realm + "/", HttpOnly: true})
	if p.UsernameFirst {
		p.usernamePage(w, code, q.Get("login_hint"), "")
		return
	}
	p.loginPage(w, code, q.Get("login_hint"), "")
}

func (p *IdP) action(code string) string {
	return html.EscapeString(p.srv.URL + "/realms/" + p.Realm + "/login-actions/authenticate?session_code=" + code + "&execution=x&tab_id=y")
}

func (p *IdP) loginPage(w http.ResponseWriter, code, user, errText string) {
	errHTML := ""
	if errText != "" {
		errHTML = `<span id="input-error" class="pf-c-form__helper-text" aria-live="polite">` + html.EscapeString(errText) + `</span>`
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, `<!DOCTYPE html><html><head><title>Sign in to %s</title><script src="/x.js"></script></head><body>
<div id="kc-header">%s</div>
<form id="kc-form-login" onsubmit="login.disabled = true; return true;" action="%s" method="post">
  <label for="username" class="pf-c-form__label">Username or email</label>
  <input tabindex="1" id="username" class="pf-c-form-control" name="username" value="%s" type="text" autofocus autocomplete="off"/>
  <label for="password" class="pf-c-form__label">Password</label>
  <input tabindex="2" id="password" class="pf-c-form-control" name="password" type="password" autocomplete="off"/>
  %s
  <input type="checkbox" id="rememberMe" name="rememberMe"> Remember me
  <input type="hidden" id="id-hidden-input" name="credentialId"/>
  <input tabindex="4" class="pf-c-button" name="login" id="kc-login" type="submit" value="Sign In"/>
</form></body></html>`, html.EscapeString(p.Realm), html.EscapeString(p.Realm), p.action(code), html.EscapeString(user), errHTML)
}

func (p *IdP) usernamePage(w http.ResponseWriter, code, user, errText string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, `<!DOCTYPE html><html><head><title>Sign in to %s</title></head><body>
<div class="alert-error">%s</div>
<form id="kc-form-login" action="%s" method="post">
  <label for="username">Username or email</label>
  <input id="username" name="username" value="%s" type="text" autocomplete="username"/>
  <input name="login" id="kc-login" type="submit" value="Next"/>
</form></body></html>`, html.EscapeString(p.Realm), html.EscapeString(errText), p.action(code), html.EscapeString(user))
}

func (p *IdP) otpPage(w http.ResponseWriter, code, errText string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, `<!DOCTYPE html><html><head><title>Sign in to %s</title></head><body>
<h1>Two-factor authentication</h1>
<span id="input-error-otp-code">%s</span>
<form id="kc-otp-login-form" action="%s" method="post">
  <label for="otp">One-time code</label>
  <input id="otp" name="otp" autocomplete="off" type="text" autofocus/>
  <input name="login" id="kc-login" type="submit" value="Sign In"/>
</form></body></html>`, html.EscapeString(p.Realm), html.EscapeString(errText), p.action(code))
}

func (p *IdP) errorPage(w http.ResponseWriter, msg string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusBadRequest)
	fmt.Fprintf(w, `<!DOCTYPE html><html><head><title>Sign in to %s</title></head><body><div id="kc-error-message"><p class="instruction">%s</p></div></body></html>`, html.EscapeString(p.Realm), html.EscapeString(msg))
}

func (p *IdP) authenticate(w http.ResponseWriter, r *http.Request) {
	code := r.URL.Query().Get("session_code")
	pd, ok := p.pending[code]
	if ck, err := r.Cookie("AUTH_SESSION_ID"); !ok || err != nil || ck.Value != code {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, `<html><head><title>Page has expired</title></head><body><p id="instruction1" class="instruction">Page has expired. To restart the login process <a href="/x">Click here</a>.</p></body></html>`)
		return
	}
	_ = r.ParseForm()
	p.FormPosts++
	if pd.user != "" {
		// The password was right; this is the one-time code.
		if r.Form.Get("otp") != p.users[pd.user].OTP {
			p.otpPage(w, code, "Invalid authenticator code.")
			return
		}
		p.loggedIn(w, r, code, pd, pd.user, r.Form.Get("rememberMe") == "on")
		return
	}
	name := r.Form.Get("username")
	if p.UsernameFirst && r.Form.Get("password") == "" {
		if _, ok := p.users[name]; !ok {
			p.usernamePage(w, code, name, "Invalid username.")
			return
		}
		p.loginPage(w, code, name, "")
		return
	}
	u, ok := p.users[name]
	if !ok || u.Password != r.Form.Get("password") {
		p.loginPage(w, code, name, "Invalid username or password.")
		return
	}
	if u.OTP != "" {
		pd.user = name
		p.pending[code] = pd
		p.otpPage(w, code, "")
		return
	}
	p.loggedIn(w, r, code, pd, name, r.Form.Get("rememberMe") == "on")
}

func (p *IdP) loggedIn(w http.ResponseWriter, r *http.Request, code string, pd pending, user string, remember bool) {
	delete(p.pending, code)
	sid := random()
	p.sessions[sid] = session{user: user, ends: p.Now().Add(p.SessionTTL)}
	ck := &http.Cookie{Name: "KEYCLOAK_IDENTITY", Value: sid, Path: "/realms/" + p.Realm + "/", HttpOnly: true}
	if remember {
		ck.Expires = p.Now().Add(p.SessionTTL)
	}
	http.SetCookie(w, ck)
	p.finish(w, r, pd, user)
}

// finish sends the browser back with a code, or a token for the implicit
// grant.
func (p *IdP) finish(w http.ResponseWriter, r *http.Request, pd pending, user string) {
	if pd.responseType == "token" {
		tok := p.issueAccess(user, pd.client)
		frag := url.Values{"access_token": {tok}, "token_type": {"Bearer"}, "expires_in": {fmt.Sprint(int(p.AccessTTL.Seconds()))}, "state": {pd.state}}
		http.Redirect(w, r, pd.redirect+"#"+frag.Encode(), http.StatusFound)
		return
	}
	code := random()
	p.codes[code] = codeInfo{client: pd.client, redirect: pd.redirect, challenge: pd.challenge, method: pd.method, user: user, at: p.Now()}
	sep := "?"
	if strings.Contains(pd.redirect, "?") {
		sep = "&"
	}
	http.Redirect(w, r, pd.redirect+sep+url.Values{"state": {pd.state}, "session_state": {random()}, "code": {code}}.Encode(), http.StatusFound)
}

// jwt makes an unsigned-looking token whose claims posthero can read.
func jwt(claims map[string]any) string {
	enc := base64.RawURLEncoding
	h, _ := jsonx.Marshal(map[string]any{"alg": "RS256", "typ": "JWT"})
	c, _ := jsonx.Marshal(claims)
	return enc.EncodeToString(h) + "." + enc.EncodeToString(c) + "." + enc.EncodeToString([]byte(random()))
}

func (p *IdP) issueAccess(user, client string) string {
	now := p.Now()
	tok := jwt(map[string]any{"exp": now.Add(p.AccessTTL).Unix(), "iat": now.Unix(), "jti": random(), "preferred_username": user, "azp": client, "realm_access": map[string]any{"roles": []string{"user"}}})
	p.access[tok] = tokenInfo{user: user, client: client, ends: now.Add(p.AccessTTL)}
	return tok
}

func (p *IdP) issue(w http.ResponseWriter, user, client string) {
	now := p.Now()
	access := p.issueAccess(user, client)
	refresh := jwt(map[string]any{"exp": now.Add(p.RefreshTTL).Unix(), "typ": "Refresh", "jti": random()})
	p.refresh[refresh] = tokenInfo{user: user, client: client, ends: now.Add(p.RefreshTTL)}
	writeJSON(w, http.StatusOK, map[string]any{
		"access_token": access, "expires_in": int(p.AccessTTL.Seconds()), "refresh_expires_in": int(p.RefreshTTL.Seconds()),
		"refresh_token": refresh, "token_type": "Bearer", "scope": "openid profile email", "id_token": jwt(map[string]any{"preferred_username": user}),
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func oauthErr(w http.ResponseWriter, status int, code, desc string) {
	writeJSON(w, status, map[string]string{"error": code, "error_description": desc})
}

// clientOf authenticates the client of a token request: Basic auth or the
// form.
func (p *IdP) clientOf(r *http.Request) (*Client, bool) {
	id, secret, basic := r.BasicAuth()
	if basic {
		id, _ = url.QueryUnescape(id)
		secret, _ = url.QueryUnescape(secret)
	} else {
		id, secret = r.Form.Get("client_id"), r.Form.Get("client_secret")
	}
	c := p.clients[id]
	if c == nil || c.Secret != secret {
		return nil, false
	}
	return c, true
}

func (p *IdP) token(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	c, ok := p.clientOf(r)
	if !ok {
		oauthErr(w, http.StatusUnauthorized, "invalid_client", "Invalid client or Invalid client credentials")
		return
	}
	now := p.Now()
	switch r.Form.Get("grant_type") {
	case "authorization_code":
		ci, ok := p.codes[r.Form.Get("code")]
		delete(p.codes, r.Form.Get("code"))
		if !ok || ci.client != c.ID || now.Sub(ci.at) > time.Minute {
			oauthErr(w, http.StatusBadRequest, "invalid_grant", "Code not valid")
			return
		}
		if ci.redirect != r.Form.Get("redirect_uri") {
			oauthErr(w, http.StatusBadRequest, "invalid_grant", "Incorrect redirect_uri")
			return
		}
		if ci.challenge != "" {
			v := r.Form.Get("code_verifier")
			want := v
			if ci.method == "S256" {
				sum := sha256.Sum256([]byte(v))
				want = base64.RawURLEncoding.EncodeToString(sum[:])
			}
			if v == "" || want != ci.challenge {
				oauthErr(w, http.StatusBadRequest, "invalid_grant", "PKCE verification failed")
				return
			}
		}
		p.Exchanges++
		p.issue(w, ci.user, c.ID)
	case "refresh_token":
		rt := r.Form.Get("refresh_token")
		ti, ok := p.refresh[rt]
		delete(p.refresh, rt) // refresh tokens rotate
		if !ok || !ti.ends.After(now) || ti.client != c.ID {
			oauthErr(w, http.StatusBadRequest, "invalid_grant", "Token is not active")
			return
		}
		p.Refreshes++
		p.issue(w, ti.user, c.ID)
	case "password":
		u, ok := p.users[r.Form.Get("username")]
		if !ok || u.Password != r.Form.Get("password") {
			oauthErr(w, http.StatusUnauthorized, "invalid_grant", "Invalid user credentials")
			return
		}
		p.PasswordGrants++
		p.issue(w, u.Name, c.ID)
	case "client_credentials":
		if c.Secret == "" {
			oauthErr(w, http.StatusUnauthorized, "unauthorized_client", "Public client not allowed to retrieve service account")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"access_token": p.issueAccess("service-account-"+c.ID, c.ID), "expires_in": int(p.AccessTTL.Seconds()), "token_type": "Bearer"})
	default:
		oauthErr(w, http.StatusBadRequest, "unsupported_grant_type", "Unsupported grant_type")
	}
}
