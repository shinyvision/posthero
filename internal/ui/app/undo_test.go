package app

import (
	"slices"
	"strings"
	"testing"

	"github.com/shinyvision/posthero/internal/postman"
)

// Every change posthero makes in Postman is taken back by u, that change
// and nothing older. Each case first saves a request (the change u must
// not reach), then makes its change, then undoes it once.
func TestUndoEveryChange(t *testing.T) {
	envValue := func(d *driver, env, key string) string {
		for _, e := range d.m.st.Environments() {
			if e.Name() == env {
				for _, v := range d.demo.Postman.Environment(e.Summary.ID).Values {
					if v.Key == key {
						return string(v.Value)
					}
				}
			}
		}
		return "(none)"
	}
	cases := []struct {
		name   string
		change func(d *driver)
		// changed and back check Postman after the change and after u.
		changed, back func(d *driver) bool
	}{
		{
			name: "copy a request",
			change: func(d *driver) {
				d.selectItem("Get product")
				d.keys("yy", "j", "p")
				d.waitFor("copied Get product")
			},
			changed: func(d *driver) bool { return findByName(d.colByName("Shop API"), "Get product Copy") != nil },
			back:    func(d *driver) bool { return findByName(d.colByName("Shop API"), "Get product Copy") == nil },
		},
		{
			name: "duplicate a request",
			change: func(d *driver) {
				d.selectItem("Get product")
				d.keys(":dup<CR>")
				d.waitFor("copied Get product")
			},
			changed: func(d *driver) bool { return findByName(d.colByName("Shop API"), "Get product Copy") != nil },
			back:    func(d *driver) bool { return findByName(d.colByName("Shop API"), "Get product Copy") == nil },
		},
		{
			name: "folder settings",
			change: func(d *driver) {
				d.selectItem("Orders")
				d.keys("e")
				d.waitFor("Folder · Orders")
				d.typeText(":%s/Your orders. These log in with the collection's auth./Orders, by user./")
				d.keys("<CR>")
				d.writeQuit()
				d.waitFor("saved Orders to Postman")
			},
			changed: func(d *driver) bool { return folderDesc(d, "Orders") == "Orders, by user." },
			back: func(d *driver) bool {
				return folderDesc(d, "Orders") == "Your orders. These log in with the collection's auth."
			},
		},
		{
			name: "collection settings",
			change: func(d *driver) {
				d.selectCollection("Shop API")
				d.keys("e")
				d.waitFor("Collection · Shop API")
				d.typeText(":%s/X-Request-Id/X-Trace-Id/")
				d.keys("<CR>")
				d.writeQuit()
				d.waitFor("saved the collection to Postman")
			},
			changed: func(d *driver) bool {
				return strings.Contains(postman.Scripts(d.colByName("Shop API").Event, "prerequest"), "X-Trace-Id")
			},
			back: func(d *driver) bool {
				return strings.Contains(postman.Scripts(d.colByName("Shop API").Event, "prerequest"), "X-Request-Id")
			},
		},
		{
			name: "an environment's values",
			change: func(d *driver) {
				d.keys("gE")
				d.waitFor("Environment · Shop · Local")
				d.typeText(":%s/realm: shop/realm: other/")
				d.keys("<CR>")
				d.writeQuit()
				d.waitFor("saved Shop · Local to Postman")
			},
			changed: func(d *driver) bool {
				return envValue(d, "Shop · Local", "realm") == "other" && envValue(d, "Shop · Local", "key") == "(none)"
			},
			back: func(d *driver) bool { return envValue(d, "Shop · Local", "realm") == "shop" },
		},
		{
			name: "the globals",
			change: func(d *driver) {
				d.keys(":globals<CR>")
				d.waitFor("Globals")
				d.typeText(":%s/2026-10/2027-01/")
				d.keys("<CR>")
				d.writeQuit()
				d.waitFor("saved the globals to Postman")
			},
			changed: func(d *driver) bool {
				g := d.demo.Postman.Globals(d.demo.Workspace)
				return len(g) == 1 && string(g[0].Value) == "2027-01"
			},
			back: func(d *driver) bool { return string(d.demo.Postman.Globals(d.demo.Workspace)[0].Value) == "2026-10" },
		},
		{
			name: "make an environment",
			change: func(d *driver) {
				d.keys(":env new Staging<CR>")
				d.waitFor("Environment · Staging")
				d.typeText(":q")
				d.keys("<CR>")
			},
			changed: func(d *driver) bool {
				return slices.Contains(d.demo.Postman.EnvironmentNames(d.demo.Workspace), "Staging")
			},
			back: func(d *driver) bool {
				return !slices.Contains(d.demo.Postman.EnvironmentNames(d.demo.Workspace), "Staging")
			},
		},
		{
			name: "rename an environment",
			change: func(d *driver) {
				d.keys(":env rename Shop · Dev<CR>")
				d.waitFor("renamed to Shop · Dev")
			},
			changed: func(d *driver) bool {
				return slices.Contains(d.demo.Postman.EnvironmentNames(d.demo.Workspace), "Shop · Dev")
			},
			back: func(d *driver) bool {
				return slices.Contains(d.demo.Postman.EnvironmentNames(d.demo.Workspace), "Shop · Local")
			},
		},
		{
			name: "delete an environment",
			change: func(d *driver) {
				d.keys(":env delete<CR>")
				d.waitFor("Delete Shop · Local?")
				d.keys("y")
				d.waitFor("deleted Shop · Local")
			},
			changed: func(d *driver) bool {
				return !slices.Contains(d.demo.Postman.EnvironmentNames(d.demo.Workspace), "Shop · Local")
			},
			back: func(d *driver) bool {
				return slices.Contains(d.demo.Postman.EnvironmentNames(d.demo.Workspace), "Shop · Local") && envValue(d, "Shop · Local", "realm") == "shop"
			},
		},
		{
			name: "save an example",
			change: func(d *driver) {
				d.open("list products")
				d.sendOpen()
				d.waitFor("200 OK")
				d.m.focusPane(paneTree)
				d.keys(":example Ten products<CR>")
				d.waitFor("saved Ten products as an example of List products")
			},
			changed: func(d *driver) bool { return exampleNamed(d, "List products", "Ten products") },
			back:    func(d *driver) bool { return !exampleNamed(d, "List products", "Ten products") },
		},
		{
			name: "make a collection",
			change: func(d *driver) {
				d.keys(":collection new Scratch<CR>")
				d.waitFor("made Scratch")
			},
			changed: func(d *driver) bool {
				return slices.Contains(d.demo.Postman.CollectionNames(d.demo.Workspace), "Scratch")
			},
			back: func(d *driver) bool {
				return !slices.Contains(d.demo.Postman.CollectionNames(d.demo.Workspace), "Scratch")
			},
		},
		{
			name: "delete a collection",
			change: func(d *driver) {
				d.selectCollection("Shop GraphQL")
				d.keys("D")
				d.keys("y")
				d.waitFor("deleted the collection Shop GraphQL")
			},
			changed: func(d *driver) bool {
				return !slices.Contains(d.demo.Postman.CollectionNames(d.demo.Workspace), "Shop GraphQL")
			},
			back: func(d *driver) bool {
				if !slices.Contains(d.demo.Postman.CollectionNames(d.demo.Workspace), "Shop GraphQL") {
					return false
				}
				return findByName(d.colByName("Shop GraphQL"), "Place order") != nil
			},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := newDriver(t)
			// An older change that u must leave alone: a saved request.
			d.open("my orders")
			d.m.focusPane(paneEditor)
			d.keys("<Esc>")
			d.typeText(":%s/api\\/orders/api\\/orders?mine=1/")
			d.keys("<CR>")
			d.typeText(":w")
			d.keys("<CR>")
			d.waitFor("saved My orders to Postman")
			d.m.focusPane(paneTree)

			c.change(d)
			d.settle()
			if !c.changed(d) {
				var log []string
				for _, e := range d.m.log {
					log = append(log, e.text)
				}
				t.Fatalf("the change didn't happen; messages: %q; globals %+v", log, d.demo.Postman.Globals(d.demo.Workspace))
			}
			d.m.focusPane(paneTree)
			d.keys("<Esc>", "u")
			d.waitFor("took back")
			d.settle()
			if !c.back(d) {
				t.Fatalf("u didn't take it back:\n%s", d.screen())
			}
			if it := findByName(d.colByName("Shop API"), "My orders"); !strings.Contains(it.Request.URL.Raw, "mine=1") {
				t.Fatalf("u took back the older save too: %s", it.Request.URL.Raw)
			}
		})
	}
}

// selectItem puts the tree's cursor on an item of the demo by name.
func (d *driver) selectItem(name string) {
	d.t.Helper()
	d.m.focusPane(paneTree)
	for _, c := range d.m.st.Collections() {
		if it := findByName(c.Data, name); it != nil {
			for _, p := range parentsOf(c.Data, it.ID) {
				d.m.tree.Expand(p)
			}
			d.m.tree.Expand(c.Summary.ID)
			if !d.m.tree.Select(it.ID) {
				d.t.Fatalf("can't select %s", name)
			}
			return
		}
	}
	d.t.Fatalf("no item %s", name)
}

// selectCollection puts the tree's cursor on a collection.
func (d *driver) selectCollection(name string) {
	d.t.Helper()
	d.m.focusPane(paneTree)
	for _, c := range d.m.st.Collections() {
		if c.Name() == name {
			d.m.tree.Select(c.Summary.ID)
			return
		}
	}
	d.t.Fatalf("no collection %s", name)
}

// writeQuit saves and closes a dialog editor.
func (d *driver) writeQuit() {
	d.typeText(":w")
	d.keys("<CR>")
	d.settle()
	d.typeText(":q")
	d.keys("<CR>")
}

func parentsOf(c *postman.Collection, id string) []string {
	_, parents := c.Find(id)
	var out []string
	for _, p := range parents {
		out = append(out, p.ID)
	}
	return out
}

func folderDesc(d *driver, name string) string {
	it := findByName(d.colByName("Shop API"), name)
	if it == nil || it.Description == nil {
		return ""
	}
	return it.Description.Content
}

func exampleNamed(d *driver, request, name string) bool {
	it := findByName(d.colByName("Shop API"), request)
	for _, r := range it.Response {
		if r.Name == name {
			return true
		}
	}
	return false
}

// shape is a collection's structure: names, order, nesting and each
// request's method and URL. Ids and times don't count.
func shape(c *postman.Collection) string {
	var b strings.Builder
	var walk func(items []*postman.Item, depth int)
	walk = func(items []*postman.Item, depth int) {
		for _, it := range items {
			b.WriteString(strings.Repeat("  ", depth) + it.Name)
			if it.Request != nil {
				b.WriteString(" " + it.Request.Method)
				if it.Request.URL != nil {
					b.WriteString(" " + it.Request.URL.Raw)
				}
			}
			if it.Description != nil {
				b.WriteString(" «" + it.Description.Content + "»")
			}
			b.WriteByte('\n')
			walk(it.Item, depth+1)
		}
	}
	walk(c.Item, 0)
	return b.String()
}

// A run of different changes, then u for each: the collection is as it
// was.
func TestUndoAChainOfChanges(t *testing.T) {
	d := newDriver(t)
	original := shape(d.colByName("Shop API"))
	steps := []func(){
		func() { // copy a request into another folder
			d.selectItem("Get product")
			d.keys("yy")
			d.selectItem("My orders")
			d.keys("p")
			d.waitFor("copied Get product")
		},
		func() { // move a request into another folder
			d.selectItem("Echo")
			d.keys("dd")
			d.selectItem("Products")
			d.keys("p")
			d.waitFor("moved Echo")
		},
		func() { // rename a folder
			d.selectItem("Tools")
			d.keys("r", "<C-u>")
			d.typeText("Utilities")
			d.keys("<CR>")
			d.waitFor("renamed to Utilities")
		},
		func() { // a new request
			d.selectItem("Get product")
			d.keys("a")
			d.typeText("Search")
			d.keys("<CR>")
			d.waitFor("made Search")
			d.m.focusPane(paneTree)
		},
		func() { // delete a request
			d.selectItem("Cancel order")
			d.keys("D", "y")
			d.waitFor("deleted Cancel order")
		},
		func() { // copy a whole folder
			d.selectItem("Orders")
			d.keys("yy")
			d.selectItem("Products")
			d.keys("p")
			d.waitFor("copied Orders")
		},
		func() { // move a folder above another
			d.selectItem("Utilities")
			d.keys("dd")
			d.selectItem("Products")
			d.keys("P")
			d.waitFor("moved Utilities")
		},
		func() { // edit and save a request
			d.open("list products")
			d.m.focusPane(paneEditor)
			d.keys("<Esc>")
			d.typeText(":%s/products/items/")
			d.keys("<CR>")
			d.typeText(":w")
			d.keys("<CR>")
			d.waitFor("saved List products")
			d.m.focusPane(paneTree)
		},
	}
	for i, step := range steps {
		step()
		d.settle()
		if shape(d.colByName("Shop API")) == original {
			t.Fatalf("step %d changed nothing", i+1)
		}
	}
	for i := range steps {
		d.m.focusPane(paneTree)
		d.keys("<Esc>", "u")
		for range 40 {
			if strings.Contains(d.m.status, "took back") {
				break
			}
			d.settle()
		}
		if !strings.Contains(d.m.status, "took back") {
			var log []string
			for _, e := range d.m.log {
				log = append(log, e.text)
			}
			t.Fatalf("undo %d: status %q; messages:\n%s\n%s", i+1, d.m.status, strings.Join(log, "\n"), shape(d.colByName("Shop API")))
		}
		d.setStatusForTest("")
	}
	if got := shape(d.colByName("Shop API")); got != original {
		t.Fatalf("after taking everything back:\n%s\nwant:\n%s", got, original)
	}
	d.keys("u")
	d.see("Already at oldest change")
}

// setStatusForTest clears the status bar, so the next waitFor sees only
// what comes after.
func (d *driver) setStatusForTest(s string) { d.m.status = s }
