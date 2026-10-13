package local

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"testing"

	"github.com/shinyvision/posthero/internal/postman"
	"github.com/shinyvision/posthero/internal/postman/pmtest"
)

const shop = `{
  "info": {"name": "Shop"},
  "item": [
    {"name": "Products", "item": [
      {"name": "List products", "request": {"method": "GET", "url": "{{base}}/products"}},
      {"name": "Get product", "request": {"method": "GET", "url": "{{base}}/products/1"},
       "response": [{"name": "found", "code": 200, "status": "OK", "body": "{}"}]}
    ]},
    {"name": "Orders", "item": [
      {"name": "List orders", "request": {"method": "GET", "url": "{{base}}/orders"}}
    ]},
    {"name": "Health", "request": {"method": "GET", "url": "{{base}}/health"}}
  ]
}`

type world struct {
	t      *testing.T
	ctx    context.Context
	team   *pmtest.Server // Postman
	api    *postman.Client
	ws     postman.Workspace
	col    string // the shop's id
	envID  string
	c      *Copy
	here   *postman.Client // the local copy, as posthero talks to it
	hereWS string
}

func newWorld(t *testing.T) *world {
	t.Helper()
	w := &world{t: t, ctx: context.Background(), team: pmtest.New(), ws: postman.Workspace{ID: "ws1", Name: "Team"}}
	url := w.team.Start()
	t.Cleanup(w.team.Close)
	w.api = postman.NewClient(pmtest.Key, url)
	var c postman.Collection
	if err := json.Unmarshal([]byte(shop), &c); err != nil {
		t.Fatal(err)
	}
	w.col = w.team.AddCollection("ws1", &c)
	w.envID = w.team.AddEnvironment("ws1", &postman.Environment{Name: "Shop · Local", Values: []postman.EnvValue{{Key: "base", Value: "http://localhost"}, {Key: "token", Value: "t1"}}})
	w.team.SetGlobals("ws1", []postman.EnvValue{{Key: "g", Value: "1"}})
	cols, envs, globals := w.snapshot()
	cp, err := Start(t.TempDir(), w.ws, cols, envs, globals)
	if err != nil {
		t.Fatal(err)
	}
	w.c = cp
	w.here = cp.Client()
	return w
}

// snapshot is the team's version, as posthero has it after fetching.
func (w *world) snapshot() ([]Remote, []RemoteEnv, []postman.EnvValue) {
	w.t.Helper()
	sums, err := w.api.Collections(w.ctx, "ws1")
	if err != nil {
		w.t.Fatal(err)
	}
	var cols []Remote
	for _, s := range sums {
		c, err := w.api.Collection(w.ctx, s.UID)
		if err != nil {
			w.t.Fatal(err)
		}
		cols = append(cols, Remote{UID: s.UID, UpdatedAt: s.UpdatedAt, Data: c})
	}
	esums, _ := w.api.Environments(w.ctx, "ws1")
	var envs []RemoteEnv
	for _, s := range esums {
		e, _ := w.api.Environment(w.ctx, s.UID)
		envs = append(envs, RemoteEnv{UID: s.UID, UpdatedAt: s.UpdatedAt, Data: e})
	}
	g, _ := w.api.Globals(w.ctx, "ws1")
	return cols, envs, g
}

// localCol is the shop as the local copy has it, and its uid there.
func (w *world) localCol() (*postman.Collection, string) {
	w.t.Helper()
	sums, err := w.here.Collections(w.ctx, "ws1")
	if err != nil {
		w.t.Fatal(err)
	}
	for _, s := range sums {
		if s.ID == w.col {
			c, err := w.here.Collection(w.ctx, s.UID)
			if err != nil {
				w.t.Fatal(err)
			}
			return c, s.UID
		}
	}
	w.t.Fatal("the shop isn't in the local copy")
	return nil, ""
}

func find(c *postman.Collection, name string) *postman.Item {
	var out *postman.Item
	c.Walk(func(it *postman.Item, _ []*postman.Item) bool {
		if it.Name == name {
			out = it
		}
		return true
	})
	return out
}

// teamEdit changes a request in Postman, as a teammate.
func (w *world) teamEdit(name string, f func(it *postman.Item)) {
	w.team.Edit(w.col, func(c *postman.Collection) *postman.Item {
		it := find(c, name)
		f(it)
		return it
	})
}

// edit changes a request in the local copy, as posthero would.
func (w *world) edit(name string, f func(it *postman.Item)) {
	w.t.Helper()
	c, uid := w.localCol()
	it := find(c, name)
	f(it)
	if _, err := w.here.UpdateRequest(w.ctx, uid, it); err != nil {
		w.t.Fatal(err)
	}
}

func changes(c *Copy) string {
	var out []string
	for _, ch := range c.Changes() {
		out = append(out, ch.What+" "+ch.Kind+" "+ch.Name)
	}
	sort.Strings(out)
	return strings.Join(out, ", ")
}

func (w *world) push(overwrite map[string]bool) (*Plan, Pushed) {
	w.t.Helper()
	p, err := w.c.Plan(w.ctx, w.api)
	if err != nil {
		w.t.Fatal(err)
	}
	res, err := w.c.Push(w.ctx, w.api, p, overwrite, "")
	if err != nil {
		w.t.Fatal(err)
	}
	return p, res
}

func url(it *postman.Item) string { return it.Request.URL.String() }

func TestStartIsInSync(t *testing.T) {
	w := newWorld(t)
	if got := changes(w.c); got != "" {
		t.Fatalf("a fresh copy has changes: %s", got)
	}
	p, err := w.c.Plan(w.ctx, w.api)
	if err != nil || p.Changes() != 0 || len(p.Conflicts) != 0 {
		t.Fatalf("a fresh copy pushes %d, conflicts %d (%v)", p.Changes(), len(p.Conflicts), err)
	}
	// Opened again (posthero restarted), it is the same.
	again, err := Open(w.c.dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := changes(again); got != "" {
		t.Fatalf("reopened, the copy has changes: %s", got)
	}
}

// What changes here stays here until it is pushed.
func TestEditThenPush(t *testing.T) {
	w := newWorld(t)
	w.edit("Get product", func(it *postman.Item) { it.Request.Method = "POST" })
	if got := changes(w.c); got != "changed request Get product" {
		t.Fatalf("changes: %s", got)
	}
	if it := find(w.team.Collection(w.col), "Get product"); it.Request.Method != "GET" {
		t.Fatal("a local change reached Postman")
	}
	_, res := w.push(nil)
	if res.Pushed != 1 {
		t.Fatalf("pushed %d", res.Pushed)
	}
	if it := find(w.team.Collection(w.col), "Get product"); it.Request.Method != "POST" {
		t.Fatal("the push didn't reach Postman")
	}
	if it := find(w.team.Collection(w.col), "Get product"); len(it.Response) != 1 {
		t.Fatalf("the example went: %d", len(it.Response))
	}
	if got := changes(w.c); got != "" {
		t.Fatalf("after the push, changes: %s", got)
	}
}

// A pull takes in what the team changed, and keeps what changed here.
func TestPull(t *testing.T) {
	w := newWorld(t)
	w.teamEdit("List orders", func(it *postman.Item) { it.Request.Method = "HEAD" })
	w.team.Edit(w.col, func(c *postman.Collection) *postman.Item {
		orders := find(c, "Orders")
		orders.Item = append(orders.Item, &postman.Item{ID: "new-1", Name: "Cancel order", Request: &postman.Request{Method: "DELETE", URL: postman.Rebuild(nil, "{{base}}/orders/1", nil, nil)}})
		c.Remove(find(c, "Health").ID)
		return nil
	})
	w.edit("List products", func(it *postman.Item) { it.Request.Method = "PUT" })
	got, err := w.c.Pull(w.ctx, w.api)
	if err != nil {
		t.Fatal(err)
	}
	if got.Took != 3 || got.Kept != 1 {
		t.Fatalf("pulled %+v", got)
	}
	c, _ := w.localCol()
	if it := find(c, "List orders"); it.Request.Method != "HEAD" {
		t.Error("the team's change didn't come in")
	}
	if it := find(c, "Cancel order"); it == nil {
		t.Error("the team's new request didn't come in")
	} else if _, parents := c.Find(it.ID); len(parents) != 1 || parents[0].Name != "Orders" {
		t.Error("the team's new request isn't in its folder")
	}
	if find(c, "Health") != nil {
		t.Error("the team's deletion didn't come in")
	}
	if it := find(c, "List products"); it.Request.Method != "PUT" {
		t.Error("the local change went")
	}
	if got := changes(w.c); got != "changed request List products" {
		t.Fatalf("after the pull, changes: %s", got)
	}
}

// Both changed a request: the push asks; overwriting pushes mine, and not
// overwriting keeps it here.
func TestConflict(t *testing.T) {
	w := newWorld(t)
	w.teamEdit("Get product", func(it *postman.Item) { it.Request.Method = "PATCH" })
	w.edit("Get product", func(it *postman.Item) { it.Request.Method = "PUT" })
	w.edit("Health", func(it *postman.Item) { it.Request.Method = "HEAD" })
	p, res := w.push(nil)
	if len(p.Conflicts) != 1 || p.Conflicts[0].Kind != "request" || p.Conflicts[0].Name != "Shop / Products / Get product" || p.Conflicts[0].What != "changed here and in Postman" {
		t.Fatalf("conflicts: %+v", p.Conflicts)
	}
	cf := p.Conflicts[0]
	if cf.Base.Request.Method != "GET" || cf.Mine.Request.Method != "PUT" || cf.Theirs.Request.Method != "PATCH" {
		t.Fatalf("versions: %s %s %s", cf.Base.Request.Method, cf.Mine.Request.Method, cf.Theirs.Request.Method)
	}
	if res.Pushed != 1 || find(w.team.Collection(w.col), "Health").Request.Method != "HEAD" {
		t.Fatal("the change without a conflict wasn't pushed")
	}
	if find(w.team.Collection(w.col), "Get product").Request.Method != "PATCH" {
		t.Fatal("the conflict was pushed without asking")
	}
	if got := changes(w.c); got != "changed request Get product" {
		t.Fatalf("after skipping, changes: %s", got)
	}
	// Again, overwriting.
	p, res = w.push(map[string]bool{cf.Key: true})
	if len(p.Conflicts) != 1 || res.Pushed != 1 {
		t.Fatalf("second push: %d conflicts, %d pushed", len(p.Conflicts), res.Pushed)
	}
	if find(w.team.Collection(w.col), "Get product").Request.Method != "PUT" {
		t.Fatal("overwriting didn't push mine")
	}
	if got := changes(w.c); got != "" {
		t.Fatalf("after overwriting, changes: %s", got)
	}
}

// New folders and requests made here go to Postman in their place, and the
// copy takes the ids Postman gave them.
func TestCreateAndMove(t *testing.T) {
	w := newWorld(t)
	c, uid := w.localCol()
	fid, err := w.here.CreateFolder(w.ctx, uid, "", &postman.Item{Name: "Admin", Folder: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.here.CreateRequest(w.ctx, uid, fid, &postman.Item{Name: "Users", Request: &postman.Request{Method: "GET", URL: postman.Rebuild(nil, "{{base}}/users", nil, nil)}}); err != nil {
		t.Fatal(err)
	}
	// Health into Orders.
	owner, _ := postman.SplitUID(uid)
	orders := find(c, "Orders")
	if _, err := w.here.Transfer(w.ctx, false, []string{owner + "-" + find(c, "Health").ID}, postman.Place{Collection: uid, Folder: owner + "-" + orders.ID, Position: "end"}, false); err != nil {
		t.Fatal(err)
	}
	if got := changes(w.c); got != "moved request Health, new folder Admin, new request Users" {
		t.Fatalf("changes: %s", got)
	}
	_, res := w.push(nil)
	if res.Pushed != 3 {
		t.Fatalf("pushed %d", res.Pushed)
	}
	team := w.team.Collection(w.col)
	admin := find(team, "Admin")
	if admin == nil || len(admin.Item) != 1 || admin.Item[0].Name != "Users" {
		t.Fatalf("Admin in Postman: %+v", admin)
	}
	if _, parents := team.Find(find(team, "Health").ID); len(parents) != 1 || parents[0].Name != "Orders" {
		t.Fatal("Health didn't move in Postman")
	}
	here, _ := w.localCol()
	if find(here, "Admin").ID != admin.ID || res.IDs[fid] != admin.ID {
		t.Fatal("the copy doesn't have Postman's ids")
	}
	if got := changes(w.c); got != "" {
		t.Fatalf("after the push, changes: %s", got)
	}
}

func TestDeleteHereAndThere(t *testing.T) {
	w := newWorld(t)
	_, uid := w.localCol()
	c, _ := w.localCol()
	if err := w.here.DeleteRequest(w.ctx, uid, find(c, "Health").ID); err != nil {
		t.Fatal(err)
	}
	// Deleted here, changed there: asked.
	if err := w.here.DeleteFolder(w.ctx, uid, find(c, "Orders").ID); err != nil {
		t.Fatal(err)
	}
	w.teamEdit("List orders", func(it *postman.Item) { it.Request.Method = "HEAD" })
	p, res := w.push(nil)
	if res.Pushed != 1 || find(w.team.Collection(w.col), "Health") != nil {
		t.Fatal("the deletion wasn't pushed")
	}
	if len(p.Conflicts) != 1 || p.Conflicts[0].What != "deleted here, changed in Postman" || p.Conflicts[0].Kind != "folder" {
		t.Fatalf("conflicts: %+v", p.Conflicts)
	}
	if find(w.team.Collection(w.col), "Orders") == nil {
		t.Fatal("a folder the team changed was deleted without asking")
	}
	p, _ = w.push(map[string]bool{p.Conflicts[0].Key: true})
	if find(w.team.Collection(w.col), "Orders") != nil {
		t.Fatal("overwriting didn't delete it")
	}
	if got := changes(w.c); got != "" {
		t.Fatalf("after the push, changes: %s", got)
	}
}

// Changed here, deleted in Postman: overwriting makes it again.
func TestChangedHereDeletedThere(t *testing.T) {
	w := newWorld(t)
	w.edit("Health", func(it *postman.Item) { it.Request.Method = "HEAD" })
	w.team.Edit(w.col, func(c *postman.Collection) *postman.Item {
		c.Remove(find(c, "Health").ID)
		return nil
	})
	p, _ := w.push(nil)
	if len(p.Conflicts) != 1 || p.Conflicts[0].What != "changed here, deleted in Postman" || p.Conflicts[0].Theirs != nil {
		t.Fatalf("conflicts: %+v", p.Conflicts)
	}
	_, res := w.push(map[string]bool{p.Conflicts[0].Key: true})
	if it := find(w.team.Collection(w.col), "Health"); it == nil || it.Request.Method != "HEAD" || res.Pushed != 1 {
		t.Fatal("overwriting didn't make it again")
	}
	if got := changes(w.c); got != "" {
		t.Fatalf("after the push, changes: %s", got)
	}
}

// Environments: what only one side changed merges by variable; the same
// variable changed on both sides is a conflict.
func TestEnvironments(t *testing.T) {
	w := newWorld(t)
	esum, _ := w.here.Environments(w.ctx, "ws1")
	e, _ := w.here.Environment(w.ctx, esum[0].UID)
	e.Values[0].Value = "http://here"
	if err := w.here.PutEnvironment(w.ctx, esum[0].UID, e); err != nil {
		t.Fatal(err)
	}
	te := w.team.Environment(w.envID)
	te.Values[1].Value = "t2"
	w.team.AddEnvironment("ws1", te) // same id: replaced
	p, res := w.push(nil)
	if len(p.Conflicts) != 0 || res.Pushed != 1 {
		t.Fatalf("%d conflicts, %d pushed", len(p.Conflicts), res.Pushed)
	}
	got := w.team.Environment(w.envID)
	if v, _ := got.Get("base"); v != "http://here" {
		t.Errorf("base: %s", v)
	}
	if v, _ := got.Get("token"); v != "t2" {
		t.Errorf("the team's token went: %s", v)
	}
	// Both change token.
	e, _ = w.here.Environment(w.ctx, esum[0].UID)
	e.Values[1].Value = "mine"
	_ = w.here.PutEnvironment(w.ctx, esum[0].UID, e)
	te = w.team.Environment(w.envID)
	te.Values[1].Value = "theirs"
	w.team.AddEnvironment("ws1", te)
	p, _ = w.push(nil)
	if len(p.Conflicts) != 1 || p.Conflicts[0].Kind != "environment" {
		t.Fatalf("conflicts: %+v", p.Conflicts)
	}
}

// A collection made here goes to Postman whole, and the copy takes its ids.
func TestNewCollection(t *testing.T) {
	w := newWorld(t)
	sum, err := w.here.CreateCollection(w.ctx, "ws1", &postman.Collection{Info: postman.Info{Name: "Billing"}, Item: []*postman.Item{
		{Name: "Invoices", Folder: true, Item: []*postman.Item{{Name: "List", Request: &postman.Request{Method: "GET", URL: postman.Rebuild(nil, "/invoices", nil, nil)}}}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if got := changes(w.c); got != "new collection Billing" {
		t.Fatalf("changes: %s", got)
	}
	_, res := w.push(nil)
	to := res.IDs[sum.ID]
	team := w.team.Collection(to)
	if team == nil || find(team, "List") == nil {
		t.Fatal("the collection didn't reach Postman")
	}
	if got := changes(w.c); got != "" {
		t.Fatalf("after the push, changes: %s", got)
	}
	sums, _ := w.here.Collections(w.ctx, "ws1")
	n := 0
	for _, s := range sums {
		if s.Name == "Billing" {
			n++
			if s.ID != to {
				t.Error("the copy doesn't have Postman's id")
			}
		}
	}
	if n != 1 {
		t.Fatalf("%d Billing collections here", n)
	}
}

// Collection settings: what changed here is patched; a pull takes the
// team's.
func TestCollectionSettings(t *testing.T) {
	w := newWorld(t)
	_, uid := w.localCol()
	name := "Shop (local)"
	if err := w.here.PatchCollection(w.ctx, uid, postman.CollectionPatch{Name: &name}); err != nil {
		t.Fatal(err)
	}
	if got := changes(w.c); got != "changed collection Shop (local)" {
		t.Fatalf("changes: %s", got)
	}
	w.push(nil)
	if got := w.team.Collection(w.col).Info.Name; got != name {
		t.Fatalf("name in Postman: %s", got)
	}
	w.team.Edit(w.col, func(c *postman.Collection) *postman.Item {
		c.Info.Description = &postman.Description{Content: "from the team"}
		return nil
	})
	if _, err := w.c.Pull(w.ctx, w.api); err != nil {
		t.Fatal(err)
	}
	c, _ := w.localCol()
	if c.Info.Description == nil || c.Info.Description.Content != "from the team" {
		t.Fatal("the team's description didn't come in")
	}
}

func names(items []*postman.Item) string {
	var out []string
	for _, it := range items {
		out = append(out, it.Name)
	}
	return strings.Join(out, ",")
}

// The order of things here is the order they get in Postman: a request
// moved to the start of its folder, one made between two others. (In
// Postman, as here, a folder's requests come after its folders.)
func TestOrder(t *testing.T) {
	w := newWorld(t)
	c, uid := w.localCol()
	owner, _ := postman.SplitUID(uid)
	products := find(c, "Products")
	if _, err := w.here.Transfer(w.ctx, false, []string{owner + "-" + find(c, "Get product").ID}, postman.Place{Collection: uid, Folder: owner + "-" + products.ID, Position: "start"}, false); err != nil {
		t.Fatal(err)
	}
	made, err := w.here.CreateRequest(w.ctx, uid, products.ID, &postman.Item{Name: "Search", Request: &postman.Request{Method: "GET", URL: postman.Rebuild(nil, "/search", nil, nil)}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.here.Transfer(w.ctx, false, []string{owner + "-" + made}, postman.Place{Collection: uid, Folder: owner + "-" + products.ID, Position: "after", Next: owner + "-" + find(c, "Get product").ID}, false); err != nil {
		t.Fatal(err)
	}
	here, _ := w.localCol()
	if got := names(find(here, "Products").Item); got != "Get product,Search,List products" {
		t.Fatalf("here: %s", got)
	}
	w.push(nil)
	team := w.team.Collection(w.col)
	if got := names(find(team, "Products").Item); got != "Get product,Search,List products" {
		t.Errorf("Products in Postman: %s", got)
	}
	if got := changes(w.c); got != "" {
		t.Fatalf("after the push, changes: %s", got)
	}
	// Nothing changed anywhere: a pull takes nothing, and a push sends
	// nothing.
	if got, err := w.c.Pull(w.ctx, w.api); err != nil || got.Took != 0 || got.Kept != 0 {
		t.Fatalf("an idle pull: %+v %v", got, err)
	}
	if p, _ := w.push(nil); p.Changes() != 0 {
		t.Fatalf("an idle push sends %d", p.Changes())
	}
}

// The team's order changes come in where nothing moved here.
func TestPullOrder(t *testing.T) {
	w := newWorld(t)
	w.team.Edit(w.col, func(c *postman.Collection) *postman.Item {
		products := find(c, "Products")
		products.Item[0], products.Item[1] = products.Item[1], products.Item[0]
		return nil
	})
	if _, err := w.c.Pull(w.ctx, w.api); err != nil {
		t.Fatal(err)
	}
	here, _ := w.localCol()
	if got := names(find(here, "Products").Item); got != "Get product,List products" {
		t.Fatalf("here: %s", got)
	}
	if got := changes(w.c); got != "" {
		t.Fatalf("after the pull, changes: %s", got)
	}
}
