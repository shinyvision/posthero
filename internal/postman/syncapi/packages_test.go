package syncapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/shinyvision/posthero/internal/postman/syncapi/synctest"
	"golang.org/x/net/websocket"
)

func TestTeamPackageProxy(t *testing.T) {
	var paths []string
	var dropped atomic.Bool
	srv := httptest.NewServer(websocket.Server{Handshake: func(*websocket.Config, *http.Request) error { return nil }, Handler: func(ws *websocket.Conn) {
		defer ws.Close()
		_ = websocket.Message.Send(ws, `0{"pingInterval":60000,"pingTimeout":60000}`)
		_ = websocket.Message.Send(ws, "40")
		for {
			var msg string
			if websocket.Message.Receive(ws, &msg) != nil {
				return
			}
			if !strings.HasPrefix(msg, "42") {
				continue
			}
			if !dropped.Swap(true) {
				return
			} // replay the lost package read once
			i := strings.IndexByte(msg, '[')
			var parts []json.RawMessage
			_ = json.Unmarshal([]byte(msg[i:]), &parts)
			var req struct {
				Method, URL string
				Headers     map[string]string
				Data        struct{ Service, Method, Path string }
			}
			if len(parts) != 2 || json.Unmarshal(parts[1], &req) != nil {
				t.Error("bad socket request")
				return
			}
			if req.Method != "post" || req.URL != "/ws/proxy" || req.Data.Service != "package-library-service" || req.Data.Method != "get" || req.Headers["x-access-token"] != "session" {
				t.Errorf("unexpected proxy request: %+v", req)
			}
			if req.Headers["x-app-version"] != "13.2.1" || !strings.Contains(req.Headers["user-agent"], "PostmanClient/13.2.1 (AppId=installation)") {
				t.Error("stale proxy client identity")
			}
			paths = append(paths, req.Data.Path)
			body := `{}`
			switch req.Data.Path {
			case "/packages?isDeleted=false&cursor=":
				body = `{"data":[],"meta":{"nextCursor":"next"}}`
			case "/packages?isDeleted=false&cursor=next":
				body = `{"data":[{"id":"pkg","name":"helper"}],"meta":{}}`
			case "/packages/pkg":
				body = `{"data":{"indexScript":{"id":"script"}}}`
			case "/packages/pkg/scripts/script?include=contents":
				if req.Headers["x-src-collection"] != "collection" {
					t.Error("missing source collection")
				}
				body = `{"data":{"contents":{"src":"module.exports = 42"}}}`
			default:
				t.Errorf("unexpected path: %s", req.Data.Path)
			}
			_ = websocket.Message.Send(ws, "43"+msg[2:i]+`[{"statusCode":200,"body":`+body+`}]`)
		}
	}})
	defer srv.Close()
	c := New("session", Options{URL: srv.URL, Version: "13.2.1", AppID: "installation"})
	defer c.Close()
	for _, id := range []string{"", "pkg"} {
		code, err := c.Package(context.Background(), "collection", "@team/helper", id)
		if err != nil || code != "module.exports = 42" {
			t.Fatalf("source=%q err=%v", code, err)
		}
	}
	want := []string{"/packages?isDeleted=false&cursor=", "/packages?isDeleted=false&cursor=next", "/packages/pkg", "/packages/pkg/scripts/script?include=contents", "/packages/pkg", "/packages/pkg/scripts/script?include=contents"}
	if !reflect.DeepEqual(paths, want) {
		t.Fatalf("paths=%v", paths)
	}
}

func TestExternalPackageBundles(t *testing.T) {
	for _, registry := range []string{"npm", "jsr"} {
		t.Run(registry, func(t *testing.T) {
			var handshakes, bundles atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "GET" {
					t.Error("package fetch mutated remote data")
				}
				switch r.URL.Path {
				case "/v1/package-access":
					if registry == "jsr" {
						t.Error("queried policy without access-control entitlement")
						w.WriteHeader(http.StatusForbidden)
						return
					}
					if r.Header.Get("Authorization") != "Bearer bundle-token" {
						t.Error("wrong policy credentials")
					}
					fmt.Fprint(w, `{"access":{"scope":"restricted","packages":["`+registry+`:@scope/helper@*"]}}`)
				case "/handshake/token":
					handshakes.Add(1)
					if r.URL.RawQuery != "source=packman" || r.Header.Get("x-access-token") != synctest.Token || r.Header.Get("Authorization") != "" {
						t.Error("wrong handshake credentials")
					}
					fmt.Fprint(w, `{"token":"bundle-token"}`)
				default:
					bundles.Add(1)
					if r.URL.EscapedPath() != "/v1/bundle/"+registry+"/@scope%2Fhelper@1.2.3" {
						t.Errorf("path=%s", r.URL.EscapedPath())
					}
					if r.Header.Get("Authorization") != "Bearer bundle-token" || r.Header.Get("x-access-token") != "" {
						t.Error("wrong bundle credentials")
					}
					w.Header().Set("Package-Version", "1.2.3")
					fmt.Fprint(w, `module.exports = {add:(a,b)=>a+b}`)
				}
			}))
			defer srv.Close()
			features := synctest.New()
			features.PackageAccessControl = registry == "npm"
			base := features.Start()
			defer features.Close()
			c := New(synctest.Token, Options{URL: base, IdentityURL: base, RuntimeAgentURL: srv.URL, PackageURL: srv.URL})
			defer c.Close()
			code, err := c.Package(context.Background(), "", registry+":@scope/helper", registry+":@scope/helper@1.2.3")
			if err != nil || !strings.Contains(code, "module.exports") || handshakes.Load() != 1 || bundles.Load() != 1 {
				t.Fatalf("source=%q err=%v calls=%d/%d", code, err, handshakes.Load(), bundles.Load())
			}
		})
	}
}

func TestPackageCredentialsDoNotFollowRedirects(t *testing.T) {
	var redirected atomic.Bool
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { redirected.Store(true) }))
	defer target.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, 302) }))
	defer srv.Close()
	c := New("session", Options{RuntimeAgentURL: srv.URL})
	defer c.Close()
	if _, err := c.Package(context.Background(), "", "npm:helper@1.2.3", ""); err == nil {
		t.Fatal("redirect succeeded")
	}
	if redirected.Load() {
		t.Fatal("package credentials sent to redirect target")
	}
}

func TestDeniedPackageIsNotDownloaded(t *testing.T) {
	for _, policy := range []string{`{"access":{"scope":"none"}}`, `{"access":{"scope":"restricted","packages":["npm:other@*"]}}`, `{}`} {
		t.Run(policy, func(t *testing.T) {
			var downloaded atomic.Bool
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/handshake/token":
					fmt.Fprint(w, `{"token":"bundle-token"}`)
				case "/v1/package-access":
					fmt.Fprint(w, policy)
				default:
					downloaded.Store(true)
					w.Header().Set("Package-Version", "1.0.0")
					fmt.Fprint(w, "module.exports = 42")
				}
			}))
			defer srv.Close()
			features := synctest.New()
			features.PackageAccessControl = true
			base := features.Start()
			defer features.Close()
			c := New(synctest.Token, Options{URL: base, IdentityURL: base, RuntimeAgentURL: srv.URL, PackageURL: srv.URL})
			defer c.Close()
			if _, err := c.Package(context.Background(), "", "npm:helper@1.0.0", ""); err == nil {
				t.Fatal("disallowed package succeeded")
			}
			if downloaded.Load() {
				t.Fatal("downloaded a disallowed package")
			}
		})
	}
}
