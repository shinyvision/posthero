package config

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"

	"github.com/shinyvision/posthero/internal/postman/syncapi"
	"github.com/shinyvision/posthero/internal/secret"
)

func TestPostmanInstallationSurvivesConcurrentStartsAndLogout(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	const count = 20
	ids := make(chan string, count)
	var workers sync.WaitGroup
	for range count {
		workers.Go(func() {
			id, err := installationID()
			if err != nil {
				t.Error(err)
			}
			ids <- id
		})
	}
	workers.Wait()
	close(ids)
	first := ""
	for id := range ids {
		if first == "" {
			first = id
		}
		if id != first {
			t.Error("concurrent startups created different installations")
		}
	}
	secret.MockInit()
	if err := ForgetLogin(); err != nil {
		t.Fatal(err)
	}
	if again, err := installationID(); err != nil || again != first {
		t.Fatal("logout changed installation identity", err)
	}
	path, _ := configPath("postman-installation-id")
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("installation file permissions", err)
	}
	if err := os.WriteFile(path, []byte("damaged"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := installationID(); err == nil {
		t.Fatal("silently replaced a damaged installation ID")
	}
}

func TestPostmanVersionCheckOnlyForBrowserSessions(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	calls := 0
	latest := func(ctx context.Context, current string) (string, error) { calls++; return "13.2.1", nil }
	first, note, err := postmanClient(context.Background(), false, latest)
	if err != nil || note != "" || calls != 0 || first.Version != syncapi.AppVersion {
		t.Fatalf("API key startup: %+v %q %v calls=%d", first, note, err, calls)
	}
	for range 2 {
		opts, note, err := postmanClient(context.Background(), true, latest)
		if err != nil || note != "" || opts.Version != "13.2.1" || opts.AppID != first.AppID {
			t.Fatalf("browser startup: %+v %q %v", opts, note, err)
		}
	}
	if calls != 2 {
		t.Fatal("did not check on every browser startup")
	}
	fail := func(ctx context.Context, current string) (string, error) {
		if current != "13.2.1" {
			t.Error("lost cached version")
		}
		return "", errors.New("offline")
	}
	opts, note, err := postmanClient(context.Background(), true, fail)
	if err != nil || note == "" || opts.Version != "13.2.1" || opts.AppID != first.AppID {
		t.Fatalf("offline fallback: %+v %q %v", opts, note, err)
	}
	if _, _, err := postmanClient(context.Background(), false, func(context.Context, string) (string, error) {
		t.Fatal("API key startup checked version")
		return "", nil
	}); err != nil {
		t.Fatal(err)
	}
}
