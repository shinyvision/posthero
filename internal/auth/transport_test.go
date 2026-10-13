package auth_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/shinyvision/posthero/internal/auth"
)

// roundTripper is a transport made of a function.
type roundTripper func(*http.Request) (*http.Response, error)

func (f roundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// Token requests go through the transport the manager is given (the one
// requests use, with their CA file, proxy and client certificates); without
// one, through Go's default.
func TestTransportForTokens(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"access_token":"tok","expires_in":300}`)
	}))
	defer srv.Close()
	m, _ := newManager(t)
	c := auth.Config{Grant: auth.GrantClient, TokenURL: srv.URL + "/token", ClientID: "svc", ClientSecret: "x"}
	// Go's default doesn't trust the test server's certificate.
	if _, _, err := m.Token(context.Background(), c, nil, ""); err == nil || !strings.Contains(err.Error(), "certificate") {
		t.Fatalf("without the transport: %v", err)
	}
	m.Transport = func() http.RoundTripper { return srv.Client().Transport }
	if tok, _, err := m.Token(context.Background(), c, nil, ""); err != nil || tok.AccessToken != "tok" {
		t.Fatalf("with the transport: %+v %v", tok, err)
	}
	// A transport that can't be had leaves Go's default.
	m.Transport = func() http.RoundTripper { return nil }
	if _, _, err := m.Token(context.Background(), c, nil, auth.Fresh); err == nil || !strings.Contains(err.Error(), "certificate") {
		t.Fatalf("without a transport to give: %v", err)
	}
}

// The login pages go through it as well, with posthero's own handling of
// redirects and the session's cookies, and so does logging out, at the
// server the token came from.
func TestTransportForLogins(t *testing.T) {
	idp, _, m, _ := setup(t)
	var mu sync.Mutex
	var seen []string
	m.Transport = func() http.RoundTripper {
		return roundTripper(func(r *http.Request) (*http.Response, error) {
			mu.Lock()
			seen = append(seen, r.Method+" "+r.URL.Host+r.URL.Path)
			mu.Unlock()
			if r.URL.Hostname() == "tokens.example.test" {
				return &http.Response{StatusCode: http.StatusNoContent, Body: http.NoBody, Header: http.Header{}, Request: r}, nil
			}
			return http.DefaultTransport.RoundTrip(r)
		})
	}
	c := auth.Config{Grant: auth.GrantPKCE, AuthURL: idp.AuthURL() + "?login_hint=rachel", TokenURL: idp.TokenURL(), ClientID: "portal", RedirectURI: "http://localhost:3000/cb", Challenge: "S256"}
	p := &prompter{logins: []auth.LoginAnswer{{Username: "rachel", Password: "hunter2"}}}
	if _, _, err := m.Token(context.Background(), c, p, ""); err != nil {
		t.Fatal(err)
	}
	realm := strings.TrimPrefix(idp.URL(), "http://") + "/realms/system"
	want := []string{
		"GET " + realm + "/protocol/openid-connect/auth",
		"POST " + realm + "/login-actions/authenticate",
		"POST " + realm + "/protocol/openid-connect/token",
	}
	mu.Lock()
	if strings.Join(seen, "\n") != strings.Join(want, "\n") {
		t.Errorf("went to:\n%s", strings.Join(seen, "\n"))
	}
	seen = nil
	mu.Unlock()

	// A host that starts with "token" is not a token URL's "/token".
	k := auth.Config{Grant: auth.GrantPKCE, AuthURL: "http://tokens.example.test/realms/x/protocol/openid-connect/auth", TokenURL: "http://tokens.example.test/realms/x/protocol/openid-connect/token", ClientID: "portal", ClientSecret: "s3cret"}
	m.Tokens.Put(&auth.Token{Key: k.Key(), AccessToken: "x", RefreshToken: "r", TokenURL: k.TokenURL})
	m.Forget(k, false)
	mu.Lock()
	defer mu.Unlock()
	if len(seen) != 1 || seen[0] != "POST tokens.example.test/realms/x/protocol/openid-connect/logout" {
		t.Errorf("logged out at %q", seen)
	}
}
