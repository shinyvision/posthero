// Package editortest checks that an editor backend behaves the way posthero
// needs, the same for every backend.
package editortest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/shinyvision/posthero/internal/editor"
	"github.com/shinyvision/posthero/internal/editor/runtime"
)

// Start starts a backend.
type Start func(opts editor.Options, w, h int) (editor.Backend, error)

// Variables completes {{variable}} names.
func Variables(lines []string, row, col int) (int, []editor.Item) {
	before := lines[row][:col]
	at := strings.LastIndex(before, "{{")
	if at < 0 || strings.Contains(before[at:], "}}") {
		return -1, nil
	}
	var out []editor.Item
	for _, name := range []string{"BackendEndpoint", "Origin"} {
		if strings.HasPrefix(strings.ToLower(name), strings.ToLower(before[at+2:])) {
			out = append(out, editor.Item{Word: "{{" + name + "}}", Menu: "environment"})
		}
	}
	return at, out
}

type harness struct {
	t *testing.T
	b editor.Backend
	// backlog keeps events that came while waiting for something else.
	backlog []editor.Event
}

func (h *harness) screen() string { return ansi.Strip(strings.Join(h.b.Render(), "\n")) }

func (h *harness) wait(what string, match func(editor.Event) bool) editor.Event {
	h.t.Helper()
	for i, ev := range h.backlog {
		if match(ev) {
			h.backlog = append(h.backlog[:i:i], h.backlog[i+1:]...)
			return ev
		}
	}
	timeout := time.After(5 * time.Second)
	for {
		select {
		case ev := <-h.b.Events():
			if match(ev) {
				return ev
			}
			if _, redraw := ev.(editor.Redraw); !redraw {
				h.backlog = append(h.backlog, ev)
			}
		case <-timeout:
			h.t.Fatalf("timed out waiting for %s; screen:\n%s", what, h.screen())
		}
	}
}

func (h *harness) waitScreen(text string) {
	h.t.Helper()
	if strings.Contains(h.screen(), text) {
		return
	}
	timeout := time.After(5 * time.Second)
	for !strings.Contains(h.screen(), text) {
		select {
		case ev := <-h.b.Events():
			if _, redraw := ev.(editor.Redraw); !redraw {
				h.backlog = append(h.backlog, ev)
			}
		case <-timeout:
			h.t.Fatalf("timed out waiting for the screen to show %s; screen:\n%s", text, h.screen())
		}
	}
}

func (h *harness) typ(s string) {
	for _, r := range s {
		h.b.Key(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
}

func (h *harness) key(code rune, mod tea.KeyMod) { h.b.Key(tea.KeyPressMsg{Code: code, Mod: mod}) }

func file(t *testing.T, dir, name, text string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(text+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// Modal checks a dialog's editor: completing {{ while typing, and :w.
func Modal(t *testing.T, start Start) {
	dir := t.TempDir()
	f := file(t, dir, "value.posthero", "hello")
	b, err := start(editor.Options{File: f, Insert: "A", Complete: Variables}, 60, 12)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	h := &harness{t: t, b: b}
	h.waitScreen("hello")
	// Typing {{ opens the menu on its own without inserting anything; <C-y>
	// accepts the highlighted match.
	h.typ(" {{Back")
	h.waitScreen("environment")
	h.key('y', tea.ModCtrl)
	time.Sleep(200 * time.Millisecond)
	h.key(tea.KeyEscape, 0)
	b.Command("w")
	ev := h.wait("the write", func(ev editor.Event) bool { _, ok := ev.(editor.Written); return ok }).(editor.Written)
	if ev.Text != "hello {{BackendEndpoint}}" {
		t.Fatalf("written %q", ev.Text)
	}
}

// Main checks the request pane: Open with a draft, change reports, <CR>
// sending, :w, K, and ctrl-w window commands.
func Main(t *testing.T, start Start) {
	dir := t.TempDir()
	a := file(t, dir, "a.posthero", "GET http://a")
	bfile := file(t, dir, "b.posthero", "GET http://b")
	be, err := start(editor.Options{File: a, Main: true, Complete: Variables}, 60, 12)
	if err != nil {
		t.Fatal(err)
	}
	defer be.Close()
	h := &harness{t: t, b: be}
	h.waitScreen("GET http://a")

	// A change is reported with the text, as not yet written. Typing may
	// be reported a key at a time, so wait for all of it.
	h.typ("A/x")
	h.key(tea.KeyEscape, 0)
	h.wait("a change", func(ev editor.Event) bool {
		c, ok := ev.(editor.Changed)
		return ok && c.Modified && strings.HasSuffix(c.File, "a.posthero") && c.Text == "GET http://a/x"
	})

	// <CR> in normal mode sends what is in the buffer.
	h.key(tea.KeyEnter, 0)
	send := h.wait("the send", func(ev editor.Event) bool { _, ok := ev.(editor.Send); return ok }).(editor.Send)
	if strings.Join(send.Lines, "\n") != "GET http://a/x" || !strings.HasSuffix(send.File, "a.posthero") {
		t.Fatalf("sent %+v", send)
	}

	// Another request, with a draft: its text, not yet written.
	draft := "POST http://b\n\n{}"
	be.Open(bfile, &draft)
	h.waitScreen("POST http://b")
	h.wait("the draft's change", func(ev editor.Event) bool {
		c, ok := ev.(editor.Changed)
		return ok && strings.HasSuffix(c.File, "b.posthero") && c.Modified && c.Text == draft
	})

	// :w writes it.
	be.Command("w")
	w := h.wait("the write", func(ev editor.Event) bool { _, ok := ev.(editor.Written); return ok }).(editor.Written)
	if !strings.HasSuffix(w.File, "b.posthero") || w.Text != draft {
		t.Fatalf("written %+v", w)
	}

	// Back to the first, which kept its change.
	be.Open(a, nil)
	h.waitScreen("GET http://a/x")

	// K asks about what is under the cursor.
	h.typ("K")
	hv := h.wait("K", func(ev editor.Event) bool { _, ok := ev.(editor.Hover); return ok }).(editor.Hover)
	if hv.Key != "K" || !strings.HasSuffix(hv.File, "a.posthero") {
		t.Fatalf("hover %+v", hv)
	}

	// ctrl-w commands go to posthero.
	h.key('w', tea.ModCtrl)
	h.typ("h")
	wc := h.wait("the window command", func(ev editor.Event) bool { _, ok := ev.(editor.WinCmd); return ok }).(editor.WinCmd)
	if wc.Key != "h" {
		t.Fatalf("window command %q", wc.Key)
	}

	// Hints and Goto don't get in the way.
	be.SetHints(a, []editor.Hint{{Line: 0, Text: "= http://a", Kind: "value"}})
	be.Goto(0, 0)

	// A file changed on disk is read again when its buffer has no changes.
	if err := os.WriteFile(bfile, []byte("PUT http://b2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	be.Reload(bfile)
	be.Open(bfile, nil)
	h.waitScreen("PUT http://b2")

	// :q, :q! and :wq in a request go to posthero, which quits (after the
	// save, for :wq); the editor itself asks nothing and stays.
	for _, c := range []struct{ typed, want string }{{":q", "quit"}, {":q!", "quit!"}, {":wq", "writequit"}} {
		h.key(tea.KeyEscape, 0)
		h.typ(c.typed)
		h.key(tea.KeyEnter, 0)
		got := h.wait("posthero's "+c.want, func(ev editor.Event) bool {
			w, ok := ev.(editor.WinCmd)
			return ok && strings.HasPrefix(w.Key, "quit") || ok && strings.HasPrefix(w.Key, "writequit")
		}).(editor.WinCmd)
		if got.Key != c.want {
			t.Fatalf("%q sent %q", c.typed, got.Key)
		}
	}
}

// Sections checks posthero's filetype in a dialog: a line typed below a
// ### section line isn't turned into a # comment (which would switch it
// off).
func Sections(t *testing.T, start Start) {
	dir := t.TempDir()
	rt, err := runtime.Write(filepath.Join(dir, "runtime"))
	if err != nil {
		t.Fatal(err)
	}
	f := file(t, dir, "collection.posthero", "# notes\n### auth bearer")
	b, err := start(editor.Options{File: f, Runtime: rt}, 60, 12)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	h := &harness{t: t, b: b}
	h.waitScreen("### auth bearer")
	h.typ("Go")
	h.typ("token: x")
	h.key(tea.KeyEscape, 0)
	b.Command("w")
	ev := h.wait("the write", func(ev editor.Event) bool { _, ok := ev.(editor.Written); return ok }).(editor.Written)
	if ev.Text != "# notes\n### auth bearer\ntoken: x" {
		t.Fatalf("written %q", ev.Text)
	}
}

// Prompt checks that a question from the editor (one the user's config
// makes it ask, like 'confirm') doesn't lock posthero out: keys still
// answer it, and what posthero asks of the editor afterwards goes through.
func Prompt(t *testing.T, start Start) {
	dir := t.TempDir()
	a := file(t, dir, "a.posthero", "GET http://a")
	bfile := file(t, dir, "b.posthero", "GET http://b")
	be, err := start(editor.Options{File: a, Main: true}, 60, 12)
	if err != nil {
		t.Fatal(err)
	}
	defer be.Close()
	h := &harness{t: t, b: be}
	h.waitScreen("GET http://a")
	h.typ("Ax")
	h.key(tea.KeyEscape, 0)
	h.waitScreen("GET http://ax")
	be.Command("confirm bdelete")
	h.waitScreen("Save changes")
	h.typ("c") // Cancel
	be.Open(bfile, nil)
	h.waitScreen("GET http://b")
}

// Switching checks that typing in insert mode isn't lost when posthero
// switches requests (ctrl-h/j/k/l leave the pane still in insert mode): it
// is reported, and opening the request again with an older draft keeps
// what the buffer has.
func Switching(t *testing.T, start Start) {
	dir := t.TempDir()
	a := file(t, dir, "a.posthero", "GET http://a")
	bfile := file(t, dir, "b.posthero", "GET http://b")
	be, err := start(editor.Options{File: a, Main: true}, 60, 12)
	if err != nil {
		t.Fatal(err)
	}
	defer be.Close()
	h := &harness{t: t, b: be}
	h.waitScreen("GET http://a")
	h.typ("A/users/42") // and no <Esc>
	h.wait("the typing, reported", func(ev editor.Event) bool {
		c, ok := ev.(editor.Changed)
		return ok && strings.HasSuffix(c.File, "a.posthero") && c.Text == "GET http://a/users/42"
	})
	be.Open(bfile, nil)
	h.waitScreen("GET http://b")
	stale := "GET http://a/us"
	be.Open(a, &stale)
	h.waitScreen("GET http://a/users/42")
	time.Sleep(300 * time.Millisecond)
	if s := h.screen(); !strings.Contains(s, "GET http://a/users/42") {
		t.Fatalf("the typing was replaced by an older draft:\n%s", s)
	}
}

// Quitting checks ZZ and ZQ: in a request they go to posthero like :x and
// :q! (the editor stays); in a dialog without changes ZZ closes it without
// writing (which would submit it), as :xit does.
func Quitting(t *testing.T, start Start) {
	dir := t.TempDir()
	a := file(t, dir, "a.posthero", "GET http://a")
	be, err := start(editor.Options{File: a, Main: true}, 60, 12)
	if err != nil {
		t.Fatal(err)
	}
	defer be.Close()
	h := &harness{t: t, b: be}
	h.waitScreen("GET http://a")
	for _, c := range []struct{ typed, want string }{{"ZZ", "writequit"}, {"ZQ", "quit!"}} {
		h.typ(c.typed)
		got := h.wait("posthero's "+c.want, func(ev editor.Event) bool {
			switch ev.(type) {
			case editor.WinCmd, editor.Exited:
				return true
			}
			return false
		})
		if w, ok := got.(editor.WinCmd); !ok || w.Key != c.want {
			t.Fatalf("%s: %#v, want the window command %q", c.typed, got, c.want)
		}
	}

	for _, typed := range []string{"ZZ", ":xit\r", ":qall\r"} {
		f := file(t, dir, "value.md", "hello")
		d, err := start(editor.Options{File: f}, 60, 12)
		if err != nil {
			t.Fatal(err)
		}
		hd := &harness{t: t, b: d}
		hd.waitScreen("hello")
		for _, r := range typed {
			if r == '\r' {
				hd.key(tea.KeyEnter, 0)
			} else {
				hd.typ(string(r))
			}
		}
		ev := hd.wait("the dialog to close", func(ev editor.Event) bool {
			switch ev.(type) {
			case editor.Written, editor.Exited:
				return true
			}
			return false
		})
		if _, ok := ev.(editor.Exited); !ok {
			t.Fatalf("%q in a dialog without changes: %#v, want it closed without a write", typed, ev)
		}
		d.Close()
	}
}

// Rewritten checks that a request's file posthero rewrites (a teammate
// changed the request) is read again without a question, also when its
// buffer was changed and changed back (Vim's W12).
func Rewritten(t *testing.T, start Start) {
	dir := t.TempDir()
	a := file(t, dir, "a.posthero", "GET http://a")
	bfile := file(t, dir, "b.posthero", "GET http://b")
	be, err := start(editor.Options{File: a, Main: true}, 60, 12)
	if err != nil {
		t.Fatal(err)
	}
	defer be.Close()
	h := &harness{t: t, b: be}
	h.waitScreen("GET http://a")
	h.typ("Ax")
	h.key(tea.KeyBackspace, 0)
	h.key(tea.KeyEscape, 0)
	h.wait("the change back, reported", func(ev editor.Event) bool {
		c, ok := ev.(editor.Changed)
		// The built-in editor tells changes by the text alone.
		return ok && strings.HasSuffix(c.File, "a.posthero") && c.Text == "GET http://a" && (c.Modified || be.Name() == "builtin")
	})
	be.Open(bfile, nil)
	h.waitScreen("GET http://b")
	time.Sleep(50 * time.Millisecond) // a newer modification time
	if err := os.WriteFile(a, []byte("GET http://a2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	be.Reload(a)
	be.Open(a, nil)
	h.waitScreen("GET http://a2")
	time.Sleep(300 * time.Millisecond)
	if s := h.screen(); strings.Contains(s, "W12") || strings.Contains(s, "[O]K") || strings.Contains(s, "Press ENTER") {
		t.Fatalf("the editor asks about the rewritten file:\n%s", s)
	}
}
