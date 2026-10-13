package auth_test

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/shinyvision/posthero/internal/auth"
	"github.com/shinyvision/posthero/internal/secret"
)

// TestMain keeps the tests away from the person's own logins: an in-memory
// keyring from the start, and a home directory of their own.
func TestMain(m *testing.M) {
	secret.MockInit()
	home, err := os.MkdirTemp("", "posthero-auth-test")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	for k, dir := range map[string]string{
		"HOME": home, "XDG_CONFIG_HOME": filepath.Join(home, ".config"), "XDG_STATE_HOME": filepath.Join(home, ".local/state"),
		"XDG_DATA_HOME": filepath.Join(home, ".local/share"), "XDG_CACHE_HOME": filepath.Join(home, ".cache"),
	} {
		_ = os.Setenv(k, dir)
	}
	code := m.Run()
	_ = os.RemoveAll(home)
	os.Exit(code)
}

// newManager is a manager keeping its files in a directory of the test's,
// with an in-memory keyring and a browser that never opens.
func newManager(t *testing.T) (*auth.Manager, string) {
	t.Helper()
	secret.MockInit()
	dir := t.TempDir()
	m := auth.NewManager(dir)
	m.OpenBrowser = func(string) error { return nil }
	return m, dir
}
