package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os/exec"
	"runtime"
	"strings"
	"testing"
	"time"
)

// login gets a new token, asking p what it must.
func (m *Manager) login(ctx context.Context, c Config, p Prompter, old *Token) (*Token, error) {
	switch c.Grant {
	case GrantClient:
		if p != nil {
			p.Status("getting a token from " + c.Issuer())
		}
		t, err := m.clientCredentials(ctx, c)
		if err != nil {
			return nil, err
		}
		m.keep(c, t)
		return t, nil
	case GrantPassword:
		return m.loginPassword(ctx, c, p, "")
	case GrantCode, GrantPKCE, GrantImplicit:
		return m.authorize(ctx, c, p, old, "")
	}
	return nil, fmt.Errorf("posthero doesn't know the OAuth 2.0 grant type %q", c.Grant)
}

// loginPassword uses the user name and password of the settings, else the
// saved ones, else asks. as is a user to log in as instead of the
// settings' (ForceLogin); the token is the config's all the same.
func (m *Manager) loginPassword(ctx context.Context, c Config, p Prompter, as string) (*Token, error) {
	idp := c.IdP()
	user, pass, fromSettings := c.Username, c.Password, c.Password != ""
	if as != "" && as != c.Username {
		user, pass, fromSettings = as, "", false
	}
	fresh := as == newUser
	if fresh {
		user = ""
	}
	saved := false
	errText := ""
	if !fromSettings && !fresh {
		var note string
		if user, pass, note = m.savedPassword(c, user); pass != "" {
			saved = true
		}
		errText = note
	}
	for attempt := 0; attempt < 5; attempt++ {
		remember := false
		if pass == "" {
			if p == nil {
				return nil, errNoUser
			}
			def := firstNonEmpty(user, m.Logins.Username(idp))
			if fresh {
				def = user
			}
			ans, err := p.Login(ctx, LoginPrompt{Issuer: c.Issuer(), Username: def, Error: errText, Remember: true})
			if err != nil {
				return nil, err
			}
			user, pass, remember = ans.Username, ans.Password, ans.Remember
		}
		if p != nil {
			p.Status("logging in to " + c.Issuer() + " as " + user)
		}
		t, err := m.passwordGrant(ctx, c, user, pass)
		if err == nil {
			t.Login = user
			if remember {
				_, _ = m.Logins.Save(idp, c.tokenFrom(), user, pass)
			} else if !fromSettings {
				m.Logins.Remember(idp, user)
			}
			m.keep(c, t)
			return t, nil
		}
		// Only a wrong user name or password is worth asking again for: an
		// account that must be set up first, or is disabled, isn't fixed by
		// typing, and the password may well be right.
		var te *TokenError
		switch {
		case !errors.As(err, &te):
			return nil, err
		case te.Code == "invalid_grant" && !te.wrongPassword():
			return nil, fmt.Errorf("%s refused the login of %s: %s", c.Issuer(), user, te.About)
		case !te.wrongPassword() || fromSettings:
			return nil, err
		}
		if saved {
			m.Logins.ForgetPassword(idp, user)
			saved = false
		}
		errText, pass = te.About, ""
		if errText == "" {
			errText = "the user name or password is wrong"
		}
	}
	return nil, errors.New("too many attempts to log in")
}

// savedPassword returns the saved password of user (or of the user who
// logged in last, when empty) for a login with c, and who it is for. When
// there is one that isn't to be filled in by itself for c (its token URL
// is somewhere else than the password was saved for), note says so, for
// the person asked instead.
func (m *Manager) savedPassword(c Config, user string) (who, pass, note string) {
	idp, to := c.IdP(), c.tokenFrom()
	if user == "" {
		if user = m.Logins.Username(idp); user == "" {
			return "", "", ""
		}
	}
	pw, ok, elsewhere := m.Logins.password(idp, to, user)
	switch {
	case ok:
		return user, pw, ""
	case elsewhere:
		return user, "", "Your saved password isn't filled in here: this login gets its token from " + hostOf(to) + ", which you haven't logged in through before. Type it only if you trust that server."
	}
	return user, "", ""
}

// result is where the identity provider sent the browser back to.
type result struct {
	code, state, errCode, errText string
	token                         *Token // for the implicit grant
}

// pkce makes a code verifier and its challenge.
func pkce(method, verifier string) (string, string) {
	if verifier == "" {
		b := make([]byte, 32)
		_, _ = rand.Read(b)
		verifier = base64.RawURLEncoding.EncodeToString(b)
	}
	if strings.EqualFold(method, "plain") {
		return verifier, verifier
	}
	sum := sha256.Sum256([]byte(verifier))
	return verifier, base64.RawURLEncoding.EncodeToString(sum[:])
}

func randomState() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// authURL is the authorization request, with the settings' extra
// parameters, an empty login_hint left out, and prompt=login when a new
// login is forced.
func authURL(c Config, state, challenge string, force bool) (string, error) {
	u, err := url.Parse(c.AuthURL)
	if err != nil || u.Host == "" {
		return "", fmt.Errorf("the OAuth 2.0 Auth URL %q isn't a URL", c.AuthURL)
	}
	q := u.Query()
	for k, v := range q {
		if len(v) == 1 && v[0] == "" {
			q.Del(k)
		}
	}
	rt := "code"
	if c.Grant == GrantImplicit {
		rt = "token"
	}
	q.Set("response_type", rt)
	q.Set("client_id", c.ClientID)
	q.Set("redirect_uri", c.RedirectURI)
	if c.Scope != "" {
		q.Set("scope", c.Scope)
	}
	q.Set("state", state)
	if c.Grant == GrantPKCE {
		q.Set("code_challenge", challenge)
		method := "S256"
		if strings.EqualFold(c.Challenge, "plain") {
			method = "plain"
		}
		q.Set("code_challenge_method", method)
	}
	if c.Grant == GrantImplicit && strings.Contains(c.Scope, "openid") && q.Get("nonce") == "" {
		q.Set("nonce", randomState())
	}
	if c.Audience != "" && q.Get("audience") == "" {
		q.Set("audience", c.Audience)
	}
	for _, e := range c.AuthParams {
		if e.SendAs == "request_url" || e.SendAs == "request_body" {
			q.Set(e.Key, e.Value)
		}
	}
	if force {
		q.Set("prompt", "login")
	}
	u.RawQuery = q.Encode()
	return u.String(), nil
}

// newUser, as ForceLogin's username, logs in as someone new: posthero asks
// who, without filling in anyone's name.
const newUser = "\x01"

// ForceLogin logs in again as username (or as anyone, when empty), past
// the identity provider's session, and keeps the token: the config's,
// whoever it is for now.
func (m *Manager) ForceLogin(ctx context.Context, c Config, p Prompter, username string) (*Token, error) {
	unlock, err := m.lock(ctx, c.Key())
	if err != nil {
		return nil, err
	}
	defer unlock()
	switch c.Grant {
	case GrantCode, GrantPKCE, GrantImplicit:
		m.Sessions.Clear(c.session())
		return m.authorize(ctx, c, p, nil, firstNonEmpty(username, "\x00"))
	case GrantPassword:
		return m.loginPassword(ctx, c, p, username)
	}
	return m.login(ctx, c, p, nil)
}

// authorize runs the authorization code (or implicit) flow: through the
// identity provider's session when it still has one, else its login form
// filled in here, else the browser. force is a user name to log in as
// anew ("\x00" for anyone).
func (m *Manager) authorize(ctx context.Context, c Config, p Prompter, old *Token, force string) (*Token, error) {
	state := c.State
	if state == "" {
		state = randomState()
	}
	verifier, challenge := pkce(c.Challenge, c.CodeVerifier)
	start, err := authURL(c, state, challenge, force != "")
	if err != nil {
		return nil, err
	}
	idp := c.IdP()
	hint := ""
	if u, err := url.Parse(start); err == nil {
		hint = u.Query().Get("login_hint")
	}
	fresh := force == newUser
	if force != "" && force != "\x00" && !fresh {
		hint = force
	}
	if hint == "" && old != nil {
		hint = old.Login
	}
	if p != nil {
		p.Status("logging in to " + c.Issuer())
	}
	session := c.session()
	w := &walker{m: m, c: c, p: p, idp: idp, session: session, hint: hint, force: force != "", fresh: fresh}
	res, err := w.walk(ctx, start)
	var fallback *needBrowser
	if errors.As(err, &fallback) {
		if p == nil {
			return nil, errNoUser
		}
		res, err = m.browser(ctx, c, p, start, fallback.why)
	}
	if err != nil {
		return nil, err
	}
	_ = m.Sessions.Save(session)
	if res.errCode != "" {
		return nil, &TokenError{Code: res.errCode, About: res.errText}
	}
	if res.state != state {
		return nil, errors.New("the login came back for another request (the state doesn't match); try again")
	}
	var t *Token
	if res.token != nil {
		t = res.token
	} else {
		if res.code == "" {
			return nil, errors.New("the identity provider sent no code back")
		}
		form := url.Values{"grant_type": {"authorization_code"}, "code": {res.code}, "redirect_uri": {c.RedirectURI}}
		if c.Grant == GrantPKCE {
			form.Set("code_verifier", verifier)
		}
		if t, err = m.tokenRequest(ctx, c.TokenURL, c, form, c.TokenParams); err != nil {
			return nil, err
		}
	}
	t.Login = w.user
	if t.Login == "" && old != nil {
		t.Login = old.Login
	}
	if w.remember && w.user != "" {
		if note, err := m.Logins.Save(idp, c.tokenFrom(), w.user, w.pass); err == nil && note != "" && p != nil {
			p.Status(note)
		}
	} else if w.user != "" {
		m.Logins.Remember(idp, w.user)
	}
	m.keep(c, t)
	return t, nil
}

// needBrowser says the login can't go on here.
type needBrowser struct{ why string }

func (e *needBrowser) Error() string { return "the login needs a browser: " + e.why }

// walker follows the identity provider's pages until they send the browser
// back to the redirect URI.
type walker struct {
	m   *Manager
	c   Config
	p   Prompter
	idp string
	// session is the cookie jar of the session this login uses.
	session string
	hint    string
	force   bool
	// fresh says the login is for someone new: nobody's name is filled in.
	fresh bool
	// user and pass are what logged in; remember says to save them.
	user, pass string
	remember   bool
	// triedSaved says the saved password was tried, and usedSaved that the
	// last password sent was it.
	triedSaved, usedSaved bool
	restarted             bool
	// sent is what the last step sent, and passwordForm the shape of the
	// form the last password went in: a login form that comes back means
	// the password (or user name) didn't do.
	sent         stepSent
	passwordForm string
}

// stepSent is what a step of a login sent.
type stepSent int

const (
	sentOther stepSent = iota
	sentUser           // a user name, on a page asking for it first
	sentPassword
)

func (w *walker) walk(ctx context.Context, start string) (*result, error) {
	client := w.m.httpClient(w.m.Sessions.Jar(w.session))
	method, target, form := http.MethodGet, start, url.Values(nil)
	for step := 0; step < 40; step++ {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		var req *http.Request
		var err error
		if method == http.MethodPost {
			req, err = http.NewRequestWithContext(ctx, method, target, strings.NewReader(form.Encode()))
			if err == nil {
				req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			}
		} else {
			if form != nil {
				u, perr := url.Parse(target)
				if perr == nil {
					u.RawQuery = form.Encode()
					target = u.String()
				}
			}
			req, err = http.NewRequestWithContext(ctx, method, target, nil)
		}
		if err != nil {
			return nil, err
		}
		req.Header.Set("Accept", "text/html,application/xhtml+xml")
		req.Header.Set("User-Agent", "Mozilla/5.0 (posthero)")
		resp, err := client.Do(req)
		if err != nil {
			if res, ok := w.redirectFromError(err); ok {
				return res, nil
			}
			return nil, fmt.Errorf("couldn't reach %s: %w", req.URL.Host, err)
		}
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
		resp.Body.Close()
		if loc := resp.Header.Get("Location"); resp.StatusCode >= 300 && resp.StatusCode < 400 && loc != "" {
			next, err := req.URL.Parse(loc)
			if err != nil {
				return nil, err
			}
			if res, ok := matchRedirect(next, w.c.RedirectURI); ok {
				return res, nil
			}
			method, target, form = http.MethodGet, next.String(), nil
			continue
		}
		if resp.StatusCode >= 400 && !strings.Contains(resp.Header.Get("Content-Type"), "html") {
			return nil, fmt.Errorf("the login page answered HTTP %d", resp.StatusCode)
		}
		pg, err := parsePage(req.URL, strings.NewReader(string(data)))
		if err != nil {
			return nil, err
		}
		f := pg.loginForm()
		if f == nil {
			// A page left from an old session ("page has expired"): start again
			// with a clean one, once.
			if !w.restarted && step > 0 {
				w.restarted = true
				w.m.Sessions.Clear(w.session)
				method, target, form = http.MethodGet, start, nil
				continue
			}
			why := "the login page has no form posthero can fill in"
			if pg.message != "" {
				why = "the login page says: " + pg.message
			} else if pg.scripts > 0 {
				why = "the login page is a JavaScript app"
			}
			return nil, &needBrowser{why: why}
		}
		vals, button, err := w.fill(ctx, pg, f)
		if err != nil {
			return nil, err
		}
		if button.Name != "" {
			vals.Set(button.Name, button.Value)
		}
		method, target, form = f.method, f.action.String(), vals
	}
	return nil, errors.New("the login went through too many pages")
}

// trusted reports whether a page or form address is the identity
// provider's own: its host and port, by the same scheme (or https).
func (w *walker) trusted(u *url.URL) bool {
	a, err := url.Parse(w.c.AuthURL)
	if err != nil || u == nil {
		return false
	}
	return onServer(u, a)
}

// redirectFromError catches a client error that happened because the
// redirect target was followed anyway (some clients do).
func (w *walker) redirectFromError(err error) (*result, bool) {
	var ue *url.Error
	if errors.As(err, &ue) {
		if u, perr := url.Parse(ue.URL); perr == nil {
			return matchRedirect(u, w.c.RedirectURI)
		}
	}
	return nil, false
}

// fill decides what to send for a form.
func (w *walker) fill(ctx context.Context, pg *page, f *htmlForm) (url.Values, FormButton, error) {
	vals := f.values()
	var button FormButton
	if len(f.buttons) > 0 {
		button = f.buttons[0]
	}
	// What is typed here goes only to the identity provider itself, as
	// securely as it was reached: a login that continues elsewhere (another
	// identity provider, a form posting over plain http) is done in the
	// browser, where you see where you are.
	if !w.trusted(pg.url) || !w.trusted(f.action) {
		where := "another server"
		if f.action != nil && f.action.Host != "" {
			where = f.action.Scheme + "://" + f.action.Host
		}
		return nil, button, &needBrowser{why: "the login continues at " + where + ", where posthero doesn't type your password"}
	}
	last := w.sent
	w.sent = sentOther
	pw, un := f.password(), f.username()
	switch {
	case f.newPassword():
		// Setting a password (Keycloak's "update password") rather than
		// logging in with one: the person fills it in, as any other step.
	case pw == nil && un != nil && len(f.visible()) == 1:
		// A page asking for the user name first.
		user, err := w.userName(ctx, pg, last == sentUser)
		if err != nil {
			return nil, button, err
		}
		vals.Set(un.name, user)
		w.sent = sentUser
		return vals, button, nil
	case pw != nil:
		user, pass, err := w.password(ctx, pg, f, last == sentPassword)
		if err != nil {
			return nil, button, err
		}
		if un != nil {
			vals.Set(un.name, user)
		}
		vals.Set(pw.name, pass)
		w.sent, w.passwordForm = sentPassword, f.shape()
		return vals, button, nil
	}
	// Some other step: a one-time code, a new password, a consent, terms.
	// Ask.
	if w.p == nil {
		return nil, button, errNoUser
	}
	q := FormPrompt{Issuer: w.c.Issuer(), Title: pg.heading(), Message: pg.message, Buttons: f.buttons}
	for _, in := range f.visible() {
		q.Fields = append(q.Fields, FormField{Name: in.name, Label: firstNonEmpty(in.label, in.placeholder, in.name), Value: in.value, Secret: in.typ == "password"})
	}
	if len(q.Fields) == 0 && len(q.Buttons) == 0 {
		return nil, button, &needBrowser{why: "the login page asks for something posthero can't show"}
	}
	if q.Message == "" {
		q.Message = trimText(pg.text, 300)
	}
	ans, err := w.p.Form(ctx, q)
	if err != nil {
		return nil, button, err
	}
	for k, v := range ans.Values {
		vals.Set(k, v)
	}
	if ans.Button.Name != "" || ans.Button.Label != "" {
		button = ans.Button
	}
	if f.newPassword() {
		// The new password is the one that logs in from now on: saved in
		// place of the old one when that was saved.
		if pass := f.newPasswordValue(vals); pass != "" {
			w.pass = pass
			w.remember = w.remember || w.usedSaved
		}
	}
	return vals, button, nil
}

// userName is the user name for a page asking for it first: the one the
// login is for (its login_hint, or who logged in last). When the page comes
// back after one was sent, the identity provider doesn't know that name
// (the hint isn't the account's name): the person is asked, with what the
// page says, rather than the same name sent again.
func (w *walker) userName(ctx context.Context, pg *page, again bool) (string, error) {
	user := ""
	if !again && !w.fresh {
		user = firstNonEmpty(w.user, w.hint, w.m.Logins.Username(w.idp))
	}
	if user == "" {
		if w.p == nil {
			return "", errNoUser
		}
		q := LoginPrompt{Issuer: w.c.Issuer(), Error: pg.message, Remember: true}
		if again {
			q.Username = w.user
			q.Error = firstNonEmpty(pg.message, "the identity provider doesn't know "+w.user)
			w.hint, w.user, w.pass = "", "", ""
		}
		ans, err := w.p.Login(ctx, q)
		if err != nil {
			return "", err
		}
		w.user, w.pass, w.remember = ans.Username, ans.Password, ans.Remember
		user = ans.Username
	}
	w.user = user
	return user, nil
}

// password is the user name and password for a login form: what the
// person typed already, else the saved password, else what they answer
// now. failed says the last step sent a password and got a password form
// back: when it is the same login form, saying the user name or password
// is wrong, a saved password that was sent is forgotten (it was changed).
// Anything else (a disabled account) is the person's to read, and the
// password is kept.
func (w *walker) password(ctx context.Context, pg *page, f *htmlForm, failed bool) (user, pass string, err error) {
	if failed && w.usedSaved && f.shape() == w.passwordForm && refusedLogin(pg, f) {
		w.m.Logins.ForgetPassword(w.idp, w.user)
	}
	w.usedSaved = false
	note := ""
	switch {
	case failed:
	case w.pass != "":
		user, pass = w.user, w.pass
	case !w.triedSaved && !w.force:
		w.triedSaved = true
		user, pass, note = w.m.savedPassword(w.c, firstNonEmpty(w.user, w.hint))
		w.usedSaved = pass != ""
	}
	if pass == "" {
		if w.p == nil {
			return "", "", errNoUser
		}
		msg := note
		if failed {
			msg = firstNonEmpty(pg.message, "that didn't work")
		}
		def := firstNonEmpty(w.user, w.hint, w.m.Logins.Username(w.idp))
		if w.fresh {
			def = w.user
		}
		if f.username() == nil {
			// The user name was asked for on an earlier page.
			def = firstNonEmpty(w.user, def)
		}
		ans, err := w.p.Login(ctx, LoginPrompt{Issuer: w.c.Issuer(), Username: def, Error: msg, Remember: true})
		if err != nil {
			return "", "", err
		}
		user, pass, w.remember = ans.Username, ans.Password, ans.Remember
	}
	w.user, w.pass = firstNonEmpty(user, w.user), pass
	return user, pass, nil
}

func trimText(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// matchRedirect reports whether u is the redirect URI coming back, and
// what it brings: a code (in the query) or a token (in the fragment).
func matchRedirect(u *url.URL, redirect string) (*result, bool) {
	r, err := url.Parse(redirect)
	if err != nil {
		return nil, false
	}
	if !strings.EqualFold(u.Scheme, r.Scheme) || !strings.EqualFold(u.Host, r.Host) {
		return nil, false
	}
	if strings.TrimRight(u.Path, "/") != strings.TrimRight(r.Path, "/") {
		return nil, false
	}
	return parseRedirect(u), true
}

func parseRedirect(u *url.URL) *result {
	q := u.Query()
	if frag, err := url.ParseQuery(u.Fragment); err == nil && u.Fragment != "" {
		// Implicit grants (and response_mode=fragment) answer in the fragment.
		for k, v := range frag {
			if q.Get(k) == "" {
				q[k] = v
			}
		}
	}
	res := &result{code: q.Get("code"), state: q.Get("state"), errCode: q.Get("error"), errText: q.Get("error_description")}
	if at := q.Get("access_token"); at != "" {
		t := &Token{AccessToken: at, TokenType: q.Get("token_type"), IDToken: q.Get("id_token"), Scope: q.Get("scope")}
		if n, err := time.ParseDuration(q.Get("expires_in") + "s"); err == nil && q.Get("expires_in") != "" {
			t.ExpiresAt = time.Now().Add(n)
		}
		res.token = t
	}
	return res
}

// browser logs in through the system browser: posthero catches the way
// back on the redirect URI when it is this machine and the port is free,
// and otherwise asks for the address the browser ends up on.
func (m *Manager) browser(ctx context.Context, c Config, p Prompter, start, why string) (*result, error) {
	done := make(chan string, 1)
	listening := false
	if ln, ok := listenFor(c.RedirectURI); ok {
		listening = true
		srv := &http.Server{Handler: catcher(done), ReadHeaderTimeout: 10 * time.Second}
		go func() { _ = srv.Serve(ln) }()
		defer srv.Close()
	}
	open := m.OpenBrowser
	if open == nil {
		open = OpenURL
	}
	_ = open(start)
	back, err := p.Browser(ctx, BrowserPrompt{Issuer: c.Issuer(), URL: start, Why: why, Listening: listening, Done: done})
	if err != nil {
		return nil, err
	}
	u, err := url.Parse(strings.TrimSpace(back))
	if err != nil {
		return nil, fmt.Errorf("%q isn't the address the browser came back to", back)
	}
	res, ok := matchRedirect(u, c.RedirectURI)
	if !ok {
		// Accept a pasted address with the code even if the host differs
		// (Postman's own callback page, for one).
		res = parseRedirect(u)
		if res.code == "" && res.token == nil && res.errCode == "" {
			return nil, fmt.Errorf("that address has no code; it should start with %s", c.RedirectURI)
		}
	}
	return res, nil
}

// listenFor listens on the redirect URI's port when it is on this machine.
func listenFor(redirect string) (net.Listener, bool) {
	u, err := url.Parse(redirect)
	if err != nil || u.Scheme != "http" {
		return nil, false
	}
	host := u.Hostname()
	if host != "localhost" && host != "127.0.0.1" && host != "::1" && !strings.HasSuffix(host, ".localhost") {
		return nil, false
	}
	port := u.Port()
	if port == "" {
		port = "80"
	}
	addr := net.JoinHostPort("127.0.0.1", port)
	if host == "::1" {
		addr = net.JoinHostPort("::1", port)
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, false
	}
	return ln, true
}

const backPage = `<!doctype html><meta charset="utf-8"><title>posthero</title>
<style>body{font:16px system-ui;margin:20vh auto;max-width:28em;text-align:center;color:#333}</style>
<p id="m">Logged in. You can close this tab and go back to posthero.</p>
<script>
if (location.hash.length > 1) {
  fetch('/__posthero_fragment?' + location.hash.slice(1)).catch(function () {
    document.getElementById('m').textContent = 'posthero did not get the token; copy this address into posthero.';
  });
}
</script>`

// catcher answers the browser coming back and delivers its address.
func catcher(done chan<- string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if r.URL.Path == "/__posthero_fragment" {
			u := *r.URL
			u.Path, u.Fragment, u.RawQuery = "/", r.URL.RawQuery, ""
			select {
			case done <- "http://" + r.Host + "/#" + u.Fragment:
			default:
			}
			return
		}
		_, _ = io.WriteString(w, backPage)
		if r.URL.RawQuery != "" {
			select {
			case done <- "http://" + r.Host + r.URL.RequestURI():
			default:
			}
		}
	})
}

// OpenURL opens a URL in the system's browser. Under go test it opens
// nothing: tests never take over the browser of whoever runs them.
func OpenURL(u string) error {
	if testing.Testing() {
		return nil
	}
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", u)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", u)
	default:
		cmd = exec.Command("xdg-open", u)
	}
	cmd.Stdout, cmd.Stderr = nil, nil
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
}
