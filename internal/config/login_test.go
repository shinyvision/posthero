package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shinyvision/posthero/internal/postman/syncapi"
	"github.com/shinyvision/posthero/internal/secret"
)

func TestSessionStorage(t *testing.T) {
	for _, tc := range []struct {
		name               string
		keyring, renewable bool
	}{
		{"keyring", true, true},
		{"file", false, true},
		{"keyring without renewal", true, false},
		{"file without renewal", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())
			t.Setenv("POSTMAN_API_KEY", "environment-key")
			if tc.keyring {
				secret.MockInit()
			} else {
				secret.MockUnavailable(errors.New("no keyring"))
			}
			s := syncapi.Session{Token: "secret-access", RefreshToken: "secret-refresh", UserID: 7, TeamID: 5, TeamDomain: "example", ExpiresAt: time.Now().UTC().Truncate(time.Second)}
			if !tc.renewable {
				s.RefreshToken, s.ExpiresAt = "", time.Time{}
			}
			note, err := SaveSession(s)
			if err != nil {
				t.Fatal(err)
			}
			if (note == "") != tc.keyring {
				t.Fatalf("fallback notice %q", note)
			}
			path := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "posthero", "login.json")
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if tc.keyring && strings.Contains(string(raw), "secret-") {
				t.Fatal("tokens leaked into metadata")
			}
			info, _ := os.Stat(path)
			if info.Mode().Perm() != 0600 {
				t.Fatal("login file isn't private")
			}
			l, ok, err := LoadLogin()
			if err != nil || !ok || l.Session == nil || *l.Session != s || l.APIKey != "" {
				t.Fatal("session did not roundtrip", err)
			}
			if _, err := SaveLogin("api-key"); err != nil {
				t.Fatal(err)
			}
			l, ok, err = LoadLogin()
			if err != nil || !ok || l.Session != nil || l.APIKey != "api-key" {
				t.Fatal("key didn't replace session", err)
			}
			if err := ForgetLogin(); err != nil {
				t.Fatal(err)
			}
			l, ok, err = LoadLogin()
			if err != nil || !ok || l.APIKey != "environment-key" {
				t.Fatal("environment fallback", err)
			}
		})
	}
}

func TestUnavailableSessionDoesNotSilentlySwitchAccounts(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("POSTMAN_API_KEY", "different-account")
	secret.MockInit()
	if _, err := SaveSession(syncapi.Session{Token: "a", RefreshToken: "r", UserID: 1}); err != nil {
		t.Fatal(err)
	}
	secret.MockUnavailable(errors.New("locked"))
	_, ok, err := LoadLogin()
	if ok || err == nil {
		t.Fatal("silently switched to environment account")
	}
}

func TestLegacyAPIKeyLogin(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	secret.MockInit()
	p, err := configPath("login.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(`{"apiKey":"old-key"}`), 0600); err != nil {
		t.Fatal(err)
	}
	l, ok, err := LoadLogin()
	if err != nil || !ok || l.APIKey != "old-key" || l.Session != nil {
		t.Fatal("old login stopped working", err)
	}
}
