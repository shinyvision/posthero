package app

import (
	"context"
	"strings"
	"testing"

	"github.com/shinyvision/posthero/internal/postman"
	"github.com/shinyvision/posthero/internal/store"
)

// yy on a collection asks which workspace to copy it to, and copies it
// there, requests and all; u takes the copy back. Picking this workspace
// puts a copy next to it; :dup does that at once.
func TestCopyCollectionToWorkspace(t *testing.T) {
	d := newDriver(t)
	d.demo.Postman.AddWorkspace("ws2", "Personal")
	d.m.workspaces = append(d.m.workspaces, postman.Workspace{ID: "ws2", Name: "Personal", Type: "personal"})

	// Only the collection.
	d.selectCollection("Shop API")
	d.keys("yy")
	d.waitFor("Copy Shop API to which workspace?")
	d.typeText("Personal")
	d.keys("<CR>")
	d.waitFor("Its variables come from Shop · Local, Shop · Production")
	d.keys("n")
	d.waitFor("copied Shop API to workspace Personal")
	if got := d.demo.Postman.CollectionNames("ws2"); strings.Join(got, ",") != "Shop API" {
		t.Fatalf("the other workspace has %v", got)
	}
	if got := d.demo.Postman.EnvironmentNames("ws2"); len(got) != 0 {
		t.Fatalf("environments went along: %v", got)
	}
	if got := d.demo.Postman.CollectionNames("ws1"); strings.Contains(strings.Join(got, ","), "Copy") {
		t.Fatalf("this workspace got a copy too: %v", got)
	}
	d.keys("u")
	d.waitFor("took back: copy Shop API to workspace Personal")
	if got := d.demo.Postman.CollectionNames("ws2"); len(got) != 0 {
		t.Fatalf("the copy is still there: %v", got)
	}

	// With its environments, and your current values in them.
	local := d.m.envByName("Shop · Local")
	if err := d.m.st.Values.Set(local.Summary.ID, store.Overrides{"token": {Value: "mine"}}); err != nil {
		t.Fatal(err)
	}
	d.selectCollection("Shop API")
	d.keys("yy")
	d.waitFor("Copy Shop API to which workspace?")
	d.typeText("Personal")
	d.keys("<CR>")
	d.waitFor("Its variables come from")
	d.keys("y")
	d.waitFor("copied Shop API to workspace Personal with Shop · Local, Shop · Production")
	if got := d.demo.Postman.EnvironmentNames("ws2"); strings.Join(got, ",") != "Shop · Local,Shop · Production" {
		t.Fatalf("the other workspace's environments: %v", got)
	}
	envs, err := d.m.api.Environments(context.Background(), "ws2")
	if err != nil {
		t.Fatal(err)
	}
	carried := false
	for _, e := range envs {
		if e.Name == "Shop · Local" && d.m.st.Values.Get(e.ID)["token"].Value == "mine" {
			carried = true
		}
	}
	if !carried {
		t.Fatal("your current value didn't go along")
	}
	d.keys("u")
	d.waitFor("took back: copy Shop API to workspace Personal")
	if c, e := d.demo.Postman.CollectionNames("ws2"), d.demo.Postman.EnvironmentNames("ws2"); len(c)+len(e) != 0 {
		t.Fatalf("copies still there: %v %v", c, e)
	}

	// :env copy copies the environment in use to a workspace.
	d.keys(":env Shop · Local<CR>")
	d.keys(":env copy<CR>")
	d.waitFor("Copy Shop · Local to which workspace?")
	d.typeText("Personal")
	d.keys("<CR>")
	d.waitFor("copied Shop · Local to workspace Personal")
	if got := d.demo.Postman.EnvironmentNames("ws2"); strings.Join(got, ",") != "Shop · Local" {
		t.Fatalf("the other workspace's environments: %v", got)
	}

	d.selectCollection("Shop API")
	d.keys("yy")
	d.waitFor("Copy Shop API to which workspace?")
	d.typeText("Team")
	d.keys("<CR>")
	d.waitFor("copied Shop API · Shop API Copy")
	orig, cp := d.colByName("Shop API"), d.colByName("Shop API Copy")
	if n, m := countRequests(orig), countRequests(cp); n == 0 || n != m {
		t.Fatalf("the copy has %d requests, the original %d", m, n)
	}
	d.keys("u")
	d.waitFor("took back: copy Shop API")
	if got := d.demo.Postman.CollectionNames("ws1"); strings.Contains(strings.Join(got, ","), "Copy") {
		t.Fatalf("the copy is still there: %v", got)
	}

	d.selectCollection("Shop API")
	d.keys(":dup<CR>")
	d.waitFor("copied Shop API · Shop API Copy")
}

func countRequests(c *postman.Collection) int {
	n := 0
	c.Walk(func(it *postman.Item, _ []*postman.Item) bool {
		if !it.Folder {
			n++
		}
		return true
	})
	return n
}

// A folder put on a request's row goes after the other folders there:
// Postman keeps folders before requests, and places a folder only next to
// a folder.
func TestPutFolderOnARequest(t *testing.T) {
	d := newDriver(t)
	d.selectItem("Tools")
	d.keys("dd")
	d.selectItem("List products")
	d.keys("p")
	d.waitFor("moved Tools")
	products := findByName(d.colByName("Shop API"), "Products")
	var names []string
	for _, it := range products.Item {
		names = append(names, it.Name)
	}
	if strings.Join(names, ",") != "Tools,List products,Get product" {
		t.Fatalf("Products: %v", names)
	}
}
