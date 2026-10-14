package signin

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/shinyvision/posthero/internal/postman/syncapi"
)

func identity(t *testing.T) *httptest.Server {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/init") {
			fmt.Fprint(w, `{"redirectUrl":"identity.getpostman.com/login"}`)
			return
		}
		fmt.Fprint(w, `{"session":{"token":"private-token"},"sessionData":{"identity":{"user":7}}}`)
	}))
	t.Cleanup(s.Close)
	return s
}

func TestLocalPageCompletesAndRejectsOtherOrigins(t *testing.T) {
	id := identity(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	addresses := make(chan string, 1)
	result := make(chan error, 1)
	go func() {
		s, err := Run(ctx, Options{Login: syncapi.LoginOptions{IdentityURL: id.URL}, OpenBrowser: func(address string) error { addresses <- address; return nil }})
		if err == nil && s.Token != "private-token" {
			err = errors.New("wrong session")
		}
		result <- err
	}()
	var address string
	select {
	case address = <-addresses:
	case <-time.After(3 * time.Second):
		t.Fatal("page didn't open")
	}
	u, _ := url.Parse(address)
	r, err := http.Get(address)
	if err != nil {
		t.Fatal(err)
	}
	page, _ := io.ReadAll(r.Body)
	r.Body.Close()
	if r.StatusCode != 200 || !strings.Contains(string(page), "Sign in to Postman") || r.Header.Get("Referrer-Policy") != "no-referrer" {
		t.Fatal("missing login page or privacy headers")
	}
	for _, foreign := range []bool{true, false} {
		req, _ := http.NewRequest("POST", address, strings.NewReader("code=valid"))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		if foreign {
			req.Header.Set("Origin", "https://evil.test")
		} else {
			req.Host = "evil.test"
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode == 200 {
			t.Fatal("accepted request from another origin")
		}
	}
	r, err = http.Get(u.Scheme + "://" + u.Host + "/wrong/")
	if err != nil {
		t.Fatal(err)
	}
	r.Body.Close()
	if r.StatusCode != 404 {
		t.Fatal("unguessable path not required")
	}
	r, err = http.PostForm(address, url.Values{"code": {"postman://auth/callback?code=valid"}})
	if err != nil {
		t.Fatal(err)
	}
	page, _ = io.ReadAll(r.Body)
	r.Body.Close()
	if !strings.Contains(string(page), "Connected to Postman") || strings.Contains(string(page), "private-token") || strings.Contains(string(page), "private-refresh") {
		t.Fatal("incorrect success page")
	}
	select {
	case err := <-result:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("login didn't finish")
	}
	r, err = http.Get(address + "status")
	if err != nil {
		t.Fatal(err)
	}
	status, _ := io.ReadAll(r.Body)
	r.Body.Close()
	if !strings.Contains(string(status), `"done":true`) {
		t.Fatal("success page not available after return")
	}
}

func TestCancelClosesLocalPage(t *testing.T) {
	id := identity(t)
	ctx, cancel := context.WithCancel(context.Background())
	addresses := make(chan string, 1)
	done := make(chan error, 1)
	go func() {
		_, err := Run(ctx, Options{Login: syncapi.LoginOptions{IdentityURL: id.URL}, Ready: func(address string) { addresses <- address }})
		done <- err
	}()
	address := <-addresses
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if r, err := http.Get(address); err == nil {
		r.Body.Close()
		t.Fatal("page still listening after cancel")
	}
}
