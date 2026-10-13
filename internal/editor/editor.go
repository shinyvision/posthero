// Package editor defines the text editor backend contract. posthero edits
// requests (and variables, settings, cookies) in the user's own editor:
// implementations live in subpackages: nvim (embedded Neovim over
// msgpack-RPC), vimpty (Vim running in a pseudo terminal) and builtin
// (posthero's own vim engine).
package editor

import (
	tea "charm.land/bubbletea/v2"

	"github.com/shinyvision/posthero/internal/mouse"
)

// Options configure a backend instance.
type Options struct {
	// File is the file being edited. Its extension sets the filetype.
	File string
	// Insert starts in insert mode: "i" at the start of the buffer, "A" at
	// the end of its last line, "1A" at the end of the first line. Empty
	// starts in normal mode. A number followed by "A" or "i" ("3A") picks
	// the line.
	Insert string
	// Complete answers omni completion (<C-x><C-o>, and the {{ completion
	// posthero sets up). Nil offers nothing.
	Complete Completer
	// Main makes the editor posthero's request pane: <CR> in normal mode
	// sends, ctrl-w window commands go to posthero's panes, K and gd ask
	// about the variable under the cursor, changes are reported as they
	// happen, and Open switches between requests. Other editors (dialogs)
	// report writes, and quitting closes them.
	Main bool
	// Runtime is a directory to add to 'runtimepath': posthero's filetype
	// and syntax.
	Runtime string
	// ReadOnly opens the file without letting it be changed (a response).
	ReadOnly bool
}

// Completer completes the word before the cursor at the 0-based line row
// and byte column col: it returns the byte column where the completed text
// starts and the candidates, best first. A negative start means there is
// nothing to complete.
type Completer func(lines []string, row, col int) (start int, items []Item)

// Item is a completion candidate.
type Item struct {
	// Word is the text that replaces what is being completed.
	Word string
	// Abbr is shown in the menu instead of Word when set.
	Abbr string
	// Menu is a short description shown next to it, e.g. "environment".
	Menu string
	// Info is a longer description.
	Info string
}

// Hint is a note shown after the end of a line, like a variable's value.
type Hint struct {
	// Line is 0-based.
	Line int
	Text string
	// Kind is "value", "missing" (a variable nobody set) or "note".
	Kind string
}

// Backend is an embedded editor.
type Backend interface {
	// Name identifies the backend ("nvim", "vim", "builtin").
	Name() string
	// Key forwards a key press.
	Key(tea.KeyPressMsg)
	// Paste forwards bracketed-paste text.
	Paste(string)
	// Mouse forwards a mouse event in editor cells, within its size.
	Mouse(mouse.Event)
	// Command runs an ex command such as "w" or "q!", as if typed after ":"
	// in normal mode.
	Command(string)
	// Resize changes the editor's size in cells.
	Resize(w, h int)
	// Render returns exactly h lines, each w cells wide, with ANSI styling.
	Render() []string
	// Cursor returns the cursor cell and shape; ok is false when hidden.
	Cursor() (x, y int, shape tea.CursorShape, ok bool)
	// Mode returns a short mode name for the status bar ("NORMAL", ...).
	Mode() string
	// Events delivers the events below.
	Events() <-chan Event
	// Close shuts the editor down.
	Close()
	// Open switches to another file (Main editors). With text, the buffer
	// holds that text instead, not yet written: a draft. A buffer already
	// open keeps its changes and undo history.
	Open(file string, text *string)
	// Reload reads a file again from disk when its buffer has no changes.
	Reload(file string)
	// SetHints shows notes after lines of a file, replacing earlier ones.
	SetHints(file string, hints []Hint)
	// Goto moves the cursor to a 0-based line and byte column.
	Goto(line, col int)
}

// Event is sent by a backend.
type Event interface{ isEvent() }

// Redraw asks the UI to re-render the editor.
type Redraw struct{}

// Written reports that a buffer was written (:w).
type Written struct {
	File string
	Text string
}

// Exited reports that the editor process ended (e.g. after :q). Early says
// it ended before it was ready: it couldn't start, and another editor
// should be tried.
type Exited struct {
	Err   error
	Early bool
}

// Send asks to send the request in a buffer: its lines and the cursor
// (0-based line, byte column).
type Send struct {
	File      string
	Lines     []string
	Line, Col int
}

// WinCmd is a ctrl-w window command typed in the editor, e.g. "o" for
// <C-w>o; posthero's panes are the windows.
type WinCmd struct{ Key string }

// Changed reports a buffer's text after a change, and whether it differs
// from the file.
type Changed struct {
	File     string
	Text     string
	Modified bool
}

// Hover asks about what is under the cursor: K (explain) or gd (go to its
// definition).
type Hover struct {
	File      string
	Lines     []string
	Line, Col int
	Key       string
}

func (Redraw) isEvent()  {}
func (Exited) isEvent()  {}
func (Written) isEvent() {}
func (Send) isEvent()    {}
func (WinCmd) isEvent()  {}
func (Changed) isEvent() {}
func (Hover) isEvent()   {}

// Notify sends an event without blocking; redraws are coalesced.
func Notify(ch chan Event, ev Event) {
	if _, ok := ev.(Redraw); ok {
		select {
		case ch <- ev:
		default:
		}
		return
	}
	ch <- ev
}

// OmniResult runs a Completer for Vim's omnifunc protocol: the start column
// and the candidates as complete-items. -3 cancels completion silently.
func OmniResult(c Completer, lines []string, row, col int) []any {
	if c == nil || row < 0 || row >= len(lines) {
		return []any{-3, []any{}}
	}
	col = max(0, min(col, len(lines[row])))
	start, items := c(lines, row, col)
	if start < 0 {
		return []any{-3, []any{}}
	}
	out := make([]any, 0, len(items))
	for _, it := range items {
		item := map[string]any{"word": it.Word, "menu": it.Menu, "info": it.Info, "icase": 1, "dup": 1}
		if it.Abbr != "" {
			item["abbr"] = it.Abbr
		}
		out = append(out, item)
	}
	return []any{min(start, col), out}
}
