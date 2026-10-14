//go:build linux

package signin

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func register(ctx context.Context, address string, complete func(string) error) (func(), string) {
	if testing.Testing() {
		return func() {}, "Paste the code here to finish."
	}
	cleanup, err := registerDesktop(ctx, address, complete)
	if err != nil {
		return func() {}, "Automatic return isn't available. Paste the code from Postman's success page here to finish."
	}
	return cleanup, ""
}

func registerDesktop(ctx context.Context, address string, complete func(string) error) (func(), error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	data := os.Getenv("XDG_DATA_HOME")
	if !filepath.IsAbs(data) {
		data = filepath.Join(home, ".local/share")
	}
	config := os.Getenv("XDG_CONFIG_HOME")
	if !filepath.IsAbs(config) {
		config = filepath.Join(home, ".config")
	}
	apps := filepath.Join(data, "applications")
	if err := os.MkdirAll(apps, 0700); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Join(config, "posthero"), 0700); err != nil {
		return nil, err
	}
	lock, err := os.OpenFile(filepath.Join(config, "posthero", "signin.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err := unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		lock.Close()
		return nil, errors.New("another sign-in is active")
	}
	dir, err := os.MkdirTemp("", "posthero-signin-")
	if err != nil {
		lock.Close()
		return nil, err
	}
	var listener net.Listener
	desktop := filepath.Base(dir) + ".desktop"
	file := filepath.Join(apps, desktop)
	// xdg-mime uses different files under different desktops. Remember just
	// the scheme's entries, and undo only ours, preserving unrelated edits.
	paths := []string{filepath.Join(config, "mimeapps.list"), filepath.Join(apps, "mimeapps.list")}
	for _, desktopName := range strings.Split(os.Getenv("XDG_CURRENT_DESKTOP"), ":") {
		if desktopName != "" && !strings.ContainsAny(desktopName, "/\\") {
			paths = append(paths, filepath.Join(config, strings.ToLower(desktopName)+"-mimeapps.list"))
		}
	}
	before := map[string][]byte{}
	for _, p := range paths {
		before[p], _ = os.ReadFile(p)
	}
	cleanup := sync.OnceFunc(func() {
		if listener != nil {
			_ = listener.Close()
		}
		for p, old := range before {
			now, err := os.ReadFile(p)
			if err == nil {
				restored := restoreAssociation(string(now), string(old), desktop)
				if restored != string(now) {
					if len(old) == 0 && strings.TrimSpace(restored) == "" {
						_ = os.Remove(p)
					} else {
						_ = os.WriteFile(p, []byte(restored), 0600)
					}
				}
			}
		}
		_ = os.Remove(file)
		_ = desktopCommand(context.Background(), "update-desktop-database", apps)
		_ = os.RemoveAll(dir)
		_ = unix.Flock(int(lock.Fd()), unix.LOCK_UN)
		_ = lock.Close()
	})
	fail := func(err error) (func(), error) { cleanup(); return nil, err }
	exe, err := os.Executable()
	if err != nil {
		return fail(err)
	}
	socket := filepath.Join(dir, "callback.sock")
	listener, err = net.Listen("unix", socket)
	if err != nil {
		return fail(err)
	}
	content := "[Desktop Entry]\nType=Application\nName=posthero sign-in\nNoDisplay=true\nTerminal=false\nMimeType=x-scheme-handler/postman;\nExec=" + desktopQuote(exe) + " --postman-callback " + desktopQuote(socket) + " %u\n"
	if err := os.WriteFile(file, []byte(content), 0600); err != nil {
		return fail(err)
	}
	if err := desktopCommand(ctx, "update-desktop-database", apps); err != nil {
		return fail(err)
	}
	if err := desktopCommand(ctx, "xdg-mime", "default", desktop, "x-scheme-handler/postman"); err != nil {
		return fail(err)
	}
	go func() {
		for {
			c, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				_ = c.SetDeadline(time.Now().Add(35 * time.Second))
				scanner := bufio.NewScanner(c)
				scanner.Buffer(make([]byte, 1024), 8192)
				if !scanner.Scan() {
					return
				}
				_ = complete(scanner.Text())
				_, _ = fmt.Fprint(c, address)
			}()
		}
	}()
	return cleanup, nil
}

func desktopCommand(ctx context.Context, name string, args ...string) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return exec.CommandContext(ctx, name, args...).Run()
}

// Exec has both desktop-file escaping and command argument quoting.
func desktopQuote(s string) string {
	s = strings.NewReplacer("\\", "\\\\", "\"", "\\\"", "`", "\\`", "$", "\\$", "%", "%%").Replace(s)
	s = "\"" + s + "\""
	return strings.NewReplacer("\\", "\\\\", "\n", "\\n", "\r", "\\r", "\t", "\\t").Replace(s)
}

func restoreAssociation(current, before, desktop string) string {
	const key = "x-scheme-handler/postman="
	old := map[string]string{}
	section := ""
	for _, line := range strings.Split(before, "\n") {
		if strings.HasPrefix(line, "[") {
			section = line
		}
		if strings.HasPrefix(line, key) {
			old[section] = line
		}
	}
	section = ""
	var lines []string
	for _, line := range strings.Split(current, "\n") {
		if strings.HasPrefix(line, "[") {
			section = line
		}
		if strings.HasPrefix(line, key) && strings.Contains(line, desktop) {
			if previous := old[section]; previous != "" {
				lines = append(lines, previous)
			}
			continue
		}
		lines = append(lines, line)
	}
	// xdg-mime may have made a section (or file) just for this handler.
	// Remove newly introduced empty sections, leaving existing ones alone.
	var restored []string
	for i := 0; i < len(lines); {
		if !strings.HasPrefix(lines[i], "[") {
			restored = append(restored, lines[i])
			i++
			continue
		}
		j := i + 1
		empty := true
		for j < len(lines) && !strings.HasPrefix(lines[j], "[") {
			if strings.TrimSpace(lines[j]) != "" {
				empty = false
			}
			j++
		}
		if !empty || strings.Contains(before, lines[i]) {
			restored = append(restored, lines[i:j]...)
		}
		i = j
	}
	return strings.Join(restored, "\n")
}
