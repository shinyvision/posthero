package local

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/shinyvision/posthero/internal/postman"
)

// TestLiveRoundTrip pushes local changes to the real Postman API, in a
// collection and environment it makes in POSTHERO_LIVE_WORKSPACE (use a
// personal one) and deletes afterwards. It runs only with POSTMAN_API_KEY
// set.
func TestLiveRoundTrip(t *testing.T) {
	key, ws := os.Getenv("POSTMAN_API_KEY"), os.Getenv("POSTHERO_LIVE_WORKSPACE")
	if key == "" || ws == "" {
		t.Skip("POSTMAN_API_KEY and POSTHERO_LIVE_WORKSPACE are not set")
	}
	ctx := context.Background()
	api := postman.NewClient(key, "")
	req := func(name, method string) *postman.Item {
		return &postman.Item{Name: name, Request: &postman.Request{Method: method, URL: postman.Rebuild(nil, "https://example.com/"+strings.ToLower(name), nil, nil)}}
	}
	sum, err := api.CreateCollection(ctx, ws, &postman.Collection{Info: postman.Info{Name: "posthero local test (safe to delete)"}, Item: []*postman.Item{
		{Name: "F", Folder: true, Item: []*postman.Item{req("R1", "GET"), req("R2", "GET")}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := api.DeleteCollection(ctx, sum.UID); err != nil {
			t.Errorf("clean up the collection: %v", err)
		}
	}()
	esum, err := api.CreateEnvironment(ctx, ws, &postman.Environment{Name: "posthero local test env (safe to delete)", Values: []postman.EnvValue{{Key: "a", Value: "1"}, {Key: "b", Value: "1"}}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := api.DeleteEnvironment(ctx, esum.UID); err != nil {
			t.Errorf("clean up the environment: %v", err)
		}
	}()
	// The copy starts from the team's version (only these two).
	team := func() *postman.Collection {
		t.Helper()
		c, err := api.Collection(ctx, sum.UID)
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	sums, err := api.Collections(ctx, ws)
	if err != nil {
		t.Fatal(err)
	}
	var cols []Remote
	for _, s := range sums {
		if s.ID == sum.ID {
			cols = append(cols, Remote{UID: s.UID, UpdatedAt: s.UpdatedAt, Data: team()})
		}
	}
	esums, _ := api.Environments(ctx, ws)
	var envs []RemoteEnv
	for _, s := range esums {
		if s.ID == esum.ID {
			e, err := api.Environment(ctx, s.UID)
			if err != nil {
				t.Fatal(err)
			}
			envs = append(envs, RemoteEnv{UID: s.UID, UpdatedAt: s.UpdatedAt, Data: e})
		}
	}
	globals, _ := api.Globals(ctx, ws)
	c, err := Start(t.TempDir(), postman.Workspace{ID: ws}, cols, envs, globals)
	if err != nil {
		t.Fatal(err)
	}
	here := c.Client()
	lsums, _ := here.Collections(ctx, ws)
	var luid string
	for _, s := range lsums {
		if s.ID == sum.ID {
			luid = s.UID
		}
	}
	lc, err := here.Collection(ctx, luid)
	if err != nil {
		t.Fatal(err)
	}
	owner, _ := postman.SplitUID(luid)
	f := find(lc, "F")
	// Here: R1 changed; R3 made in F after R1; folder G with R4; R2 moved
	// to the top; an environment variable changed.
	r1 := find(lc, "R1")
	r1.Request.Method = "POST"
	if _, err := here.UpdateRequest(ctx, luid, r1); err != nil {
		t.Fatal(err)
	}
	r3, err := here.CreateRequest(ctx, luid, f.ID, req("R3", "PUT"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := here.Transfer(ctx, false, []string{owner + "-" + r3}, postman.Place{Collection: luid, Folder: owner + "-" + f.ID, Position: "after", Next: owner + "-" + r1.ID}, false); err != nil {
		t.Fatal(err)
	}
	g, err := here.CreateFolder(ctx, luid, "", &postman.Item{Name: "G", Folder: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := here.CreateRequest(ctx, luid, g, req("R4", "DELETE")); err != nil {
		t.Fatal(err)
	}
	if _, err := here.Transfer(ctx, false, []string{owner + "-" + find(lc, "R2").ID}, postman.Place{Collection: luid, Position: "end"}, false); err != nil {
		t.Fatal(err)
	}
	lenvs, _ := here.Environments(ctx, ws)
	le, _ := here.Environment(ctx, lenvs[0].UID)
	le.Values[0].Value = "here"
	if err := here.PutEnvironment(ctx, lenvs[0].UID, le); err != nil {
		t.Fatal(err)
	}
	// The team changes the other variable.
	te, _ := api.Environment(ctx, esum.UID)
	te.Values[1].Value = "team"
	if err := api.PutEnvironment(ctx, esum.UID, te); err != nil {
		t.Fatal(err)
	}

	p, err := c.Plan(ctx, api)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Conflicts) != 0 {
		t.Fatalf("conflicts: %+v", p.Conflicts)
	}
	res, err := c.Push(ctx, api, p, nil, "")
	if err != nil {
		t.Fatalf("push: %v", err)
	}
	got := team()
	if names(got.Item) != "F,G,R2" {
		t.Errorf("top in Postman: %s", names(got.Item))
	}
	if gf := find(got, "F"); names(gf.Item) != "R1,R3" {
		t.Errorf("F in Postman: %s", names(gf.Item))
	}
	if find(got, "R1").Request.Method != "POST" || find(got, "R4") == nil {
		t.Errorf("R1 %s, R4 %v", find(got, "R1").Request.Method, find(got, "R4"))
	}
	e, _ := api.Environment(ctx, esum.UID)
	if a, _ := e.Get("a"); a != "here" {
		t.Errorf("a: %s", a)
	}
	if b, _ := e.Get("b"); b != "team" {
		t.Errorf("b: %s", b)
	}
	if left := c.Changes(); len(left) != 0 {
		t.Errorf("after the push, left: %+v", left)
	}
	t.Logf("pushed %d", res.Pushed)

	// Both change R1: a conflict; overwriting pushes mine.
	lc, _ = here.Collection(ctx, luid)
	r1 = find(lc, "R1")
	r1.Request.Method = "PATCH"
	if _, err := here.UpdateRequest(ctx, luid, r1); err != nil {
		t.Fatal(err)
	}
	tr1 := find(team(), "R1")
	tr1.Request.Method = "HEAD"
	if _, err := api.UpdateRequest(ctx, sum.UID, tr1); err != nil {
		t.Fatal(err)
	}
	p, err = c.Plan(ctx, api)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Conflicts) != 1 || p.Conflicts[0].Item != r1.ID {
		t.Fatalf("conflicts: %+v", p.Conflicts)
	}
	if _, err := c.Push(ctx, api, p, map[string]bool{p.Conflicts[0].Key: true}, ""); err != nil {
		t.Fatal(err)
	}
	if m := find(team(), "R1").Request.Method; m != "PATCH" {
		t.Errorf("overwritten: %s", m)
	}
	if left := c.Changes(); len(left) != 0 {
		t.Errorf("after overwriting, left: %+v", left)
	}
}
