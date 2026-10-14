package syncapi

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestLatestVersion(t *testing.T) {
	for _, tc := range []struct {
		name       string
		status     int
		body, want string
		wantErr    bool
	}{
		{"new release", 200, `{"version":"13.2.1"}`, "13.2.1", false},
		{"current", 204, "", "12.32.2", false},
		{"stale response", 200, `{"version":"12.9.0"}`, "12.32.2", false},
		{"outage", 503, "", "", true},
		{"bad JSON", 200, `<html>error</html>`, "", true},
		{"missing version", 200, `{}`, "", true},
		{"unsafe version", 200, `{"version":"13.0.0\r\nx-header: value"}`, "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "GET" || r.URL.Query().Get("currentVersion") != "12.32.2" || r.URL.Query().Get("platform") != "linux_64" || r.URL.Query().Get("channel") != "stable" {
					t.Error("wrong update query")
				}
				if r.Header.Get("x-access-token") != "" || r.Header.Get("Authorization") != "" {
					t.Error("sent credentials to update service")
				}
				w.WriteHeader(tc.status)
				fmt.Fprint(w, tc.body)
			}))
			defer srv.Close()
			got, err := latestVersion(context.Background(), srv.URL, "linux_64", "12.32.2")
			if got != tc.want || (err != nil) != tc.wantErr {
				t.Fatalf("got=%q err=%v", got, err)
			}
		})
	}
}

func TestVersionCheckCancellation(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := latestVersion(ctx, srv.URL, "linux_64", AppVersion); err == nil {
		t.Fatal("cancelled lookup succeeded")
	}
	if time.Since(start) > time.Second {
		t.Fatal("version check blocked startup after cancellation")
	}
}

func TestClientVersionIdentityAndRenewal(t *testing.T) {
	const version = "13.2.1"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.UserAgent(), "Postman/"+version) || strings.Contains(r.UserAgent(), "Postman/"+AppVersion) {
			t.Errorf("stale user agent: %s", r.UserAgent())
		}
		if r.URL.Path == "/client-oauth2-refresh" {
			fmt.Fprint(w, `{"access_token":"new","refresh_token":"refresh","expires_in":1296000}`)
			return
		}
		if !strings.Contains(r.UserAgent(), "PostmanClient/"+version+" (AppId=installation)") {
			t.Errorf("missing client identity: %s", r.UserAgent())
		}
		fmt.Fprint(w, `{"session":{"identity":{"user":2000}}}`)
	}))
	defer srv.Close()
	c := NewSession(Session{Token: "old", RefreshToken: "refresh", UserID: 2000, ExpiresAt: time.Now()}, Options{Version: version, AppID: "installation", IdentityURL: srv.URL, TokenURL: srv.URL})
	defer c.Close()
	if _, err := c.Me(context.Background()); err != nil {
		t.Fatal(err)
	}
	if c.sock.Header.Get("User-Agent") != userAgent(version) {
		t.Fatal("stale socket handshake version")
	}
}
