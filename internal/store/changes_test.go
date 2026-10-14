package store

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/shinyvision/posthero/internal/postman"
	"github.com/shinyvision/posthero/internal/postman/pmtest"
)

type countedAPI struct {
	postman.API
	mu    sync.Mutex
	calls map[string]int
}

func (c *countedAPI) hit(k string) { c.mu.Lock(); defer c.mu.Unlock(); c.calls[k]++ }
func (c *countedAPI) Collections(ctx context.Context, ws string) ([]postman.Summary, error) {
	c.hit("collections")
	return c.API.Collections(ctx, ws)
}
func (c *countedAPI) Environments(ctx context.Context, ws string) ([]postman.Summary, error) {
	c.hit("environments")
	return c.API.Environments(ctx, ws)
}
func (c *countedAPI) Collection(ctx context.Context, id string) (*postman.Collection, error) {
	c.hit(id)
	return c.API.Collection(ctx, id)
}
func (c *countedAPI) Environment(ctx context.Context, id string) (*postman.Environment, error) {
	c.hit(id)
	return c.API.Environment(ctx, id)
}

func TestLiveRefreshFetchesOnlyAffectedEntities(t *testing.T) {
	srv := pmtest.New()
	defer srv.Close()
	api := &countedAPI{API: postman.NewClient(pmtest.Key, srv.Start()), calls: map[string]int{}}
	col := &postman.Collection{Info: postman.Info{Name: "One"}, Item: []*postman.Item{{Name: "Request", Request: &postman.Request{Method: "GET", URL: postman.ParseURL("https://example.test")}}}}
	id := srv.AddCollection("ws1", col)
	other := srv.AddCollection("ws1", &postman.Collection{Info: postman.Info{Name: "Two"}})
	env := srv.AddEnvironment("ws1", &postman.Environment{Name: "Environment"})
	s := Open(api, postman.Workspace{ID: "ws1"}, "", "")
	f, err := Fetch(context.Background(), s.Plan(false))
	if err != nil {
		t.Fatal(err)
	}
	s.Apply(f)
	uid := s.Col(id).Summary.UID
	api.calls = map[string]int{}
	srv.Edit(id, func(c *postman.Collection) *postman.Item { c.Item[0].Name = "Changed"; return c.Item[0] })
	p := s.Plan(false)
	p.Changes = []postman.Change{{Kind: "collection", UID: uid, Action: "update"}, {Kind: "collection", UID: uid, Action: "update"}}
	p.Recheck = []string{other} // ordinary polling rechecks must not leak into live fetches
	f, err = Fetch(context.Background(), p)
	if err != nil || len(f.Errors) > 0 {
		t.Fatalf("%v %v", err, f.Errors)
	}
	s.Apply(f)
	if s.Col(id).Data.Item[0].Name != "Changed" {
		t.Fatal("lost live edit")
	}
	if api.calls["collections"] != 1 || api.calls[uid] != 1 || len(api.calls) != 2 {
		t.Fatalf("unrelated requests: %+v", api.calls)
	}
	// Even a stale delete notification cannot remove something that still exists.
	p = s.Plan(false)
	p.Changes = []postman.Change{{Kind: "collection", UID: uid, Action: "delete"}}
	f, err = Fetch(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	s.Apply(f)
	if s.Col(id) == nil {
		t.Fatal("stale delete removed live collection")
	}
	api.calls = map[string]int{}
	envUID := s.Env(env).Summary.UID
	if err := api.DeleteEnvironment(context.Background(), envUID); err != nil {
		t.Fatal(err)
	}
	p = s.Plan(false)
	p.Changes = []postman.Change{{Kind: "environment", UID: envUID, Action: "update"}}
	f, err = Fetch(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	s.Apply(f)
	if s.Env(env) != nil || len(api.calls) != 1 || api.calls["environments"] != 1 {
		t.Fatalf("environment reconciliation: %+v", api.calls)
	}
	if s.Col(other) == nil {
		t.Fatal("unaffected collection removed")
	}
}

func TestStaleFetchAndProgressCannotOverwriteSaveOrDeletion(t *testing.T) {
	dir := t.TempDir()
	s := Open(nil, postman.Workspace{ID: "ws"}, dir, "")
	sum := postman.Summary{ID: "c", UID: "c", Name: "Original"}
	s.PutCol(&Col{Summary: sum, Data: &postman.Collection{Info: postman.Info{Name: "Original"}}})
	plan := s.Plan(false)
	old := &Col{Summary: sum, Data: s.Col("c").Data.Clone()}
	s.Col("c").Data.Info.Name = "Saved while fetching"
	s.Touched("c", "")
	f := Fetched{versions: plan.versions, Cols: []*Col{old}}
	if s.Apply(f).Any() {
		t.Fatal("stale result accepted")
	}
	if s.ApplyProgress(Progress{Loaded: old, versions: plan.versions}) {
		t.Fatal("stale progress accepted")
	}
	if err := FlushCache(context.Background()); err != nil {
		t.Fatal(err)
	}
	again := Open(nil, s.Workspace, dir, "")
	if again.Col("c").Name() != "Saved while fetching" {
		t.Fatal("stale cache overwrote saved edit")
	}
	plan = s.Plan(false)
	s.RemoveCol("c")
	s.Apply(Fetched{versions: plan.versions, Cols: []*Col{old}})
	if s.Col("c") != nil {
		t.Fatal("stale fetch resurrected deletion")
	}
	if err := FlushCache(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "ws", "collections", "c.json")); !os.IsNotExist(err) {
		t.Fatalf("deleted cache survived: %v", err)
	}
}

func TestCacheCoalescesAndOrdersAcrossStores(t *testing.T) {
	dir := t.TempDir()
	ws := postman.Workspace{ID: "ws"}
	first, second := Open(nil, ws, dir, ""), Open(nil, ws, dir, "")
	for i := 0; i < 100; i++ {
		first.PutCol(&Col{Summary: postman.Summary{ID: "c", UID: "c"}, Data: &postman.Collection{Info: postman.Info{Name: "old"}}})
	}
	second.PutCol(&Col{Summary: postman.Summary{ID: "c", UID: "c"}, Data: &postman.Collection{Info: postman.Info{Name: "new"}}})
	if err := FlushCache(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := Open(nil, ws, dir, "").Col("c").Name(); got != "new" {
		t.Fatal(got)
	}
	first.Touched("c", "")
	second.RemoveCol("c")
	if err := FlushCache(context.Background()); err != nil {
		t.Fatal(err)
	}
	if Open(nil, ws, dir, "").Col("c") != nil {
		t.Fatal("old queued write resurrected deleted collection")
	}
}
