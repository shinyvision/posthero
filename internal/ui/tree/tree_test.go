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
