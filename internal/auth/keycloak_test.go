package auth_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/shinyvision/posthero/internal/auth"
	"github.com/shinyvision/posthero/internal/postman"
	"github.com/shinyvision/posthero/internal/secret"
	"github.com/shinyvision/posthero/internal/send"
	"github.com/shinyvision/posthero/internal/vars"
)

// TestKeycloak logs in to a real Keycloak at $POSTHERO_KEYCLOAK (e.g.
// http://127.0.0.1:18080) with realm "system", a public client "portal"
// (redirect http://localhost:3000/*), a confidential client "backend"
// (secret s3cret) and the user control@example.invalid / ctrl-pass.
// Admin access (admin/admin) changes a password along the way.
func TestKeycloak(t *testing.T) {
	kc := os.Getenv("POSTHERO_KEYCLOAK")
	if kc == "" {
		t.Skip("POSTHERO_KEYCLOAK is not set")
	}
	secret.MockInit()
	m := auth.NewManager(t.TempDir())
	env := vars.New(&vars.Scope{Kind: vars.Environment, Vars: []vars.Var{
		{Key: "KeycloakURL", Value: kc}, {Key: "Realm", Value: "system"}, {Key: "ClientID", Value: "portal"},
		{Key: "AuthCallbackURL", Value: "http://localhost:3000"},
	}})
	// A collection's auth as teams set it up in Postman: Keycloak with a
	// login hint.
	shop := &postman.Auth{}
	_ = json.Unmarshal([]byte(`{"type":"oauth2","oauth2":[
		{"key":"authUrl","value":"{{KeycloakURL}}/realms/{{Realm}}/protocol/openid-connect/auth?login_hint=control%40example.invalid","type":"string"},
		{"key":"grant_type","value":"authorization_code_with_pkce","type":"string"},
		{"key":"redirect_uri","value":"{{AuthCallbackURL}}","type":"string"},
		{"key":"scope","value":"openid","type":"string"},
		{"key":"clientId","value":"{{ClientID}}","type":"string"},
		{"key":"accessTokenUrl","value":"{{KeycloakURL}}/realms/{{Realm}}/protocol/openid-connect/token","type":"string"},
		{"key":"tokenName","value":"Shop Token","type":"string"},
		{"key":"addTokenTo","value":"header","type":"string"}]}`), shop)
	userinfo := kc + "/realms/system/protocol/openid-connect/userinfo"
	do := func(a *postman.Auth, p auth.Prompter) (*send.Response, *auth.Used) {
		t.Helper()
		req := &send.Request{Method: "GET", URL: &postman.URL{Raw: userinfo}, Settings: send.DefaultSettings()}
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
	cfg := auth.ConfigFrom(auth.Resolve(shop, env.Resolve))

	// First login: posthero asks for the password (the user name comes from
	// login_hint) and fills in Keycloak's own login form.
	p := &prompter{logins: []auth.LoginAnswer{{Username: "control@example.invalid", Password: "ctrl-pass", Remember: true}}}
	resp, used := do(shop, p)
	if resp.Status != 200 || !strings.Contains(string(resp.Body), `"preferred_username":"control@example.invalid"`) {
		t.Fatalf("first login: %d %s (asked %+v)", resp.Status, resp.Body, p.asked)
	}
	if len(p.asked) != 1 || p.asked[0].Username != "control@example.invalid" || used.Token.Subject != "control@example.invalid" {
		t.Fatalf("asked %+v, subject %q", p.asked, used.Token.Subject)
	}
	if used.Token.Issuer != "system at "+strings.TrimPrefix(strings.TrimPrefix(kc, "http://"), "https://") {
		t.Errorf("issuer: %q", used.Token.Issuer)
	}

	// Kept, then refreshed once it runs out.
	_, used = do(shop, p)
	if used.Fetch != auth.Cached {
		t.Errorf("second call: %v", used.Fetch)
	}
	tok := m.Tokens.Get(cfg.Key())
	tok.ExpiresAt = time.Now().Add(-time.Minute)
	m.Tokens.Put(tok)
	resp, used = do(shop, p)
	if resp.Status != 200 || used.Fetch != auth.Refreshed {
		t.Errorf("refresh: %d %v", resp.Status, used.Fetch)
	}

	// Without a refresh token, Keycloak's session logs in again by itself.
	tok = m.Tokens.Get(cfg.Key())
	tok.ExpiresAt, tok.RefreshToken = time.Now().Add(-time.Minute), ""
	m.Tokens.Put(tok)
	resp, used = do(shop, p)
	if resp.Status != 200 || used.Fetch != auth.LoggedIn || len(p.asked) != 1 {
		t.Errorf("silent login: %d %v asked %d", resp.Status, used.Fetch, len(p.asked))
	}

	// Without the session, the saved password fills in the form.
	m.EndSession(cfg)
	tok = m.Tokens.Get(cfg.Key())
	tok.ExpiresAt, tok.RefreshToken = time.Now().Add(-time.Minute), ""
	m.Tokens.Put(tok)
	resp, used = do(shop, p)
	if resp.Status != 200 || used.Fetch != auth.LoggedIn || len(p.asked) != 1 {
		t.Errorf("form login with the saved password: %d %v asked %d", resp.Status, used.Fetch, len(p.asked))
	}

	// A changed password: Keycloak's error is shown and posthero asks again.
	setPassword(t, kc, "control@example.invalid", "new-pass")
	defer setPassword(t, kc, "control@example.invalid", "ctrl-pass")
	m.EndSession(cfg)
	tok = m.Tokens.Get(cfg.Key())
	tok.ExpiresAt, tok.RefreshToken = time.Now().Add(-time.Minute), ""
	m.Tokens.Put(tok)
	p.logins = []auth.LoginAnswer{{Username: "control@example.invalid", Password: "new-pass", Remember: true}}
	resp, _ = do(shop, p)
	if resp.Status != 200 || len(p.asked) != 2 || p.asked[1].Error != "Invalid username or password." {
		t.Errorf("changed password: %d asked %+v", resp.Status, p.asked)
	}

	// Logging out ends it; the next request logs in through the form again.
	m.Forget(cfg, false)
	if m.Tokens.Get(cfg.Key()) != nil {
		t.Error("token kept after forgetting")
	}
	resp, used = do(shop, p)
	if resp.Status != 200 || used.Fetch != auth.LoggedIn || len(p.asked) != 2 {
		t.Errorf("after logout: %d %v asked %d", resp.Status, used.Fetch, len(p.asked))
	}

	// Other grants.
	other := func(kv map[string]string) *postman.Auth {
		a := &postman.Auth{Type: "oauth2", Params: map[string][]postman.AuthParam{}}
		base := map[string]string{"accessTokenUrl": "{{KeycloakURL}}/realms/{{Realm}}/protocol/openid-connect/token", "authUrl": "{{KeycloakURL}}/realms/{{Realm}}/protocol/openid-connect/auth", "scope": "openid", "redirect_uri": "http://localhost:3000"}
		for k, v := range kv {
			base[k] = v
		}
		for k, v := range base {
			raw, _ := json.Marshal(v)
			a.Params["oauth2"] = append(a.Params["oauth2"], postman.AuthParam{Key: k, Value: raw})
		}
		return a
	}
	if resp, _ := do(other(map[string]string{"grant_type": "client_credentials", "clientId": "backend", "clientSecret": "s3cret"}), nil); resp.Status != 200 {
		t.Errorf("client credentials: %d %s", resp.Status, resp.Body)
	}
	if resp, _ := do(other(map[string]string{"grant_type": "password_credentials", "clientId": "portal", "username": "rachel", "password": "hunter2"}), nil); resp.Status != 200 {
		t.Errorf("password grant: %d %s", resp.Status, resp.Body)
	}
	p.logins = []auth.LoginAnswer{{Username: "rachel", Password: "hunter2"}}
	if resp, _ := do(other(map[string]string{"grant_type": "implicit", "clientId": "portal"}), p); resp.Status != 200 {
		t.Errorf("implicit: %d %s", resp.Status, resp.Body)
	}
	p.logins = []auth.LoginAnswer{{Username: "rachel", Password: "hunter2"}}
	if resp, _ := do(other(map[string]string{"grant_type": "authorization_code", "clientId": "backend", "clientSecret": "s3cret", "client_authentication": "body"}), p); resp.Status != 200 {
		t.Errorf("authorization code, confidential: %d %s", resp.Status, resp.Body)
	}
}

func setPassword(t *testing.T, kc, user, password string) {
	t.Helper()
	form := url.Values{"client_id": {"admin-cli"}, "username": {"admin"}, "password": {"admin"}, "grant_type": {"password"}}
	resp, err := http.PostForm(kc+"/realms/master/protocol/openid-connect/token", form)
	if err != nil {
		t.Fatal(err)
	}
	var tok struct {
		AccessToken string `json:"access_token"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&tok)
	resp.Body.Close()
	get, _ := http.NewRequest("GET", kc+"/admin/realms/system/users?exact=true&username="+url.QueryEscape(user), nil)
	get.Header.Set("Authorization", "Bearer "+tok.AccessToken)
	resp, err = http.DefaultClient.Do(get)
	if err != nil {
		t.Fatal(err)
	}
	var users []struct {
		ID string `json:"id"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&users)
	resp.Body.Close()
	if len(users) != 1 {
		t.Fatalf("user %s not found", user)
	}
	body := `{"type":"password","value":"` + password + `","temporary":false}`
	put, _ := http.NewRequest("PUT", kc+"/admin/realms/system/users/"+users[0].ID+"/reset-password", strings.NewReader(body))
	put.Header.Set("Authorization", "Bearer "+tok.AccessToken)
	put.Header.Set("Content-Type", "application/json")
	resp, err = http.DefaultClient.Do(put)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 204 {
		t.Fatalf("reset password: %d", resp.StatusCode)
	}
}
