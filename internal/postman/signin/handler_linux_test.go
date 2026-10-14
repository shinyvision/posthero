//go:build linux

package signin

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDesktopRelayRestoresAssociation(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir+"/config")
	t.Setenv("XDG_DATA_HOME", dir+"/data")
	t.Setenv("XDG_CURRENT_DESKTOP", "")
	for _, p := range []string{dir + "/bin", dir + "/config"} {
		if err := os.MkdirAll(p, 0700); err != nil {
			t.Fatal(err)
		}
	}
	old := "[Default Applications]\nx-scheme-handler/postman=Postman.desktop;\ntext/plain=editor.desktop;\n"
	mime := dir + "/config/mimeapps.list"
	if err := os.WriteFile(mime, []byte(old), 0600); err != nil {
		t.Fatal(err)
	}
	for name, script := range map[string]string{
		"update-desktop-database": "#!/bin/sh\nexit 0\n",
		"xdg-mime":                "#!/bin/sh\nprintf '[Default Applications]\\nx-scheme-handler/postman=%s;\\ntext/plain=editor.desktop;\\n' \"$2\" > \"$XDG_CONFIG_HOME/mimeapps.list\"\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, "bin", name), []byte(script), 0700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir+"/bin:"+os.Getenv("PATH"))
	got := make(chan string, 1)
	cleanup, err := registerDesktop(context.Background(), "http://127.0.0.1:1234/success/", func(code string) error { got <- code; return nil })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cleanup)
	files, _ := filepath.Glob(dir + "/data/applications/posthero-signin-*.desktop")
	if len(files) != 1 {
		t.Fatal("handler wasn't registered")
	}
	raw, _ := os.ReadFile(files[0])
	if !strings.Contains(string(raw), "--postman-callback") {
		t.Fatal("handler doesn't relay")
	}
	// The temporary basename is also the private socket directory.
	socket := filepath.Join(os.TempDir(), strings.TrimSuffix(filepath.Base(files[0]), ".desktop"), "callback.sock")
	address, err := Relay(socket, "postman://auth/callback?code=relay-code")
	if err != nil || address != "http://127.0.0.1:1234/success/" {
		t.Fatal("callback not relayed", err)
	}
	if code := <-got; code != "postman://auth/callback?code=relay-code" {
		t.Fatal("wrong callback")
	}
	if _, err := Relay(socket, "postman://collection/example?code=bad"); err == nil {
		t.Fatal("accepted unrelated link")
	}
	// A concurrent login mustn't change the first one's handler.
	if cleanup2, err := registerDesktop(context.Background(), address, func(string) error { return nil }); err == nil {
		cleanup2()
		t.Fatal("allowed overlapping handlers")
	}
	current, _ := os.ReadFile(mime)
	_ = os.WriteFile(mime, []byte(strings.ReplaceAll(string(current), "editor.desktop", "new-editor.desktop")), 0600)
	cleanup()
	back, _ := os.ReadFile(mime)
	if string(back) != strings.ReplaceAll(old, "editor.desktop", "new-editor.desktop") {
		t.Fatalf("incorrect restore: %s", back)
	}
	if _, err := os.Stat(files[0]); !os.IsNotExist(err) {
		t.Fatal("handler was left behind")
	}
	if _, err := os.Stat(filepath.Dir(socket)); !os.IsNotExist(err) {
		t.Fatal("socket directory was left behind")
	}
}

func TestRestoreAssociationKeepsNewDefault(t *testing.T) {
	before := "[Default Applications]\nx-scheme-handler/postman=Postman.desktop;\n"
	current := "[Default Applications]\nx-scheme-handler/postman=NewApp.desktop;\n"
	if got := restoreAssociation(current, before, "posthero.desktop"); got != current {
		t.Fatal("overwrote a newer choice")
	}
	if got := restoreAssociation("[Default Applications]\nx-scheme-handler/postman=posthero.desktop;\n", "", "posthero.desktop"); got != "" {
		t.Fatalf("left a new empty association file: %q", got)
	}
}
