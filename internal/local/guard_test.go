package local

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/shinyvision/posthero/internal/postman"
	"github.com/shinyvision/posthero/internal/postman/pmtest"
	"github.com/shinyvision/posthero/internal/postman/syncapi"
	"github.com/shinyvision/posthero/internal/postman/syncapi/synctest"
)

func pushFixture(t *testing.T, sync bool) (*Copy, postman.API, *postman.Client, postman.Summary, postman.Summary) {
	t.Helper()
	var api postman.API
	if sync {
		s := synctest.New()
		base := s.Start()
		t.Cleanup(s.Close)
		client := syncapi.New(synctest.Token, syncapi.Options{URL: base, IdentityURL: base})
		t.Cleanup(client.Close)
		api = client
	} else {
		s := pmtest.New()
		api = postman.NewClient(pmtest.Key, s.Start())
		t.Cleanup(s.Close)
	}
	ctx := context.Background()
	var col postman.Collection
	if err := json.Unmarshal([]byte(shop), &col); err != nil {
		t.Fatal(err)
	}
	cs, err := api.CreateCollection(ctx, "ws1", &col)
	if err != nil {
		t.Fatal(err)
	}
	remote, err := api.Collection(ctx, cs.UID)
	if err != nil {
		t.Fatal(err)
	}
	es, err := api.CreateEnvironment(ctx, "ws1", &postman.Environment{Name: "Env", Values: []postman.EnvValue{{Key: "token", Value: "base"}}})
	if err != nil {
		t.Fatal(err)
	}
	env, err := api.Environment(ctx, es.UID)
	if err != nil {
		t.Fatal(err)
	}
	cp, err := Start(t.TempDir(), postman.Workspace{ID: "ws1"}, []Remote{{UID: cs.UID, Data: remote}}, []RemoteEnv{{UID: es.UID, Data: env}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return cp, api, cp.Client(), cs, es
}

func TestPushRejectsStalePlan(t *testing.T) {
	for _, sync := range []bool{false, true} {
		for _, action := range []string{"request", "approved conflict", "delete request", "delete folder", "delete collection", "folder", "move", "environment", "delete environment", "globals", "local edit"} {
			t.Run(fmt.Sprintf("sync=%v/%s", sync, action), func(t *testing.T) {
				c, api, here, cs, es := pushFixture(t, sync)
				ctx := context.Background()
				localUID, localEnvUID := pmtest.Owner+"-"+cs.ID, pmtest.Owner+"-"+es.ID
				col, err := here.Collection(ctx, localUID)
				if err != nil {
					t.Fatal(err)
				}
				request, folder := find(col, "Health"), find(col, "Products")
				switch action {
				case "delete request":
					err = here.DeleteRequest(ctx, localUID, postman.Ref(request.ID, request.UID))
				case "delete folder":
					err = here.DeleteFolder(ctx, localUID, postman.Ref(folder.ID, folder.UID))
				case "delete collection":
					err = here.DeleteCollection(ctx, localUID)
				case "folder":
					folder.Name = "mine"
					err = here.UpdateFolder(ctx, localUID, folder)
				case "move":
					_, err = here.Transfer(ctx, false, []string{postman.Ref(request.ID, request.UID)}, postman.Place{Collection: localUID, Folder: postman.Ref(folder.ID, folder.UID), Position: "end"}, false)
				case "delete environment":
					err = here.DeleteEnvironment(ctx, localEnvUID)
				case "environment":
					err = here.PutEnvironment(ctx, localEnvUID, &postman.Environment{Name: "Env", Values: []postman.EnvValue{{Key: "token", Value: "mine"}}})
				case "globals":
					err = here.PutGlobals(ctx, "ws1", []postman.EnvValue{{Key: "token", Value: "mine"}})
				default:
					request.Request.Method = "POST"
					_, err = here.UpdateRequest(ctx, localUID, request)
				}
				if err != nil {
					t.Fatal(err)
				}
				if action == "approved conflict" {
					remote, err := api.Collection(ctx, cs.UID)
					if err != nil {
						t.Fatal(err)
					}
					r := find(remote, "Health")
					r.Request.Method = "HEAD"
					if _, err := api.UpdateRequest(ctx, cs.UID, r); err != nil {
						t.Fatal(err)
					}
				}
				p, err := c.Plan(ctx, api)
				if err != nil {
					t.Fatal(err)
				}
				if p.Changes()+len(p.Conflicts) == 0 {
					t.Fatal("fixture has no pending changes")
				}
				overwrite := map[string]bool{}
				for _, cf := range p.Conflicts {
					overwrite[cf.Key] = true
				}
				// Another edit after planning, even after an overwrite was approved.
				switch action {
				case "environment", "delete environment":
					err = api.PutEnvironment(ctx, es.UID, &postman.Environment{Name: "Env", Values: []postman.EnvValue{{Key: "token", Value: "teammate"}}})
				case "globals":
					err = api.PutGlobals(ctx, "ws1", []postman.EnvValue{{Key: "token", Value: "teammate"}})
				case "local edit":
					request.Request.Method = "PATCH"
					_, err = here.UpdateRequest(ctx, localUID, request)
				default:
					remote, getErr := api.Collection(ctx, cs.UID)
					if getErr != nil {
						t.Fatal(getErr)
					}
					r := find(remote, "Health")
					r.Request.Method = "OPTIONS"
					_, err = api.UpdateRequest(ctx, cs.UID, r)
				}
				if err != nil {
					t.Fatal(err)
				}
				before, _ := api.Collection(ctx, cs.UID)
				beforeEnv, _ := api.Environment(ctx, es.UID)
				beforeGlobals, _ := api.Globals(ctx, "ws1")
				res, err := c.Push(ctx, api, p, overwrite, "")
				if err == nil || res.Pushed != 0 || !strings.Contains(err.Error(), "review") {
					t.Fatalf("stale plan wrote: %+v, %v", res, err)
				}
				after, err := api.Collection(ctx, cs.UID)
				if err != nil || !colSame(before, after) {
					t.Fatalf("collection was changed: %v", err)
				}
				afterEnv, err := api.Environment(ctx, es.UID)
				if err != nil || !sameEnv(beforeEnv, afterEnv) {
					t.Fatalf("environment was changed: %v", err)
				}
				afterGlobals, err := api.Globals(ctx, "ws1")
				if err != nil || !sameValues(beforeGlobals, afterGlobals) {
					t.Fatalf("globals were changed: %v", err)
				}
			})
		}
	}
}

type editDuringPush struct {
	postman.API
	after  func()
	writes int
}

func (a *editDuringPush) UpdateRequest(ctx context.Context, uid string, it *postman.Item) (string, error) {
	at, err := a.API.UpdateRequest(ctx, uid, it)
	if err == nil {
		a.writes++
		if a.after != nil {
			f := a.after
			a.after = nil
			f()
		}
	}
	return at, err
}

func TestPushChecksBetweenWrites(t *testing.T) {
	for _, sync := range []bool{false, true} {
		t.Run(fmt.Sprint(sync), func(t *testing.T) {
			c, api, here, cs, _ := pushFixture(t, sync)
			ctx := context.Background()
			localUID := pmtest.Owner + "-" + cs.ID
			col, err := here.Collection(ctx, localUID)
			if err != nil {
				t.Fatal(err)
			}
			for _, name := range []string{"Get product", "Health"} {
				it := find(col, name)
				it.Request.Method = "POST"
				if _, err := here.UpdateRequest(ctx, localUID, it); err != nil {
					t.Fatal(err)
				}
			}
			p, err := c.Plan(ctx, api)
			if err != nil {
				t.Fatal(err)
			}
			if p.Changes() != 2 {
				t.Fatalf("expected two writes, got %d", p.Changes())
			}
			interleaved := &editDuringPush{API: api, after: func() {
				col, err := api.Collection(ctx, cs.UID)
				if err != nil {
					t.Fatal(err)
				}
				it := find(col, "Health")
				it.Request.Method = "HEAD"
				if _, err := api.UpdateRequest(ctx, cs.UID, it); err != nil {
					t.Fatal(err)
				}
			}}
			res, err := c.Push(ctx, interleaved, p, nil, "")
			if err == nil || res.Pushed != 1 || interleaved.writes != 1 {
				t.Fatalf("later write wasn't stopped: %+v, %v", res, err)
			}
			col, err = api.Collection(ctx, cs.UID)
			if err != nil {
				t.Fatal(err)
			}
			if find(col, "Health").Request.Method != "HEAD" {
				t.Fatal("teammate's edit was overwritten")
			}
			local, _ := here.Collection(ctx, localUID)
			if find(local, "Health").Request.Method != "POST" {
				t.Fatal("unpublished local edit was lost")
			}
		})
	}
}

type pushReadFailure struct {
	postman.API
	err error
}

func TestPushChecksBeforeWritingExamples(t *testing.T) {
	for _, sync := range []bool{false, true} {
		t.Run(fmt.Sprint(sync), func(t *testing.T) {
			c, api, here, cs, _ := pushFixture(t, sync)
			ctx := context.Background()
			uid := pmtest.Owner + "-" + cs.ID
			col, err := here.Collection(ctx, uid)
			if err != nil {
				t.Fatal(err)
			}
			it := find(col, "Get product")
			it.Request.Method = "POST"
			if _, err := here.UpdateRequest(ctx, uid, it); err != nil {
				t.Fatal(err)
			}
			ex := it.Response[0]
			ex.Body = "mine"
			if err := here.UpdateResponse(ctx, uid, ex); err != nil {
				t.Fatal(err)
			}
			p, err := c.Plan(ctx, api)
			if err != nil {
				t.Fatal(err)
			}
			interleaved := &editDuringPush{API: api, after: func() {
				col, err := api.Collection(ctx, cs.UID)
				if err != nil {
					t.Fatal(err)
				}
				_, ex, _ := col.FindExample(ex.ID)
				ex.Body = "teammate"
				if err := api.UpdateResponse(ctx, cs.UID, ex); err != nil {
					t.Fatal(err)
				}
			}}
			if _, err := c.Push(ctx, interleaved, p, nil, ""); err == nil {
				t.Fatal("example was overwritten after its parent was saved")
			}
			col, err = api.Collection(ctx, cs.UID)
			if err != nil {
				t.Fatal(err)
			}
			_, got, _ := col.FindExample(ex.ID)
			if got.Body != "teammate" {
				t.Fatal("lost teammate's example edit")
			}
		})
	}
}

func (a pushReadFailure) Collection(context.Context, string) (*postman.Collection, error) {
	return nil, a.err
}

func TestPushStopsWhenRecheckFails(t *testing.T) {
	w := newWorld(t)
	w.edit("Health", func(it *postman.Item) { it.Request.Method = "POST" })
	p, err := w.c.Plan(w.ctx, w.api)
	if err != nil {
		t.Fatal(err)
	}
	cause := errors.New("offline")
	res, err := w.c.Push(w.ctx, pushReadFailure{w.api, cause}, p, nil, "")
	if !errors.Is(err, cause) || res.Pushed != 0 {
		t.Fatalf("failed read didn't stop push: %+v, %v", res, err)
	}
	if find(w.team.Collection(w.col), "Health").Request.Method != "GET" {
		t.Fatal("write after failed read")
	}
}

func TestDiffReviewDoesNotApproveANewerVersion(t *testing.T) {
	w := newWorld(t)
	w.edit("Health", func(it *postman.Item) { it.Request.Method = "POST" })
	w.teamEdit("Health", func(it *postman.Item) { it.Request.Method = "HEAD" })
	p, err := w.c.Plan(w.ctx, w.api)
	if err != nil || len(p.Conflicts) != 1 {
		t.Fatalf("expected a conflict: %v", err)
	}
	cf := p.Conflicts[0]
	if err := p.CheckReviewed(cf); err != nil {
		t.Fatal(err)
	}
	w.teamEdit("Get product", func(it *postman.Item) { it.Request.Method = "PUT" })
	unrelated, err := w.c.Plan(w.ctx, w.api)
	if err != nil {
		t.Fatal(err)
	}
	if err := unrelated.CheckReviewed(cf); err != nil {
		t.Fatalf("another request invalidated this diff: %v", err)
	}
	w.teamEdit("Health", func(it *postman.Item) { it.Request.Method = "OPTIONS" })
	newPlan, err := w.c.Plan(w.ctx, w.api)
	if err != nil {
		t.Fatal(err)
	}
	if err := newPlan.CheckReviewed(cf); err == nil {
		t.Fatal("an old diff approved the new remote version")
	}
}

func TestPushReviewsConflictingMoves(t *testing.T) {
	for _, edit := range []bool{false, true} {
		t.Run(fmt.Sprint(edit), func(t *testing.T) {
			w := newWorld(t)
			col, uid := w.localCol()
			health := find(col, "Health")
			if edit {
				w.edit("Health", func(it *postman.Item) { it.Request.Method = "POST" })
			}
			if _, err := w.here.Transfer(w.ctx, false, []string{postman.Ref(health.ID, health.UID)}, postman.Place{Collection: uid, Folder: postman.Ref(find(col, "Orders").ID, find(col, "Orders").UID), Position: "end"}, false); err != nil {
				t.Fatal(err)
			}
			if _, err := w.api.Transfer(w.ctx, false, []string{postman.Ref(health.ID, health.UID)}, postman.Place{Collection: uid, Folder: postman.Ref(find(col, "Products").ID, find(col, "Products").UID), Position: "end"}, false); err != nil {
				t.Fatal(err)
			}
			p, err := w.c.Plan(w.ctx, w.api)
			if err != nil || len(p.Conflicts) != 1 || p.Changes() != 0 {
				t.Fatalf("move wasn't a conflict: %+v, %v", p, err)
			}
			res, err := w.c.Push(w.ctx, w.api, p, nil, "")
			if err != nil || res.Pushed != 0 {
				t.Fatalf("skipped move was pushed: %+v, %v", res, err)
			}
			p, err = w.c.Plan(w.ctx, w.api)
			if err != nil {
				t.Fatal(err)
			}
			if len(p.Conflicts) != 1 {
				t.Fatal("skipping the conflict lost the pending move")
			}
			res, err = w.c.Push(w.ctx, w.api, p, map[string]bool{p.Conflicts[0].Key: true}, "")
			if err != nil || res.Pushed == 0 {
				t.Fatalf("approved move wasn't pushed: %+v, %v", res, err)
			}
			team := w.team.Collection(w.col)
			_, parents := team.Find(health.ID)
			if len(parents) != 1 || parents[0].Name != "Orders" {
				t.Fatal("approved move went to the wrong folder")
			}
			if edit && find(team, "Health").Request.Method != "POST" {
				t.Fatal("approved content change wasn't pushed")
			}
		})
	}
}
