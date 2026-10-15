package app

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/shinyvision/posthero/internal/auth"
	"github.com/shinyvision/posthero/internal/demo"
	"github.com/shinyvision/posthero/internal/postman"
)

func (d *driver) tokenUser() string {
	d.t.Helper()
	cfg, _, ok := d.m.currentAuth()
	if !ok {
		d.t.Fatal("the request doesn't log in")
	}
	if t := d.m.authm.Tokens.Get(cfg.Key()); t != nil {
		return t.Subject
	}
	return ""
}

// :auth lists the users, a adds one, a number or ⏎ switches back without
// logging in again, :auth NAME switches directly, and dd removes one.
func TestSwitchUsers(t *testing.T) {
	d := newDriver(t)
	d.open("create order")
	d.sendOpen()
	d.waitFor("Log in to shop")
	d.typeText(demo.User)
	d.keys("<Tab>")
	d.typeText(demo.Password)
	d.keys("<C-s>")
	d.waitFor("201 Created")

	d.cmd("auth")
	d.see("Users", "shop at", "1 ● ada", "signed in")
	// a signs in as someone new: nobody's name is filled in.
	d.keys("a")
	d.waitFor("Log in to shop")
	d.dontSee("Users")
	d.typeText(demo.User2)
	d.keys("<Tab>")
	d.typeText(demo.Password2)
	d.keys("<C-s>")
	d.waitFor("you are grace")
	if got := d.tokenUser(); got != demo.User2 {
		t.Fatalf("logged in as %q", got)
	}

	// Both are listed, and ada's token was kept: 1 switches back at once.
	posts := d.demo.IdP.FormPosts
	d.cmd("auth")
	d.see("1   ada", "ready · switches at once", "2 ● grace")
	d.keys("1")
	d.waitFor("you are ada")
	if got := d.tokenUser(); got != demo.User {
		t.Fatalf("switched to %q", got)
	}
	// :auth NAME (completed with tab) goes straight to the user.
	d.m.focusPane(paneTree)
	d.keys(":")
	d.typeText("auth gr")
	d.keys("<Tab>", "<CR>")
	d.waitFor("you are grace")
	if d.demo.IdP.FormPosts != posts {
		t.Fatalf("switching back and forth logged in %d times", d.demo.IdP.FormPosts-posts)
	}
	d.sendOpen()
	d.waitFor("201 Created")

	// G and ⏎ work too; on the one in use it says so.
	d.cmd("auth")
	d.keys("gg", "G", "<CR>")
	d.waitFor("you are grace already")

	// dd removes ada, after asking.
	d.cmd("auth")
	d.keys("gg", "dd")
	d.see("Remove ada?")
	d.keys("y")
	d.waitFor("removed ada")
	d.see("Users", "1 ● grace")
	if users := d.m.authm.Users(d.cfg()); len(users) != 1 || users[0].Name != demo.User2 {
		t.Fatalf("users after removing ada: %+v", users)
	}
	d.keys("<Esc>")
	d.dontSee("Users")
	if _, ok := d.m.authm.Logins.Password(d.cfg().IdP(), d.cfg().TokenURL, demo.User); ok {
		t.Fatal("ada's password is still saved")
	}
}

func (d *driver) cfg() auth.Config {
	cfg, _, _ := d.m.currentAuth()
	return cfg
}

func TestAuthCommandInheritsFolderLogin(t *testing.T) {
	for _, backend := range []string{"API key", "browser session"} {
		t.Run(backend, func(t *testing.T) {
			var d *driver
			if backend == "API key" {
				d = newDriver(t)
			} else {
				d, _, _ = newSyncDriver(t)
			}
			oauth := func(client string) *postman.Auth {
				a := &postman.Auth{Type: "oauth2"}
				a.Set("grant_type", auth.GrantPassword)
				a.Set("accessTokenUrl", "{{issuer}}/token")
				a.Set("clientId", client)
				return a
			}
			root, own := oauth("{{client}}"), oauth("folder-client")
			sum, err := d.m.api.CreateCollection(context.Background(), d.m.st.Workspace.ID, &postman.Collection{
				Info: postman.Info{Name: "Auth inheritance"}, Auth: root,
				Variable: []postman.Variable{{Key: "issuer", Value: "https://idp.example.test"}, {Key: "client", Value: "root-client"}},
				Item: []*postman.Item{
					{Name: "Inherited login", Folder: true, Item: []*postman.Item{
						{Name: "Nested login", Folder: true, Auth: &postman.Auth{Type: "inherit"}, Item: []*postman.Item{
							{Name: "OAuth request", Request: &postman.Request{Method: "GET", URL: postman.ParseURL("https://example.test"), Auth: &postman.Auth{Type: "inherit"}}},
						}},
					}},
					{Name: "Own login", Folder: true, Auth: own},
					{Name: "Public folder", Folder: true, Auth: &postman.Auth{Type: "noauth"}},
					{Name: "Bearer request", Request: &postman.Request{Method: "GET", URL: postman.ParseURL("https://example.test"), Auth: &postman.Auth{Type: "bearer"}}},
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			d.exec(d.m.refresh(true))
			d.settle()
			col := d.m.st.Col(sum.ID)
			if col == nil || col.Data == nil {
				t.Fatal("fixture wasn't loaded")
			}
			for name, a := range map[string]*postman.Auth{"root-user": root, "folder-user": own} {
				cfg := auth.ConfigFrom(auth.Resolve(a, d.m.scopes(sum.ID).Resolve))
				d.m.authm.Tokens.Put(&auth.Token{Key: cfg.Key(), Subject: name, Login: name, AccessToken: "expired-test-token", ExpiresAt: time.Now().Add(-time.Hour), TokenURL: cfg.TokenURL, ClientID: cfg.ClientID, Grant: cfg.Grant})
			}
			d.open("OAuth request")
			for _, name := range []string{"Inherited login", "Nested login"} {
				d.selectItem(name)
				d.cmd("auth")
				d.see("Users", "root-user")
				d.dontSee("has no users")
				if cfg, _, ok := d.m.currentAuth(); !ok || cfg.ClientID != "root-client" || cfg.TokenURL != "https://idp.example.test/token" {
					t.Fatal("inherited auth didn't resolve its variables")
				}
				d.keys("<Esc>")
			}
			// The status bar and :auth act on the same selection, even while
			// another request remains open in the editor.
			d.selectItem("Own login")
			if line := d.m.authLine(); !strings.Contains(line, "folder-user") || strings.Contains(line, "root-user") {
				t.Fatalf("status shows the wrong login: %s", line)
			}
			d.cmd("auth")
			d.see("Users", "folder-user")
			d.keys("<Esc>")
			d.selectItem("Public folder")
			if _, ok := d.m.userConfig(); ok {
				t.Fatal("noauth inherited the collection's login")
			}
			if line := d.m.authLine(); line != "" {
				t.Fatalf("public folder shows a login: %s", line)
			}
			d.selectItem("Bearer request")
			if line := d.m.authLine(); !strings.Contains(line, "Bearer token") {
				t.Fatalf("status shows the open request instead: %s", line)
			}
			// From either right-hand pane, auth follows the open request.
			for _, focus := range []pane{paneEditor, paneResp} {
				d.m.focusPane(focus)
				if cfg, _, ok := d.m.currentAuth(); !ok || cfg.ClientID != "root-client" {
					t.Fatal("auth didn't follow the focused request")
				}
				if !strings.Contains(d.m.authLine(), "root-user") {
					t.Fatal("status didn't follow the focused request")
				}
			}
			// The response pane accepts app commands; the editor owns its
			// own command line. Keep the tree's different selection in place.
			d.typeText(":auth")
			d.keys("<CR>")
			d.see("Users", "root-user")
			d.keys("<Esc>")
		})
	}
}
