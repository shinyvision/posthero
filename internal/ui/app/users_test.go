package app

import (
	"testing"

	"github.com/shinyvision/posthero/internal/auth"
	"github.com/shinyvision/posthero/internal/demo"
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
