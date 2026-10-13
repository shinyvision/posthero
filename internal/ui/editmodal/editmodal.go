// Package editmodal opens an editor (Neovim, Vim or the built-in engine) in
// a dialog. Writing the buffer (:w) submits it; quitting without a write
// (:q) cancels. posthero uses it for descriptions, comments and whole issues.
package editmodal

import (
	"os"
	"path/filepath"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/shinyvision/posthero/internal/editor"
	"github.com/shinyvision/posthero/internal/editor/launch"
	"github.com/shinyvision/posthero/internal/mouse"
	"github.com/shinyvision/posthero/internal/ui/modal"
	"github.com/shinyvision/posthero/internal/ui/theme"
	"github.com/shinyvision/posthero/internal/vim"
)

// Request describes what to edit and what writing it does.
type Request struct {
	Title  string
	Hint   string // shown in the bottom border
	Text   string
	Ext    string // file extension, for filetype detection (".md")
	Insert string // see editor.Options.Insert
	// Complete offers completions (mentions, field values).
	Complete editor.Completer
	// Name is the file's base name without extension; "value" when empty.
	Name string
	// Tall makes the dialog take most of the screen, for writing prose.
	Tall bool
	// Runtime is posthero's runtime directory (its filetype and syntax),
	// see editor.Options.Runtime.
	Runtime string
	// Submit handles a write. Its command must produce a ResultMsg.
	Submit func(text string) tea.Cmd
}

// ResultMsg is the outcome of Submit. On success Then is delivered, and the
// editor stays open as vim does after :w (it closes after :wq); on failure
// the error is shown and editing continues (and a Then, for what was saved
// before the failure, is delivered too).
type ResultMsg struct {
	Err  error
	Then tea.Msg
	// Again saves a later write of the same editor, after this one worked
	// (a new comment is then edited, not posted twice). Without it, later
	// writes go to Submit again.
	Again func(text string) tea.Cmd
	from  *Model // the editor that submitted, so a late result can't close another
}

// FromEditor reports whether an editor's Submit produced the result (else
// it is a save that used the same message without an editor).
func (r ResultMsg) FromEditor() bool { return r.from != nil }

// Fail returns a command reporting err for a Submit.
func Fail(err error) tea.Cmd { return func() tea.Msg { return ResultMsg{Err: err} } }

// OpenMsg asks the app to open a modal editor.
type OpenMsg struct{ Request Request }

// Open returns a command opening r.
func Open(r Request) tea.Cmd { return func() tea.Msg { return OpenMsg{r} } }

type eventMsg struct {
	m  *Model
	ev editor.Event
}

// Model is an open modal editor.
type Model struct {
	req     Request
	regs    *vim.Registers
	dir     string
	backend editor.Backend
	pending bool // a write is being submitted
	exited  bool
	text    string // last written text
	sent    string // the text being submitted
	w, h    int
	save    [2]int // x range of the clickable "save" in the bottom border
	cancel  [2]int
	skip    []string // editors that stopped while starting
}

// New starts an editor for r sized for a screen of sw×sh cells. When the
// editor can't start, the model is nil and the command shows why.
func New(r Request, regs *vim.Registers, sw, sh int) (*Model, tea.Cmd) {
	m := &Model{req: r, regs: regs, text: r.Text}
	m.size(sw, sh)
	cmd := m.start(r.Insert)
	if m.backend == nil {
		m.cleanup()
		return nil, cmd
	}
	return m, cmd
}

func (m *Model) size(sw, sh int) {
	lines := strings.Count(m.text, "\n") + 1
	m.w = max(30, min(sw-8, 110))
	m.h = max(8, min(sh-4, lines+6))
	if m.req.Tall {
		m.h = max(m.h, min(sh-4, sh*3/4))
	}
}

func (m *Model) start(insert string) tea.Cmd {
	if m.dir == "" {
		dir, err := os.MkdirTemp("", "posthero-edit-")
		if err != nil {
			return modal.Error("Could not open the editor", err.Error())
		}
		m.dir = dir
	}
	name := m.req.Name
	if name == "" {
		name = "value"
	}
	file := filepath.Join(m.dir, name+m.req.Ext)
	if err := os.WriteFile(file, []byte(m.text+"\n"), 0o600); err != nil {
		return modal.Error("Could not open the editor", err.Error())
	}
	m.exited = false
	m.backend, _ = launch.Start(editor.Options{File: file, Insert: insert, Complete: m.req.Complete, Runtime: m.req.Runtime}, m.regs, m.w-2, m.h-2, m.skip...)
	if m.backend == nil {
		return modal.Error("Could not open the editor", "no editor could start")
	}
	return m.wait()
}

// send submits the last written text.
func (m *Model) send() tea.Cmd {
	m.pending, m.sent = true, m.text
	return m.submit(m.text)
}

// submit hands written text to the request, marking the result as this
// editor's.
func (m *Model) submit(text string) tea.Cmd {
	cmd := m.req.Submit(text)
	if cmd == nil {
		m.pending = false
		return nil
	}
	return func() tea.Msg { return m.tag(cmd()) }
}

// Wrapped is a message carrying another, such as the app's note that work
// in the background is done: a result inside one is still this editor's.
type Wrapped interface {
	Inner() tea.Msg
	WithInner(tea.Msg) tea.Msg
}

// tag marks a result (also one wrapped in another message) as this
// editor's.
func (m *Model) tag(msg tea.Msg) tea.Msg {
	switch x := msg.(type) {
	case ResultMsg:
		x.from = m
		return x
	case Wrapped:
		return x.WithInner(m.tag(x.Inner()))
	}
	return msg
}

func (m *Model) wait() tea.Cmd {
	ch, self := m.backend.Events(), m
	return func() tea.Msg { return eventMsg{m: self, ev: <-ch} }
}

// Update handles editor events and submit results. closed reports that the
// modal is done and should be removed.
func (m *Model) Update(msg tea.Msg) (cmd tea.Cmd, closed bool) {
	switch msg := msg.(type) {
	case eventMsg:
		if msg.m != m {
			return nil, false
		}
		switch ev := msg.ev.(type) {
		case editor.Written:
			m.text = ev.Text
			if m.pending {
				return m.wait(), false // saved when the save in progress is done
			}
			return tea.Batch(m.wait(), m.send()), false
		case editor.Exited:
			if ev.Early && !m.pending {
				// The editor couldn't start: the next one.
				m.skip = append(m.skip, m.backend.Name())
				m.backend.Close()
				cmd := m.start(m.req.Insert)
				if m.backend == nil {
					m.cleanup()
					return cmd, true
				}
				return cmd, false
			}
			m.exited = true
			if !m.pending {
				m.backend.Close()
				m.cleanup()
				return nil, true
			}
			return nil, false
		}
		return m.wait(), false
	case ResultMsg:
		if msg.from != nil && msg.from != m {
			return nil, false // for an editor that is gone
		}
		m.pending = false
		later := m.text != m.sent // written again while this was saving
		if msg.Err != nil {
			if later {
				cmd := m.send() // the newer text is what counts
				if msg.Then != nil {
					then := msg.Then
					cmd = tea.Batch(cmd, func() tea.Msg { return then })
				}
				return cmd, false
			}
			show := modal.Error("Not saved", msg.Err.Error())
			if msg.Then != nil {
				// Part of it was saved: that part is reported too.
				then := msg.Then
				show = tea.Batch(show, func() tea.Msg { return then })
			}
			if m.exited {
				// :wq closed the editor; reopen it with what was written.
				m.backend.Close()
				cmd := m.start("")
				if m.backend == nil {
					m.cleanup()
					return tea.Batch(cmd, show), true
				}
				return tea.Batch(cmd, show), false
			}
			return show, false
		}
		var then tea.Cmd
		if msg.Then != nil {
			then = func() tea.Msg { return msg.Then }
		}
		if msg.Again != nil {
			m.req.Submit = msg.Again
		}
		if later {
			return tea.Batch(then, m.send()), false
		}
		if !m.exited {
			// As in vim, :w saves and the editor stays; :q or :wq closes it.
			return then, false
		}
		m.backend.Close()
		m.cleanup()
		return then, true
	}
	return nil, false
}

// Key forwards a key press to the editor.
func (m *Model) Key(k tea.KeyPressMsg) { m.backend.Key(k) }

// Paste forwards pasted text.
func (m *Model) Paste(s string) { m.backend.Paste(s) }

// Mouse handles a mouse event relative to the dialog's top-left corner:
// "save" and "cancel" in the bottom border write or abandon the buffer, and
// everything inside goes to the editor.
func (m *Model) Mouse(ev mouse.Event) {
	if ev.Action == mouse.Press && ev.Button == tea.MouseLeft && ev.Y == m.h-1 {
		switch {
		case ev.X >= m.save[0] && ev.X < m.save[1]:
			m.backend.Command("w")
		case ev.X >= m.cancel[0] && ev.X < m.cancel[1]:
			m.backend.Command("qa!")
		}
		return
	}
	ev = ev.Offset(1, 1)
	iw, ih := m.w-2, m.h-2
	if ev.Action == mouse.Press && !ev.In(iw, ih) {
		return
	}
	m.backend.Mouse(ev.Clamp(iw, ih))
}

// Close shuts the editor down (when posthero quits).
func (m *Model) Close() {
	m.backend.Close()
	m.cleanup()
}

func (m *Model) cleanup() {
	if m.dir != "" {
		_ = os.RemoveAll(m.dir)
	}
}

// View renders the dialog for a screen of sw×sh cells and returns the
// cursor position inside it.
func (m *Model) View(sw, sh int) (box string, cx, cy int, cursor bool, shape tea.CursorShape) {
	w, h := max(30, min(sw-8, 110)), m.h
	h = max(8, min(sh-4, h))
	if w != m.w || h != m.h {
		m.w, m.h = w, h
		m.backend.Resize(w-2, h-2)
	}
	hint := m.req.Hint
	m.save, m.cancel = [2]int{}, [2]int{}
	if m.pending {
		hint = "saving…"
	} else {
		hint = m.buttons(hint, w)
	}
	box = theme.Box(m.req.Title, hint, m.backend.Render(), w, h, true)
	x, y, shape, ok := m.backend.Cursor()
	return box, x + 1, y + 1, ok, shape
}

// buttons appends clickable "save" and "cancel" to the border hint and
// records where they are; a hint too long for the border drops them.
func (m *Model) buttons(hint string, w int) string {
	const start = 3 // "╰─ "
	room := w - 2 - 4
	prefix := hint + " │ "
	if hint == "" {
		prefix = ""
	}
	x := start + ansi.StringWidth(prefix)
	if x-start+len("save · cancel") > room {
		return hint
	}
	m.save = [2]int{x, x + 4}
	m.cancel = [2]int{x + 7, x + 13}
	return prefix + theme.AccentFg.Render("save") + " · " + theme.AccentFg.Render("cancel")
}
