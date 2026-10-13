package nvim

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/shinyvision/posthero/internal/editor"
	"github.com/shinyvision/posthero/internal/editor/editortest"
)

func start(t *testing.T) editortest.Start {
	if !Available() {
		t.Skip("nvim not installed")
	}
	t.Setenv("XDG_CONFIG_HOME", t.TempDir()) // no user config: predictable keys
	t.Setenv("XDG_STATE_HOME", t.TempDir())  // nor the user's shada
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	return func(opts editor.Options, w, h int) (editor.Backend, error) { return Start(opts, nil, w, h) }
}

func TestModal(t *testing.T) { editortest.Modal(t, start(t)) }

func TestMain(t *testing.T) { editortest.Main(t, start(t)) }

func TestQuitsWithoutUI(t *testing.T) {
	if !Available() {
		t.Skip("nvim not installed")
	}
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	file := filepath.Join(t.TempDir(), "c.md")
	if err := os.WriteFile(file, []byte("x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	b, err := Start(editor.Options{File: file}, nil, 40, 10)
	if err != nil {
		t.Fatal(err)
	}
	defer b.kill()
	_ = b.v.Close() // posthero went away without saying goodbye
	select {
	case <-b.done:
	case <-time.After(5 * time.Second):
		t.Fatal("nvim kept running without its UI")
	}
}
func TestSections(t *testing.T)  { editortest.Sections(t, start(t)) }
func TestPrompt(t *testing.T)    { editortest.Prompt(t, start(t)) }
func TestSwitching(t *testing.T) { editortest.Switching(t, start(t)) }
func TestQuitting(t *testing.T)  { editortest.Quitting(t, start(t)) }
func TestRewritten(t *testing.T) { editortest.Rewritten(t, start(t)) }

// userConfig starts Neovim with an init.lua of the user's.
func userConfig(t *testing.T, init string) {
	t.Helper()
	if !Available() {
		t.Skip("nvim not installed")
	}
	cfg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfg)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	if err := os.MkdirAll(filepath.Join(cfg, "nvim"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfg, "nvim", "init.lua"), []byte(init), 0o600); err != nil {
		t.Fatal(err)
	}
}

func screenOf(b *Backend) string { return ansi.Strip(strings.Join(b.Render(), "\n")) }

func waitScreen(t *testing.T, b *Backend, text string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(screenOf(b), text) {
		if time.Now().After(deadline) {
			t.Fatalf("the screen never showed %q:\n%s", text, screenOf(b))
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func drain(b *Backend) {
	go func() {
		for range b.Events() {
		}
	}()
}

// A config that says something at startup (a plugin's warning, lazy.nvim's
// notes on UIEnter) doesn't freeze posthero: Start returns at once, the
// "Press ENTER" shows in the pane, and a key answers it.
func TestStartupMessages(t *testing.T) {
	for name, init := range map[string]string{
		"UIEnter":   `vim.api.nvim_create_autocmd('UIEnter', { callback = function() vim.api.nvim_echo({{'plugin: one\ntwo\nthree'}}, true, {}) end })`,
		"scheduled": `vim.schedule(function() vim.notify('plugin: one\ntwo\nthree', vim.log.levels.WARN) end)`,
		"error":     `vim.schedule(function() error('plugin blew up') end)`,
	} {
		t.Run(name, func(t *testing.T) {
			userConfig(t, init)
			dir := t.TempDir()
			a := filepath.Join(dir, "a.posthero")
			b2 := filepath.Join(dir, "b.posthero")
			_ = os.WriteFile(a, []byte("GET http://a\n"), 0o600)
			_ = os.WriteFile(b2, []byte("GET http://b\n"), 0o600)
			began := time.Now()
			b, err := Start(editor.Options{File: a, Main: true}, nil, 60, 12)
			if err != nil {
				t.Fatal(err)
			}
			defer b.Close()
			if d := time.Since(began); d > time.Second {
				t.Fatalf("Start took %s", d)
			}
			exited := make(chan editor.Exited, 1)
			go func() {
				for ev := range b.Events() {
					if x, ok := ev.(editor.Exited); ok {
						exited <- x
					}
				}
			}()
			waitScreen(t, b, "Press ENTER")
			b.Key(tea.KeyPressMsg{Code: tea.KeyEnter})
			b.Open(b2, nil)
			waitScreen(t, b, "GET http://b")
			select {
			case x := <-exited:
				t.Fatalf("nvim stopped: %+v", x)
			default:
			}
		})
	}
}

// UIEnter fires for posthero's UI after VimEnter, as in a terminal:
// lazy.nvim loads its VeryLazy plugins on it.
func TestUIEnter(t *testing.T) {
	userConfig(t, `vim.g.seen = ''
vim.api.nvim_create_autocmd('VimEnter', { callback = function() vim.g.seen = vim.g.seen .. 'VimEnter ' end })
vim.api.nvim_create_autocmd('UIEnter', { callback = function() vim.g.seen = vim.g.seen .. 'UIEnter(' .. #vim.api.nvim_list_uis() .. ') ' end })`)
	f := filepath.Join(t.TempDir(), "a.posthero")
	_ = os.WriteFile(f, []byte("GET http://a\n"), 0o600)
	b, err := Start(editor.Options{File: f, Main: true}, nil, 60, 12)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	drain(b)
	waitScreen(t, b, "GET http://a")
	b.Command("echo 'seen: ' . g:seen")
	waitScreen(t, b, "seen: VimEnter UIEnter(1)")
}

// Closing doesn't wait on a Neovim that waits in a prompt.
func TestCloseDuringPrompt(t *testing.T) {
	userConfig(t, `vim.o.confirm = true`)
	f := filepath.Join(t.TempDir(), "a.posthero")
	_ = os.WriteFile(f, []byte("GET http://a\n"), 0o600)
	b, err := Start(editor.Options{File: f, Main: true}, nil, 60, 12)
	if err != nil {
		t.Fatal(err)
	}
	drain(b)
	waitScreen(t, b, "GET http://a")
	b.Key(tea.KeyPressMsg{Code: 'A', Text: "A"})
	b.Key(tea.KeyPressMsg{Code: 'x', Text: "x"})
	b.Key(tea.KeyPressMsg{Code: tea.KeyEscape})
	b.Command("confirm bdelete")
	waitScreen(t, b, "Save changes")
	began := time.Now()
	b.Close()
	if d := time.Since(began); d > 3*time.Second {
		t.Fatalf("Close took %s", d)
	}
	select {
	case <-b.done:
	default:
		t.Fatal("nvim still running after Close")
	}
}

// The request pane's Neovim goes too when posthero goes without closing it.
func TestMainQuitsWithoutUI(t *testing.T) {
	userConfig(t, ``)
	f := filepath.Join(t.TempDir(), "a.posthero")
	_ = os.WriteFile(f, []byte("GET http://a\n"), 0o600)
	b, err := Start(editor.Options{File: f, Main: true}, nil, 40, 10)
	if err != nil {
		t.Fatal(err)
	}
	defer b.kill()
	drain(b)
	waitScreen(t, b, "GET http://a")
	_ = b.v.Close()
	select {
	case <-b.done:
	case <-time.After(5 * time.Second):
		t.Fatal("nvim kept running without its UI")
	}
}

// posthero's temporary files leave no undo files in the user's undodir.
func TestNoUndoFiles(t *testing.T) {
	undo := t.TempDir()
	userConfig(t, `vim.o.undofile = true
vim.o.undodir = `+luaString(undo))
	dir := t.TempDir()
	for _, main := range []bool{true, false} {
		f := filepath.Join(dir, "a.posthero")
		_ = os.WriteFile(f, []byte("GET http://a\n"), 0o600)
		b, err := Start(editor.Options{File: f, Main: main}, nil, 40, 10)
		if err != nil {
			t.Fatal(err)
		}
		drain(b)
		waitScreen(t, b, "GET http://a")
		for _, r := range "Ax" {
			b.Key(tea.KeyPressMsg{Code: r, Text: string(r)})
		}
		b.Key(tea.KeyPressMsg{Code: tea.KeyEscape})
		b.Command("w")
		waitScreen(t, b, "written")
		b.Close()
	}
	if ents, _ := os.ReadDir(undo); len(ents) > 0 {
		t.Fatalf("undo files left: %v", ents)
	}
}
