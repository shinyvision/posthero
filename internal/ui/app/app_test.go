package app

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/shinyvision/posthero/internal/config"
	"github.com/shinyvision/posthero/internal/demo"
	"github.com/shinyvision/posthero/internal/postman"
	"github.com/shinyvision/posthero/internal/secret"
)

// driver runs the app against the demo (a fake Postman, a fake Keycloak
// and the shop) the way Bubble Tea would: commands run in goroutines and
// their messages come back to Update.
type driver struct {
	t        *testing.T
	m        *Model
	demo     *demo.Demo
	msgs     chan tea.Msg
	quit     bool
	inflight atomic.Int32
}

func newDriver(t *testing.T) *driver {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir+"/config")
	t.Setenv("XDG_STATE_HOME", dir+"/state")
	t.Setenv("XDG_CACHE_HOME", dir+"/cache")
	t.Setenv("POSTHERO_EDITOR", "builtin")
	secret.MockInit()
	dm, err := demo.Start()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(dm.Close)
	settings := config.DefaultSettings()
	settings.Refresh = 0
	m := New(Options{
		Login:    config.Login{APIKey: dm.APIKey},
		HasLogin: true,
		APIBase:  dm.APIURL,
		RC:       config.RC{Settings: settings},
		State:    config.State{Workspace: dm.Workspace},
		Dir:      dir,
		StateDir: dir + "/state",
		CacheDir: dir + "/cache",
	})
	d := &driver{t: t, m: m, demo: dm, msgs: make(chan tea.Msg, 1024)}
	m.send = func(msg tea.Msg) { d.msgs <- msg }
	t.Cleanup(m.Close)
	d.handle(tea.WindowSizeMsg{Width: 160, Height: 45})
	d.exec(m.Init())
	d.settle()
	return d
}

func (d *driver) exec(cmd tea.Cmd) {
	if cmd == nil {
		return
	}
	d.inflight.Add(1)
	go func() {
		msg := cmd()
		d.inflight.Add(-1)
		d.msgs <- msg
	}()
}

func (d *driver) handle(msg tea.Msg) {
	switch msg := msg.(type) {
	case nil, tickMsg:
	case tea.BatchMsg:
		for _, c := range msg {
			d.exec(c)
		}
	case tea.QuitMsg:
		d.quit = true
	default:
		_, cmd := d.m.Update(msg)
		d.exec(cmd)
	}
}

// settle delivers messages until none arrive for a while: the editor and
// prompts wait for keys forever, so "nothing running" never comes.
func (d *driver) settle() {
	d.t.Helper()
	deadline := time.After(15 * time.Second)
	for {
		select {
		case msg := <-d.msgs:
			d.handle(msg)
		case <-time.After(300 * time.Millisecond):
			return
		case <-deadline:
			d.t.Fatal("the app never settled")
		}
	}
}

// keys presses keys written in vim notation ("dd", "<CR>", "<C-p>").
func (d *driver) keys(ks ...string) {
	d.t.Helper()
	for _, s := range ks {
		for _, k := range splitKeys(s) {
			d.handle(keyMsg(k))
		}
		d.settle()
	}
}

// typeText types literal text.
func (d *driver) typeText(s string) {
	d.t.Helper()
	for _, r := range s {
		d.handle(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	d.settle()
}

func keyMsg(k string) tea.KeyPressMsg {
	named := map[string]rune{"<CR>": tea.KeyEnter, "<Esc>": tea.KeyEscape, "<Tab>": tea.KeyTab, "<BS>": tea.KeyBackspace, "<Up>": tea.KeyUp, "<Down>": tea.KeyDown}
	if c, ok := named[k]; ok {
		return tea.KeyPressMsg{Code: c}
	}
	if strings.HasPrefix(k, "<C-S-") && len(k) == 7 {
		return tea.KeyPressMsg{Code: rune(k[5]), Mod: tea.ModCtrl | tea.ModShift}
	}
	if strings.HasPrefix(k, "<C-") && len(k) == 5 {
		return tea.KeyPressMsg{Code: rune(k[3]), Mod: tea.ModCtrl}
	}
	if k == " " {
		return tea.KeyPressMsg{Code: tea.KeySpace, Text: " "}
	}
	r := []rune(k)[0]
	return tea.KeyPressMsg{Code: r, Text: k}
}

func (d *driver) screen() string { return ansi.Strip(d.m.View().Content) }

func (d *driver) see(want ...string) {
	d.t.Helper()
	s := d.screen()
	for _, w := range want {
		if !strings.Contains(s, w) {
			d.t.Fatalf("screen lacks %q:\n%s", w, s)
		}
	}
}

func (d *driver) dontSee(not ...string) {
	d.t.Helper()
	s := d.screen()
	for _, w := range not {
		if strings.Contains(s, w) {
			d.t.Fatalf("screen has %q:\n%s", w, s)
		}
	}
}

// waitFor settles until the screen shows want.
func (d *driver) waitFor(want string) {
	d.t.Helper()
	for range 40 {
		if strings.Contains(d.screen(), want) {
			return
		}
		select {
		case msg := <-d.msgs:
			d.handle(msg)
		case <-time.After(100 * time.Millisecond):
		}
	}
	d.t.Fatalf("screen never showed %q:\n%s", want, d.screen())
}

// open finds a request with ctrl+p and opens it in the editor.
func (d *driver) open(query string) {
	d.t.Helper()
	d.keys("<C-p>")
	d.typeText(query)
	d.keys("<CR>")
	if d.m.open == nil {
		d.t.Fatalf("%q didn't open a request:\n%s", query, d.screen())
	}
}

// sendOpen sends the request in the editor (⏎ in normal mode).
func (d *driver) sendOpen() {
	d.t.Helper()
	d.m.focusPane(paneEditor)
	d.keys("<Esc>", "<CR>")
}

// colByName is the demo's collection on the fake server.
func (d *driver) colByName(name string) *postman.Collection {
	d.t.Helper()
	for _, c := range d.m.st.Collections() {
		if c.Name() == name {
			return d.demo.Postman.Collection(c.Summary.ID)
		}
	}
	d.t.Fatalf("no collection %q", name)
	return nil
}

func findByName(c *postman.Collection, name string) *postman.Item {
	var out *postman.Item
	c.Walk(func(it *postman.Item, _ []*postman.Item) bool {
		if it.Name == name {
			out = it
		}
		return out == nil
	})
	return out
}

func TestStartShowsTheWorkspace(t *testing.T) {
	d := newDriver(t)
	d.see("Team", "Shop API", "Shop GraphQL")
	d.keys("l")
	d.see("Products", "Orders", "Tools")
}

func TestSendAndTests(t *testing.T) {
	d := newDriver(t)
	d.open("list products")
	d.see("Products / List products", "{{baseUrl}}/api/products")
	// The environment that goes with the collection is picked by itself.
	d.see("Shop · Local")
	d.sendOpen()
	d.waitFor("200 OK")
	d.see("Mechanical keyboard", "tests ✓ 2 passed")
	// The test script set a collection variable; Get product uses it.
	d.open("get product")
	d.sendOpen()
	d.waitFor(`"stock": 14`)
}

func TestLoginOnceThenSilently(t *testing.T) {
	d := newDriver(t)
	d.open("create order")
	d.sendOpen()
	d.waitFor("Log in to shop")
	d.typeText(demo.User)
	d.keys("<Tab>")
	d.typeText(demo.Password)
	d.keys("<C-s>")
	d.waitFor("201 Created")
	d.see(`"productId": "p-1001"`)
	if d.demo.IdP.FormPosts == 0 {
		t.Fatal("posthero didn't fill in the login form")
	}

	// GraphQL in the other collection logs in the same way: same token.
	d.open("place order")
	d.sendOpen()
	d.waitFor(`"createOrder"`)
	d.dontSee("Log in to shop")

	// The token runs out: posthero refreshes it, without a word.
	key := ""
	for _, tok := range d.m.authm.Tokens.All() {
		key = tok.Key
	}
	tok := d.m.authm.Tokens.Get(key)
	tok.ExpiresAt = time.Now().Add(-time.Minute)
	d.m.authm.Tokens.Put(tok)
	refreshes := d.demo.IdP.Refreshes
	d.open("my orders")
	d.sendOpen()
	d.waitFor("200 OK")
	if d.demo.IdP.Refreshes == refreshes {
		t.Fatal("the token wasn't refreshed")
	}

	// The session is over and the refresh token is dead: posthero logs in
	// again with the saved password, filling in the form itself.
	d.demo.IdP.EndSessions()
	d.demo.IdP.RevokeAccess()
	tok = d.m.authm.Tokens.Get(key)
	tok.ExpiresAt = time.Now().Add(-time.Minute)
	tok.RefreshToken = "dead"
	d.m.authm.Tokens.Put(tok)
	posts := d.demo.IdP.FormPosts
	d.open("create order")
	d.sendOpen()
	d.waitFor("201 Created")
	d.dontSee("Log in to shop")
	if d.demo.IdP.FormPosts == posts {
		t.Fatal("posthero didn't log in again by itself")
	}
}

func TestEditAndSave(t *testing.T) {
	d := newDriver(t)
	d.open("get product")
	d.m.focusPane(paneEditor)
	d.keys("<Esc>")
	d.typeText(":%s/api\\/products/api\\/items/")
	d.keys("<CR>")
	// Not saved yet: a draft.
	d.see("[+]")
	d.typeText(":w")
	d.keys("<CR>")
	d.waitFor("saved Get product to Postman")
	it := findByName(d.colByName("Shop API"), "Get product")
	if it == nil || !strings.Contains(it.Request.URL.Raw, "/api/items/") {
		t.Fatalf("Postman has %+v", it.Request.URL)
	}
	d.dontSee("[+]")

	// u in the tree undoes the save, in Postman too.
	d.m.focusPane(paneTree)
	d.keys("u")
	d.waitFor("took back: save Get product")
	it = findByName(d.colByName("Shop API"), "Get product")
	if strings.Contains(it.Request.URL.Raw, "/api/items/") {
		t.Fatalf("not undone: %s", it.Request.URL.Raw)
	}
}

func TestConflictWhenSomeoneElseSaved(t *testing.T) {
	d := newDriver(t)
	d.open("get product")
	// A teammate changes the request in Postman.
	colID := d.m.open.colID
	d.demo.Postman.Edit(colID, func(c *postman.Collection) *postman.Item {
		it := findByName(c, "Get product")
		it.Request.Header = append(it.Request.Header, postman.Param{Key: "X-Teammate", Value: "was here"})
		return it
	})
	d.m.focusPane(paneEditor)
	d.keys("<Esc>")
	d.typeText(":%s/:id/:productId/g")
	d.keys("<CR>")
	d.typeText(":w")
	d.keys("<CR>")
	d.waitFor("Changed in Postman since you opened it")
	d.keys("d")
	d.waitFor("- theirs  + yours")
	d.see("- X-Teammate: was here", "+     :productId = {{productId}}")
	d.keys("q")
	d.see("Changed in Postman since you opened it")
	d.keys("t") // take theirs
	d.waitFor("X-Teammate")
}

func TestProductionAsksFirst(t *testing.T) {
	d := newDriver(t)
	d.open("place order")
	d.keys("<Esc>")
	d.m.focusPane(paneTree)
	d.keys("E")
	d.typeText("production")
	d.keys("<CR>")
	d.see("Shop · Production")
	d.sendOpen()
	d.waitFor("Send to Shop · Production?")
	d.keys("n")
	d.dontSee("Log in to shop")
	if len(d.demo.Shop.Orders()) != 0 {
		t.Fatal("the order went out")
	}
}

func TestTreeChanges(t *testing.T) {
	d := newDriver(t)
	d.keys("l", "j", "l") // Shop API › Products, open
	d.keys("a")
	d.typeText("Search the catalogue")
	d.keys("<CR>")
	d.waitFor("made Search the catalogue")
	c := d.colByName("Shop API")
	it := findByName(c, "Search the catalogue")
	if it == nil {
		t.Fatal("the request isn't in Postman")
	}
	// A new request starts with the URL its neighbours start with.
	if it.Request.URL.Raw != "{{baseUrl}}/" {
		t.Errorf("new request's URL = %q", it.Request.URL.Raw)
	}
	d.m.focusPane(paneTree)
	d.m.tree.Select(it.ID)
	d.keys("r", "<C-u>")
	d.typeText("Search")
	d.keys("<CR>")
	d.waitFor("Search")
	if findByName(d.colByName("Shop API"), "Search") == nil {
		t.Fatal("not renamed in Postman")
	}
	d.keys("D", "y")
	d.waitFor("deleted Search")
	if findByName(d.colByName("Shop API"), "Search") != nil {
		t.Fatal("not deleted in Postman")
	}
	d.keys("u")
	d.waitFor("took back: delete Search")
	if findByName(d.colByName("Shop API"), "Search") == nil {
		t.Fatal("not brought back in Postman")
	}
}

func TestRunner(t *testing.T) {
	d := newDriver(t)
	// The runner logs in like a single request does.
	d.keys("j", "S") // Shop GraphQL
	d.waitFor("Log in to shop")
	d.typeText(demo.User)
	d.keys("<Tab>")
	d.typeText(demo.Password)
	d.keys("<C-s>")
	d.waitFor("ran 4 requests")
	d.see("QRY  Products", "MUT  Place order")
}

func TestRefreshSeesTeamChanges(t *testing.T) {
	d := newDriver(t)
	d.keys("l", "j", "l")
	d.see("List products")
	colID := d.m.tree.Current().Col.Summary.ID
	d.demo.Postman.Edit(colID, func(c *postman.Collection) *postman.Item {
		it := findByName(c, "List products")
		it.Name = "List all products"
		return it
	})
	d.keys("R")
	d.waitFor("List all products")
}

func TestCollectionSettings(t *testing.T) {
	d := newDriver(t)
	d.keys("e") // on Shop API
	d.waitFor("Collection · Shop API")
	d.typeText(":%s/X-Request-Id/X-Trace-Id/")
	d.keys("<CR>")
	d.typeText(":%s/clientId: shop-web/clientId: shop-cli/")
	d.keys("<CR>")
	d.typeText(":w")
	d.keys("<CR>")
	d.waitFor("saved the collection to Postman")
	// The dialog stays after :w, as vim does, and :q closes it.
	if d.m.edit == nil {
		t.Fatal(":w closed the editor")
	}
	d.typeText(":q")
	d.keys("<CR>")
	if d.m.edit != nil {
		t.Fatal(":q didn't close the editor")
	}
	c := d.colByName("Shop API")
	if got := postman.Scripts(c.Event, "prerequest"); !strings.Contains(got, "X-Trace-Id") {
		t.Fatalf("the script in Postman: %q", got)
	}
	if c.Auth == nil || c.Auth.Type != "oauth2" || !strings.Contains(string(mustJSON(t, c.Auth)), "shop-cli") {
		t.Fatalf("the auth in Postman: %s", mustJSON(t, c.Auth))
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestYourValues(t *testing.T) {
	d := newDriver(t)
	d.open("list products")
	d.m.focusPane(paneTree)
	d.keys("gv")
	d.waitFor("Variables · Shop API")
	d.typeText(":%s/pageSize = 10/pageSize = 3/")
	d.keys("<CR>")
	d.typeText(":w")
	d.keys("<CR>")
	d.waitFor("kept your values")
	d.typeText(":q")
	d.keys("<CR>")
	// Your value is the one sent; the team still has theirs.
	d.sendOpen()
	d.waitFor("200 OK")
	d.see("limit=3", `"p-1003"`)
	d.dontSee(`"p-1004"`)
	if v := d.colByName("Shop API").Variable; findVar(v, "pageSize") != "10" {
		t.Fatalf("the team's value changed: %q", findVar(v, "pageSize"))
	}
	// :share gives it to the team.
	d.m.focusPane(paneTree)
	d.keys(":share pageSize<CR>")
	d.waitFor("Give your team these values?")
	d.keys("y")
	d.settle()
	if v := d.colByName("Shop API").Variable; findVar(v, "pageSize") != "3" {
		t.Fatalf("not shared: %q", findVar(v, "pageSize"))
	}
}

func findVar(vs []postman.Variable, key string) string {
	for _, v := range vs {
		if v.Key == key {
			return string(v.Value)
		}
	}
	return "(none)"
}

// parentName is the folder an item is in on the fake server ("" at the top).
func parentName(c *postman.Collection, name string) (string, bool) {
	it := findByName(c, name)
	if it == nil {
		return "", false
	}
	p, _ := c.ParentOf(it.ID)
	if p == nil {
		return "", true
	}
	return p.Name, true
}

func TestMoveAndUndo(t *testing.T) {
	d := newDriver(t)
	d.keys("l", "j", "l", "j", "j") // Shop API › Products › Get product
	if n := d.m.tree.Current(); n == nil || n.Name() != "Get product" {
		t.Fatalf("cursor on %v", n)
	}
	d.keys("dd", "j", "p") // into Orders
	d.waitFor("moved Get product")
	if p, _ := parentName(d.colByName("Shop API"), "Get product"); p != "Orders" {
		t.Fatalf("moved into %q", p)
	}
	d.keys("u")
	d.waitFor("took back: move Get product")
	if p, _ := parentName(d.colByName("Shop API"), "Get product"); p != "Products" {
		t.Fatalf("back in %q", p)
	}
	// Taking a move back isn't itself a change to take back.
	d.keys("u")
	d.see("Already at oldest change")

	// Across collections, and back.
	d.m.tree.Select(findByName(d.colByName("Shop API"), "List products").ID)
	d.keys("dd")
	for _, c := range d.m.st.Collections() {
		if c.Name() == "Shop GraphQL" {
			d.m.tree.Select(c.Summary.ID)
		}
	}
	d.keys("p")
	d.waitFor("moved List products")
	if _, ok := parentName(d.colByName("Shop GraphQL"), "List products"); !ok {
		t.Fatal("not in Shop GraphQL")
	}
	d.keys("u")
	d.waitFor("took back: move List products")
	if p, ok := parentName(d.colByName("Shop API"), "List products"); !ok || p != "Products" {
		t.Fatalf("back in %q (%v)", p, ok)
	}
	if _, ok := parentName(d.colByName("Shop GraphQL"), "List products"); ok {
		t.Fatal("still in Shop GraphQL too")
	}
}

// A teammate's change shows when a request is opened again, and a draft
// started before a refresh still asks before overwriting it.
func TestTeammateChangesAndDrafts(t *testing.T) {
	d := newDriver(t)
	d.open("get product")
	colID := d.m.open.colID
	d.open("list products")
	teammate := func(header string) {
		d.demo.Postman.Edit(colID, func(c *postman.Collection) *postman.Item {
			it := findByName(c, "Get product")
			it.Request.Header = append(it.Request.Header, postman.Param{Key: header, Value: "1"})
			return it
		})
	}
	teammate("X-First")
	d.m.focusPane(paneTree)
	d.keys("R")
	d.open("get product")
	d.waitFor("X-First: 1")

	// Now a draft, then a teammate's change comes in with a refresh.
	d.m.focusPane(paneEditor)
	d.keys("<Esc>")
	d.typeText(":%s/:id/:productId/g")
	d.keys("<CR>")
	teammate("X-Second")
	d.m.focusPane(paneTree)
	d.keys("R")
	d.settle()
	d.m.focusPane(paneEditor)
	d.typeText(":w")
	d.keys("<CR>")
	d.waitFor("Changed in Postman since you opened it")
}

// After a GraphQL request was sent, the server's schema completes fields
// and K says what one is.
func TestGraphQLSchema(t *testing.T) {
	d := newDriver(t)
	d.open("place order")
	d.sendOpen()
	d.waitFor("Log in to shop")
	d.typeText(demo.User)
	d.keys("<Tab>")
	d.typeText(demo.Password)
	d.keys("<C-s>")
	d.waitFor(`"createOrder"`)
	d.settle()
	if d.m.openSchema() == nil {
		t.Fatalf("no schema; schemas: %+v", d.m.schemas)
	}
	// Completion inside the selection set of createOrder (an Order).
	text, _ := d.m.docText(d.m.open.docRef)
	lines := strings.Split(text, "\n")
	row := -1
	for i, l := range lines {
		if strings.TrimSpace(l) == "total" {
			row = i
		}
	}
	if row < 0 {
		t.Fatalf("no total line in %q", lines)
	}
	lines[row] = "    no"
	start, items := d.m.complete(lines, row, len(lines[row]))
	if start != 4 || len(items) != 1 || items[0].Word != "note" || items[0].Menu != "String" {
		t.Fatalf("completion at %d: %+v", start, items)
	}
	at := -1
	for i, l := range lines {
		if strings.Contains(l, "createOrder(") {
			at = i
		}
	}
	info, ok := graphqlInfo(d.m.openSchema(), lines, at, strings.Index(lines[at], "createOrder")+1)
	if !ok || !strings.HasPrefix(info, "Mutation.createOrder(input: OrderInput!): Order") {
		t.Fatalf("info %q", info)
	}
}

// :wq in a request saves it to Postman, then quits posthero; :q with
// changes not saved asks first.
func TestQuitFromTheRequest(t *testing.T) {
	d := newDriver(t)
	d.open("get product")
	d.m.focusPane(paneEditor)
	d.keys("<Esc>")
	d.typeText(":%s/:id/:productId/g")
	d.keys("<CR>")
	d.typeText(":q")
	d.keys("<CR>")
	d.waitFor("Changes not saved to Postman")
	if d.quit {
		t.Fatal("quit with a request not saved")
	}
	d.keys("c")
	d.m.focusPane(paneEditor)
	d.typeText(":wq")
	d.keys("<CR>")
	for range 40 {
		if d.quit {
			break
		}
		d.settle()
	}
	if !d.quit {
		t.Fatalf("didn't quit after :wq:\n%s", d.screen())
	}
	if it := findByName(d.colByName("Shop API"), "Get product"); !strings.Contains(it.Request.URL.Raw, ":productId") {
		t.Fatalf("not saved before quitting: %s", it.Request.URL.Raw)
	}
}

// :wq (and :x, ZZ) on a request with a mistake in it keeps posthero open,
// with the mistake shown; fixed, :x saves and quits even though the buffer
// itself had been written already.
func TestWriteQuitWithAMistake(t *testing.T) {
	d := newDriver(t)
	d.open("get product")
	d.m.focusPane(paneEditor)
	d.keys("<Esc>")
	d.typeText("ggOnonsense")
	d.keys("<Esc>")
	d.typeText(":wq")
	d.keys("<CR>")
	d.waitFor("a header is a name and a value")
	for range 10 {
		d.settle()
	}
	if d.quit || len(d.m.modals) > 0 {
		t.Fatalf("quit (or asked to) with a mistake in the request:\n%s", d.screen())
	}
	d.keys("<Esc>")
	d.typeText("ggdd:w")
	d.keys("<CR>")
	d.typeText("ggOGET http://x")
	d.keys("<Esc>")
	d.typeText("ddZZ")
	for range 40 {
		if d.quit {
			break
		}
		d.settle()
	}
	if !d.quit {
		t.Fatalf("ZZ didn't quit:\n%s", d.screen())
	}
}

// The history on disk keeps no secrets: not a secret variable's value, nor
// one from the vault, wherever the request (or its response) has them.
func TestHistoryKeepsNoSecrets(t *testing.T) {
	d := newDriver(t)
	if err := d.m.vault.Set("apiKey", "vault-s3cret-42"); err != nil {
		t.Fatal(err)
	}
	d.open("echo")
	d.m.focusPane(paneEditor)
	d.keys("<Esc>")
	d.typeText(":%s/X-Demo: yes/X-Demo: {{vault:apiKey}}/")
	d.keys("<CR>")
	d.typeText(":%s/name: Ada/name: {{basicPassword}}/")
	d.keys("<CR>")
	d.sendOpen()
	d.waitFor("200")
	if rec := d.m.last[d.m.open.itemID]; rec == nil || !strings.Contains(string(rec.Body), "s3cret") {
		t.Fatalf("the echo didn't carry the secret, so this tests nothing: %+v", rec)
	}
	found := 0
	_ = filepath.WalkDir(filepath.Join(os.Getenv("XDG_STATE_HOME"), "history"), func(path string, e fs.DirEntry, err error) error {
		if err != nil || e.IsDir() {
			return nil
		}
		found++
		data, _ := os.ReadFile(path)
		for _, s := range []string{"s3cret", "vault-s3cret-42"} {
			if strings.Contains(string(data), s) {
				t.Errorf("%s keeps %q", path, s)
			}
		}
		return nil
	})
	if found == 0 {
		t.Fatal("no history written")
	}

	// :history clear forgets it all (after asking).
	d.m.focusPane(paneTree)
	d.keys(":")
	d.typeText("history clear")
	d.keys("<CR>")
	d.waitFor("Forget every earlier response?")
	d.keys("y")
	d.waitFor("forgot every earlier response")
	if l := d.m.hist.List(); len(l) != 0 {
		t.Fatalf("history after :history clear: %v", l)
	}
}

// Logging in goes through the transport requests use (proxy, CA file,
// client certificates), also after :set changes it.
func TestLoginUsesRequestTransport(t *testing.T) {
	d := newDriver(t)
	if d.m.authm.Transport == nil || d.m.authm.Transport() == nil {
		t.Fatal("the token manager has no transport of the requests'")
	}
	before := d.m.authm.Transport()
	d.keys(":")
	d.typeText("set proxy=127.0.0.1:9")
	d.keys("<CR>")
	if after := d.m.authm.Transport(); after == nil || after == before {
		t.Fatal(":set proxy didn't reach logging in")
	}
}

// Postman's scripts have packages and requests, which its API doesn't take
// back. Changing a variable leaves the scripts be; changing a script
// leaves empty ones out, and one that holds something isn't saved.
func TestCollectionSettingsWithPostmansScripts(t *testing.T) {
	d := newDriver(t)
	colID := d.m.tree.Current().Col.Summary.ID
	postmans := func(packages string) {
		t.Helper()
		d.demo.Postman.Edit(colID, func(c *postman.Collection) *postman.Item {
			for i := range c.Event {
				c.Event[i].Script.Extra = postman.Extra{"packages": json.RawMessage(packages), "requests": json.RawMessage(`{}`)}
			}
			return nil
		})
		d.keys("R")
		if c := d.m.st.Col(colID); c == nil || len(c.Data.Event) == 0 || string(c.Data.Event[0].Script.Extra["packages"]) != packages {
			t.Fatalf("posthero doesn't have Postman's scripts: %+v", c.Data.Event)
		}
	}
	save := func() {
		d.typeText(":w")
		d.keys("<CR>")
	}
	postmans(`{"@shop/utils":{"id":"p1"}}`)

	d.keys("e")
	d.waitFor("Collection · Shop API")
	d.typeText(":%s/pageSize: 10/pageSize: 25/")
	d.keys("<CR>")
	save()
	d.waitFor("saved the collection to Postman")
	c := d.colByName("Shop API")
	if findVar(c.Variable, "pageSize") != "25" || string(c.Event[0].Script.Extra["packages"]) != `{"@shop/utils":{"id":"p1"}}` {
		t.Fatalf("Postman has %+v and %+v", c.Variable, c.Event)
	}

	d.typeText(":%s/X-Request-Id/X-Trace-Id/")
	d.keys("<CR>")
	save()
	d.waitFor("Not saved")
	d.see("the collection's pre-request script has packages, which Postman's API can't save; change that script in Postman")
	d.keys("<CR>")
	d.typeText(":q!")
	d.keys("<CR>")
	if d.m.edit != nil {
		t.Fatal(":q! didn't close the editor")
	}

	postmans(`{}`)
	d.keys("e")
	d.waitFor("Collection · Shop API")
	d.typeText(":%s/X-Request-Id/X-Trace-Id/")
	d.keys("<CR>")
	save()
	d.waitFor("saved the collection to Postman")
	if got := postman.Scripts(d.colByName("Shop API").Event, "prerequest"); !strings.Contains(got, "X-Trace-Id") {
		t.Fatalf("the script in Postman: %q", got)
	}
}

// Postman doesn't move a collection's updatedAt when what is in it
// changes, so its list doesn't show a teammate's change to a request. R
// fetches every collection again; refreshing by itself, posthero fetches
// the open request's collection.
func TestRefreshFindsTeammatesChanges(t *testing.T) {
	d := newDriver(t)
	d.open("get product")
	var shopID, gqlID string
	for _, c := range d.m.st.Collections() {
		switch c.Name() {
		case "Shop API":
			shopID = c.Summary.ID
		case "Shop GraphQL":
			gqlID = c.Summary.ID
		}
	}
	d.demo.Postman.Edit(gqlID, func(c *postman.Collection) *postman.Item {
		it := findByName(c, "Me")
		it.Name = "Who am I"
		return it
	})
	d.demo.Postman.Edit(shopID, func(c *postman.Collection) *postman.Item {
		it := findByName(c, "Get product")
		it.Request.Header = append(it.Request.Header, postman.Param{Key: "X-Teammate", Value: "was here"})
		return it
	})
	// By itself: the open request's collection.
	d.exec(d.m.refresh(false))
	d.settle()
	d.see("X-Teammate: was here")
	if findByName(d.m.st.Col(gqlID).Data, "Who am I") != nil {
		t.Fatal("refreshing by itself fetched a collection that isn't open")
	}
	// R: all of them, and it says when it is done.
	d.m.focusPane(paneTree)
	d.keys("R")
	d.waitFor("up to date with Postman")
	if findByName(d.m.st.Col(gqlID).Data, "Who am I") == nil {
		t.Fatal("R didn't find the teammate's change")
	}

	// R while a refresh runs: it goes once that one is done.
	d.demo.Postman.Edit(gqlID, func(c *postman.Collection) *postman.Item {
		it := findByName(c, "Who am I")
		it.Name = "Me again"
		return it
	})
	running := d.m.refresh(false)
	d.keys("R")
	d.see("asking Postman what changed…")
	d.exec(running)
	d.waitFor("up to date with Postman")
	if findByName(d.m.st.Col(gqlID).Data, "Me again") == nil {
		t.Fatal("the R asked for while a refresh ran didn't go")
	}
}
