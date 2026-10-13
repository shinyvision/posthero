package tree

import (
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/shinyvision/posthero/internal/postman"
	"github.com/shinyvision/posthero/internal/store"
	"github.com/shinyvision/posthero/internal/ui/theme"
)

func req(id, name, method, url string) *postman.Item {
	return &postman.Item{ID: id, Name: name, Request: &postman.Request{Method: method, URL: postman.ParseURL(url)}}
}

func shop() []*store.Col {
	gql := req("q1", "Locations", "POST", "{{base}}/graphql")
	gql.Request.Body = &postman.Body{Mode: "graphql", GraphQL: &postman.GraphQL{Query: "query Locations { locations { id } }"}}
	mut := req("q2", "Delete location", "POST", "{{base}}/graphql")
	mut.Request.Body = &postman.Body{Mode: "graphql", GraphQL: &postman.GraphQL{Query: "# remove it\nmutation Del { deleteLocation(id: 1) }"}}
	c := &postman.Collection{Info: postman.Info{Name: "Shop API"}, Item: []*postman.Item{
		{ID: "f1", Name: "Products", Folder: true, Item: []*postman.Item{
			req("r1", "List products", "GET", "{{base}}/api/products"),
			req("r2", "Get product", "GET", "{{base}}/api/products/:id"),
		}},
		{ID: "f2", Name: "Orders", Folder: true, Item: []*postman.Item{
			req("r3", "Create order", "POST", "{{base}}/api/orders"),
			req("r4", "Cancel order", "DELETE", "{{base}}/api/orders/{{orderId}}"),
		}},
		gql, mut,
	}}
	return []*store.Col{{Summary: postman.Summary{ID: "c1", Name: "Shop API"}, Data: c}}
}

func names(m *Model) []string {
	var out []string
	for _, n := range m.Rows() {
		out = append(out, strings.Repeat(" ", n.Depth)+n.Name())
	}
	return out
}

func TestFoldAndMove(t *testing.T) {
	m := New(nil)
	m.SetData(shop())
	if got := names(m); len(got) != 1 {
		t.Fatalf("closed: %q", got)
	}
	m.Key("l")
	m.Key("j")
	m.Key("l")
	want := []string{"Shop API", " Products", "  List products", "  Get product", " Orders", " Locations", " Delete location"}
	if got := names(m); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("open: %q", got)
	}
	// ⏎ on a request asks to open it.
	m.Key("j")
	if a := m.Key("<CR>"); a != Open || m.Current().Item.ID != "r1" {
		t.Fatalf("enter: %v on %v", a, m.Current().Name())
	}
	// h on a request goes to its folder, and closes it.
	m.Key("h")
	m.Key("h")
	if n := m.Current(); n.Name() != "Products" || len(m.Rows()) != 5 {
		t.Fatalf("h: %q, %q", n.Name(), names(m))
	}
	// The tree remembers what is open, and the cursor stays on its node.
	m.SetData(shop())
	if m.Current().Name() != "Products" {
		t.Fatalf("cursor after SetData: %s", m.Current().Name())
	}
	m.Key("z")
	m.Key("R")
	if len(m.Rows()) != 9 {
		t.Fatalf("zR: %q", names(m))
	}
	m.Key("z")
	m.Key("M")
	if len(m.Rows()) != 1 {
		t.Fatalf("zM: %q", names(m))
	}
}

func TestFilterPrefersWholeWords(t *testing.T) {
	m := New(nil)
	m.SetData(shop())
	m.StartFilter()
	for _, r := range "order" {
		m.Key(string(r))
	}
	m.Key("<CR>")
	got := strings.Join(names(m), "|")
	// "order" is in the orders' names and URLs; the products only have its
	// letters scattered about, so they don't show.
	if got != "Shop API| Orders|  Create order|  Cancel order" {
		t.Fatalf("filtered: %q", got)
	}
	m.ClearFilter()
	// Without any whole-word match, scattered letters still find things.
	m.StartFilter()
	for _, r := range "lsprd" {
		m.Key(string(r))
	}
	m.Key("<CR>")
	if got := strings.Join(names(m), "|"); !strings.Contains(got, "List products") {
		t.Fatalf("fuzzy fallback: %q", got)
	}
}

func TestBadgesAndIcons(t *testing.T) {
	cols := shop()
	c := cols[0].Data
	if b := Badge(c.Item[2]); b != "QRY" {
		t.Errorf("query badge %q", b)
	}
	if b := Badge(c.Item[3]); b != "MUT" {
		t.Errorf("mutation badge %q (after a comment)", b)
	}
	if b := Badge(c.Item[1].Item[1]); b != "DEL" {
		t.Errorf("delete badge %q", b)
	}
	gql := func(q string) *postman.Item {
		return &postman.Item{Request: &postman.Request{Method: "POST", Body: &postman.Body{Mode: "graphql", GraphQL: &postman.GraphQL{Query: postman.Str(q)}}}}
	}
	if b := Badge(gql("fragment F on Order { id }\nmutation Place { place { ...F } }")); b != "MUT" {
		t.Errorf("mutation after a fragment: %q", b)
	}
	if b := Badge(gql("query A { a }\nmutation B { b }")); b != "MUT" {
		t.Errorf("mutation after a query: %q", b)
	}
	m := New([]string{"c1"})
	m.SetData(cols)
	view := ansi.Strip(strings.Join(m.View(40, 10, true), "\n"))
	// Folders open and close with ql's chevrons, and show what's in them.
	if !strings.Contains(view, theme.Expanded+"Shop API") || !strings.Contains(view, theme.Collapsed+"Products 2") {
		t.Fatalf("view:\n%s", view)
	}
}

func keys(m *Model, ks ...string) {
	for _, k := range ks {
		m.Key(k)
	}
}

// big is a collection of n requests.
func big(n int) []*store.Col {
	c := &postman.Collection{Info: postman.Info{Name: "Big"}}
	for i := range n {
		c.Item = append(c.Item, req(fmt.Sprintf("r%d", i), fmt.Sprintf("req %d", i), "GET", "/x"))
	}
	return []*store.Col{{Summary: postman.Summary{ID: "c1", Name: "Big"}, Data: c}}
}

// A click on a folder (Toggle) opens and closes it.
func TestToggle(t *testing.T) {
	m := New(nil)
	m.SetData(shop())
	m.Toggle()
	if len(m.Rows()) != 5 {
		t.Fatalf("toggle collection: %q", names(m))
	}
	m.SetCursor(1)
	m.Toggle()
	if len(m.Rows()) != 7 || m.Current().Name() != "Products" {
		t.Fatalf("toggle folder: %q on %s", names(m), m.Current().Name())
	}
	m.Toggle()
	if len(m.Rows()) != 5 {
		t.Fatalf("toggle folder closed: %q", names(m))
	}
}

func TestCounts(t *testing.T) {
	m := New([]string{"c1"})
	m.SetData(big(100))
	m.View(40, 20, true)
	keys(m, "5", "G")
	if m.cursor != 4 {
		t.Fatalf("5G: row %d", m.cursor)
	}
	keys(m, "3", "g", "g")
	if m.cursor != 2 {
		t.Fatalf("3gg: row %d", m.cursor)
	}
	keys(m, "G")
	if m.cursor != 100 {
		t.Fatalf("G: row %d", m.cursor)
	}
	keys(m, "g", "g")
	if m.cursor != 0 {
		t.Fatalf("gg: row %d", m.cursor)
	}
}

// <C-d> and <C-u> scroll the view and the cursor together; a count sets
// how far; <C-e> and <C-y> scroll the view and keep the cursor.
func TestScrollKeys(t *testing.T) {
	m := New([]string{"c1"})
	m.SetData(big(100))
	m.View(40, 20, true)
	keys(m, "3", "j", "<C-d>")
	m.View(40, 20, true)
	if m.cursor != 13 || m.top != 10 {
		t.Fatalf("<C-d>: cursor %d top %d, want 13 10", m.cursor, m.top)
	}
	keys(m, "<C-u>")
	m.View(40, 20, true)
	if m.cursor != 3 || m.top != 0 {
		t.Fatalf("<C-u>: cursor %d top %d, want 3 0", m.cursor, m.top)
	}
	keys(m, "4", "<C-d>", "<C-d>")
	m.View(40, 20, true)
	if m.cursor != 11 || m.top != 8 {
		t.Fatalf("4<C-d><C-d>: cursor %d top %d, want 11 8", m.cursor, m.top)
	}
	keys(m, "<C-e>")
	m.View(40, 20, true)
	if m.cursor != 11 || m.top != 9 {
		t.Fatalf("<C-e>: cursor %d top %d, want 11 9", m.cursor, m.top)
	}
	keys(m, "5", "<C-e>")
	m.View(40, 20, true)
	if m.cursor != 14 || m.top != 14 {
		t.Fatalf("5<C-e>: cursor %d top %d, want 14 14", m.cursor, m.top)
	}
	keys(m, "<C-y>")
	m.View(40, 20, true)
	if m.cursor != 14 || m.top != 13 {
		t.Fatalf("<C-y>: cursor %d top %d, want 14 13", m.cursor, m.top)
	}
}

// zM and zR keep the cursor on its node, or the folder it is now in.
func TestFoldAllKeepsCursor(t *testing.T) {
	m := New([]string{"c1", "f1", "f2"})
	m.SetData(shop())
	m.Select("r4") // Cancel order, in Orders
	keys(m, "z", "R")
	if n := m.Current(); n.ID() != "r4" {
		t.Fatalf("zR moved the cursor to %s", n.Name())
	}
	keys(m, "z", "M")
	if n := m.Current(); n.ID() != "c1" {
		t.Fatalf("zM: cursor on %s, want the collection", n.Name())
	}
	m = New([]string{"c1"})
	m.SetData(shop())
	m.Select("f2") // Orders, closed
	keys(m, "z", "R")
	if n := m.Current(); n.ID() != "f2" {
		t.Fatalf("zR from a folder moved the cursor to %s", n.Name())
	}
}

func TestViewTiny(t *testing.T) {
	for w := range 3 {
		for h := range 3 {
			m := New([]string{"c1"})
			m.SetData(big(10))
			m.View(w, h, true)
			keys(m, "G", "<C-d>", "<C-u>", "<C-e>")
			m.View(w, h, true)
			m.StartFilter()
			if got := m.View(w, h, true); len(got) != h {
				t.Errorf("View(%d, %d) gave %d lines", w, h, len(got))
			}
		}
	}
}

// withExamples is the shop with examples saved for Get product.
func withExamples() []*store.Col {
	cols := shop()
	get := cols[0].Data.Item[0].Item[1]
	get.Response = []*postman.Response{
		{ID: "e1", Name: "A product", Code: 200, Status: "OK"},
		{ID: "e2", Name: "No such product", Code: 404, Status: "Not Found"},
	}
	return cols
}

// A request's examples fold under it: h and l, za, Tab and the capitals
// open and close them; ⏎ and l on one open it, h goes up to its request.
func TestExamplesFoldUnderTheirRequest(t *testing.T) {
	m := New([]string{"c1", "f1"})
	m.SetData(withExamples())
	if got := strings.Join(names(m), "|"); strings.Contains(got, "A product") {
		t.Fatalf("examples showed folded: %q", got)
	}
	view := ansi.Strip(strings.Join(m.View(50, 12, true), "\n"))
	if !strings.Contains(view, theme.Collapsed+"GET  Get product 2") {
		t.Fatalf("a request with examples:\n%s", view)
	}
	m.Select("r2")
	keys(m, "z", "a")
	want := "Shop API| Products|  List products|  Get product|   A product|   No such product| Orders| Locations| Delete location"
	if got := strings.Join(names(m), "|"); got != want {
		t.Fatalf("open: %q", got)
	}
	view = ansi.Strip(strings.Join(m.View(50, 12, true), "\n"))
	if !strings.Contains(view, theme.Expanded+"GET  Get product 2") || !strings.Contains(view, "e.g. A product") || !strings.Contains(view, "e.g. No such product") {
		t.Fatalf("view:\n%s", view)
	}
	// The status an example has shows on its row.
	for _, l := range strings.Split(view, "\n") {
		if strings.Contains(l, "No such product") && !strings.HasSuffix(strings.TrimRight(l, " "), "404") {
			t.Errorf("no status: %q", l)
		}
	}
	keys(m, "j")
	if n := m.Current(); n.Kind != Example || n.ID() != "e1" || n.Item.ID != "r2" || n.Path() != "Shop API / Products / Get product / A product" {
		t.Fatalf("on %+v", n)
	}
	if a := m.Key("<CR>"); a != Open {
		t.Errorf("⏎ on an example: %v", a)
	}
	if a := m.Key("l"); a != Open {
		t.Errorf("l on an example: %v", a)
	}
	keys(m, "h")
	if n := m.Current(); n.ID() != "r2" {
		t.Fatalf("h went to %s", n.Name())
	}
	keys(m, "h")
	if strings.Contains(strings.Join(names(m), "|"), "A product") || m.Current().ID() != "r2" {
		t.Fatalf("h on an open request: %q on %s", names(m), m.Current().Name())
	}
	keys(m, "<Tab>")
	if !strings.Contains(strings.Join(names(m), "|"), "A product") {
		t.Fatalf("Tab: %q", names(m))
	}
	keys(m, "j", "z", "c")
	if n := m.Current(); n.ID() != "r2" || strings.Contains(strings.Join(names(m), "|"), "A product") {
		t.Fatalf("zc on an example: %q on %s", names(m), n.Name())
	}
	// za on a request without examples still folds its folder.
	keys(m, "k", "z", "a")
	if n := m.Current(); n.ID() != "f1" {
		t.Fatalf("za on List products went to %s", n.Name())
	}
	// zR opens everything, examples too; zM closes it all.
	keys(m, "z", "R")
	if !strings.Contains(strings.Join(names(m), "|"), "No such product") {
		t.Fatalf("zR: %q", names(m))
	}
	keys(m, "z", "M")
	if len(names(m)) != 1 {
		t.Fatalf("zM: %q", names(m))
	}
}

// l opens a request's examples, and then the request; o opens and closes
// them, and opens a request without any.
func TestLAndOOnARequestWithExamples(t *testing.T) {
	m := New([]string{"c1", "f1"})
	m.SetData(withExamples())
	m.Select("r2")
	if a := m.Key("l"); a != None || !strings.Contains(strings.Join(names(m), "|"), "A product") || m.Current().ID() != "r2" {
		t.Fatalf("l on a folded request: %v, %q on %s", a, names(m), m.Current().Name())
	}
	if a := m.Key("l"); a != Open || m.Current().ID() != "r2" {
		t.Fatalf("l again: %v on %s", a, m.Current().Name())
	}
	if a := m.Key("<Right>"); a != Open {
		t.Fatalf("→ on an open request: %v", a)
	}
	if a := m.Key("o"); a != None || strings.Contains(strings.Join(names(m), "|"), "A product") {
		t.Fatalf("o on an open request: %v, %q", a, names(m))
	}
	if a := m.Key("o"); a != None || !strings.Contains(strings.Join(names(m), "|"), "A product") {
		t.Fatalf("o on a folded request: %v, %q", a, names(m))
	}
	if a := m.Key("<CR>"); a != Open {
		t.Fatalf("⏎ on a request with examples: %v", a)
	}
	keys(m, "j")
	if a := m.Key("o"); a != Open || m.Current().Kind != Example {
		t.Fatalf("o on an example: %v", a)
	}
	m.Select("r1")
	if a := m.Key("o"); a != Open {
		t.Fatalf("o on a request without examples: %v", a)
	}
	if a := m.Key("l"); a != Open {
		t.Fatalf("l on a request without examples: %v", a)
	}
	// While filtering, the examples that match show anyway: l opens.
	m.Select("r2")
	keys(m, "h")
	m.StartFilter()
	for _, r := range "get product" {
		m.Key(string(r))
	}
	m.Key("<CR>")
	m.Select("r2")
	if n := m.Current(); n == nil || n.ID() != "r2" || m.Filter() == "" {
		t.Fatalf("filtered to %v", names(m))
	}
	if a := m.Key("l"); a != Open {
		t.Fatalf("l while filtering: %v", a)
	}
}

// Selecting an example opens what it is in; marks show on its row.
func TestSelectExample(t *testing.T) {
	m := New(nil)
	m.SetData(withExamples())
	if !m.Select("e2") {
		t.Fatal("not selected")
	}
	if n := m.Current(); n.Kind != Example || n.Name() != "No such product" {
		t.Fatalf("on %s", n.Name())
	}
	m.SetMarks(Marks{Open: "e2", Drafts: map[string]bool{"e2": true}, Cut: map[string]bool{}})
	view := ansi.Strip(strings.Join(m.View(50, 12, true), "\n"))
	for _, l := range strings.Split(view, "\n") {
		if strings.Contains(l, "No such product") && (!strings.Contains(l, "● e.g. No such product") || !strings.Contains(l, "+ 404")) {
			t.Errorf("marks: %q", l)
		}
	}
	// Folding away the examples keeps the cursor on the request.
	m.Key("h")
	keys(m, "h")
	if n := m.Current(); n.ID() != "r2" {
		t.Fatalf("on %s", n.Name())
	}
}

// The filter finds examples by name, and shows them under their request.
func TestFilterFindsExamples(t *testing.T) {
	m := New(nil)
	m.SetData(withExamples())
	m.StartFilter()
	for _, r := range "no such" {
		m.Key(string(r))
	}
	if got := strings.Join(names(m), "|"); got != "Shop API| Products|  Get product|   No such product" {
		t.Fatalf("filtered: %q", got)
	}
	if n := m.Current(); n.Kind != Example || n.ID() != "e2" {
		t.Fatalf("cursor on %s", n.Name())
	}
	view := ansi.Strip(strings.Join(m.View(50, 12, true), "\n"))
	if !strings.Contains(view, theme.Expanded+"GET  Get product 2") {
		t.Fatalf("view:\n%s", view)
	}
	m.ClearFilter()
	// A request and its example both matching show once each.
	m.StartFilter()
	for _, r := range "product" {
		m.Key(string(r))
	}
	got := strings.Join(names(m), "|")
	if strings.Count(got, "Get product") != 1 || !strings.Contains(got, "  Get product|   A product|   No such product") {
		t.Fatalf("filtered: %q", got)
	}
}
