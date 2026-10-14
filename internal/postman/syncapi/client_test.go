package syncapi_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shinyvision/posthero/internal/postman"
	"github.com/shinyvision/posthero/internal/postman/syncapi"
	"github.com/shinyvision/posthero/internal/postman/syncapi/synctest"
)

func start(t *testing.T) (*synctest.Server, *syncapi.Client) {
	t.Helper()
	srv := synctest.New()
	base := srv.Start()
	c := syncapi.New(synctest.Token, syncapi.Options{URL: base, IdentityURL: base})
	t.Cleanup(func() {
		c.Close()
		srv.Close()
	})
	return srv, c
}

// eventually waits for cond, failing after a few seconds.
func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if cond() {
			return
		}
	}
	t.Fatalf("timed out waiting for %s", what)
}

const sample = `{
	"id": "c0000000-0000-4000-8000-000000000001", "name": "Sample", "order": ["r0000000-0000-4000-8000-000000000003"],
	"folders_order": ["f0000000-0000-4000-8000-000000000001"],
	"folders": [{"id": "f0000000-0000-4000-8000-000000000001", "name": "Users", "folder": null,
		"order": ["r0000000-0000-4000-8000-000000000001", "r0000000-0000-4000-8000-000000000002"], "folders_order": []}],
	"requests": [
		{"id": "r0000000-0000-4000-8000-000000000001", "name": "List", "folder": "f0000000-0000-4000-8000-000000000001", "method": "GET", "url": "{{base}}/users",
		 "responses_order": ["e0000000-0000-4000-8000-000000000001"],
		 "responses": [{"id": "e0000000-0000-4000-8000-000000000001", "name": "OK", "responseCode": {"code": 200, "name": "OK"}, "text": "[]"}]},
		{"id": "r0000000-0000-4000-8000-000000000002", "name": "Get", "folder": "f0000000-0000-4000-8000-000000000001", "method": "GET", "url": "{{base}}/users/:id",
		 "pathVariableData": [{"key": "id", "value": "7"}]},
		{"id": "r0000000-0000-4000-8000-000000000003", "name": "Health", "folder": null, "method": "GET", "url": "{{base}}/health"}
	]}`

// TestLostConnection: a read lost with the connection is asked again on a
// new one; a write isn't (it may have been made), and says so.
func TestLostConnection(t *testing.T) {
	srv, c := start(t)
	ctx := context.Background()
	uid, _ := srv.AddCollection("ws1", []byte(sample))
	if _, err := c.Collections(ctx, "ws1"); err != nil {
		t.Fatal(err)
	}
	srv.DropNext("get /collection")
	list, err := c.Collections(ctx, "ws1")
	if err != nil || len(list) != 1 {
		t.Fatalf("a read across a lost connection: %v %v", list, err)
	}
	if n := srv.CallCount("get /collection"); n != 3 {
		t.Errorf("listed %d times, want 3 (one lost and asked again)", n)
	}
	srv.DropNext("put /collection/c0000000-0000-4000-8000-000000000001")
	name := "Renamed"
	err = c.PatchCollection(ctx, uid, postman.CollectionPatch{Name: &name})
	if !errors.Is(err, syncapi.ErrDisconnected) {
		t.Fatalf("a write across a lost connection: %v", err)
	}
	if n := srv.CallCount("put /collection/c0000000-0000-4000-8000-000000000001"); n != 1 {
		t.Errorf("the write was sent %d times", n)
	}
	// The next request connects again.
	if err := c.PatchCollection(ctx, uid, postman.CollectionPatch{Name: &name}); err != nil {
		t.Fatal(err)
	}
	if got := srv.Collection(uid)["name"]; got != "Renamed" {
		t.Errorf("name %v", got)
	}
}

// TestHeartbeat: a connection whose service stopped answering is given up,
// and the next request makes a new one.
func TestHeartbeat(t *testing.T) {
	srv := synctest.New()
	srv.PingInterval, srv.PingTimeout = 30*time.Millisecond, 60*time.Millisecond
	base := srv.Start()
	defer srv.Close()
	c := syncapi.New(synctest.Token, syncapi.Options{URL: base, IdentityURL: base})
	defer c.Close()
	ctx := context.Background()
	if _, err := c.Workspaces(ctx); err != nil {
		t.Fatal(err)
	}
	srv.SetSilent(true)
	eventually(t, "the silent connection to be given up", func() bool { return srv.Connections() == 0 })
	srv.SetSilent(false)
	if _, err := c.Workspaces(ctx); err != nil {
		t.Fatal(err)
	}
	if srv.Connections() != 1 {
		t.Errorf("%d connections", srv.Connections())
	}
}

// TestWatch: changes are told about as they happen, and after a lost
// connection the watch is set up again (and says changes may have been
// missed).
func TestWatch(t *testing.T) {
	srv, c := start(t)
	ctx, cancel := context.WithCancel(context.Background())
	uid, _ := srv.AddCollection("ws1", []byte(sample))
	var mu sync.Mutex
	var got []postman.Change
	if err := c.Watch(ctx, "ws1", func(ch postman.Change) {
		mu.Lock()
		got = append(got, ch)
		mu.Unlock()
	}); err != nil {
		t.Fatal(err)
	}
	seen := func(want postman.Change) func() bool {
		return func() bool {
			mu.Lock()
			defer mu.Unlock()
			for _, ch := range got {
				if ch == want {
					return true
				}
			}
			return false
		}
	}
	env, err := c.CreateEnvironment(ctx, "ws1", &postman.Environment{Name: "Local"})
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "the new environment", seen(postman.Change{Kind: "environment", UID: env.UID, Action: "create"}))
	srv.Edit("r0000000-0000-4000-8000-000000000003", func(f map[string]any) { f["name"] = "Ping" })
	eventually(t, "the teammate's change", seen(postman.Change{Kind: "collection", UID: uid, Action: "update"}))

	srv.Drop()
	eventually(t, "the reconnect", seen(postman.Change{Action: "reconnect"}))
	if err := c.DeleteEnvironment(ctx, env.UID); err != nil {
		t.Fatal(err)
	}
	eventually(t, "a change after the reconnect", seen(postman.Change{Kind: "environment", UID: env.UID, Action: "delete"}))

	cancel()
	time.Sleep(50 * time.Millisecond)
	mu.Lock()
	n := len(got)
	mu.Unlock()
	srv.Edit("r0000000-0000-4000-8000-000000000003", func(f map[string]any) { f["name"] = "Pong" })
	time.Sleep(100 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if len(got) != n {
		t.Errorf("told about a change after the watch ended: %+v", got[n:])
	}
}

// TestCollectionUpdatedAt: like the Postman API, a collection's updatedAt
// is when the collection itself last changed, not what is in it; the
// service's answer for the collection says the latter.
func TestCollectionUpdatedAt(t *testing.T) {
	srv, c := start(t)
	ctx := context.Background()
	uid, _ := srv.AddCollection("ws1", []byte(sample))
	list, err := c.Collections(ctx, "ws1")
	if err != nil {
		t.Fatal(err)
	}
	srv.Edit("r0000000-0000-4000-8000-000000000001", func(f map[string]any) { f["name"] = "All" })
	col, err := c.Collection(ctx, uid)
	if err != nil {
		t.Fatal(err)
	}
	if col.Info.UpdatedAt != list[0].UpdatedAt {
		t.Errorf("updatedAt %s, want the collection's own %s", col.Info.UpdatedAt, list[0].UpdatedAt)
	}
	if it, _ := col.Find("r0000000-0000-4000-8000-000000000001"); it == nil || it.Name != "All" || it.UpdatedAt <= list[0].UpdatedAt {
		t.Errorf("the changed request: %+v", it)
	}
	// A change to the collection itself moves it.
	name := "Sample 2"
	if err := c.PatchCollection(ctx, uid, postman.CollectionPatch{Name: &name}); err != nil {
		t.Fatal(err)
	}
	col, _ = c.Collection(ctx, uid)
	if col.Info.UpdatedAt <= list[0].UpdatedAt {
		t.Errorf("updatedAt didn't move with the collection: %s", col.Info.UpdatedAt)
	}
}

// TestTransfer: moving and copying put things where Place says, copies
// with everything in them.
func TestTransfer(t *testing.T) {
	srv, c := start(t)
	ctx := context.Background()
	uid, _ := srv.AddCollection("ws1", []byte(sample))
	names := func(items []*postman.Item) string {
		var out []string
		for _, it := range items {
			out = append(out, it.Name)
		}
		return strings.Join(out, ",")
	}
	owner := synctest.Owner + "-"
	// Health goes into Users, before Get.
	moved, err := c.Transfer(ctx, false, []string{owner + "r0000000-0000-4000-8000-000000000003"},
		postman.Place{Collection: uid, Folder: owner + "f0000000-0000-4000-8000-000000000001", Position: "before", Next: owner + "r0000000-0000-4000-8000-000000000002"}, false)
	if err != nil || len(moved) != 1 || moved[0] != owner+"r0000000-0000-4000-8000-000000000003" {
		t.Fatalf("move: %v %v", moved, err)
	}
	col, _ := c.Collection(ctx, uid)
	if got := names(col.Item[0].Item); got != "List,Health,Get" || len(col.Item) != 1 {
		t.Fatalf("after the move: %s (%d at the top)", got, len(col.Item))
	}
	// A copy of the folder, at the top: everything in it again, examples
	// too, with new ids.
	copies, err := c.Transfer(ctx, true, []string{owner + "f0000000-0000-4000-8000-000000000001"}, postman.Place{Collection: uid, Position: "start"}, true)
	if err != nil || len(copies) != 1 {
		t.Fatalf("copy: %v %v", copies, err)
	}
	col, _ = c.Collection(ctx, uid)
	if got := names(col.Item); got != "Users Copy,Users" {
		t.Fatalf("after the copy: %s", got)
	}
	cp := col.Item[0]
	if cp.UID != copies[0] || names(cp.Item) != "List,Health,Get" || len(cp.Item[0].Response) != 1 || cp.Item[0].Response[0].Body != "[]" {
		t.Errorf("the copy: %s %+v", cp.UID, cp.Item)
	}
	if cp.Item[0].ID == "r0000000-0000-4000-8000-000000000001" || cp.Item[2].Request.URL.Variable[0].Value != "7" {
		t.Errorf("the copy's requests: %+v", cp.Item)
	}
	// A request after a request, at the end, and a folder's place among
	// requests (which Postman refuses).
	if _, err := c.Transfer(ctx, false, []string{owner + "r0000000-0000-4000-8000-000000000001"}, postman.Place{Collection: uid, Position: "end"}, false); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Transfer(ctx, false, []string{owner + "r0000000-0000-4000-8000-000000000002"}, postman.Place{Collection: uid, Position: "after", Next: owner + "r0000000-0000-4000-8000-000000000001"}, false); err != nil {
		t.Fatal(err)
	}
	col, _ = c.Collection(ctx, uid)
	if got := names(col.Item); got != "Users Copy,Users,List,Get" {
		t.Errorf("at the top: %s", got)
	}
	_, err = c.Transfer(ctx, true, []string{copies[0]}, postman.Place{Collection: uid, Position: "before", Next: owner + "r0000000-0000-4000-8000-000000000001"}, false)
	var pe *postman.Error
	if !errors.As(err, &pe) || pe.Status != 400 {
		t.Errorf("a folder before a request: %v", err)
	}
	if _, err := c.Transfer(ctx, false, []string{owner + "c0ffee00-0000-4000-8000-000000000000"}, postman.Place{Collection: uid, Position: "end"}, false); !postman.IsNotFound(err) {
		t.Errorf("moving what isn't there: %v", err)
	}
}

// TestNotFound: what isn't there is a 404, a collection's says so.
func TestNotFound(t *testing.T) {
	_, c := start(t)
	ctx := context.Background()
	_, err := c.Collection(ctx, synctest.Owner+"-c0ffee00-0000-4000-8000-000000000000")
	var pe *postman.Error
	if !postman.IsNotFound(err) || !errors.As(err, &pe) || !strings.Contains(pe.Message, "collection") {
		t.Errorf("a missing collection: %v", err)
	}
	if err := c.DeleteRequest(ctx, synctest.Owner+"-c0ffee00-0000-4000-8000-000000000000", "c0ffee00-0000-4000-8000-000000000009"); !postman.IsNotFound(err) {
		t.Errorf("a missing request: %v", err)
	}
}
