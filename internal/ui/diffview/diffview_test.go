package diffview

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

func tabs() []*Tab {
	return []*Tab{
		{Key: "a", Name: "Shop / Get product", What: "changed here and in Postman", Base: "GET /a", Mine: "PUT /a", Theirs: "PATCH /a"},
		{Key: "b", Name: "Shop / Health", What: "deleted here, changed in Postman", Base: "GET /h", Theirs: "HEAD /h"},
		{Key: "c", Name: "Shop · Local", What: "changed here and in Postman", Base: "x: 1", Mine: "x: 2", Theirs: "x: 3"},
	}
}

func typ(v *View, keys ...string) (bool, tea.Cmd) {
	var done bool
	var cmd tea.Cmd
	for _, k := range keys {
		done, cmd = v.Key(k)
	}
	return done, cmd
}

func screen(v *View) string { return ansi.Strip(v.View(120, 40)) }

func TestTabs(t *testing.T) {
	v := New(tabs())
	if s := screen(v); !strings.Contains(s, "1 Get product") || !strings.Contains(s, "- PATCH /a") || !strings.Contains(s, "+ PUT /a") {
		t.Fatalf("first tab:\n%s", s)
	}
	typ(v, "<Tab>")
	if v.Current().Key != "b" {
		t.Fatal("tab didn't go to the next")
	}
	typ(v, "<S-Tab>", "<S-Tab>")
	if v.Current().Key != "c" {
		t.Fatal("shift-tab didn't go back around")
	}
}

// :q closes a tab and moves on; q asks first; :qq closes all, Q asks first.
func TestClosing(t *testing.T) {
	v := New(tabs())
	if done, _ := typ(v, ":", "q", "<CR>"); done || v.Len() != 2 || v.Current().Key != "b" {
		t.Fatalf(":q: done %v, %d left, at %s", done, v.Len(), v.Current().Key)
	}
	typ(v, "q")
	if !strings.Contains(screen(v), "Close this diff?") {
		t.Fatalf("q didn't ask:\n%s", screen(v))
	}
	typ(v, "n")
	if v.Len() != 2 {
		t.Fatal("q then n closed it")
	}
	typ(v, "q", "y")
	if v.Len() != 1 || v.Current().Key != "c" {
		t.Fatal("q then y didn't close it")
	}
	typ(v, "Q")
	if !strings.Contains(screen(v), "Close all 1 diffs?") {
		t.Fatalf("Q didn't ask:\n%s", screen(v))
	}
	if done, _ := typ(v, "n"); done {
		t.Fatal("Q then n closed them")
	}
	if done, _ := typ(v, "Q", "y"); !done {
		t.Fatal("Q then y didn't close them")
	}
	if done, _ := typ(New(tabs()), ":", "q", "q", "<CR>"); !done {
		t.Fatal(":qq didn't close them")
	}
}

func TestPushAndMerge(t *testing.T) {
	v := New(tabs())
	_, cmd := typ(v, ":", "w", "<CR>")
	if cmd == nil {
		t.Fatal(":w asked for nothing")
	}
	if m, ok := cmd().(PushMsg); !ok || m.Tab.Key != "a" || !m.Tab.Busy {
		t.Fatalf(":w: %#v", cmd())
	}
	_, cmd = typ(v, "c")
	if m, ok := cmd().(EditMsg); !ok || m.Tab.Key != "a" {
		t.Fatalf("c: %#v", cmd())
	}
	if text := v.Current().MergeText(); !strings.Contains(text, "<<<<<<< yours\nPUT /a\n=======\nPATCH /a\n>>>>>>> Postman") {
		t.Fatalf("merge text: %q", text)
	}
	// Merged, with markers left: :w says so.
	tab := v.tabs[2]
	tab.Mine, tab.Merged = "<<<<<<< yours\nx: 2\n=======\nx: 3\n>>>>>>> Postman", true
	v.Changed(tab)
	v.cur = 2
	if _, cmd := typ(v, ":", "w", "<CR>"); cmd != nil || !strings.Contains(screen(v), "conflict markers left") {
		t.Fatalf(":w with markers:\n%s", screen(v))
	}
	// Nothing to merge where one side deleted it.
	v.cur = 1
	if _, cmd := typ(v, "c"); cmd != nil || !strings.Contains(screen(v), "nothing to merge") {
		t.Fatalf("c on a deletion:\n%s", screen(v))
	}
}
