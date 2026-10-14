package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/shinyvision/posthero/internal/postman"
	"github.com/shinyvision/posthero/internal/postman/pmtest"
	"github.com/shinyvision/posthero/internal/postman/syncapi"
	"github.com/shinyvision/posthero/internal/postman/syncapi/synctest"
	"github.com/shinyvision/posthero/internal/reqdoc"
	"github.com/shinyvision/posthero/internal/store"
	"github.com/shinyvision/posthero/internal/ui/editmodal"
)

func saveFixture(t *testing.T, sync bool) (*Model, *store.Col, *store.Env) {
	t.Helper()
	var api postman.API
	if sync {
		srv := synctest.New()
		base := srv.Start()
		t.Cleanup(srv.Close)
		c := syncapi.New(synctest.Token, syncapi.Options{URL: base, IdentityURL: base})
		t.Cleanup(c.Close)
		api = c
	} else {
		srv := pmtest.New()
		api = postman.NewClient(pmtest.Key, srv.Start())
		t.Cleanup(srv.Close)
	}
	ctx := context.Background()
	a := &postman.Auth{Type: "bearer"}
	a.Set("token", "before")
	events := postman.SetScript(nil, "prerequest", "// before")
	events = postman.SetScript(events, "test", "// tests")
	sum, err := api.CreateCollection(ctx, "ws1", &postman.Collection{
		Info: postman.Info{Name: "Collection"}, Auth: a, Event: events,
		Variable: []postman.Variable{{Key: "base", Value: "before"}},
		Item: []*postman.Item{{Name: "Folder", Folder: true, Auth: a.Clone(), Event: events,
			Item: []*postman.Item{{Name: "Request", Event: events, Request: &postman.Request{
				Method: "POST", URL: postman.ParseURL("https://example.test/before"), Auth: a.Clone(), Body: &postman.Body{Mode: "raw", Raw: "before"},
			}}}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	col, err := api.Collection(ctx, sum.UID)
	if err != nil {
		t.Fatal(err)
	}
	es, err := api.CreateEnvironment(ctx, "ws1", &postman.Environment{Name: "Environment", Values: []postman.EnvValue{{Key: "base", Value: "before"}}})
	if err != nil {
		t.Fatal(err)
	}
	env, err := api.Environment(ctx, es.UID)
	if err != nil {
		t.Fatal(err)
	}
	m := &Model{ctx: ctx, busy: map[int]string{}, api: api, st: store.Open(api, postman.Workspace{ID: "ws1"}, "", "")}
	c, e := &store.Col{Summary: sum, Data: col}, &store.Env{Summary: es, Data: env}
	m.st.PutCol(c)
	m.st.PutEnv(e)
	return m, c, e
}

func saveResult(cmd tea.Cmd) error {
	msg := cmd()
	for {
		if wrapped, ok := msg.(editmodal.Wrapped); ok {
			msg = wrapped.Inner()
			continue
		}
		switch msg := msg.(type) {
		case editmodal.ResultMsg:
			return msg.Err
		case changedMsg:
			return msg.err
		case errorMsg:
			return msg.err
		}
		return nil
	}
}

func TestRenamePreservesRemoteContent(t *testing.T) {
	for _, sync := range []bool{false, true} {
		t.Run(fmt.Sprint("sync=", sync), func(t *testing.T) {
			m, c, e := saveFixture(t, sync)
			ctx := context.Background()
			folder, request := c.Data.Item[0], c.Data.Item[0].Item[0]
			remote := request.Clone()
			remote.Request.URL = postman.ParseURL("https://example.test/teammate")
			remote.Request.Body.Raw = "teammate"
			remote.Request.Auth.Set("token", "teammate")
			remote.Event = postman.SetScript(remote.Event, "test", "// teammate")
			if _, err := m.api.UpdateRequest(ctx, c.Summary.UID, remote); err != nil {
				t.Fatal(err)
			}
			if err := saveResult(m.setName(c, request, "Renamed", true)); err != nil {
				t.Fatal(err)
			}
			got, err := m.api.Collection(ctx, c.Summary.UID)
			if err != nil {
				t.Fatal(err)
			}
			it, _ := got.Find(request.ID)
			if it.Name != "Renamed" || !sameJSON(it.Request, remote.Request) || !sameJSON(it.Event, remote.Event) {
				t.Fatal("rename overwrote a teammate's request, auth or scripts")
			}
			made, err := m.api.CreateResponse(ctx, c.Summary.UID, postman.Ref(request.ID, request.UID), &postman.Response{Name: "Example", Body: "before", Code: 200})
			if err != nil {
				t.Fatal(err)
			}
			request.Response = []*postman.Response{{ID: made.ID, UID: made.UID, Name: "Example", Body: "before", Code: 200}}
			remoteExample := request.Response[0].Clone()
			remoteExample.Body, remoteExample.Code = "teammate", 201
			if err := m.api.UpdateResponse(ctx, c.Summary.UID, remoteExample); err != nil {
				t.Fatal(err)
			}
			if err := saveResult(m.renameExample(docRef{colID: c.Summary.ID, itemID: request.ID, exID: made.ID}, "Renamed example", true)); err != nil {
				t.Fatal(err)
			}
			got, _ = m.api.Collection(ctx, c.Summary.UID)
			_, example, _ := got.FindExample(made.ID)
			if example.Name != "Renamed example" || example.Body != "teammate" || example.Code != 201 {
				t.Fatal("example rename overwrote the remote response")
			}
			remoteFolder := folder.Clone()
			remoteFolder.Auth.Set("token", "teammate")
			remoteFolder.Event = postman.SetScript(remoteFolder.Event, "prerequest", "// teammate")
			if err := m.api.UpdateFolder(ctx, c.Summary.UID, remoteFolder); err != nil {
				t.Fatal(err)
			}
			if err := saveResult(m.setName(c, folder, "Renamed folder", true)); err != nil {
				t.Fatal(err)
			}
			got, _ = m.api.Collection(ctx, c.Summary.UID)
			it, _ = got.Find(folder.ID)
			if it.Name != "Renamed folder" || !sameJSON(it.Auth, remoteFolder.Auth) || !sameJSON(it.Event, remoteFolder.Event) {
				t.Fatal("folder rename overwrote auth or scripts")
			}
			fresh := e.Data.Clone()
			fresh.Values[0].Value = "teammate"
			if err := m.api.PutEnvironment(ctx, e.Summary.UID, fresh); err != nil {
				t.Fatal(err)
			}
			if err := saveResult(m.renameEnv(e, "Renamed environment", true)); err != nil {
				t.Fatal(err)
			}
			env, _ := m.api.Environment(ctx, e.Summary.UID)
			if env.Name != "Renamed environment" || env.Values[0].Value != "teammate" {
				t.Fatal("environment rename overwrote variables")
			}
		})
	}
}

type failedReads struct {
	postman.API
	err    error
	writes int
}

func (a *failedReads) Collection(context.Context, string) (*postman.Collection, error) {
	return nil, a.err
}
func (a *failedReads) Environment(context.Context, string) (*postman.Environment, error) {
	return nil, a.err
}
func (a *failedReads) Globals(context.Context, string) ([]postman.EnvValue, error) { return nil, a.err }
func (a *failedReads) PutEnvironment(context.Context, string, *postman.Environment) error {
	a.writes++
	return nil
}
func (a *failedReads) PutGlobals(context.Context, string, []postman.EnvValue) error {
	a.writes++
	return nil
}
func (a *failedReads) PatchCollection(context.Context, string, postman.CollectionPatch) error {
	a.writes++
	return nil
}
func (a *failedReads) PatchFolder(context.Context, string, string, postman.ItemPatch) error {
	a.writes++
	return nil
}

func TestSavesStopWhenLatestReadFails(t *testing.T) {
	m, c, e := saveFixture(t, false)
	for _, cause := range []error{context.Canceled, &postman.Error{Status: 401}, &postman.Error{Status: 404}, &postman.Error{Status: 500}} {
		a := &failedReads{API: m.st.API, err: cause}
		m.api = a
		changed := c.Data.Clone()
		changed.Variable[0].Value = "mine"
		folder := c.Data.Item[0].Clone()
		folder.Auth.Set("token", "mine")
		for name, cmd := range map[string]tea.Cmd{
			"environment":          m.saveEnvValues(e, e.Data.Values, []postman.EnvValue{{Key: "base", Value: "mine"}}, nil),
			"environment rename":   m.renameEnv(e, "mine", false),
			"globals":              m.saveGlobals(nil, []postman.EnvValue{{Key: "mine", Value: "mine"}}, nil),
			"collection variables": m.saveColVars(c, c.Data.Variable, changed.Variable, nil),
			"collection settings":  m.saveCollectionSettings(c, changed, nil),
			"folder settings":      m.saveFolderSettings(c, c.Data.Item[0], folder, nil),
		} {
			if err := saveResult(cmd); !errors.Is(err, cause) {
				t.Errorf("%s: lost read error: %v", name, err)
			}
		}
		if a.writes != 0 {
			t.Fatalf("%d writes after failed reads", a.writes)
		}
	}
}

func TestEnvironmentSaveRetriesTheOriginalChanges(t *testing.T) {
	m, _, e := saveFixture(t, false)
	api := m.api
	request := m.editEnv(e)().(editmodal.OpenMsg).Request
	text := strings.Replace(request.Text, "base: before", "base: mine", 1)
	m.api = &failedReads{API: api, err: errors.New("offline")}
	if err := saveResult(request.Submit(text)); err == nil {
		t.Fatal("failed read was accepted")
	}
	m.api = api
	fresh := e.Data.Clone()
	fresh.Values = append(fresh.Values, postman.EnvValue{Key: "teammate", Value: "keep"})
	if err := api.PutEnvironment(context.Background(), e.Summary.UID, fresh); err != nil {
		t.Fatal(err)
	}
	if err := saveResult(request.Submit(text)); err != nil {
		t.Fatal(err)
	}
	request.Saved()
	got, _ := api.Environment(context.Background(), e.Summary.UID)
	if got.Values[0].Value != "mine" || len(got.Values) != 2 {
		t.Fatal("retry lost the user's edit or the teammate's variable")
	}
}

func TestSettingsPreserveRemoteAuthAndOtherScripts(t *testing.T) {
	for _, sync := range []bool{false, true} {
		t.Run(fmt.Sprint("sync=", sync), func(t *testing.T) {
			m, c, _ := saveFixture(t, sync)
			base := c.Data.Clone()
			mine, err := reqdoc.ApplyCollection(base, strings.Replace(reqdoc.RenderCollection(base), "// before", "// mine", 1))
			if err != nil {
				t.Fatal(err)
			}
			remote := base.Clone()
			remote.Auth.Set("token", "teammate")
			remote.Event = postman.SetScript(remote.Event, "test", "// teammate tests")
			if err := m.api.PatchCollection(context.Background(), c.Summary.UID, postman.CollectionPatch{Auth: &remote.Auth, Event: &remote.Event}); err != nil {
				t.Fatal(err)
			}
			// Live refresh has reached the store after the editor was opened.
			c.Data, err = m.api.Collection(context.Background(), c.Summary.UID)
			if err != nil {
				t.Fatal(err)
			}
			if err := saveResult(m.saveCollectionSettingsFrom(c, base, mine, nil)); err != nil {
				t.Fatal(err)
			}
			got, _ := m.api.Collection(context.Background(), c.Summary.UID)
			if got.Auth.Get("token") != "teammate" || postman.Scripts(got.Event, "test") != "// teammate tests" || postman.Scripts(got.Event, "prerequest") != "// mine" {
				t.Fatal("collection settings overwrote untouched auth or scripts")
			}
			folder := base.Item[0]
			mineFolder := folder.Clone()
			mineFolder.Event = postman.SetScript(mineFolder.Event, "prerequest", "// mine")
			remoteFolder := folder.Clone()
			remoteFolder.Name = "Teammate's name"
			remoteFolder.Auth.Set("token", "teammate")
			remoteFolder.Event = postman.SetScript(remoteFolder.Event, "test", "// teammate tests")
			if err := m.api.UpdateFolder(context.Background(), c.Summary.UID, remoteFolder); err != nil {
				t.Fatal(err)
			}
			if err := saveResult(m.saveFolderSettings(c, folder, mineFolder, nil)); err != nil {
				t.Fatal(err)
			}
			got, _ = m.api.Collection(context.Background(), c.Summary.UID)
			f, _ := got.Find(folder.ID)
			if f.Name != "Teammate's name" || f.Auth.Get("token") != "teammate" || postman.Scripts(f.Event, "test") != "// teammate tests" || postman.Scripts(f.Event, "prerequest") != "// mine" {
				t.Fatal("folder settings overwrote untouched fields")
			}
			mine.Auth.Set("token", "mine")
			if err := saveResult(m.saveCollectionSettingsFrom(c, base, mine, nil)); err == nil || !strings.Contains(err.Error(), "auth changed") {
				t.Fatalf("concurrent auth changes weren't rejected: %v", err)
			}
		})
	}
}
