package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/shinyvision/posthero/internal/config"
	"github.com/shinyvision/posthero/internal/postman"
	"github.com/shinyvision/posthero/internal/postman/pmtest"
	"github.com/shinyvision/posthero/internal/secret"
)

// example is a request's example on the fake server, by name.
func (d *driver) example(request, name string) *postman.Response {
	d.t.Helper()
	for _, c := range d.m.st.Collections() {
		if it := findByName(d.demo.Postman.Collection(c.Summary.ID), request); it != nil {
			for _, r := range it.Response {
				if r.Name == name {
					return r
				}
			}
			return nil
		}
	}
	d.t.Fatalf("no request %s", request)
	return nil
}

// selectExample puts the tree's cursor on an example of the demo.
func (d *driver) selectExample(request, name string) {
	d.t.Helper()
	d.selectItem(request)
	for _, c := range d.m.st.Collections() {
		if it := findByName(c.Data, request); it != nil {
			for _, r := range it.Response {
				if r.Name == name && d.m.tree.Select(r.ID) {
					return
				}
			}
		}
	}
	d.t.Fatalf("no example %s of %s", name, request)
}

// A request's examples fold under it in the tree. One opens like a
// request: its request, then ### response and what came back; the
// response pane shows what it says comes back. :w saves it, u takes that
// back.
func TestExampleOpenEditSave(t *testing.T) {
	d := newDriver(t)
	d.selectItem("Get product")
	d.dontSee("e.g. A product")
	d.keys("<Tab>")
	d.see("e.g. A product", "e.g. No such product")
	d.keys("j") // the cursor on it shows it
	if d.m.open == nil || d.m.open.exID == "" {
		t.Fatalf("not open: %+v", d.m.open)
	}
	d.see("Get product / e.g. A product", "### response 200 OK", "example 200 OK", "saved with GET")
	d.dontSee("0 µs")

	// l on a request opens its examples first, then goes to the editor; o
	// opens and closes them.
	d.keys("k", "o")
	d.dontSee("e.g. A product")
	d.keys("l")
	d.see("e.g. A product")
	if d.m.focus != paneTree {
		t.Fatalf("l on a folded request went to %v", d.m.focus)
	}
	d.keys("l")
	if d.m.focus != paneEditor || d.m.open.exID != "" || d.m.open.itemID != d.m.tree.Current().ID() {
		t.Fatalf("l on an open request: on %v, %+v", d.m.focus, d.m.open)
	}
	d.m.focusPane(paneTree)
	d.keys("j")

	d.m.focusPane(paneEditor)
	d.keys("<Esc>")
	d.typeText(":%s/Mechanical keyboard/Clicky keyboard/")
	d.keys("<CR>")
	d.see("e.g. A product [+]")
	if !d.m.drafts.ids()[d.m.open.exID] {
		t.Fatal("no draft")
	}
	d.typeText(":w")
	d.keys("<CR>")
	d.waitFor("saved A product to Postman")
	ex := d.example("Get product", "A product")
	if !strings.Contains(ex.Body, "Clicky keyboard") || ex.Code != 200 || ex.OriginalRequest.URL.Raw != "{{baseUrl}}/api/products/p-1001" {
		t.Fatalf("Postman has %+v", ex)
	}
	d.dontSee("[+]")
	if d.m.drafts.ids()[d.m.open.exID] {
		t.Fatal("the draft stayed")
	}

	// The status line: a code, and its text.
	d.typeText(":%s/200 OK/201/")
	d.keys("<CR>", ":w<CR>")
	d.waitFor("saved A product")
	if ex := d.example("Get product", "A product"); ex.Code != 201 || ex.Status != "Created" {
		t.Fatalf("status %d %q", ex.Code, ex.Status)
	}
	d.waitFor("example 201 Created") // the response pane shows it saved

	// A mistake says where it is, and saves nothing.
	d.typeText(":%s/### response 201 Created/### response soon/")
	d.keys("<CR>", ":w<CR>")
	d.waitFor("the status starts with its code")
	d.typeText(":e!")
	d.keys("<CR>")

	d.m.focusPane(paneTree)
	d.keys("u")
	d.waitFor("took back: save A product")
	d.keys("u")
	d.waitFor("took back: save A product")
	if ex := d.example("Get product", "A product"); strings.Contains(ex.Body, "Clicky") || ex.Code != 200 {
		t.Fatalf("not undone: %+v", ex)
	}
}

// ⏎ in an example sends its request, with its request's scripts; the
// response is the example's. :example! keeps it in the example.
func TestSendExampleAndKeepTheResponse(t *testing.T) {
	d := newDriver(t)
	d.open("no such product")
	if d.m.open.exID == "" {
		t.Fatal("ctrl+p didn't open the example")
	}
	d.sendOpen()
	d.waitFor("404 Not Found · ")
	d.see("✗ 2 failed", "no product p-9999") // Get product's tests ran
	exID := d.m.open.exID
	if r := d.m.last[exID]; r == nil || r.Status != 404 {
		t.Fatalf("the response isn't the example's: %+v", d.m.last)
	}
	// The request's own last response is untouched.
	if _, ok := d.m.last[d.m.open.itemID]; ok {
		t.Fatal("the request got the example's response")
	}

	// Change what the example says, then keep what really comes back.
	d.typeText(":%s/no product p-9999/nothing here/")
	d.keys("<CR>", ":w<CR>")
	d.waitFor("saved No such product")
	d.cmd("example!")
	d.waitFor("No such product now says 404 Not Found, as it came back")
	ex := d.example("Get product", "No such product")
	if !strings.Contains(ex.Body, "no product p-9999") {
		t.Fatalf("body %q", ex.Body)
	}
	if _, ok := ex.Header.Get("Date"); !ok {
		t.Fatalf("headers %+v", ex.Header)
	}
	d.keys("u")
	d.waitFor("took back: keep the response in No such product")
	if ex := d.example("Get product", "No such product"); !strings.Contains(ex.Body, "nothing here") {
		t.Fatalf("not undone: %q", ex.Body)
	}

	// Without a response of its own, :example! says what to do.
	d.selectExample("Get product", "A product")
	d.cmd("example!")
	d.waitFor("send the example first")
}

// r renames an example, D deletes it, yy and p copy it to a request, dd
// and p move it, :dup copies it next to itself; u takes each back.
func TestExampleTreeChanges(t *testing.T) {
	d := newDriver(t)
	d.selectExample("Get product", "A product")
	d.keys("r", "<C-u>")
	d.typeText("Keyboard")
	d.keys("<CR>")
	d.waitFor("renamed to Keyboard")
	if d.example("Get product", "Keyboard") == nil {
		t.Fatal("not renamed")
	}
	d.keys("u")
	d.waitFor("took back: rename A product")
	if d.example("Get product", "A product") == nil {
		t.Fatal("rename not undone")
	}

	d.selectExample("Get product", "A product")
	d.keys("D")
	d.waitFor("Delete the example A product?")
	d.keys("y")
	d.waitFor("deleted A product · u brings it back")
	if d.example("Get product", "A product") != nil {
		t.Fatal("not deleted")
	}
	d.keys("u")
	d.waitFor("took back: delete A product")
	if ex := d.example("Get product", "A product"); ex == nil || !strings.Contains(ex.Body, "Mechanical keyboard") {
		t.Fatalf("not brought back: %+v", ex)
	}

	// yy and p on another request copy it there.
	d.selectExample("Get product", "No such product")
	d.keys("yy")
	d.waitFor("copied No such product")
	d.selectItem("List products")
	d.keys("p")
	d.waitFor("copied No such product to List products")
	if d.example("List products", "No such product") == nil || d.example("Get product", "No such product") == nil {
		t.Fatal("not copied")
	}
	d.keys("u")
	d.waitFor("took back: copy No such product")
	if d.example("List products", "No such product") != nil {
		t.Fatal("copy not undone")
	}

	// dd and p on another request's example move it to that request.
	d.selectExample("Get product", "No such product")
	d.keys("dd")
	d.selectExample("List products", "Search for desks")
	d.keys("p")
	d.waitFor("moved No such product to List products")
	if d.example("List products", "No such product") == nil || d.example("Get product", "No such product") != nil {
		t.Fatal("not moved")
	}
	d.keys("u")
	d.waitFor("took back: move No such product")
	if d.example("List products", "No such product") != nil || d.example("Get product", "No such product") == nil {
		t.Fatal("move not undone")
	}

	// An example goes under a request, nowhere else.
	d.selectExample("Get product", "A product")
	d.keys("yy")
	d.selectItem("Orders")
	d.keys("p")
	d.waitFor("an example goes under a request")

	d.selectExample("Get product", "A product")
	d.cmd("dup")
	d.waitFor("copied A product to Get product")
	if d.example("Get product", "A product Copy") == nil {
		t.Fatal(":dup made no copy")
	}

	// Deleting a request says its examples go with it.
	d.selectItem("Get product")
	d.keys("D")
	d.waitFor("With its 3 examples. u brings it all back.")
	d.keys("n")
}

// :example saves the response shown as an example of its request, with
// the request as it was sent; with none shown, it makes a blank example
// and opens it to fill in.
func TestMakeExamples(t *testing.T) {
	d := newDriver(t)
	d.open("list products")
	d.m.focusPane(paneEditor)
	d.keys("<Esc>")
	d.typeText(":%s/# &q=/\\&q=key/")
	d.keys("<CR>")
	d.sendOpen()
	d.waitFor("200 OK · ")
	d.cmd("example Keys")
	d.waitFor("saved Keys as an example of List products to Postman")
	ex := d.example("List products", "Keys")
	if ex == nil || ex.Code != 200 || !strings.Contains(ex.Body, "Mechanical keyboard") || ex.PreviewLanguage != "json" {
		t.Fatalf("example %+v", ex)
	}
	if q, _ := ex.OriginalRequest.URL.Query.Get("q"); q != "key" {
		t.Fatalf("not the request as sent: %+v", ex.OriginalRequest.URL)
	}
	d.see("e.g. Keys")

	d.open("cancel order")
	d.cmd("example Cancelled")
	d.waitFor("made Cancelled, an example of Cancel order")
	if d.m.open == nil || d.m.open.exID == "" || d.m.focus != paneEditor {
		t.Fatalf("the blank example isn't open: %+v", d.m.open)
	}
	d.see("Cancel order / e.g. Cancelled", "### response 200 OK")
	d.keys("<Esc>")
	d.typeText(":%s/200 OK/204/")
	d.keys("<CR>", ":w<CR>")
	d.waitFor("saved Cancelled")
	if ex := d.example("Cancel order", "Cancelled"); ex.Code != 204 || ex.OriginalRequest.Method != "DELETE" {
		t.Fatalf("example %+v", ex)
	}
}

// ge and :examples list a request's examples to open one; / and ctrl+p
// find them by name.
func TestFindExamples(t *testing.T) {
	d := newDriver(t)
	d.selectItem("Get product")
	d.keys("ge")
	d.waitFor("Examples of Get product")
	d.see("e.g. A product", "404 Not Found")
	d.typeText("such")
	d.keys("<CR>")
	if d.m.open == nil || d.docNameOpen() != "Get product · No such product" {
		t.Fatalf("open: %s", d.docNameOpen())
	}

	d.m.focusPane(paneTree)
	d.keys("/")
	d.typeText("desks")
	d.keys("<CR>")
	if n := d.m.tree.Current(); n.Example == nil || n.Example.Name != "Search for desks" {
		t.Fatalf("the filter is on %s", n.Name())
	}
	d.keys("<Esc>")

	d.keys("<C-p>")
	d.typeText("desks")
	d.see("e.g. Search for desks", "Shop API / Products / List products")
	d.keys("<CR>")
	if d.docNameOpen() != "List products · Search for desks" {
		t.Fatalf("open: %s", d.docNameOpen())
	}
	d.see("### response 200 OK", "\"name\": \"Desk lamp\"") // JSON on one line, spread out

	// K says what an example holds.
	d.m.focusPane(paneTree)
	d.keys("K")
	d.waitFor("an example of List products")
	d.see("says 200 OK")
}

func (d *driver) docNameOpen() string {
	if d.m.open == nil {
		return ""
	}
	return d.m.docName(d.m.open.docRef)
}

// An example's draft survives a teammate's change, which :e! takes; one
// without a draft shows the change at once.
func TestExampleTeammateChanges(t *testing.T) {
	d := newDriver(t)
	d.open("a product")
	team := func(body string) {
		d.teamEdit("Shop API", "Get product", func(it *postman.Item) { it.Response[0].Body = body })
	}
	team(`{"id": "p-1001", "teammate": 1}`)
	d.m.focusPane(paneTree)
	d.keys("R")
	d.waitFor(`"teammate": 1`)

	d.m.focusPane(paneEditor)
	d.keys("<Esc>")
	d.typeText(":%s/teammate/mine/")
	d.keys("<CR>")
	team(`{"id": "p-1001", "teammate": 2}`)
	d.m.focusPane(paneTree)
	d.keys("R")
	d.waitFor("someone changed this example in Postman; your changes are kept")
	d.see(`"mine": 1`)

	// Saving asks first: their version, or yours.
	d.m.focusPane(paneEditor)
	d.typeText(":w")
	d.keys("<CR>")
	d.waitFor("Changed in Postman since you opened it")
	d.keys("d")
	d.waitFor("- theirs  + yours")
	d.see(`-   "teammate": 2`, `+   "mine": 1`)
	d.keys("q")
	d.keys("t")
	d.waitFor(`"teammate": 2`)
	if strings.Contains(d.example("Get product", "A product").Body, "mine") {
		t.Fatal("take theirs saved mine")
	}

	// Again, and this time overwrite theirs.
	d.typeText(":%s/teammate/mine/")
	d.keys("<CR>")
	team(`{"id": "p-1001", "teammate": 3}`)
	d.m.focusPane(paneTree)
	d.keys("R")
	d.waitFor("someone changed this example in Postman")
	d.m.focusPane(paneEditor)
	d.typeText(":w")
	d.keys("<CR>")
	d.waitFor("Changed in Postman since you opened it")
	d.keys("o")
	d.waitFor("saved A product")
	if !strings.Contains(d.example("Get product", "A product").Body, `"mine": 2`) {
		t.Fatalf("not overwritten: %q", d.example("Get product", "A product").Body)
	}
	// Saved, it starts from what Postman has: the next save asks nothing.
	d.typeText(":%s/mine/ours/")
	d.keys("<CR>", ":w<CR>")
	d.waitFor("saved A product")
	d.dontSee("Changed in Postman since you opened it")
}

// In local mode, examples change in the local copy; :push sends them.
func TestExamplesLocalMode(t *testing.T) {
	d := newDriver(t)
	d.cmd("set local")
	d.waitFor("local mode: changes stay on this machine")
	d.waitFor("Shop API")
	before := d.writes()
	d.open("a product")
	d.m.focusPane(paneEditor)
	d.keys("<Esc>")
	d.typeText(":%s/129.0/99.0/")
	d.keys("<CR>", ":w<CR>")
	d.waitFor("saved A product locally (:push sends it)")
	d.selectExample("Get product", "No such product")
	d.keys("r", "<C-u>")
	d.typeText("Missing")
	d.keys("<CR>")
	d.waitFor("renamed to Missing")
	if d.writes() != before {
		t.Fatal("a change in local mode reached Postman")
	}
	d.waitFor("local · 1 to push")
	d.cmd("push")
	d.waitForNo("to push")
	if ex := d.example("Get product", "A product"); !strings.Contains(ex.Body, "99.0") {
		t.Fatalf("not pushed: %q", ex.Body)
	}
	if d.example("Get product", "Missing") == nil {
		t.Fatal("the rename wasn't pushed")
	}
}

// The open example comes back after a restart.
func TestExampleReopens(t *testing.T) {
	d := newDriver(t)
	d.m.opts.SaveState = func(config.State) error { return nil } // keep where it is
	d.open("no such product")
	d.restart()
	d.waitFor("e.g. No such product")
	if d.m.open == nil || d.m.open.exID == "" {
		t.Fatalf("open: %+v", d.m.open)
	}
	d.see("### response 404 Not Found")
}

// A collection opened from a file keeps its examples in the file: ids
// posthero made up stay out of it.
func TestExamplesInAFile(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir+"/config")
	t.Setenv("XDG_STATE_HOME", dir+"/state")
	t.Setenv("XDG_CACHE_HOME", dir+"/cache")
	t.Setenv("POSTHERO_EDITOR", "builtin")
	secret.MockInit()
	path := filepath.Join(dir, "shop.postman_collection.json")
	col := `{"info": {"name": "Files", "schema": "https://schema.getpostman.com/json/collection/v2.1.0/collection.json"},
		"item": [{"name": "Ping", "request": {"method": "GET", "url": "http://localhost:1/ping"},
			"response": [{"name": "Pong", "code": 200, "status": "OK", "body": "pong"}]}]}`
	if err := os.WriteFile(path, []byte(col), 0o600); err != nil {
		t.Fatal(err)
	}
	m := New(Options{Files: []string{path}, RC: config.RC{Settings: config.DefaultSettings()}, Dir: dir, StateDir: dir + "/state"})
	d := &driver{t: t, m: m, msgs: make(chan tea.Msg, 1024)}
	m.send = func(msg tea.Msg) { d.msgs <- msg }
	t.Cleanup(m.Close)
	d.handle(tea.WindowSizeMsg{Width: 160, Height: 45})
	d.exec(m.Init())
	d.settle()

	d.keys("<C-p>")
	d.typeText("pong")
	d.keys("<CR>")
	d.see("### response 200 OK", "pong")
	d.keys("<Esc>")
	d.typeText(":%s/pong/PONG/")
	d.keys("<CR>", ":w<CR>")
	d.waitFor("saved Pong")
	d.m.focusPane(paneTree)
	d.keys(":dup<CR>")
	d.waitFor("copied Pong")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var got postman.Collection
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	rs := got.Item[0].Response
	if len(rs) != 2 || rs[0].Body != "PONG" || rs[1].Name != "Pong Copy" {
		t.Fatalf("the file has %s", data)
	}
	if strings.Contains(string(data), `"ph-`) {
		t.Fatalf("made-up ids went into the file:\n%s", data)
	}
}

// Every key binding works on an example too, changing nothing in Postman
// that wasn't asked for.
func TestEveryBindingOnAnExample(t *testing.T) {
	d := newDriver(t)
	mayWrite := map[string]bool{"put": true, "putabove": true, "undo": true}
	for _, pn := range []pane{paneTree, paneResp} {
		for _, b := range bindings {
			if b.action == "quit" || b.where&map[pane]where{paneTree: inTree, paneResp: inResp}[pn] == 0 {
				continue
			}
			d.selectExample("Get product", "A product")
			d.m.focusPane(pn)
			before := d.writes()
			func() {
				defer func() {
					if r := recover(); r != nil {
						t.Fatalf("%s (%s) on an example panicked: %v", b.keys, b.action, r)
					}
				}()
				d.m.do(b.action, 0)
				d.settle()
				d.screen()
			}()
			d.closeAll()
			if d.writes() != before && !mayWrite[b.action] {
				t.Errorf("%s (%s) on an example changed something in Postman", b.keys, b.action)
			}
		}
	}
}

// What posthero makes in a teammate's collection is the API key's user's
// in Postman: its uid has that user's id, not the collection owner's.
// Saving, renaming and deleting it afterwards works all the same.
func TestChangeWhatWasMadeHere(t *testing.T) {
	d := newDriver(t)
	d.selectExample("Get product", "A product")
	d.cmd("dup")
	d.waitFor("copied A product to Get product")
	if owner, _ := postman.SplitUID(d.example("Get product", "A product Copy").UID); owner != pmtest.Caller {
		t.Fatalf("the copy is %s's, not the caller's", owner)
	}
	d.open("a product copy")
	d.m.focusPane(paneEditor)
	d.keys("<Esc>")
	d.typeText(":%s/Mechanical keyboard/Clicky keyboard/")
	d.keys("<CR>", ":w<CR>")
	d.waitFor("saved A product Copy to Postman")
	if ex := d.example("Get product", "A product Copy"); !strings.Contains(ex.Body, "Clicky keyboard") {
		t.Fatalf("Postman has %q", ex.Body)
	}
	d.m.focusPane(paneTree)
	d.selectExample("Get product", "A product Copy")
	d.keys("r", "<C-u>")
	d.typeText("Mine")
	d.keys("<CR>")
	d.waitFor("renamed to Mine")
	d.selectExample("Get product", "Mine")
	d.keys("D", "y")
	d.waitFor("deleted Mine")
	if d.example("Get product", "Mine") != nil {
		t.Fatal("not deleted in Postman")
	}

	// A request made here, and an example of it.
	d.selectItem("Get product")
	d.keys("a")
	d.typeText("Made here")
	d.keys("<CR>")
	d.waitFor("made Made here")
	if it := findByName(d.colByName("Shop API"), "Made here"); it == nil || !strings.HasPrefix(it.UID, pmtest.Caller+"-") {
		t.Fatalf("made %+v", it)
	}
	d.m.focusPane(paneEditor)
	d.keys("<Esc>")
	d.typeText(":%s/^GET/POST/")
	d.keys("<CR>", ":w<CR>")
	d.waitFor("saved Made here")
	if it := findByName(d.colByName("Shop API"), "Made here"); it.Request.Method != "POST" {
		t.Fatalf("Postman has %s", it.Request.Method)
	}
	d.cmd("example Mine")
	d.waitFor("made Mine, an example of Made here")
	d.keys("<Esc>")
	d.typeText(":%s/200 OK/201/")
	d.keys("<CR>", ":w<CR>")
	d.waitFor("saved Mine")
	if ex := d.example("Made here", "Mine"); ex == nil || ex.Code != 201 {
		t.Fatalf("Postman has %+v", ex)
	}
}
