package auth_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shinyvision/posthero/internal/auth"
)

// A config with a Refresh Token URL of its own gets a token of its own, and
// a token is refreshed only at the server it came from: its refresh token
// never goes anywhere else.
func TestRefreshTokenStaysWithItsServer(t *testing.T) {
	idp, _, m, clock := setup(t)
	var mu sync.Mutex
	var got []string
	evil := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		mu.Lock()
		got = append(got, r.PostForm.Get("refresh_token"))
		mu.Unlock()
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer evil.Close()
	a := auth.Config{Grant: auth.GrantPKCE, AuthURL: idp.AuthURL(), TokenURL: idp.TokenURL(), RefreshURL: idp.TokenURL(), ClientID: "portal", RedirectURI: "http://localhost:3000/cb", Challenge: "S256", Scope: "openid"}
	p := &prompter{logins: []auth.LoginAnswer{{Username: "rachel", Password: "hunter2"}}}
	if _, _, err := m.Token(context.Background(), a, p, ""); err != nil {
		t.Fatal(err)
	}
	b := a
	b.RefreshURL = evil.URL + "/token"
	if a.Key() == b.Key() {
		t.Error("a config refreshing at another server shares the token")
	}
	// Even handed a's token, b doesn't refresh it.
	*clock = clock.Add(5 * time.Minute)
	refreshed, failed := m.KeepFresh(context.Background(), map[string]auth.Config{a.Key(): b})
	if len(refreshed) != 0 || failed[a.Key()] == nil || !strings.Contains(failed[a.Key()].Error(), strings.TrimPrefix(evil.URL, "http://")) {
		t.Errorf("refreshed %v, failed %v", refreshed, failed)
	}
	mu.Lock()
	n := len(got)
	mu.Unlock()
	if n != 0 {
		t.Fatalf("the refresh token went to %s", evil.URL)
	}
	// The token is still good for a.
	if refreshed, failed := m.KeepFresh(context.Background(), map[string]auth.Config{a.Key(): a}); len(refreshed) != 1 {
		t.Errorf("a wasn't refreshed: %v", failed)
	}
}

// A login waiting for its password holds up neither KeepFresh nor the other
// tokens, and a request waiting for that login can give up.
func TestKeepFreshDoesNotWaitForALogin(t *testing.T) {
	clock := time.Now()
	// The first identity provider can't refresh right now; the second can.
	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer down.Close()
	var refreshes atomic.Int32
	live := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		refreshes.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"access_token":"new","expires_in":300,"refresh_token":"r2"}`)
	}))
	defer live.Close()
	m, _ := newManager(t)
	m.Now = func() time.Time { return clock }
	c1 := auth.Config{Grant: auth.GrantPassword, TokenURL: down.URL + "/token", RefreshURL: down.URL + "/token", ClientID: "a", Username: "rachel"}
	c2 := auth.Config{Grant: auth.GrantClient, TokenURL: live.URL + "/token", RefreshURL: live.URL + "/token", ClientID: "b"}
	due := clock.Add(10 * time.Second)
	m.Tokens.Put(&auth.Token{Key: c1.Key(), AccessToken: "x1", RefreshToken: "r1", ExpiresAt: due, IssuedAt: clock.Add(-5 * time.Minute), LastUsed: clock})
	m.Tokens.Put(&auth.Token{Key: c2.Key(), AccessToken: "x2", RefreshToken: "r2", ExpiresAt: due, IssuedAt: clock.Add(-5 * time.Minute), LastUsed: clock})

	// A request for c1 has to log in, and waits for the password.
	wp := &waitingPrompter{asked: make(chan struct{}, 1), release: make(chan struct{})}
	first := make(chan struct{})
	go func() {
		defer close(first)
		_, _, _ = m.Token(context.Background(), c1, wp, auth.Fresh)
	}()
	select {
	case <-wp.asked:
	case <-time.After(5 * time.Second):
		t.Fatal("the login never asked")
	}
	defer func() { close(wp.release); <-first }()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	kept := make(chan []string, 1)
	go func() {
		refreshed, _ := m.KeepFresh(ctx, map[string]auth.Config{c1.Key(): c1, c2.Key(): c2})
		kept <- refreshed
	}()
	select {
	case refreshed := <-kept:
		if len(refreshed) != 1 || refreshed[0] != c2.Key() || refreshes.Load() != 1 {
			t.Errorf("refreshed %v (%d refreshes)", refreshed, refreshes.Load())
		}
	case <-time.After(5 * time.Second):
		t.Error("KeepFresh waited for the login, past its deadline")
	}

	// Another request for c1 waits for that login, until it gives up.
	short, cancel2 := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel2()
	errc := make(chan error, 1)
	go func() {
		_, _, err := m.Token(short, c1, &prompter{}, "")
		errc <- err
	}()
	select {
	case err := <-errc:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("the waiting request: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Error("a request waiting for another's login didn't give up")
	}
}

// waitingPrompter is a person who doesn't answer until released.
type waitingPrompter struct {
	asked, release chan struct{}
}

func (w *waitingPrompter) Login(ctx context.Context, q auth.LoginPrompt) (auth.LoginAnswer, error) {
	select {
	case w.asked <- struct{}{}:
	default:
	}
	<-w.release
	return auth.LoginAnswer{}, context.Canceled
}

func (w *waitingPrompter) Form(context.Context, auth.FormPrompt) (auth.FormAnswer, error) {
	return auth.FormAnswer{}, context.Canceled
}

func (w *waitingPrompter) Browser(context.Context, auth.BrowserPrompt) (string, error) {
	return "", context.Canceled
}

func (w *waitingPrompter) Status(string) {}

// A refresh token the identity provider refused (its session ended) is let
// go: tried once rather than on every check, while the access token is kept
// until it runs out. One that couldn't be tried (the server was down) stays.
func TestRefusedRefreshTokenIsDropped(t *testing.T) {
	var hits atomic.Int32
	var down atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if down.Load() {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"error":"invalid_grant","error_description":"Session not active"}`)
	}))
	defer srv.Close()
	clock := time.Now()
	m, dir := newManager(t)
	m.Now = func() time.Time { return clock }
	c := auth.Config{Grant: auth.GrantPKCE, AuthURL: srv.URL + "/auth", TokenURL: srv.URL + "/token", RefreshURL: srv.URL + "/token", ClientID: "a"}
	fresh := func() {
		m.Tokens.Put(&auth.Token{Key: c.Key(), AccessToken: "x", RefreshToken: "r", ExpiresAt: clock.Add(time.Minute), RefreshExpiresAt: clock.Add(10 * time.Hour), IssuedAt: clock.Add(-4 * time.Minute), LastUsed: clock})
	}
	configs := map[string]auth.Config{c.Key(): c}

	down.Store(true)
	fresh()
	clock = clock.Add(15 * time.Second)
	m.KeepFresh(context.Background(), configs)
	if tok := m.Tokens.Get(c.Key()); tok.RefreshToken != "r" {
		t.Fatalf("a refresh token the server couldn't try was dropped: %+v", tok)
	}

	down.Store(false)
	hits.Store(0)
	fresh()
	// Every 15 seconds for 5 minutes.
	for i := 0; i < 20; i++ {
		clock = clock.Add(15 * time.Second)
		m.KeepFresh(context.Background(), configs)
	}
	if n := hits.Load(); n != 1 {
		t.Errorf("the refused refresh token was tried %d times", n)
	}
	for _, tok := range []*auth.Token{m.Tokens.Get(c.Key()), auth.NewStore(dir + "/tokens.json").Get(c.Key())} {
		if tok == nil || tok.AccessToken != "x" || tok.RefreshToken != "" {
			t.Errorf("after the refusal: %+v", tok)
		}
	}
}
