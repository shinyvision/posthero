package auth_test

import (
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"testing"

	"github.com/shinyvision/posthero/internal/auth"
)

// Logging out goes to the logout endpoint next to a Keycloak-style token
// URL, and nowhere for a token URL of another shape.
func TestLogoutEndpoint(t *testing.T) {
	var mu sync.Mutex
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.Method+" "+r.URL.Path)
		mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	m, _ := newManager(t)
	// "/token" is in the path before the end, too: only the end changes.
	c := auth.Config{Grant: auth.GrantPKCE, AuthURL: srv.URL + "/tokens/realms/x/protocol/openid-connect/auth", TokenURL: srv.URL + "/tokens/realms/x/protocol/openid-connect/token", ClientID: "portal", ClientSecret: "s3cret"}
	other := c
	other.TokenURL = srv.URL + "/realms/x/protocol/openid-connect/token/exchange"
	for _, c := range []auth.Config{c, other} {
		m.Tokens.Put(&auth.Token{Key: c.Key(), AccessToken: "x", RefreshToken: "r"})
		m.Forget(c, false)
	}
	mu.Lock()
	defer mu.Unlock()
	if want := []string{"POST /tokens/realms/x/protocol/openid-connect/logout"}; !slices.Equal(seen, want) {
		t.Errorf("logged out at %q", seen)
	}
}
