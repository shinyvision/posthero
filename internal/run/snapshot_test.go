package run

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/shinyvision/posthero/internal/postman"
)

func TestSnapshotPreservesExecutionAndIsolatesEdits(t *testing.T) {
	r, c, scopes, _, _ := setup(t)
	it, parents := c.Find("r1")
	it.Response = []*postman.Response{{Body: strings.Repeat("example", 1000)}}
	target := Snapshot(c, it, parents)
	if len(target.Collection.Item) != 1 || len(target.Parents[0].Item) != 1 || len(target.Item.Response) != 0 {
		t.Fatal("snapshot retained unrelated payloads")
	}
	if !reflect.DeepEqual(target.Location(), []string{"Shop", "Users", "Create user"}) {
		t.Fatal(target.Location())
	}
	c.Auth.Type = "noauth"
	c.Event[0].Script.Exec[0] = "throw Error('edited collection');"
	parents[0].Event[0].Script.Exec[0] = "throw Error('edited folder');"
	it.Request.URL.Raw = "https://invalid.test"
	ex := r.Execute(context.Background(), target, scopes(map[string]any{"name": "Ada"}), Options{})
	if ex.Err != nil {
		t.Fatal(ex.Err)
	}
	if len(ex.Tests()) != 2 || !ex.Tests()[0].Passed || !ex.Tests()[1].Passed {
		t.Fatalf("inherited scripts: %+v", ex.Tests())
	}
	if got := ex.Prepared.Get("X-Folder"); got != "users" {
		t.Fatalf("inherited context: %q", got)
	}
}

func BenchmarkSendSnapshot(b *testing.B) {
	c := &postman.Collection{Info: postman.Info{Name: "Large"}}
	for i := 0; i < 1000; i++ {
		c.Item = append(c.Item, &postman.Item{ID: "r", Name: "Request", Request: &postman.Request{Method: "GET", URL: postman.ParseURL("https://example.test")}, Response: []*postman.Response{{Body: strings.Repeat("x", 8192)}}})
	}
	b.Run("Whole_collection", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			_ = c.Clone()
		}
	})
	b.Run("Single_request", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			_ = Snapshot(c, c.Item[0], nil)
		}
	})
}
