package run

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/shinyvision/posthero/internal/auth"
	"github.com/shinyvision/posthero/internal/postman"
	"github.com/shinyvision/posthero/internal/script"
	"github.com/shinyvision/posthero/internal/send"
	"github.com/shinyvision/posthero/internal/vars"
)

type packageProvider func(context.Context, string, string, string) (string, error)

func (f packageProvider) Package(ctx context.Context, collection, name, id string) (string, error) {
	return f(ctx, collection, name, id)
}

func TestPackagesFollowEachEventAndInheritance(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{}`) }))
	defer srv.Close()
	var loaded []string
	r := &Runner{Engine: script.NewEngine(), Client: send.NewClient(nil), Auth: auth.NewManager(""), Settings: send.DefaultSettings()}
	r.Packages = packageProvider(func(ctx context.Context, collection, name, id string) (string, error) {
		if collection != "collection" || name != "@team/helper" {
			t.Fatalf("package context: %s %s", collection, name)
		}
		loaded = append(loaded, id)
		return fmt.Sprintf("module.exports = %q", id), nil
	})
	event := func(id, listen string) postman.Event {
		deps, _ := json.Marshal(map[string]any{"@team/helper": map[string]string{"id": id}})
		return postman.Event{Listen: listen, Script: postman.Script{Exec: postman.Lines{fmt.Sprintf(`const value = pm.require('@team/helper'); pm.test('package %s', () => pm.expect(value).to.equal(%q));`, id, id)}, Extra: map[string]json.RawMessage{"packages": deps}}}
	}
	c := &postman.Collection{}
	c.Info.PostmanID = "collection"
	c.Event = []postman.Event{event("collection-pre", "prerequest"), event("collection-test", "test")}
	parent := &postman.Item{Event: []postman.Event{event("folder-pre", "prerequest")}}
	item := &postman.Item{Request: &postman.Request{Method: "GET", URL: postman.ParseURL(srv.URL)}, Event: []postman.Event{event("request-pre-1", "prerequest"), event("request-pre-2", "prerequest"), event("disabled", "prerequest"), event("request-test", "test")}}
	item.Event[2].Disabled = true
	ex := r.Execute(context.Background(), Target{Collection: c, Parents: []*postman.Item{parent}, Item: item}, vars.New(&vars.Scope{Kind: vars.Local}), Options{})
	if ex.Err != nil || ex.Scripts.Failed() != 0 || ex.Scripts.Passed() != 6 {
		t.Fatalf("err=%v scripts=%+v", ex.Err, ex.Scripts)
	}
	want := []string{"collection-pre", "folder-pre", "request-pre-1", "request-pre-2", "collection-test", "request-test"}
	if !reflect.DeepEqual(loaded, want) {
		t.Fatalf("loaded=%v", loaded)
	}
	// A collection skip stops every subsequent pre-request script, too.
	c.Event[0].Script.Exec = postman.Lines{"pm.execution.skipRequest()"}
	loaded = nil
	ex = r.Execute(context.Background(), Target{Collection: c, Parents: []*postman.Item{parent}, Item: item}, vars.New(&vars.Scope{Kind: vars.Local}), Options{})
	if !ex.Skipped || len(loaded) != 0 {
		t.Fatalf("skipped=%v loaded=%v", ex.Skipped, loaded)
	}
}
