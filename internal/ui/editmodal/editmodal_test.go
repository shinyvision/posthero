package editmodal

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/shinyvision/posthero/internal/editor"
	"github.com/shinyvision/posthero/internal/mouse"
)

func clickLabel(t *testing.T, m *Model, label string) {
	t.Helper()
	box, _, _, _, _ := m.View(100, 30)
	lines := strings.Split(ansi.Strip(box), "\n")
	last := lines[len(lines)-1]
	i := strings.LastIndex(last, label)
	if i < 0 {
		t.Fatalf("no %q in %q", label, last)
	}
	m.Mouse(mouse.Event{Action: mouse.Press, Button: tea.MouseLeft, X: ansi.StringWidth(last[:i]), Y: len(lines) - 1, Clicks: 1})
}

func TestButtons(t *testing.T) {
	t.Setenv("POSTHERO_EDITOR", "builtin")
	var got string
	m, wait := New(Request{Title: "t", Hint: "text · :w save · :q cancel", Text: "hello", Submit: func(text string) tea.Cmd {
		got = text
		return nil
	}}, nil, 100, 30)
	defer m.Close()

	clickLabel(t, m, "save")
	msg := wait()
	if ev, ok := msg.(eventMsg); !ok || ev.ev.(editor.Written).Text != "hello" {
		t.Fatalf("save sent %#v", msg)
	}
	m.Update(msg)
	if got != "hello" {
		t.Fatalf("submitted %q", got)
	}
	m.Update(ResultMsg{Err: errTest{}}) // keep editing

	clickLabel(t, m, "cancel")
	if _, closed := m.Update(m.wait()()); !closed {
		t.Fatal("cancel should close the editor")
	}
}

// :w saves and the editor stays, as in vim; :q closes it.
func TestWriteStays(t *testing.T) {
	t.Setenv("POSTHERO_EDITOR", "builtin")
	var submitted []string
	m, wait := New(Request{Title: "t", Text: "hello", Submit: func(text string) tea.Cmd {
		submitted = append(submitted, text)
		return nil // the result is delivered by hand below
	}}, nil, 100, 30)
	defer m.Close()
	for _, r := range ":w\r" {
		if r == '\r' {
			m.Key(tea.KeyPressMsg{Code: tea.KeyEnter})
		} else {
			m.Key(tea.KeyPressMsg{Code: r, Text: string(r)})
		}
	}
	if _, closed := m.Update(wait()); closed || len(submitted) != 1 {
		t.Fatalf("after :w: closed %v, submitted %q", closed, submitted)
	}
	// The save worked: still open.
	if _, closed := m.Update(ResultMsg{}); closed {
		t.Fatal(":w closed the editor")
	}
	for _, r := range ":q\r" {
		if r == '\r' {
			m.Key(tea.KeyPressMsg{Code: tea.KeyEnter})
		} else {
			m.Key(tea.KeyPressMsg{Code: r, Text: string(r)})
		}
	}
	if _, closed := m.Update(m.wait()()); !closed {
		t.Fatal(":q didn't close the editor")
	}
}

type errTest struct{}

func (errTest) Error() string { return "no" }

// A write made while the previous one is still saving is saved after it,
// through the first result's Again, and the editor closes after that.
func TestWriteDuringSave(t *testing.T) {
	t.Setenv("POSTHERO_EDITOR", "builtin")
	var submitted, again []string
	m, wait := New(Request{Title: "t", Text: "hello", Submit: func(text string) tea.Cmd {
		submitted = append(submitted, text)
		return func() tea.Msg { return nil } // the result is delivered by hand below
	}}, nil, 100, 30)
	defer m.Close()
	press := func(s string) {
		for _, r := range s {
			switch r {
			case '\x1b':
				m.Key(tea.KeyPressMsg{Code: tea.KeyEscape})
			case '\r':
				m.Key(tea.KeyPressMsg{Code: tea.KeyEnter})
			default:
				m.Key(tea.KeyPressMsg{Code: r, Text: string(r)})
			}
		}
	}
	press(":w\r")
	if _, closed := m.Update(wait()); closed {
		t.Fatal("closed on the write")
	}
	// The first save is in flight; fix a typo and :wq.
	press("A world\x1b:wq\r")
	for {
		msg := m.wait()()
		if _, closed := m.Update(msg); closed {
			t.Fatal("closed before the save finished")
		}
		if _, ok := msg.(eventMsg).ev.(editor.Exited); ok {
			break
		}
	}
	// The first save's result arrives now.
	cmd, closed := m.Update(ResultMsg{from: m, Again: func(text string) tea.Cmd {
		again = append(again, text)
		return func() tea.Msg { return ResultMsg{} }
	}})
	if closed || cmd == nil {
		t.Fatalf("the later write wasn't saved: closed=%v", closed)
	}
	if len(submitted) != 1 || len(again) != 1 || again[0] != "hello world" {
		t.Fatalf("submitted %q, again %q", submitted, again)
	}
	if _, closed := m.Update(ResultMsg{from: m}); !closed {
		t.Fatal("should close once everything is saved")
	}
}
