package syncapi_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shinyvision/posthero/internal/postman"
	"github.com/shinyvision/posthero/internal/postman/syncapi"
	"github.com/shinyvision/posthero/internal/postman/syncapi/synctest"
)

func TestBrowserPKCE(t *testing.T) {
	var challenge string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.Header.Get("User-Agent") != syncapi.UserAgent {
			t.Error("missing desktop request headers")
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		switch {
		case r.URL.Path == "/api/public/pkce/exchange/init":
			challenge, _ = body["challenge"].(string)
			if len(challenge) != 64 || body["redirect_uri"] != syncapi.CallbackURL || body["target"] != "accounts" {
				t.Error("incorrect PKCE initialization")
			}
			team := body["authTarget"].(map[string]any)["team"].(map[string]any)
			if team["id"] != float64(5) || team["domain"] != "example" {
				t.Error("lost team hint")
			}
			fmt.Fprint(w, `{"redirectUrl":"identity.getpostman.com/login?handover=test"}`)
		case strings.HasPrefix(r.URL.Path, "/api/handover/pkce/"):
			verifier := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/api/handover/pkce/"), "/consume")
			hash := sha256.Sum256([]byte(verifier))
			if hex.EncodeToString(hash[:]) != challenge || body["authorizationCode"] != "test-code" {
				t.Error("PKCE wasn't bound to the callback")
			}
			fmt.Fprint(w, `{"session":{"token":"access"},"refreshToken":{"refresh_token":"refresh","expires_in":1296000},"sessionData":{"identity":{"user":7,"team":5,"domain":"example"}}}`)
		default:
			t.Errorf("unexpected request %s", r.URL.Path)
		}
	}))
	defer srv.Close()
	flow, err := syncapi.BeginLogin(context.Background(), syncapi.LoginOptions{IdentityURL: srv.URL, TeamID: 5, TeamDomain: "example"})
	if err != nil {
		t.Fatal(err)
	}
	if flow.URL != "https://identity.getpostman.com/login?handover=test" {
		t.Fatal(flow.URL)
	}
	s, err := flow.Complete(context.Background(), "postman://auth/callback?code=test-code")
	if err != nil {
		t.Fatal(err)
	}
	if s.Token != "access" || s.RefreshToken != "refresh" || s.UserID != 7 || s.TeamID != 5 || s.TeamDomain != "example" || time.Until(s.ExpiresAt) < 14*24*time.Hour {
		t.Fatal("incomplete session")
	}
}

func TestCallbackValidation(t *testing.T) {
	for _, input := range []string{"secret-code", "postman://auth/callback?code=secret-code", "https://identity.getpostman.com/browser-auth/success?code=secret-code"} {
		got, err := syncapi.AuthorizationCode(input)
		if err != nil || got != "secret-code" {
			t.Errorf("%q: %q %v", input, got, err)
		}
	}
	for _, input := range []string{"", "postman://collection/foo?code=x", "postman://auth/callback?code=", "postman://auth/callback?code=x&code=y", "https://evil.test/browser-auth/success?code=x", "postman://user@auth/callback?code=x", "postman://auth/callback?code=x#error", "two codes"} {
		if _, err := syncapi.AuthorizationCode(input); err == nil {
			t.Errorf("accepted %q", input)
		}
	}
}

func TestBrowserSessionVariants(t *testing.T) {
	for _, tc := range []struct {
		name, response, problem string
		refresh                 bool
	}{
		// The desktop app's AuthCodeModal consumes these fields; renewal
		// metadata is not part of its sign-in success requirements.
		{name: "session only", response: `{"status":"success","session":{"token":"access"},"sessionData":{"identity":{"user":7,"team":5}}}`},
		{name: "null renewal", response: `{"session":{"token":"access"},"refreshToken":null,"sessionData":{"identity":{"user":7,"team":5}}}`},
		{name: "unknown expiry", response: `{"session":{"token":"access"},"refreshToken":{"refresh_token":"refresh"},"sessionData":{"identity":{"user":7,"team":5}}}`, refresh: true},
		{name: "expired code", response: `{"status":"error","error":{"name":"InvalidAuthorizationCode","message":"secret-code"}}`, problem: "sign-in code is invalid or has expired"},
		{name: "unknown handover", response: `{"status":"error","error":{"name":"EntityNotFound","message":"secret-verifier"}}`, problem: "sign-in code is invalid or has expired"},
		{name: "service error", response: `{"status":"error","error":{"name":"secret-error","message":"secret-token"}}`, problem: "couldn't complete the sign-in"},
		{name: "missing token", response: `{"sessionData":{"identity":{"user":7}}}`, problem: "missing access token"},
		{name: "missing identity", response: `{"session":{"token":"secret-access"}}`, problem: "missing user ID"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasSuffix(r.URL.Path, "/init") {
					fmt.Fprint(w, `{"redirectUrl":"identity.getpostman.com/login"}`)
					return
				}
				fmt.Fprint(w, tc.response)
			}))
			defer srv.Close()
			flow, err := syncapi.BeginLogin(context.Background(), syncapi.LoginOptions{IdentityURL: srv.URL})
			if err != nil {
				t.Fatal(err)
			}
			s, err := flow.Complete(context.Background(), "code")
			if tc.problem != "" {
				if err == nil || !strings.Contains(err.Error(), tc.problem) || strings.Contains(err.Error(), "secret-") {
					t.Fatalf("unsafe or incorrect error: %v", err)
				}
				if s.Token != "" {
					t.Fatal("error returned a session")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if s.Token != "access" || s.UserID != 7 || s.TeamID != 5 || !s.ExpiresAt.IsZero() || (s.RefreshToken != "") != tc.refresh {
				t.Fatal("session fields changed or unknown expiry was invented")
			}
		})
	}
}

func TestSessionWithoutRenewalMetadata(t *testing.T) {
	for _, tc := range []struct {
		name, token, refresh string
		expires              time.Time
		unauthorized         bool
	}{
		{name: "unknown expiry", token: synctest.Token},
		{name: "expires soon", token: synctest.Token, expires: time.Now().Add(time.Hour)},
		{name: "renewable unknown expiry", token: synctest.Token, refresh: "refresh"},
		{name: "expired", token: synctest.Token, expires: time.Now().Add(-time.Hour), unauthorized: true},
		{name: "revoked", token: "revoked", unauthorized: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := synctest.New()
			base := srv.Start()
			defer srv.Close()
			renew := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				t.Error("attempted renewal without a refresh token or known expiry")
				w.WriteHeader(http.StatusUnauthorized)
			}))
			defer renew.Close()
			c := syncapi.NewSession(syncapi.Session{Token: tc.token, RefreshToken: tc.refresh, UserID: 2000, ExpiresAt: tc.expires}, syncapi.Options{URL: base, IdentityURL: base, TokenURL: renew.URL})
			defer c.Close()
			_, err := c.Me(context.Background())
			if tc.unauthorized {
				if !postman.IsUnauthorized(err) {
					t.Fatalf("expected reauthentication: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err := c.Workspaces(context.Background()); err != nil {
				t.Fatal(err)
			}
			if err := c.Refresh(context.Background()); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestLoginErrorsDoNotExposeSecrets(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "init") {
			fmt.Fprint(w, `{"redirectUrl":"identity.getpostman.com/login"}`)
			return
		}
		w.WriteHeader(401)
		fmt.Fprint(w, r.URL.Path+" secret-access-token")
	}))
	defer srv.Close()
	f, err := syncapi.BeginLogin(context.Background(), syncapi.LoginOptions{IdentityURL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.Complete(context.Background(), "code")
	if !postman.IsUnauthorized(err) || strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "/pkce/") {
		t.Fatalf("unsafe or incorrect error: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = f.Complete(ctx, "code"); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel: %v", err)
	}
}

func renewalServer(t *testing.T, calls *atomic.Int32) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != "POST" || r.URL.Path != "/client-oauth2-refresh" || r.URL.Query().Get("user_id") != "2000" || r.Header.Get("x-access-token") != "old" {
			t.Error("incorrect renewal request")
		}
		_ = r.ParseForm()
		if r.PostForm.Get("refresh_token") != "old-refresh" || r.PostForm.Get("grant_type") != "refresh_token" {
			t.Error("incorrect refresh grant")
		}
		fmt.Fprintf(w, `{"access_token":%q,"refresh_token":"new-refresh","expires_in":1296000}`, synctest.Token)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestSessionConcurrentRenewalAndPersistence(t *testing.T) {
	srv := synctest.New()
	base := srv.Start()
	defer srv.Close()
	var calls, saves atomic.Int32
	renew := renewalServer(t, &calls)
	c := syncapi.NewSession(syncapi.Session{Token: "old", RefreshToken: "old-refresh", UserID: 2000, ExpiresAt: time.Now().Add(-time.Hour)}, syncapi.Options{URL: base, IdentityURL: base, TokenURL: renew.URL, OnSession: func(s syncapi.Session) error {
		saves.Add(1)
		if s.Token != synctest.Token || s.RefreshToken != "new-refresh" {
			t.Error("wrong session persisted")
		}
		return nil
	}})
	defer c.Close()
	var wg sync.WaitGroup
	for range 12 {
		wg.Go(func() {
			if _, err := c.Me(context.Background()); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if calls.Load() != 1 || saves.Load() != 1 {
		t.Fatalf("exchanged %d times, saved %d times", calls.Load(), saves.Load())
	}
	if _, err := c.Workspaces(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestSessionPersistenceRetryDoesNotRotateAgain(t *testing.T) {
	var calls, saves atomic.Int32
	renew := renewalServer(t, &calls)
	c := syncapi.NewSession(syncapi.Session{Token: "old", RefreshToken: "old-refresh", UserID: 2000, ExpiresAt: time.Now().Add(-time.Hour)}, syncapi.Options{TokenURL: renew.URL, OnSession: func(syncapi.Session) error {
		if saves.Add(1) == 1 {
			return errors.New("disk full")
		}
		return nil
	}})
	defer c.Close()
	if err := c.Refresh(context.Background()); err == nil {
		t.Fatal("lost persistence failure")
	}
	if err := c.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 || saves.Load() != 2 {
		t.Fatalf("exchanges=%d saves=%d", calls.Load(), saves.Load())
	}
}

func TestUnauthorizedReadRenewsButWriteIsNotReplayed(t *testing.T) {
	for _, read := range []bool{true, false} {
		t.Run(fmt.Sprint(read), func(t *testing.T) {
			srv := synctest.New()
			base := srv.Start()
			defer srv.Close()
			var calls atomic.Int32
			renew := renewalServer(t, &calls)
			c := syncapi.NewSession(syncapi.Session{Token: "old", RefreshToken: "old-refresh", UserID: 2000, ExpiresAt: time.Now().Add(10 * 24 * time.Hour)}, syncapi.Options{URL: base, IdentityURL: base, TokenURL: renew.URL})
			defer c.Close()
			if read {
				if _, err := c.Workspaces(context.Background()); err != nil {
					t.Fatal(err)
				}
				if calls.Load() != 1 {
					t.Fatal("read did not renew")
				}
			} else {
				err := c.PutGlobals(context.Background(), "ws1", nil)
				if !postman.IsUnauthorized(err) || calls.Load() != 0 {
					t.Fatalf("write replayed: %v, %d renewals", err, calls.Load())
				}
			}
		})
	}
}

func TestRenewalFailureBackoffAndClose(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		fmt.Fprint(w, `{"result":false,"message":"Invalid refresh_token"}`)
	}))
	defer srv.Close()
	c := syncapi.NewSession(syncapi.Session{Token: "old", RefreshToken: "r", UserID: 1, ExpiresAt: time.Now().Add(-time.Hour)}, syncapi.Options{TokenURL: srv.URL})
	for range 3 {
		if err := c.Refresh(context.Background()); !postman.IsUnauthorized(err) {
			t.Fatal(err)
		}
	}
	if calls.Load() != 1 {
		t.Fatal("renewal was not backed off")
	}
	c.Close()
	if err := c.Refresh(context.Background()); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
