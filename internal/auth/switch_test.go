package auth_test

import (
	"context"
	"testing"

	"github.com/shinyvision/posthero/internal/auth"
	"github.com/shinyvision/posthero/internal/auth/idptest"
	"github.com/shinyvision/posthero/internal/secret"
)

func twoUsers(t *testing.T) (*idptest.IdP, *auth.Manager, auth.Config) {
	t.Helper()
	secret.MockInit()
	idp := idptest.New()
	idp.Realm = "system"
	idp.AddUser(idptest.User{Name: "alice", Password: "pw-a"})
	idp.AddUser(idptest.User{Name: "bob", Password: "pw-b"})
	idp.AddClient(idptest.Client{ID: "portal", Redirects: []string{"http://localhost:3000*"}})
	idp.AddClient(idptest.Client{ID: "admin", Redirects: []string{"http://localhost:3000*"}})
	idp.Start()
	t.Cleanup(idp.Close)
	m := auth.NewManager(t.TempDir())
	c := auth.Config{Grant: auth.GrantPKCE, AuthURL: idp.AuthURL(), TokenURL: idp.TokenURL(), ClientID: "portal", RedirectURI: "http://localhost:3000/cb", Challenge: "S256", Scope: "openid"}
	return idp, m, c
}

func names(us []auth.User) (out []string) {
	for _, u := range us {
		mark := ""
		if u.Current {
			mark = "*"
		}
		out = append(out, mark+u.Name)
	}
	return out
}

// Switching between two users logs each in once; after that, switching
// back and forth takes the token kept for them, without a login.
func TestSwitchUsers(t *testing.T) {
	idp, m, c := twoUsers(t)
	ctx := context.Background()
	p := &prompter{logins: []auth.LoginAnswer{{Username: "alice", Password: "pw-a", Remember: true}}}
	if _, _, err := m.Token(ctx, c, p, ""); err != nil {
		t.Fatal(err)
	}
	// Adding bob asks who, with nobody's name filled in.
	p.logins = []auth.LoginAnswer{{Username: "bob", Password: "pw-b", Remember: true}}
	tb, err := m.SwitchUser(ctx, c, p, "")
	if err != nil || tb.Subject != "bob" {
		t.Fatalf("as bob: %+v %v", tb, err)
	}
	if q := p.asked[len(p.asked)-1]; q.Username != "" {
		t.Errorf("the login for someone new had %q filled in", q.Username)
	}
	if got := names(m.Users(c)); len(got) != 2 || got[0] != "alice" || got[1] != "*bob" {
		t.Fatalf("users: %v", got)
	}

	posts, silent := idp.FormPosts, idp.SilentLogins
	for _, who := range []string{"alice", "bob", "alice"} {
		tok, err := m.SwitchUser(ctx, c, &prompter{}, who)
		if err != nil || tok.Subject != who {
			t.Fatalf("back to %s: %+v %v", who, tok, err)
		}
		if got := m.Tokens.Get(c.Key()); got.Subject != who {
			t.Fatalf("the config's token is %s's, not %s's", got.Subject, who)
		}
	}
	if idp.FormPosts != posts || idp.SilentLogins != silent {
		t.Errorf("switching logged in: %d posts, %d silent logins", idp.FormPosts-posts, idp.SilentLogins-silent)
	}
	users := m.Users(c)
	if got := names(users); got[0] != "*alice" || got[1] != "bob" {
		t.Fatalf("users: %v", got)
	}
	for _, u := range users {
		if !u.Password || !u.Ready(idp.Now()) {
			t.Errorf("%s: password %v, ready %v", u.Name, u.Password, u.Ready(idp.Now()))
		}
	}

	// The token kept for bob is gone (his session ended): switching to him
	// logs him in with his saved password, asking nobody.
	idp.EndSessions()
	idp.RevokeAccess()
	m.Tokens.Delete(c.Key() + "@bob")
	quiet := &prompter{}
	if tok, err := m.SwitchUser(ctx, c, quiet, "bob"); err != nil || tok.Subject != "bob" || len(quiet.asked) != 0 {
		t.Fatalf("bob again: %+v %v, asked %d", tok, err, len(quiet.asked))
	}
}

// A switch whose login is cancelled leaves the user that was logged in.
func TestSwitchCancelled(t *testing.T) {
	_, m, c := twoUsers(t)
	ctx := context.Background()
	if _, _, err := m.Token(ctx, c, &prompter{logins: []auth.LoginAnswer{{Username: "alice", Password: "pw-a"}}}, ""); err != nil {
		t.Fatal(err)
	}
	before := m.Tokens.Get(c.Key())
	if _, err := m.SwitchUser(ctx, c, &prompter{}, ""); err == nil {
		t.Fatal("logged in as nobody")
	}
	if got := names(m.Users(c)); len(got) != 1 || got[0] != "*alice" {
		t.Fatalf("users: %v", got)
	}
	if got := m.Tokens.Get(c.Key()); got.AccessToken != before.AccessToken {
		t.Fatal("alice's token changed")
	}
	if m.Tokens.Get(c.Key()+"@alice") != nil {
		t.Fatal("a copy of alice's token was left behind")
	}
}

// Removing a user forgets their password and kept token; removing the
// one logged in logs out.
func TestRemoveUser(t *testing.T) {
	_, m, c := twoUsers(t)
	ctx := context.Background()
	p := &prompter{logins: []auth.LoginAnswer{{Username: "alice", Password: "pw-a", Remember: true}, {Username: "bob", Password: "pw-b", Remember: true}}}
	if _, _, err := m.Token(ctx, c, p, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := m.SwitchUser(ctx, c, p, ""); err != nil {
		t.Fatal(err)
	}
	m.RemoveUser(c, "alice")
	if got := names(m.Users(c)); len(got) != 1 || got[0] != "*bob" {
		t.Fatalf("after removing alice: %v", got)
	}
	if _, ok := m.Logins.Password(c.IdP(), c.TokenURL, "alice"); ok {
		t.Error("alice's password is still saved")
	}
	m.RemoveUser(c, "bob")
	if got := m.Users(c); len(got) != 0 {
		t.Fatalf("after removing bob: %v", names(got))
	}
	if m.Tokens.Get(c.Key()) != nil {
		t.Error("still logged in as bob")
	}
}

// Another client at the same identity provider follows a switch: it takes
// the token kept for the user, or logs in as them on its next request.
func TestFollowSwitch(t *testing.T) {
	_, m, c := twoUsers(t)
	ctx := context.Background()
	admin := c
	admin.ClientID = "admin"
	p := &prompter{logins: []auth.LoginAnswer{{Username: "alice", Password: "pw-a", Remember: true}, {Username: "bob", Password: "pw-b", Remember: true}}}
	for _, cfg := range []auth.Config{c, admin} {
		if _, _, err := m.Token(ctx, cfg, p, ""); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := m.SwitchUser(ctx, c, p, ""); err != nil {
		t.Fatal(err)
	}
	if err := m.Follow(ctx, admin, "bob"); err != nil {
		t.Fatal(err)
	}
	quiet := &prompter{}
	tok, _, err := m.Token(ctx, admin, quiet, "")
	if err != nil || tok.Subject != "bob" || len(quiet.asked) != 0 {
		t.Fatalf("admin: %+v %v, asked %d", tok, err, len(quiet.asked))
	}
	// And back: alice's admin token was kept.
	if err := m.Follow(ctx, admin, "alice"); err != nil {
		t.Fatal(err)
	}
	if got := m.Tokens.Get(admin.Key()); got == nil || got.Subject != "alice" {
		t.Fatalf("admin back to alice: %+v", got)
	}
}
