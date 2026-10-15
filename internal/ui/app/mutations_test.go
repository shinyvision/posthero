package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/shinyvision/posthero/internal/postman"
	"github.com/shinyvision/posthero/internal/ui/editmodal"
	"github.com/shinyvision/posthero/internal/ui/modal"
	"github.com/shinyvision/posthero/internal/ui/tree"
)

func TestMutationsRejectUnseenChanges(t *testing.T) {
	for _, sync := range []bool{false, true} {
		for _, action := range []string{"rename request", "rename folder", "rename collection", "rename environment", "rename example", "delete request", "delete folder", "delete collection", "delete environment", "delete example", "move request", "move example", "undo folder edit", "edit folder"} {
			t.Run(fmt.Sprintf("sync=%v/%s", sync, action), func(t *testing.T) {
				m, c, e := saveFixture(t, sync)
				m.drafts, m.tree = loadDrafts(""), tree.New(nil)
				api, ctx := m.api, context.Background()
				folder, request := c.Data.Item[0], c.Data.Item[0].Item[0]
				made, err := api.CreateResponse(ctx, c.Summary.UID, postman.Ref(request.ID, request.UID), &postman.Response{Name: "Example", Body: "before", Code: 200})
				if err != nil {
					t.Fatal(err)
				}
				c.Data, err = api.Collection(ctx, c.Summary.UID)
				if err != nil {
					t.Fatal(err)
				}
				folder, request = c.Data.Item[0], c.Data.Item[0].Item[0]
				ref := docRef{colID: c.Summary.ID, itemID: request.ID, exID: made.ID}
				var cmd tea.Cmd
				switch action {
				case "rename request":
					cmd = m.setName(c, request, "mine", true)
				case "rename folder":
					cmd = m.setName(c, folder, "mine", true)
				case "rename collection":
					cmd = m.setName(c, nil, "mine", true)
				case "rename environment":
					cmd = m.renameEnv(e, "mine", true)
				case "rename example":
					cmd = m.renameExample(ref, "mine", true)
				case "delete request":
					cmd = m.remove(c, request)
				case "delete folder":
					cmd = m.remove(c, folder)
				case "delete collection":
					cmd = m.dropCollection(c, nil, "deleted")
				case "delete environment":
					cmd = m.dropEnv(e, nil)
				case "delete example":
					cmd = m.removeExample(ref, true)
				case "move request":
					m.cut = &cutState{colID: c.Summary.ID, ids: []string{request.ID}}
					cmd = m.put(&tree.Node{Kind: tree.Collection, Col: c}, false)
				case "move example":
					// A second request is the move's destination.
					target, err := api.CreateRequest(ctx, c.Summary.UID, "", &postman.Item{Name: "Target", Request: &postman.Request{Method: "GET", URL: postman.ParseURL("https://example.test")}})
					if err != nil {
						t.Fatal(err)
					}
					c.Data, err = api.Collection(ctx, c.Summary.UID)
					if err != nil {
						t.Fatal(err)
					}
					cmd = m.moveExample(ref, c.Summary.ID, target.ID, true)
				case "undo folder edit":
					cmd = m.restore(c, folder.Clone(), "restored")
				case "edit folder":
					mine := folder.Clone()
					mine.Auth.Set("token", "mine")
					cmd = m.saveFolderSettings(c, folder, mine, nil)
				}
				// Arrives after the user acted but before the command writes.
				switch {
				case strings.Contains(action, "environment"):
					env := e.Data.Clone()
					env.Name = "teammate"
					env.Values[0].Value = "teammate"
					err = api.PutEnvironment(ctx, e.Summary.UID, env)
				case strings.Contains(action, "example"):
					ex := request.Example(made.ID).Clone()
					ex.Name, ex.Body = "teammate", "teammate"
					err = api.UpdateResponse(ctx, c.Summary.UID, ex)
				case action == "rename collection":
					name := "teammate"
					err = api.PatchCollection(ctx, c.Summary.UID, postman.CollectionPatch{Name: &name})
				case action == "rename folder" || action == "undo folder edit" || action == "edit folder":
					f := folder.Clone()
					f.Name = "teammate"
					f.Auth.Set("token", "teammate")
					err = api.UpdateFolder(ctx, c.Summary.UID, f)
				default:
					r := request.Clone()
					r.Name = "teammate"
					r.Request.Body.Raw = "teammate"
					_, err = api.UpdateRequest(ctx, c.Summary.UID, r)
				}
				if err != nil {
					t.Fatal(err)
				}
				before, err := api.Collection(ctx, c.Summary.UID)
				if err != nil {
					t.Fatal(err)
				}
				beforeEnv, err := api.Environment(ctx, e.Summary.UID)
				if err != nil {
					t.Fatal(err)
				}
				if err := saveResult(cmd); err == nil || !strings.Contains(err.Error(), "changed") {
					t.Fatalf("stale mutation accepted: %v", err)
				}
				after, err := api.Collection(ctx, c.Summary.UID)
				if err != nil || !sameJSON(before, after) {
					t.Fatalf("collection changed after conflict: %v", err)
				}
				afterEnv, err := api.Environment(ctx, e.Summary.UID)
				if err != nil || !sameJSON(beforeEnv, afterEnv) {
					t.Fatalf("environment changed after conflict: %v", err)
				}
			})
		}
	}
}

func TestMutationsStopWhenLatestReadFails(t *testing.T) {
	for _, sync := range []bool{false, true} {
		t.Run(fmt.Sprint(sync), func(t *testing.T) {
			m, c, e := saveFixture(t, sync)
			m.drafts, m.tree = loadDrafts(""), tree.New(nil)
			cause := errors.New("offline")
			m.api = &failedReads{API: m.api, err: cause}
			folder, request := c.Data.Item[0], c.Data.Item[0].Item[0]
			m.cut = &cutState{colID: c.Summary.ID, ids: []string{request.ID}}
			for name, cmd := range map[string]tea.Cmd{
				"rename":             m.setName(c, request, "mine", false),
				"delete request":     m.remove(c, request),
				"delete folder":      m.remove(c, folder),
				"delete collection":  m.dropCollection(c, nil, "deleted"),
				"delete environment": m.dropEnv(e, nil),
				"move":               m.put(&tree.Node{Kind: tree.Collection, Col: c}, false),
				"undo delete":        m.removeQuiet(c, request, "undone"),
				"undo folder":        m.restore(c, folder, "undone"),
			} {
				if err := saveResult(cmd); !errors.Is(err, cause) {
					t.Errorf("%s: %v", name, err)
				}
			}
		})
	}
}

func TestFolderUndoPreservesMergedChangesAndChecksItsBaseline(t *testing.T) {
	for _, sync := range []bool{false, true} {
		for _, laterEdit := range []bool{false, true} {
			t.Run(fmt.Sprintf("sync=%v/later=%v", sync, laterEdit), func(t *testing.T) {
				m, c, _ := saveFixture(t, sync)
				ctx := context.Background()
				before := c.Data.Item[0].Clone()
				mine := before.Clone()
				mine.Auth.Set("token", "mine")
				name := "Teammate's name"
				if err := m.api.PatchFolder(ctx, c.Summary.UID, postman.Ref(before.ID, before.UID), postman.ItemPatch{Name: &name}); err != nil {
					t.Fatal(err)
				}
				u := &undoEntry{kind: undoSave, colID: c.Summary.ID, item: before}
				msg := m.saveFolderSettings(c, before, mine, u)().(doneMsg).msg.(editmodal.ResultMsg)
				if msg.Err != nil {
					t.Fatal(msg.Err)
				}
				changed := msg.Then.(changedMsg)
				if changed.err != nil {
					t.Fatal(changed.err)
				}
				c.Data = changed.col // live refresh can also replace this later
				if laterEdit {
					remote := c.Data.Item[0].Clone()
					remote.Auth.Set("token", "teammate")
					if err := m.api.UpdateFolder(ctx, c.Summary.UID, remote); err != nil {
						t.Fatal(err)
					}
					var err error
					c.Data, err = m.api.Collection(ctx, c.Summary.UID)
					if err != nil {
						t.Fatal(err)
					}
				}
				err := saveResult(changed.undo.restore())
				if (err != nil) != laterEdit {
					t.Fatalf("unexpected undo result: %v", err)
				}
				col, err := m.api.Collection(ctx, c.Summary.UID)
				if err != nil {
					t.Fatal(err)
				}
				want := "before"
				if laterEdit {
					want = "teammate"
				}
				if col.Item[0].Name != name || col.Item[0].Auth.Get("token") != want {
					t.Fatal("undo overwrote a teammate's change")
				}
			})
		}
	}
}

func TestDeleteConfirmationKeepsTheReviewedVersion(t *testing.T) {
	for _, sync := range []bool{false, true} {
		t.Run(fmt.Sprint(sync), func(t *testing.T) {
			m, c, _ := saveFixture(t, sync)
			folder := c.Data.Item[0]
			confirm := m.deleteNode(&tree.Node{Kind: tree.Folder, Col: c, Item: folder})().(modal.ShowMsg).Modal.(*modal.Confirm)
			ctx := context.Background()
			request := folder.Item[0].Clone()
			request.Request.Body.Raw = "teammate"
			if _, err := m.api.UpdateRequest(ctx, c.Summary.UID, request); err != nil {
				t.Fatal(err)
			}
			var err error
			c.Data, err = m.api.Collection(ctx, c.Summary.UID)
			if err != nil {
				t.Fatal(err)
			}
			if err := saveResult(confirm.Yes().(cmdMsg).cmd()); err == nil {
				t.Fatal("refresh silently approved a newer folder for deletion")
			}
		})
	}
}

func TestExplicitCollectionDeleteOnlyClearsItsDrafts(t *testing.T) {
	for _, sync := range []bool{false, true} {
		t.Run(fmt.Sprint(sync), func(t *testing.T) {
			m, c, _ := saveFixture(t, sync)
			m.drafts, m.tree = loadDrafts(""), tree.New(nil)
			id := c.Data.Item[0].Item[0].ID
			m.drafts.set(id, "this draft", "base")
			m.drafts.set("another workspace", "keep this draft", "other base")
			msg := m.dropCollection(c, nil, "deleted")().(doneMsg).msg
			apply, ok := msg.(cmdMsg)
			if !ok {
				t.Fatalf("delete failed: %+v", msg)
			}
			apply.cmd()
			if _, ok := m.drafts.get(id); ok {
				t.Fatal("explicit deletion kept its draft")
			}
			if text, ok := m.drafts.get("another workspace"); !ok || text != "keep this draft" {
				t.Fatal("explicit deletion lost another workspace's draft")
			}
		})
	}
}
