package app

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/shinyvision/posthero/internal/config"
	"github.com/shinyvision/posthero/internal/postman"
	"github.com/shinyvision/posthero/internal/postman/syncapi"
	"github.com/shinyvision/posthero/internal/postman/syncapi/synctest"
	"github.com/shinyvision/posthero/internal/secret"
)

func newSyncDriver(t *testing.T, sessions ...syncapi.Session) (*driver, *synctest.Server, *syncapi.Client) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir+"/config")
	t.Setenv("XDG_STATE_HOME", dir+"/state")
	t.Setenv("POSTHERO_EDITOR", "builtin")
	secret.MockInit()
	srv := synctest.New()
	for n := 1; n <= 2; n++ {
		raw := fmt.Sprintf(`{"id":"c0000000-0000-4000-8000-%012d","name":"Collection %d","order":["r0000000-0000-4000-8000-%012d"],"folders_order":[],"requests":[{"id":"r0000000-0000-4000-8000-%012d","name":"Request %d","method":"GET","url":"https://example.test/initial"}]}`, n, n, n, n, n)
		if _, err := srv.AddCollection("ws1", []byte(raw)); err != nil {
			t.Fatal(err)
		}
	}
	srv.AddEnvironment("ws1", "Local", []map[string]any{{"key": "base", "value": "before", "enabled": true}})
	srv.AddWorkspace("ws2", "Other")
	base := srv.Start()
	t.Cleanup(srv.Close)
	session := syncapi.Session{Token: synctest.Token, RefreshToken: "r", UserID: 2000, ExpiresAt: time.Now().Add(10 * 24 * time.Hour)}
	if len(sessions) != 0 {
		session = sessions[0]
	}
	settings := config.DefaultSettings()
	settings.Refresh = 0
	m := New(Options{Login: config.Login{Session: &session}, HasLogin: true, Sync: syncapi.Options{URL: base, IdentityURL: base}, RC: config.RC{Settings: settings}, State: config.State{Workspace: "ws1"}, Dir: dir, StateDir: dir + "/state", CacheDir: dir + "/cache"})
	d := &driver{t: t, m: m, msgs: make(chan tea.Msg, 1024)}
	m.send = func(msg tea.Msg) { d.msgs <- msg }
	t.Cleanup(m.Close)
	d.handle(tea.WindowSizeMsg{Width: 160, Height: 45})
	d.exec(m.Init())
	d.settle()
	other := syncapi.New(synctest.Token, syncapi.Options{URL: base, IdentityURL: base})
	t.Cleanup(other.Close)
	return d, srv, other
}

func TestPostmanSessionWithoutRenewalStartup(t *testing.T) {
	d, _, other := newSyncDriver(t, syncapi.Session{Token: synctest.Token, UserID: 2000})
	d.open("Request 1")
	c := d.m.st.Col(d.m.open.colID)
	it := c.Data.Item[0].Clone()
	it.Request.URL = postman.ParseURL("https://example.test/session-without-renewal")
	if _, err := other.UpdateRequest(context.Background(), c.Summary.UID, it); err != nil {
		t.Fatal(err)
	}
	d.waitFor("https://example.test/session-without-renewal")
}

func TestPostmanSessionStartupAndLiveRefresh(t *testing.T) {
	d, _, other := newSyncDriver(t)
	if _, ok := d.m.remote.(*syncapi.Client); !ok {
		t.Fatal("session didn't select sync backend")
	}
	d.open("Request 1")
	ctx := context.Background()
	// Change an unopened collection's request without changing the
	// collection timestamp: polling alone cannot discover this change.
	c := d.m.st.Collections()[1]
	it := c.Data.Item[0].Clone()
	it.Name = "Changed by teammate"
	if _, err := other.UpdateRequest(ctx, c.Summary.UID, it); err != nil {
		t.Fatal(err)
	}
	d.settle()
	if d.m.st.Col(c.Summary.ID).Data.Item[0].Name != "Changed by teammate" {
		t.Fatal("watch didn't fetch unopened collection")
	}
	// Clean editor follows the same refresh path.
	openCol := d.m.st.Col(d.m.open.colID)
	it = openCol.Data.Item[0].Clone()
	it.Request.URL = postman.ParseURL("https://example.test/teammate")
	if _, err := other.UpdateRequest(ctx, openCol.Summary.UID, it); err != nil {
		t.Fatal(err)
	}
	d.waitFor("https://example.test/teammate")
	// A dirty editor remains untouched when another live update arrives.
	d.m.focusPane(paneEditor)
	d.keys("<Esc>")
	d.typeText(":%s/teammate/my-draft/")
	d.keys("<CR>")
	it.Request.URL = postman.ParseURL("https://example.test/new-remote")
	if _, err := other.UpdateRequest(ctx, openCol.Summary.UID, it); err != nil {
		t.Fatal(err)
	}
	d.waitFor("your changes are kept")
	d.see("my-draft")
	if !strings.Contains(d.m.st.Col(openCol.Summary.ID).Data.Item[0].Request.URL.Raw, "new-remote") {
		t.Fatal("store did not refresh behind dirty editor")
	}
	e := d.m.st.Environments()[0]
	if err := other.PutEnvironment(ctx, e.Summary.UID, &postman.Environment{Name: "Renamed env", Values: []postman.EnvValue{{Key: "base", Value: "after"}}}); err != nil {
		t.Fatal(err)
	}
	d.settle()
	if d.m.st.Env(e.Summary.ID).Data.Values[0].Value != "after" {
		t.Fatal("live environment update lost")
	}
	if err := other.DeleteEnvironment(ctx, e.Summary.UID); err != nil {
		t.Fatal(err)
	}
	d.settle()
	if d.m.st.Env(e.Summary.ID) != nil {
		t.Fatal("live environment deletion lost")
	}
	if err := other.DeleteCollection(ctx, c.Summary.UID); err != nil {
		t.Fatal(err)
	}
	d.settle()
	if d.m.st.Col(c.Summary.ID) != nil {
		t.Fatal("live collection deletion lost")
	}
}

func TestPostmanWatchSwitchAndReconnect(t *testing.T) {
	d, srv, _ := newSyncDriver(t)
	oldEpoch := d.m.watchState.epoch
	d.exec(d.m.switchWorkspace(postman.Workspace{ID: "ws2", Name: "Other"}))
	d.settle()
	if d.m.watchState.epoch == oldEpoch {
		t.Fatal("old watch wasn't cancelled")
	}
	d.handle(watchChangeMsg{epoch: oldEpoch, change: postman.Change{Action: "reconnect"}})
	if d.m.watchState.catchup || len(d.m.watchState.dirty) > 0 {
		t.Fatal("event from old workspace accepted")
	}
	d.exec(d.m.switchWorkspace(postman.Workspace{ID: "ws1", Name: "Team"}))
	d.settle()
	// AddCollection sends no broadcast, matching the service. Reconnect
	// must catch changes missed while disconnected even with polling off.
	srv.Drop()
	if _, err := srv.AddCollection("ws1", []byte(`{"id":"c0000000-0000-4000-8000-000000000099","name":"While offline","order":[],"folders_order":[],"requests":[]}`)); err != nil {
		t.Fatal(err)
	}
	d.waitFor("While offline")
}

func TestPostmanChangeDuringRefreshIsNotLost(t *testing.T) {
	d, _, other := newSyncDriver(t)
	// Build a plan before the event. Its result mustn't clear an event
	// delivered while the plan is in flight.
	cmd := d.m.refresh(false)
	c := d.m.st.Collections()[1]
	it := c.Data.Item[0].Clone()
	it.Name = "During refresh"
	if _, err := other.UpdateRequest(context.Background(), c.Summary.UID, it); err != nil {
		t.Fatal(err)
	}
	d.settle()
	d.exec(cmd)
	d.settle()
	if d.m.st.Col(c.Summary.ID).Data.Item[0].Name != "During refresh" {
		t.Fatal("in-flight refresh swallowed event")
	}
}

func TestPostmanConnectionChoices(t *testing.T) {
	d := newDriver(t)
	d.m.form = d.m.loginForm("")
	d.see("Sign in with browser", "Use API key")
	d.keys("<Tab>", "<CR>")
	d.see("API key", "PMAK-")
}

func TestPostmanWatchLeavesLocalCopyAlone(t *testing.T) {
	d, _, other := newSyncDriver(t)
	c := d.m.st.Collections()[0]
	it := c.Data.Item[0].Clone()
	d.cmd("set local")
	d.waitFor("local mode: changes stay on this machine")
	if d.m.watchState.cancel != nil {
		t.Fatal("local copy still has a remote watch")
	}
	it.Name = "Changed remotely"
	if _, err := other.UpdateRequest(context.Background(), c.Summary.UID, it); err != nil {
		t.Fatal(err)
	}
	d.settle()
	if d.m.st.Col(c.Summary.ID).Data.Item[0].Name == it.Name {
		t.Fatal("watch overwrote the local copy")
	}
	d.cmd("pull")
	d.waitFor("pulled 1 change from the team")
	if d.m.st.Col(c.Summary.ID).Data.Item[0].Name != it.Name {
		t.Fatal("explicit pull didn't fetch the change")
	}
	d.cmd("set nolocal")
	d.settle()
	if d.m.watchState.cancel == nil {
		t.Fatal("returning to remote mode didn't restart watch")
	}
}

func TestPostmanLiveRefreshAvoidsUnrelatedTraffic(t *testing.T) {
	d, srv, other := newSyncDriver(t)
	d.open("Request 1")
	active := d.m.st.Col(d.m.open.colID)
	changed := d.m.st.Collections()[1]
	activePath := "get /collection/" + active.Summary.UID
	changedPath := "get /collection/" + changed.Summary.UID
	beforeActive := srv.CallCount(activePath)
	beforeChanged := srv.CallCount(changedPath)
	beforeEnvs := srv.CallCount("post /list/environment")
	item := changed.Data.Item[0].Clone()
	item.Name = "Only this collection"
	if _, err := other.UpdateRequest(context.Background(), changed.Summary.UID, item); err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(3 * time.Second); d.m.st.Col(changed.Summary.ID).Data.Item[0].Name != item.Name; {
		if time.Now().After(deadline) {
			t.Fatal("unopened collection was not refreshed")
		}
		d.settle()
	}
	d.settle()
	if n := srv.CallCount(activePath) - beforeActive; n != 0 {
		t.Fatalf("refetched unrelated open collection %d times", n)
	}
	if n := srv.CallCount(changedPath) - beforeChanged; n != 1 {
		t.Fatalf("changed collection fetched %d times", n)
	}
	if n := srv.CallCount("post /list/environment") - beforeEnvs; n != 0 {
		t.Fatalf("listed unrelated environments %d times", n)
	}
}

func TestWatchBurstSchedulesOneFlushAndPreservesLatestOnFailure(t *testing.T) {
	m := &Model{}
	m.watchState.epoch = 7
	ch := postman.Change{Kind: "collection", UID: "c", Action: "update"}
	for i := 0; i < 1000; i++ {
		cmd := m.watchChanged(watchChangeMsg{epoch: 7, change: ch})
		if (cmd != nil) != (i == 0) {
			t.Fatal("burst scheduled more than one timer")
		}
	}
	if len(m.watchState.dirty) != 1 {
		t.Fatal("duplicate invalidations accumulated")
	}
	m.update1(watchFlushMsg{epoch: 6})
	if !m.watchState.queued {
		t.Fatal("old workspace timer consumed current flush")
	}
	latest := postman.Change{Kind: "collection", UID: "c", Action: "delete"}
	m.watchState.dirty["collection:c"] = latest
	m.refreshSeq = 1
	m.fetched(fetchedMsg{seq: 1, err: context.Canceled, dirty: map[string]postman.Change{"collection:c": ch}})
	if m.watchState.dirty["collection:c"] != latest {
		t.Fatal("failed refresh replaced a newer event")
	}
}

func TestPostmanOrdinaryRefreshUpdatesGlobals(t *testing.T) {
	d, _, other := newSyncDriver(t)
	if err := other.PutGlobals(context.Background(), "ws1", []postman.EnvValue{{Key: "from_teammate", Value: "updated"}}); err != nil {
		t.Fatal(err)
	}
	d.exec(d.m.refresh(false))
	d.settle()
	values := d.m.st.Globals()
	if len(values) != 1 || values[0].Key != "from_teammate" || values[0].Value != "updated" {
		t.Fatalf("ordinary refresh missed global variables: %+v", values)
	}
}
