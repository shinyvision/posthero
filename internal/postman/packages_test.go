package postman

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

func TestTeamPackagesWithAPIKey(t *testing.T) {
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.Header.Get("X-Api-Key") != "test-key" {
			t.Errorf("wrong method or credentials")
		}
		paths = append(paths, r.URL.RequestURI())
		switch r.URL.Path {
		case "/packages":
			if r.URL.Query().Get("cursor") == "next+page" {
				fmt.Fprint(w, `{"data":[{"id":"pkg","name":"helper"}],"meta":{}}`)
			} else {
				fmt.Fprint(w, `{"data":[],"meta":{"nextCursor":"next+page"}}`)
			}
		case "/packages/pkg":
			fmt.Fprint(w, `{"script":"module.exports = 42"}`)
		case "/packages/saved":
			fmt.Fprint(w, `{"data":{"script":"module.exports = 43"}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	c := NewClient("test-key", srv.URL)
	for _, item := range []struct{ id, want string }{{"", "module.exports = 42"}, {"saved", "module.exports = 43"}} {
		code, err := c.Package(context.Background(), "", "@team/helper", item.id)
		if err != nil || code != item.want {
			t.Fatalf("source=%q err=%v", code, err)
		}
	}
	if !reflect.DeepEqual(paths, []string{"/packages?cursor=", "/packages?cursor=next%2Bpage", "/packages/pkg", "/packages/saved"}) {
		t.Fatalf("paths=%v", paths)
	}
	if _, err := c.Package(context.Background(), "", "npm:lodash@4.17.21", ""); err == nil {
		t.Fatal("registry packages should explain that a browser session is required")
	}
}
