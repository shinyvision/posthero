package auth

import (
	"net/url"
	"strings"
	"testing"
)

func TestLogoutURL(t *testing.T) {
	for in, want := range map[string]string{
		"https://sso.example.com/realms/x/protocol/openid-connect/token":          "https://sso.example.com/realms/x/protocol/openid-connect/logout",
		"http://tokens.example.test/realms/x/protocol/openid-connect/token":       "http://tokens.example.test/realms/x/protocol/openid-connect/logout",
		"https://sso.example.com/auth/realms/x/protocol/openid-connect/token?a=1": "https://sso.example.com/auth/realms/x/protocol/openid-connect/logout?a=1",
		"https://sso.example.com/realms/x/protocol/openid-connect/token/x":        "",
		"https://sso.example.com/oauth2/token":                                    "",
		"not a url":                                                               "",
	} {
		if got := logoutURL(in); got != want {
			t.Errorf("%s: %q, want %q", in, got, want)
		}
	}
}

func TestSameServer(t *testing.T) {
	for _, c := range []struct {
		u, base string
		want    bool
	}{
		{"https://sso.example.com/token", "https://SSO.example.com:443/auth", true},
		{"https://sso.example.com:8443/token", "http://sso.example.com/auth", true},
		{"http://sso.example.com/token", "https://sso.example.com/auth", false},
		{"https://sso.example.com:8443/token", "https://sso.example.com/auth", false},
		{"https://sso.example.com.evil.test/token", "https://sso.example.com/auth", false},
		{"", "https://sso.example.com/auth", false},
	} {
		if got := sameServer(c.u, c.base); got != c.want {
			t.Errorf("%s on %s: %v", c.u, c.base, got)
		}
	}
}

// What a login page asks for, and what it says when a password is wrong.
func TestLoginPageRules(t *testing.T) {
	form := func(body string) (*page, *htmlForm) {
		t.Helper()
		base, _ := url.Parse("https://sso.example.com/realms/x/login-actions/authenticate?session_code=1")
		pg, err := parsePage(base, strings.NewReader("<html><body>"+body+"</body></html>"))
		if err != nil || pg.loginForm() == nil {
			t.Fatalf("%s: %v", body, err)
		}
		return pg, pg.loginForm()
	}
	_, f := form(`<form method="post"><input name="username"><input type="password" name="password" autocomplete="current-password"></form>`)
	if f.newPassword() {
		t.Error("a login form sets a password")
	}
	_, f = form(`<form method="post"><input type="password" name="password-new" autocomplete="new-password"><input type="password" name="password-confirm" autocomplete="new-password"></form>`)
	if !f.newPassword() || f.newPasswordValue(url.Values{"password-new": {"n"}, "password-confirm": {"n"}}) != "n" || f.newPasswordValue(url.Values{"password-new": {"n"}, "password-confirm": {"m"}}) != "" {
		t.Error("Keycloak's update password form")
	}
	_, f = form(`<form method="post"><input type="password" name="current"><input type="password" name="next" autocomplete="new-password"></form>`)
	if !f.newPassword() || f.newPasswordValue(url.Values{"current": {"o"}, "next": {"n"}}) != "n" {
		t.Error("a form with the current and a new password")
	}

	for body, refused := range map[string]bool{
		`<span id="input-error">Invalid username or password.</span><form method="post"><input name="username"><input type="password" name="password"></form>`: true,
		// Keycloak in Dutch: the inputs say it.
		`<span id="input-error">Ongeldige gebruikersnaam of wachtwoord.</span><form method="post"><input name="username" aria-invalid="true"><input type="password" name="password" aria-invalid="true"></form>`: true,
		`<div class="alert-error">Your account or password is incorrect.</div><form method="post"><input name="login"><input type="password" name="passwd"></form>`:                                              true,
		`<div class="alert-error">Account is disabled, contact your administrator.</div><form method="post"><input name="username"><input type="password" name="password"></form>`:                               false,
		`<div class="alert-error">Account is temporarily disabled; contact your administrator or retry later.</div><form method="post"><input name="username"><input type="password" name="password"></form>`:    false,
		`<form method="post"><input name="username"><input type="password" name="password"></form>`:                                                                                                              false,
	} {
		pg, f := form(body)
		if got := refusedLogin(pg, f); got != refused {
			t.Errorf("%s: refused %v", body, got)
		}
	}
	for about, wrong := range map[string]bool{"Invalid user credentials": true, "": true, "Account is not fully set up": false, "Account disabled": false} {
		if got := (&TokenError{Code: "invalid_grant", About: about}).wrongPassword(); got != wrong {
			t.Errorf("%q: wrong password %v", about, got)
		}
	}
}
