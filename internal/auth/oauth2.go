package auth

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Grant types, as Postman names them.
const (
	GrantCode     = "authorization_code"
	GrantPKCE     = "authorization_code_with_pkce"
	GrantImplicit = "implicit"
	GrantPassword = "password_credentials"
	GrantClient   = "client_credentials"
)

// Config is an OAuth 2.0 auth's settings, variables filled in.
type Config struct {
	Grant        string
	AuthURL      string
	TokenURL     string
	RefreshURL   string
	ClientID     string
	ClientSecret string
	Scope        string
	State        string
	RedirectURI  string
	Audience     string
	Resource     string
	Username     string
	Password     string
	// ClientAuth is "header" (Basic auth, the default) or "body".
	ClientAuth string
	// Challenge is S256 or plain, for PKCE.
	Challenge    string
	CodeVerifier string
	// AddTo is "header" or "queryParams".
	AddTo        string
	HeaderPrefix string
	TokenName    string
	// UseIDToken sends the ID token instead of the access token.
	UseIDToken    bool
	AuthParams    []Extra
	TokenParams   []Extra
	RefreshParams []Extra
	// Static is a token stored in the settings themselves.
	Static string
}

// ConfigFrom reads OAuth 2.0 settings.
func ConfigFrom(p Params) Config {
	c := Config{
		Grant:         p.Or("grant_type", GrantCode),
		AuthURL:       p.Str("authUrl"),
		TokenURL:      p.Str("accessTokenUrl"),
		RefreshURL:    p.Str("refreshTokenUrl"),
		ClientID:      p.Str("clientId"),
		ClientSecret:  p.Str("clientSecret"),
		Scope:         p.Str("scope"),
		State:         p.Str("state"),
		RedirectURI:   p.Str("redirect_uri"),
		Audience:      p.Str("audience"),
		Resource:      p.Str("resource"),
		Username:      p.Str("username"),
		Password:      p.Str("password"),
		ClientAuth:    p.Or("client_authentication", "header"),
		Challenge:     p.Or("challengeAlgorithm", "S256"),
		CodeVerifier:  p.Str("code_verifier"),
		AddTo:         p.Or("addTokenTo", "header"),
		HeaderPrefix:  p.Or("headerPrefix", "Bearer"),
		TokenName:     p.Str("tokenName"),
		UseIDToken:    p.Str("tokenType") == "id_token",
		AuthParams:    p.Extras("authRequestParams"),
		TokenParams:   p.Extras("tokenRequestParams"),
		RefreshParams: p.Extras("refreshRequestParams"),
		Static:        p.Str("accessToken"),
	}
	if c.RefreshURL == "" {
		c.RefreshURL = c.TokenURL
	}
	if c.RedirectURI == "" {
		c.RedirectURI = "https://oauth.pstmn.io/v1/callback" // Postman's
	}
	return c
}

// Key identifies the token a config gets: the same client at the same
// server shares one token, whichever collection asks, unless the configs
// log in as different users or refresh it at different places.
func (c Config) Key() string {
	user := strings.ToLower(c.Hint())
	parts := []string{c.Grant, c.TokenURL, authBase(c.AuthURL), c.ClientID, c.Scope, c.Audience, c.Resource, user}
	// The refresh token goes to the refresh URL, so a config sending it
	// somewhere else never gets another's token. (Most refresh where they
	// get the token; their keys stay what they were.)
	if c.RefreshURL != "" && c.RefreshURL != c.TokenURL {
		parts = append(parts, c.RefreshURL)
	}
	h := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(h[:12])
}

// tokenFrom is where a config's logins send what they get to be traded for
// a token (the code, or the password): the token URL, or for the implicit
// grant, which gets its token from the authorization page, the Auth URL.
func (c Config) tokenFrom() string {
	if c.Grant == GrantImplicit {
		return c.AuthURL
	}
	return c.TokenURL
}

// Fetches says whether posthero can get tokens with this config itself
// (otherwise only a token stored in it can be used).
func (c Config) Fetches() bool {
	switch c.Grant {
	case GrantImplicit:
		return c.AuthURL != ""
	case GrantClient, GrantPassword, GrantCode, GrantPKCE:
		return c.TokenURL != "" && (c.Grant == GrantClient || c.Grant == GrantPassword || c.AuthURL != "")
	}
	return false
}

// IdP names the identity provider for saved logins and sessions: the
// authorization URL without its query, or the token URL.
func (c Config) IdP() string {
	if c.AuthURL != "" {
		return authBase(c.AuthURL)
	}
	return authBase(c.TokenURL)
}

// Issuer is a short name for the identity provider: a Keycloak realm and
// host, or the host.
func (c Config) Issuer() string {
	raw := c.AuthURL
	if raw == "" {
		raw = c.TokenURL
	}
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	if i := strings.Index(u.Path, "/realms/"); i >= 0 {
		realm, _, _ := strings.Cut(u.Path[i+len("/realms/"):], "/")
		return realm + " at " + u.Host
	}
	return u.Host
}

// Hint is who a config logs in as, when it says: the login_hint of the
// authorization URL or of its parameters, or the password grant's user.
func (c Config) Hint() string {
	if c.Grant == GrantPassword {
		return c.Username
	}
	if u, err := url.Parse(c.AuthURL); err == nil {
		if h := u.Query().Get("login_hint"); h != "" {
			return h
		}
	}
	for _, e := range c.AuthParams {
		if e.Key == "login_hint" && e.Value != "" {
			return e.Value
		}
	}
	return ""
}

// session names the cookie jar of the identity provider's session that a
// config's token comes from: one per token, so whoever a token was last
// logged in as is who its session is for.
func (c Config) session() string {
	return c.IdP() + "\x00" + c.Key()
}

func authBase(raw string) string {
	base, _, _ := strings.Cut(raw, "?")
	base, _, _ = strings.Cut(base, "#")
	return base
}

// sameServer reports whether address u is on the server of address base:
// the same host and port by the same scheme, or the same host by https.
func sameServer(u, base string) bool {
	a, err := url.Parse(u)
	if err != nil || a.Host == "" {
		return false
	}
	b, err := url.Parse(base)
	if err != nil || b.Host == "" {
		return false
	}
	return onServer(a, b)
}

func onServer(u, base *url.URL) bool {
	if !strings.EqualFold(u.Hostname(), base.Hostname()) {
		return false
	}
	switch {
	case u.Scheme == base.Scheme:
		return port(u) == port(base)
	case u.Scheme == "https" && base.Scheme == "http":
		return true
	}
	return false
}

// port is a URL's port, with the scheme's default filled in.
func port(u *url.URL) string {
	if p := u.Port(); p != "" {
		return p
	}
	if u.Scheme == "https" {
		return "443"
	}
	return "80"
}

// hostOf is the host (and port) of an address, to name it.
func hostOf(raw string) string {
	if u, err := url.Parse(raw); err == nil && u.Host != "" {
		return u.Host
	}
	return raw
}

// Prompter asks the person at the keyboard for what a login needs. Its
// methods block until they answer (or ctx ends).
type Prompter interface {
	// Login asks for a user name and password.
	Login(ctx context.Context, q LoginPrompt) (LoginAnswer, error)
	// Form asks to fill in a step of a login page posthero doesn't know
	// (a one-time code, a new password, a consent).
	Form(ctx context.Context, q FormPrompt) (FormAnswer, error)
	// Browser asks to log in in the browser at q.URL, which it opens, and
	// returns the address the browser ends up on: what Done delivers, or
	// what is pasted.
	Browser(ctx context.Context, q BrowserPrompt) (string, error)
	// Status says what is happening, for the status bar.
	Status(text string)
}

// NoLogin, as a Prompter, makes a request use a token that is there or
// can be refreshed, and never log in for one: for what posthero sends by
// itself (a GraphQL schema).
var NoLogin Prompter = noLogin{}

type noLogin struct{}

// ErrNotLoggedIn is what a request with NoLogin gets when it would have to
// log in.
var ErrNotLoggedIn = errors.New("not logged in")

func (noLogin) Login(context.Context, LoginPrompt) (LoginAnswer, error) {
	return LoginAnswer{}, ErrNotLoggedIn
}
func (noLogin) Form(context.Context, FormPrompt) (FormAnswer, error) {
	return FormAnswer{}, ErrNotLoggedIn
}
func (noLogin) Browser(context.Context, BrowserPrompt) (string, error) { return "", ErrNotLoggedIn }
func (noLogin) Status(string)                                          {}

// LoginPrompt asks for a user name and password.
type LoginPrompt struct {
	Issuer   string
	Username string
	// Error is why the last attempt didn't work.
	Error string
	// Remember is what the "remember" choice starts as.
	Remember bool
}

// LoginAnswer is what was typed.
type LoginAnswer struct {
	Username, Password string
	Remember           bool
}

// FormPrompt is a step of a login page.
type FormPrompt struct {
	Issuer, Title, Message string
	Fields                 []FormField
	Buttons                []FormButton
}

// FormField is an input of a login page.
type FormField struct {
	Name, Label, Value string
	Secret             bool
}

// FormButton is a submit button.
type FormButton struct{ Name, Value, Label string }

// FormAnswer is the filled-in fields and the button pressed.
type FormAnswer struct {
	Values map[string]string
	Button FormButton
}

// BrowserPrompt asks to log in in the browser.
type BrowserPrompt struct {
	Issuer string
	URL    string
	// Why is why the browser is needed.
	Why string
	// Listening says posthero catches the browser's return by itself; when
	// false the address must be pasted.
	Listening bool
	// Done delivers the address the browser came back to, when listening.
	Done <-chan string
}

// Manager gets, keeps and refreshes OAuth 2.0 tokens.
type Manager struct {
	Tokens   *Store
	Logins   *Logins
	Sessions *Sessions
	// Insecure skips checking the identity provider's certificate, when
	// Transport doesn't give the transport.
	Insecure bool
	// Transport gives the transport to reach the identity provider with:
	// the one requests use, so its CA file, proxy and client certificates
	// count for logins too. nil (or a nil transport) uses Go's default, with
	// Insecure.
	Transport func() http.RoundTripper
	// KeepAlive is how long after its last use a token is kept fresh.
	KeepAlive time.Duration
	// OpenBrowser opens a URL; nil uses the system's opener.
	OpenBrowser func(string) error
	// Now is the clock; nil is time.Now.
	Now func() time.Time

	mu sync.Mutex
	// inflight holds a lock per token: a channel with room for one.
	inflight map[string]chan struct{}
	notes    map[string]string // the last problem keeping a token fresh
}

// NewManager makes a manager keeping tokens, logins and sessions in dir
// (empty keeps them in memory).
func NewManager(dir string) *Manager {
	path := func(name string) string {
		if dir == "" {
			return ""
		}
		return dir + "/" + name
	}
	return &Manager{
		Tokens:    NewStore(path("tokens.json")),
		Logins:    NewLogins(path("logins.json")),
		Sessions:  NewSessions(path("sessions")),
		KeepAlive: 12 * time.Hour,
		inflight:  map[string]chan struct{}{},
		notes:     map[string]string{},
	}
}

func (m *Manager) now() time.Time {
	if m.Now != nil {
		return m.Now()
	}
	return time.Now()
}

// lock serializes getting one token, so a run of requests logs in once. A
// login holds it while it waits for the person, so waiting for it ends with
// ctx.
func (m *Manager) lock(ctx context.Context, key string) (unlock func(), err error) {
	l := m.tokenLock(key)
	select {
	case l <- struct{}{}:
		return func() { <-l }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// tryLock takes a token's lock only when nobody holds it.
func (m *Manager) tryLock(key string) (unlock func(), ok bool) {
	l := m.tokenLock(key)
	select {
	case l <- struct{}{}:
		return func() { <-l }, true
	default:
		return nil, false
	}
}

func (m *Manager) tokenLock(key string) chan struct{} {
	m.mu.Lock()
	defer m.mu.Unlock()
	l, ok := m.inflight[key]
	if !ok {
		l = make(chan struct{}, 1)
		m.inflight[key] = l
	}
	return l
}

// Configure changes KeepAlive and Insecure while the manager is in use:
// tokens are got and kept fresh in the background, which reads them.
func (m *Manager) Configure(keepAlive time.Duration, insecure bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.KeepAlive, m.Insecure = keepAlive, insecure
}

func (m *Manager) options() (keepAlive time.Duration, insecure bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.KeepAlive, m.Insecure
}

// httpClient talks to the identity provider: no redirects followed (posthero
// follows them itself), cookies from jar.
func (m *Manager) httpClient(jar http.CookieJar) *http.Client {
	var t http.RoundTripper
	if m.Transport != nil {
		t = m.Transport()
	}
	if t == nil {
		_, insecure := m.options()
		d := http.DefaultTransport.(*http.Transport).Clone()
		d.TLSClientConfig = &tls.Config{InsecureSkipVerify: insecure} //nolint:gosec // the user's choice
		t = d
	}
	return &http.Client{
		Transport:     t,
		Jar:           jar,
		Timeout:       60 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

// Fetch says how a token was got.
type Fetch int

// How a token came about.
const (
	Cached Fetch = iota
	Refreshed
	LoggedIn
	FromSettings
)

// Fresh, as Token's refused, skips the kept access token whatever it is.
const Fresh = "\x00"

// Token returns a token that is good now: the one kept, refreshed when it
// ran out, or a new one from logging in. p asks the user what a login
// needs; nil logs in only when nobody has to be asked. refused is an
// access token a server refused: the kept token is used only when it is
// another one (a request that waited for the lock gets the token the first
// one fetched, rather than logging in again). Fresh skips the kept token.
func (m *Manager) Token(ctx context.Context, c Config, p Prompter, refused string) (*Token, Fetch, error) {
	if !c.Fetches() {
		if c.Static != "" {
			return &Token{AccessToken: c.Static, TokenType: "Bearer", Name: c.TokenName}, FromSettings, nil
		}
		return nil, 0, missingSettings(c)
	}
	key := c.Key()
	unlock, err := m.lock(ctx, key)
	if err != nil {
		return nil, 0, err
	}
	defer unlock()
	at := m.now()
	m.Tokens.Sync(key) // another posthero may have refreshed it
	t := m.Tokens.Get(key)
	if t != nil && refused != Fresh && t.AccessToken != refused && t.Valid(at) {
		m.Tokens.Touch(key, at)
		return t, Cached, nil
	}
	if t.CanRefresh(at) {
		if p != nil {
			p.Status("refreshing the token for " + c.Issuer())
		}
		nt, err := m.refresh(ctx, c, t)
		if err == nil {
			return nt, Refreshed, nil
		}
		if ctx.Err() != nil {
			return nil, 0, ctx.Err()
		}
		// The session is over; log in again.
	}
	if _, ok := p.(noLogin); ok {
		return nil, 0, ErrNotLoggedIn
	}
	nt, err := m.login(ctx, c, p, t)
	if err != nil {
		return nil, 0, err
	}
	return nt, LoggedIn, nil
}

func missingSettings(c Config) error {
	switch {
	case c.Grant == GrantImplicit:
		return errors.New("the OAuth 2.0 settings have no Auth URL")
	case c.TokenURL == "":
		return errors.New("the OAuth 2.0 settings have no Access Token URL")
	}
	return errors.New("the OAuth 2.0 settings have no Auth URL")
}

// EndSession ends the identity provider's session a config's logins use,
// here (the provider may still keep it).
func (m *Manager) EndSession(c Config) { m.Sessions.Clear(c.session()) }

// Forget drops the token of a config and ends the identity provider's
// session here; with password set, the saved password of whoever the token
// is for goes too.
func (m *Manager) Forget(c Config, password bool) {
	key := c.Key()
	// Not in the middle of a refresh, whose token would come back after,
	// but not for long behind a login waiting for its password either.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if unlock, err := m.lock(ctx, key); err == nil {
		defer unlock()
	}
	t := m.Tokens.Get(key)
	if t != nil && t.RefreshToken != "" {
		m.logoutRemote(c, t)
	}
	// Who is logged in: after ForceLogin, not who the config says.
	user := c.Hint()
	if t != nil && t.Login != "" {
		user = t.Login
	}
	m.Tokens.Delete(key)
	if t != nil {
		m.forgetCopy(c, t)
	}
	m.Sessions.Clear(c.session())
	if password && user != "" {
		m.Logins.Forget(c.IdP(), user)
	}
}

// forgetCopy drops the copy of a password grant's token that ForceLogin
// used to keep under the key of the user it logged in as, besides the
// config's own: the same login, so it goes too.
func (m *Manager) forgetCopy(c Config, t *Token) {
	if c.Grant != GrantPassword || t.Login == "" || strings.EqualFold(t.Login, c.Username) || t.ObtainedAt.IsZero() {
		return
	}
	as := c
	as.Username, as.Password = t.Login, ""
	if cp := m.Tokens.Get(as.Key()); cp != nil && cp.Login == t.Login && cp.ObtainedAt.Equal(t.ObtainedAt) {
		m.Tokens.Delete(as.Key())
	}
}

// logoutRemote ends the session at a Keycloak-style identity provider,
// best effort.
func (m *Manager) logoutRemote(c Config, t *Token) {
	end := logoutURL(c.TokenURL)
	if end == "" || t.TokenURL != "" && !sameServer(end, t.TokenURL) {
		return
	}
	form := url.Values{"refresh_token": {t.RefreshToken}}
	req, err := m.tokenHTTPRequest(context.Background(), end, c, form, nil)
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if resp, err := m.httpClient(nil).Do(req.WithContext(ctx)); err == nil {
		resp.Body.Close()
	}
}

// logoutURL is the logout endpoint next to a Keycloak-style token URL (one
// ending in /protocol/openid-connect/token), on the same server; empty for
// a token URL of another shape.
func logoutURL(tokenURL string) string {
	const tail = "/protocol/openid-connect/token"
	u, err := url.Parse(tokenURL)
	if err != nil || u.Host == "" || !strings.HasSuffix(u.Path, tail) {
		return ""
	}
	u.Path = strings.TrimSuffix(u.Path, tail) + "/protocol/openid-connect/logout"
	u.RawPath = ""
	return u.String()
}

// refresh trades the refresh token for a new access token, at the server
// the token came from only: a refresh token is as good as a login, and a
// config elsewhere in the workspace may name another Refresh Token URL.
func (m *Manager) refresh(ctx context.Context, c Config, t *Token) (*Token, error) {
	endpoint, from := firstNonEmpty(c.RefreshURL, c.TokenURL), firstNonEmpty(t.TokenURL, c.TokenURL)
	if !sameServer(endpoint, from) {
		return nil, fmt.Errorf("posthero doesn't send the refresh token to %s, as the token came from %s (check the Refresh Token URL)", hostOf(endpoint), hostOf(from))
	}
	form := url.Values{"grant_type": {"refresh_token"}, "refresh_token": {t.RefreshToken}}
	if c.Scope != "" {
		form.Set("scope", c.Scope)
	}
	nt, err := m.tokenRequest(ctx, endpoint, c, form, c.RefreshParams)
	if err != nil {
		var te *TokenError
		if errors.As(err, &te) && te.Code == "invalid_grant" {
			m.dropRefresh(t)
		}
		return nil, err
	}
	// Some servers don't send a new refresh token; the old one still works.
	if nt.RefreshToken == "" {
		nt.RefreshToken, nt.RefreshExpiresAt = t.RefreshToken, t.RefreshExpiresAt
	}
	if nt.IDToken == "" {
		nt.IDToken = t.IDToken
	}
	nt.Login = t.Login
	nt.Refreshed = t.Refreshed + 1
	nt.ObtainedAt = t.ObtainedAt
	m.keep(c, nt)
	return nt, nil
}

// dropRefresh lets go of a refresh token the identity provider refused (its
// session ended, or it was revoked): it would be refused every time. The
// access token is kept until it runs out.
func (m *Manager) dropRefresh(t *Token) {
	cur := m.Tokens.Get(t.Key)
	if cur == nil || cur.RefreshToken != t.RefreshToken {
		return
	}
	cur.RefreshToken, cur.RefreshExpiresAt = "", time.Time{}
	m.Tokens.Put(cur)
}

// keep fills in what a token is and saves it.
func (m *Manager) keep(c Config, t *Token) {
	t.Key, t.Name, t.TokenURL, t.ClientID, t.Grant, t.Issuer = c.Key(), c.TokenName, c.TokenURL, c.ClientID, c.Grant, c.Issuer()
	at := m.now()
	if t.ObtainedAt.IsZero() {
		t.ObtainedAt = at
	}
	t.IssuedAt = at
	t.LastUsed = at
	claims, ok := Claims(t.AccessToken)
	if !ok && t.IDToken != "" {
		claims, ok = Claims(t.IDToken)
	}
	if ok {
		if s := Subject(claims); s != "" {
			t.Subject = s
		}
		if t.ExpiresAt.IsZero() {
			t.ExpiresAt = claimTime(claims, "exp")
		}
	}
	if t.RefreshExpiresAt.IsZero() && t.RefreshToken != "" {
		if rc, ok := Claims(t.RefreshToken); ok {
			// A Keycloak offline token has no exp; that is fine as zero.
			t.RefreshExpiresAt = claimTime(rc, "exp")
		}
	}
	if t.Subject == "" {
		t.Subject = t.Login
	}
	m.mu.Lock()
	delete(m.notes, t.Key)
	m.mu.Unlock()
	m.Tokens.Put(t)
}

// tokenRequest posts to a token endpoint and reads the token it answers.
func (m *Manager) tokenRequest(ctx context.Context, endpoint string, c Config, form url.Values, extras []Extra) (*Token, error) {
	if c.Audience != "" && form.Get("audience") == "" {
		form.Set("audience", c.Audience)
	}
	if c.Resource != "" && form.Get("resource") == "" {
		form.Set("resource", c.Resource)
	}
	req, err := m.tokenHTTPRequest(ctx, endpoint, c, form, extras)
	if err != nil {
		return nil, err
	}
	resp, err := m.httpClient(nil).Do(req)
	if err != nil {
		return nil, fmt.Errorf("couldn't reach the token endpoint %s: %w", endpoint, err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return parseTokenResponse(resp.StatusCode, resp.Header.Get("Content-Type"), data, m.now())
}

func (m *Manager) tokenHTTPRequest(ctx context.Context, endpoint string, c Config, form url.Values, extras []Extra) (*http.Request, error) {
	headers := http.Header{}
	u, err := url.Parse(endpoint)
	if err != nil || u.Host == "" {
		return nil, fmt.Errorf("the token URL %q isn't a URL", endpoint)
	}
	q := u.Query()
	for _, e := range extras {
		switch e.SendAs {
		case "request_header":
			headers.Set(e.Key, e.Value)
		case "request_url":
			q.Set(e.Key, e.Value)
		default:
			form.Set(e.Key, e.Value)
		}
	}
	u.RawQuery = q.Encode()
	switch {
	case c.ClientSecret != "" && c.ClientAuth != "body":
		headers.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(url.QueryEscape(c.ClientID)+":"+url.QueryEscape(c.ClientSecret))))
	default:
		form.Set("client_id", c.ClientID)
		if c.ClientSecret != "" {
			form.Set("client_secret", c.ClientSecret)
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header = headers
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	return req, nil
}

// TokenError is a refusal from a token endpoint.
type TokenError struct {
	Status      int
	Code, About string
}

// wrongPassword reports whether the password grant was refused for the
// user name or password, rather than for something about the account
// (Keycloak's "Account is not fully set up", "Account disabled").
func (e *TokenError) wrongPassword() bool {
	return e.Code == "invalid_grant" && (e.About == "" || badCredentials.MatchString(e.About))
}

func (e *TokenError) Error() string {
	msg := e.About
	if msg == "" {
		msg = e.Code
	}
	if msg == "" {
		msg = http.StatusText(e.Status)
	}
	hint := ""
	switch e.Code {
	case "invalid_client", "unauthorized_client":
		hint = " (check the client id and secret)"
	case "invalid_grant":
		hint = " (the session or code is no longer valid)"
	}
	return "the identity provider refused: " + msg + hint
}

func parseTokenResponse(status int, contentType string, data []byte, at time.Time) (*Token, error) {
	fields := map[string]any{}
	if strings.Contains(contentType, "application/x-www-form-urlencoded") || !strings.HasPrefix(strings.TrimSpace(string(data)), "{") {
		vals, err := url.ParseQuery(string(data))
		if err == nil {
			for k := range vals {
				fields[k] = vals.Get(k)
			}
		}
	} else {
		dec := json.NewDecoder(strings.NewReader(string(data)))
		dec.UseNumber()
		_ = dec.Decode(&fields)
	}
	str := func(k string) string {
		switch v := fields[k].(type) {
		case string:
			return v
		case json.Number:
			return v.String()
		}
		return ""
	}
	if status >= 400 || str("access_token") == "" {
		e := &TokenError{Status: status, Code: str("error"), About: str("error_description")}
		if e.Code == "" && e.About == "" {
			text := strings.TrimSpace(string(data))
			if len(text) > 200 {
				text = text[:200] + "…"
			}
			e.About = fmt.Sprintf("HTTP %d %s", status, text)
		}
		return nil, e
	}
	t := &Token{AccessToken: str("access_token"), RefreshToken: str("refresh_token"), IDToken: str("id_token"), TokenType: str("token_type"), Scope: str("scope")}
	if n, err := strconv.ParseFloat(str("expires_in"), 64); err == nil && n > 0 {
		t.ExpiresAt = at.Add(time.Duration(n * float64(time.Second)))
	}
	if n, err := strconv.ParseFloat(str("refresh_expires_in"), 64); err == nil && n > 0 {
		t.RefreshExpiresAt = at.Add(time.Duration(n * float64(time.Second)))
	}
	return t, nil
}

// clientCredentials and passwordGrant get tokens without a browser.
func (m *Manager) clientCredentials(ctx context.Context, c Config) (*Token, error) {
	form := url.Values{"grant_type": {"client_credentials"}}
	if c.Scope != "" {
		form.Set("scope", c.Scope)
	}
	return m.tokenRequest(ctx, c.TokenURL, c, form, c.TokenParams)
}

func (m *Manager) passwordGrant(ctx context.Context, c Config, username, password string) (*Token, error) {
	form := url.Values{"grant_type": {"password"}, "username": {username}, "password": {password}}
	if c.Scope != "" {
		form.Set("scope", c.Scope)
	}
	return m.tokenRequest(ctx, c.TokenURL, c, form, c.TokenParams)
}

// KeepFresh refreshes, in the background, the tokens used lately that are
// about to run out, so the identity provider's session never ends while
// posthero is open. It returns what it refreshed and what failed.
func (m *Manager) KeepFresh(ctx context.Context, configs map[string]Config) (refreshed []string, failed map[string]error) {
	at := m.now()
	keepAlive, _ := m.options()
	for _, t := range m.Tokens.All() {
		if ctx.Err() != nil {
			break
		}
		c, ok := configs[t.Key]
		if !ok || t.LastUsed.IsZero() || at.Sub(t.LastUsed) > keepAlive {
			continue
		}
		if !t.dueForRefresh(at) || !t.CanRefresh(at) {
			continue
		}
		// A token being got right now (a login waiting for its password, a
		// request refreshing it) is left to that.
		unlock, ok := m.tryLock(t.Key)
		if !ok {
			continue
		}
		m.Tokens.Sync(t.Key)
		cur := m.Tokens.Get(t.Key)
		if cur != nil && cur.dueForRefresh(at) && cur.CanRefresh(at) {
			lastUsed := cur.LastUsed
			nt, err := m.refresh(ctx, c, cur)
			if err != nil {
				if failed == nil {
					failed = map[string]error{}
				}
				failed[t.Key] = err
				m.mu.Lock()
				m.notes[t.Key] = err.Error()
				m.mu.Unlock()
			} else {
				// A refresh by itself isn't a use.
				nt.LastUsed = lastUsed
				m.Tokens.Put(nt)
				refreshed = append(refreshed, t.Key)
			}
		}
		unlock()
	}
	return refreshed, failed
}

// Note returns the last problem keeping a token fresh.
func (m *Manager) Note(key string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.notes[key]
}
