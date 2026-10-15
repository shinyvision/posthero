package app

import (
	"context"
	"strings"
	"testing"

	"github.com/shinyvision/posthero/internal/postman"
)

func TestDraftsSurviveWorkspaceSwitchAndRestart(t *testing.T) {
	for _, backend := range []string{"API key", "browser session"} {
		t.Run(backend, func(t *testing.T) {
			var d *driver
			if backend == "API key" {
				d = newDriver(t)
				d.demo.Postman.AddWorkspace("drafts-other", "Other drafts")
			} else {
				var srv interface{ AddWorkspace(string, string) }
				d, srv, _ = newSyncDriver(t)
				srv.AddWorkspace("drafts-other", "Other drafts")
			}
			a := d.m.st.Workspace
			b := postman.Workspace{ID: "drafts-other", Name: "Other drafts"}
			api := d.m.remote
			// The restarted app owns a new client; fixture creation finishes
			// before that restart closes this one.
			makeRequest := func(ws postman.Workspace, name string) docRef {
				t.Helper()
				summary, err := api.CreateCollection(context.Background(), ws.ID, &postman.Collection{
					Info: postman.Info{Name: name},
					Item: []*postman.Item{{Name: name, Request: &postman.Request{Method: "GET", URL: postman.ParseURL("https://example.test/original")},
						Response: []*postman.Response{{Name: "Example", Code: 200, Status: "OK", Body: "original"}},
					}},
				})
				if err != nil {
					t.Fatal(err)
				}
				col, err := api.Collection(context.Background(), summary.UID)
				if err != nil {
					t.Fatal(err)
				}
				return docRef{colID: summary.ID, itemID: col.Item[0].ID}
			}
			refresh := func() {
				t.Helper()
				d.exec(d.m.refresh(true))
				d.settle()
				if d.m.refreshing {
					t.Fatal("refresh did not finish")
				}
			}
			switchTo := func(ws postman.Workspace) {
				t.Helper()
				d.exec(d.m.switchWorkspace(ws))
				d.settle()
				if d.m.st.Workspace.ID != ws.ID || d.m.refreshing {
					t.Fatal("workspace did not finish opening")
				}
			}
			want := map[string]draft{}
			edit := func(ref docRef, marker string) {
				t.Helper()
				d.exec(d.m.openDoc(ref, true))
				d.settle()
				d.keys("<Esc>")
				d.typeText(":%s/original/" + marker + "/g")
				d.keys("<CR>")
				text, ok := d.m.drafts.get(ref.id())
				if !ok || !strings.Contains(text, marker) {
					t.Fatalf("missing edit for %s", marker)
				}
				want[ref.id()] = draft{Text: text, Base: d.m.drafts.base(ref.id())}
			}
			check := func() {
				t.Helper()
				for id, before := range want {
					text, ok := d.m.drafts.get(id)
					if !ok || text != before.Text || d.m.drafts.base(id) != before.Base {
						t.Fatalf("draft %s was lost or changed in workspace %s", id, d.m.st.Workspace.ID)
					}
				}
			}
			requestA := makeRequest(a, "Draft A")
			refresh()
			col := d.m.st.Col(requestA.colID)
			exampleA := requestA
			exampleA.exID = col.Data.Item[0].Response[0].ID
			edit(requestA, "unsaved-a")
			edit(exampleA, "unsaved-example-a")

			// Even an entirely empty workspace must leave other drafts alone.
			switchTo(b)
			if len(d.m.st.Collections()) != 0 {
				t.Fatal("expected an empty workspace")
			}
			check()
			requestB := makeRequest(b, "Draft B")
			refresh()
			edit(requestB, "unsaved-b")
			refresh()
			check()
			d.restart()
			check()
			switchTo(a)
			check()
			for _, ref := range []docRef{requestA, exampleA} {
				d.exec(d.m.openDoc(ref, true))
				d.settle()
				d.see("unsaved-")
				if text, ok := d.m.docText(ref); !ok || text != want[ref.id()].Text {
					t.Fatal("reopened document lost its draft")
				}
			}

			// Saving in A removes only that draft, keeping B's unsaved edit.
			d.exec(d.m.saveDoc(requestA, want[requestA.id()].Text))
			d.settle()
			if _, ok := d.m.drafts.get(requestA.id()); ok {
				t.Fatal("saved request still has a draft")
			}
			delete(want, requestA.id())
			check()
			switchTo(b)
			d.exec(d.m.openDoc(requestB, true))
			d.settle()
			d.see("unsaved-b")
			check()
		})
	}
}
