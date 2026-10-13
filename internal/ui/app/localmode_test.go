package app

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/shinyvision/posthero/internal/local"
	"github.com/shinyvision/posthero/internal/postman"
)

// teamEdit changes a request in (the demo's) Postman, as a teammate.
func (d *driver) teamEdit(col, name string, f func(it *postman.Item)) {
	d.t.Helper()
	id := ""
	for _, c := range d.m.st.Collections() {
		if c.Name() == col {
			id = c.Summary.ID
		}
	}
	d.demo.Postman.Edit(id, func(c *postman.Collection) *postman.Item {
		it := findByName(c, name)
		f(it)
		return it
	})
}

func (d *driver) cmd(line string) {
	d.t.Helper()
	d.m.focusPane(paneTree)
	d.keys(":")
	d.typeText(line)
	d.keys("<CR>")
}

// Local mode: saves stay on this machine; :pull takes the team's changes
// in; :push sends yours, asking about each conflict (overwrite, show the
// diff, skip); the diffs open in tabs where :w pushes and c merges.
func TestLocalMode(t *testing.T) {
	d := newDriver(t)
	d.cmd("set local")
	d.waitFor("local mode: changes stay on this machine")
	d.waitFor("Shop API")
	if !d.m.state.Local[d.m.st.Workspace.ID] || !local.Exists(d.m.localDir(d.m.st.Workspace.ID)) {
		t.Fatal("local mode isn't remembered for the workspace")
	}

	// A save goes to the local copy, not to Postman.
	before := d.writes()
	d.open("get product")
	d.m.focusPane(paneEditor)
	d.keys("<Esc>")
	d.typeText(":%s/:id/:productId/g")
	d.keys("<CR>")
	d.typeText(":w")
	d.keys("<CR>")
	d.waitFor("saved Get product locally (:push sends it)")
	if d.writes() != before {
		t.Fatal("a save in local mode reached Postman")
	}
	if it := findByName(d.colByName("Shop API"), "Get product"); strings.Contains(it.Request.URL.Raw, ":productId") {
		t.Fatal("Postman has the local change")
	}
	d.waitFor("local · 1 to push")
	if !d.m.localMarks[d.m.open.itemID] {
		t.Fatal("the tree doesn't mark the changed request")
	}

	// The team changes another request: :pull takes it in.
	d.teamEdit("Shop API", "List products", func(it *postman.Item) { it.Request.Method = "HEAD" })
	d.cmd("pull")
	d.waitFor("pulled 1 change from the team")
	d.waitFor("HEAD List products")

	// The team changes the request changed here too: :push asks.
	d.teamEdit("Shop API", "Get product", func(it *postman.Item) { it.Request.Method = "PATCH" })
	d.teamEdit("Shop API", "My orders", func(it *postman.Item) { it.Request.Method = "OPTIONS" })
	d.cmd("push")
	d.waitFor("Changed here and in Postman")
	d.see("Shop API / Products / Get product")
	d.keys("d")
	d.waitFor("1 diff to look at")
	d.waitFor("Conflicts · 1 of 1")
	d.see("- PATCH", "+ GET")
	if it := findByName(d.colByName("Shop API"), "Get product"); strings.Contains(it.Request.URL.Raw, ":productId") {
		t.Fatal("show diff pushed it")
	}
	// :w in the diff pushes mine.
	d.keys(":", "w", "<CR>")
	d.waitFor("pushed Shop API / Products / Get product")
	if it := findByName(d.colByName("Shop API"), "Get product"); !strings.Contains(it.Request.URL.Raw, ":productId") || it.Request.Method != "GET" {
		t.Fatalf("Postman's Get product: %s %s", it.Request.Method, it.Request.URL.Raw)
	}
	if len(d.m.modals) != 0 {
		t.Fatal("the diff stayed open")
	}
	d.settle()
	if c := d.m.st.Col(d.m.open.colID); findByName(c.Data, "My orders").Request.Method != "OPTIONS" {
		t.Fatal("the team's change didn't come in with the push")
	}
	d.waitForNo("to push")

	// Leaving local mode without changes asks nothing.
	d.cmd("set nolocal")
	d.waitFor("local mode off")
	if d.m.state.Local[d.m.st.Workspace.ID] || local.Exists(d.m.localDir(d.m.st.Workspace.ID)) {
		t.Fatal("the local copy is still there")
	}
	d.open("get product")
	d.m.focusPane(paneEditor)
	d.keys("<Esc>")
	d.typeText(":%s/:productId/:pid/g")
	d.keys("<CR>")
	d.typeText(":w")
	d.keys("<CR>")
	d.waitFor("saved Get product to Postman")
	if it := findByName(d.colByName("Shop API"), "Get product"); !strings.Contains(it.Request.URL.Raw, ":pid") {
		t.Fatal("out of local mode, a save didn't reach Postman")
	}
}

// Merging a conflict in the editor: c opens both versions with conflict
// markers; once merged (and the editor closed) the diff shows the merged
// text, and :w pushes it. Overwrite and skip answer the other questions.
func TestLocalMerge(t *testing.T) {
	d := newDriver(t)
	d.cmd("set local")
	d.waitFor("local mode: changes stay on this machine")
	d.waitFor("Shop API")
	edit := func(name, sub string) {
		d.open(name)
		d.m.focusPane(paneEditor)
		d.keys("<Esc>")
		d.typeText(sub)
		d.keys("<CR>")
		d.typeText(":w")
		d.keys("<CR>")
		d.settle()
	}
	edit("get product", ":1s/GET/PUT/")
	edit("list products", ":1s/GET/PUT/")
	edit("my orders", ":1s/GET/PUT/")
	d.teamEdit("Shop API", "Get product", func(it *postman.Item) {
		it.Request.Header = append(it.Request.Header, postman.Param{Key: "X-Team", Value: "1"})
		it.Request.Method = "PATCH"
	})
	d.teamEdit("Shop API", "List products", func(it *postman.Item) { it.Request.Method = "PATCH" })
	d.teamEdit("Shop API", "My orders", func(it *postman.Item) { it.Request.Method = "PATCH" })
	d.cmd("push")
	// Three questions: overwrite one, diff one, skip one (in the plan's order).
	answers := map[string]string{"Shop API / Products / List products": "o", "Shop API / Products / Get product": "d", "Shop API / Orders / My orders": "s"}
	for range 3 {
		d.waitFor("Changed here and in Postman")
		var key string
		for name, k := range answers {
			if strings.Contains(d.screen(), name) {
				key = k
			}
		}
		if key == "" {
			t.Fatalf("which question?\n%s", d.screen())
		}
		d.keys(key)
		d.settle()
	}
	d.waitFor("Conflicts · 1 of 1")
	if it := findByName(d.colByName("Shop API"), "List products"); it.Request.Method != "PUT" {
		t.Fatal("overwrite didn't push")
	}
	if it := findByName(d.colByName("Shop API"), "My orders"); it.Request.Method != "PATCH" {
		t.Fatal("skip pushed")
	}
	// c: the merge, with markers.
	d.keys("c")
	d.waitFor("Merge Shop API / Products / Get product")
	if !strings.Contains(d.screen(), "<<<<<<< yours") {
		t.Fatalf("no conflict markers:\n%s", d.screen())
	}
	// Keep Postman's method: delete the markers and my line.
	d.keys("<Esc>")
	d.typeText("gg3ddjdd")
	d.typeText(":wq")
	d.keys("<CR>")
	d.waitFor("merged · :w pushes it")
	if strings.Contains(d.screen(), "<<<<<<<") {
		t.Fatalf("markers left:\n%s", d.screen())
	}
	d.keys(":", "w", "<CR>")
	d.waitFor("pushed Shop API / Products / Get product")
	it := findByName(d.colByName("Shop API"), "Get product")
	if it.Request.Method != "PATCH" || len(it.Request.Header) == 0 {
		t.Fatalf("the merge in Postman: %s %+v", it.Request.Method, it.Request.Header)
	}
	d.waitFor("local · 1 to push") // List orders, skipped
}

// waitForNo waits until the screen doesn't show text.
func (d *driver) waitForNo(text string) {
	d.t.Helper()
	for range 40 {
		if !strings.Contains(d.screen(), text) {
			return
		}
		d.settle()
	}
	d.t.Fatalf("screen still shows %q:\n%s", text, d.screen())
}

// restart starts posthero again on the same state, as after quitting.
func (d *driver) restart() {
	d.t.Helper()
	d.settle()
	d.m.Close()
	opts := d.m.opts
	opts.State = d.m.state
	m := New(opts)
	d.m = m
	m.send = func(msg tea.Msg) { d.msgs <- msg }
	d.t.Cleanup(m.Close)
	d.handle(tea.WindowSizeMsg{Width: 160, Height: 45})
	d.exec(m.Init())
	d.settle()
}

// Local mode is remembered per workspace: after a restart the workspace is
// still local, with its changes.
func TestLocalModeAfterRestart(t *testing.T) {
	d := newDriver(t)
	d.cmd("set local")
	d.waitFor("local mode: changes stay on this machine")
	d.waitFor("Shop API")
	d.open("get product")
	d.m.focusPane(paneEditor)
	d.keys("<Esc>")
	d.typeText(":1s/GET/PUT/")
	d.keys("<CR>")
	d.typeText(":w")
	d.keys("<CR>")
	d.waitFor("local · 1 to push")
	d.restart()
	if d.m.local == nil {
		t.Fatal("not local after a restart")
	}
	d.waitFor("local · 1 to push")
	for _, c := range d.m.st.Collections() {
		if c.Name() == "Shop API" {
			if it := findByName(c.Data, "Get product"); it == nil || it.Request.Method != "PUT" {
				t.Fatal("the local change isn't there after a restart")
			}
		}
	}
	// Another workspace isn't local.
	d.demo.Postman.AddWorkspace("ws2", "Personal")
	d.m.workspaces = append(d.m.workspaces, postman.Workspace{ID: "ws2", Name: "Personal"})
	d.cmd("workspace Personal")
	d.waitFor("workspace Personal")
	if d.m.local != nil || strings.Contains(d.screen(), "local ·") {
		t.Fatal("the other workspace is local too")
	}
}
