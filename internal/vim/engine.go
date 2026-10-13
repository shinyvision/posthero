// Package vim is a self-contained modal text editing engine: normal, insert,
// replace, visual and visual-line modes, operators with motions and text
// objects, counts, registers, undo/redo, dot repeat, macros, marks, search
// and a small ex command line.
//
// It powers every text input in posthero (prompts, forms, pickers) and is the
// editor when neither Neovim nor Vim is installed. Keys are fed in Vim
// notation, see package keys.
package vim

import "strings"

// Mode is the editing mode.
type Mode int

// Modes.
const (
	Normal Mode = iota
	Insert
	Replace
	Visual
	VisualLine
	Cmdline
)

func (m Mode) String() string {
	return [...]string{"NORMAL", "INSERT", "REPLACE", "VISUAL", "V-LINE", "COMMAND"}[m]
}

// ActionKind tells the host what a key press asks of it.
type ActionKind int

// Actions.
const (
	None ActionKind = iota
	// Submit is <CR> in normal mode.
	Submit
	// SubmitSelection is <CR> in visual mode; Action.Text is the selection.
	SubmitSelection
	// Cancel is <Esc> in normal mode with nothing pending.
	Cancel
	// Ex is an ex command the engine does not handle itself (:w, :q, ...).
	Ex
	// Explain is g<CR> in normal mode: explain the statement under the
	// cursor instead of running it.
	Explain
	// Window is a ctrl-w window command; Action.Text is the key after
	// <C-w>, with <C-w><C-o> and <C-w><Left> style variants normalised to
	// "o" and "h".
	Window
)

// Action is returned from HandleKey.
type Action struct {
	Kind ActionKind
	Text string
}

// Engine is one editable buffer with vim semantics.
type Engine struct {
	buf    buffer
	cur    Pos
	mode   Mode
	anchor Pos // visual selection start
	want   int // desired column for vertical moves; -1 means end of line
	regs   *Registers

	pending []string

	undo, redo []snapshot
	changing   bool

	dot              []string // last change without its count, see setDot
	dotCount         int
	dotVisual        visualDot
	dotPending       []string
	dotPendingVisual visualDot
	recordingDot     bool
	insertRepeat     int
	insertCmd        string
	insertKeys       []string
	insertTyped      []string     // keys typed in the current insert, for counts
	replaced         []replacedCh // what replace mode overwrote, for <BS>
	scrollLines      int          // 'scroll': lines <C-d> and <C-u> move; 0 is half a screen

	pendingInsertReg bool

	macroReg  rune
	macroKeys []string
	macros    map[rune][]string
	lastMacro rune
	replaying int

	marks      map[rune]Pos
	lastFind   findSpec
	lastSearch searchSpec

	cmd        []rune
	cmdKind    byte
	cmdVisual  Mode // the visual mode a / or ? was typed in, else Normal
	lastVisual struct {
		anchor, cur Pos
		mode        Mode
	}

	top, height int
	msg         string

	// SingleLine stops <CR> in insert mode from splitting lines; it becomes a
	// Submit instead. Used for prompts and form fields.
	SingleLine bool
}

// New returns an engine holding text, in normal mode at the start.
func New(text string, regs *Registers) *Engine {
	if regs == nil {
		regs = NewRegisters()
	}
	return &Engine{
		buf:    newBuffer(text),
		regs:   regs,
		marks:  map[rune]Pos{},
		macros: map[rune][]string{},
		height: 1,
	}
}

// Text returns the whole buffer.
func (e *Engine) Text() string { return e.buf.text() }

// SetText replaces the buffer, keeping undo history.
func (e *Engine) SetText(text string) {
	e.beginChange()
	e.buf = newBuffer(text)
	e.endChange()
	e.clampCursor()
}

// Lines returns the buffer lines.
func (e *Engine) Lines() []string {
	out := make([]string, len(e.buf.lines))
	for i, l := range e.buf.lines {
		out[i] = string(l)
	}
	return out
}

// Cursor returns the cursor position.
func (e *Engine) Cursor() Pos { return e.cur }

// SetCursor moves the cursor, clamped to the buffer.
func (e *Engine) SetCursor(p Pos) {
	e.cur = p
	e.clampCursor()
	e.want = e.cur.Col
}

// Mode returns the current mode.
func (e *Engine) Mode() Mode { return e.mode }

// StartInsert switches to insert mode at the end of the text, as if A had
// been pressed. Used by prompts that open ready for typing.
func (e *Engine) StartInsert() {
	e.cur = Pos{e.buf.last(), e.buf.lineLen(e.buf.last())}
	e.enterInsert(Insert)
}

// Selection returns the visual selection; ok is false outside visual modes.
// A search typed in visual mode keeps the selection.
func (e *Engine) Selection() (start, end Pos, linewise, ok bool) {
	mode := e.mode
	if mode == Cmdline {
		mode = e.cmdVisual
	}
	if mode != Visual && mode != VisualLine {
		return Pos{}, Pos{}, false, false
	}
	s, en := order(e.anchor, e.cur)
	return s, en, mode == VisualLine, true
}

// Pending returns keys typed for an incomplete command, like vim's showcmd.
func (e *Engine) Pending() string { return strings.Join(e.pending, "") }

// CommandLine returns the command line being typed (":%s/a/b", "/foo") and
// whether one is active.
func (e *Engine) CommandLine() (string, bool) {
	if e.mode != Cmdline {
		return "", false
	}
	return string(e.cmdKind) + string(e.cmd), true
}

// Message returns the last message (errors such as "Pattern not found").
func (e *Engine) Message() string { return e.msg }

// Recording returns the register a macro is being recorded into, or 0.
func (e *Engine) Recording() rune { return e.macroReg }

// SetHeight sets the number of visible lines, used for scrolling commands.
func (e *Engine) SetHeight(h int) {
	e.height = max(1, h)
	e.scrollToCursor()
}

// Top returns the first visible line.
func (e *Engine) Top() int { return e.top }

// HandleKey feeds one key in Vim notation.
func (e *Engine) HandleKey(key string) Action {
	if e.macroReg != 0 && e.replaying == 0 {
		e.macroKeys = append(e.macroKeys, key)
	}
	if e.recordingDot && e.replaying == 0 {
		e.insertKeys = append(e.insertKeys, key)
	}
	if e.mode == Insert || e.mode == Replace {
		e.insertTyped = append(e.insertTyped, key)
	}
	e.msg = ""
	var act Action
	switch e.mode {
	case Insert, Replace:
		act = e.insertKey(key)
	case Visual, VisualLine:
		act = e.visualKey(key)
	case Cmdline:
		act = e.cmdlineKey(key)
	default:
		act = e.normalKey(key)
	}
	e.clampCursor()
	e.scrollToCursor()
	return act
}

// Replace swaps the text from from up to to (exclusive) for text and puts
// the cursor after it. Insert-mode completion uses it; in insert mode it
// belongs to the insert's undo step.
func (e *Engine) Replace(from, to Pos, text string) {
	editing := e.mode == Insert || e.mode == Replace
	if !editing {
		e.beginChange()
	}
	if to.Before(from) {
		from, to = to, from
	}
	e.buf.remove(from, to)
	e.cur = e.buf.insert(from, text)
	if !editing {
		e.endChange()
	}
	e.clampCursor()
	e.scrollToCursor()
}

// CenterCursor scrolls so the cursor line is in the middle, like zz.
func (e *Engine) CenterCursor() {
	e.scroll("zz", 0)
	e.top = clamp(e.top, 0, max(0, e.buf.last()-e.height+1))
}

// Paste inserts text as a bracketed paste would.
func (e *Engine) Paste(text string) {
	switch e.mode {
	case Insert, Replace:
		if e.SingleLine {
			text = strings.ReplaceAll(text, "\n", " ")
		}
		e.cur = e.buf.insert(e.cur, text)
	case Cmdline:
		e.cmd = append(e.cmd, []rune(strings.ReplaceAll(text, "\n", " "))...)
	default:
		e.beginChange()
		p := e.cur
		if e.buf.lineLen(p.Line) > 0 {
			p.Col++
		}
		end := e.buf.insert(p, text)
		e.cur, _ = e.buf.prev(end)
		e.endChange()
	}
	e.clampCursor()
	e.scrollToCursor()
}

func (e *Engine) clampCursor() {
	e.cur.Line = clamp(e.cur.Line, 0, e.buf.last())
	n := e.buf.lineLen(e.cur.Line)
	limit := n - 1
	if e.mode == Insert || e.mode == Replace || e.mode == Visual || e.mode == Cmdline {
		limit = n
	}
	e.cur.Col = clamp(e.cur.Col, 0, max(0, limit))
}

const scrollOff = 2

func (e *Engine) scrollToCursor() {
	off := min(scrollOff, (e.height-1)/2)
	if e.cur.Line < e.top+off {
		e.top = e.cur.Line - off
	}
	if e.cur.Line > e.top+e.height-1-off {
		e.top = e.cur.Line - e.height + 1 + off
	}
	e.top = clamp(e.top, 0, max(0, e.buf.last()-e.height+1))
}

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// --- undo ---

type snapshot struct {
	lines [][]rune
	cur   Pos
}

func (e *Engine) beginChange() {
	if e.changing {
		return
	}
	e.changing = true
	e.undo = append(e.undo, snapshot{lines: e.buf.clone(), cur: e.cur})
	e.redo = nil
}

func (e *Engine) endChange() {
	if !e.changing {
		return
	}
	e.changing = false
	if last := e.undo[len(e.undo)-1]; sameLines(last.lines, e.buf.lines) {
		e.undo = e.undo[:len(e.undo)-1]
	}
}

func sameLines(a, b [][]rune) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if string(a[i]) != string(b[i]) {
			return false
		}
	}
	return true
}

// The cursor of an undo or redo entry is where the cursor was before the
// change was first made.
func (e *Engine) undoChange(count int) {
	for range count {
		if len(e.undo) == 0 {
			e.msg = "Already at oldest change"
			return
		}
		s := e.undo[len(e.undo)-1]
		e.undo = e.undo[:len(e.undo)-1]
		e.redo = append(e.redo, snapshot{lines: e.buf.clone(), cur: s.cur})
		e.buf.lines = s.lines
		e.cur = s.cur
	}
}

func (e *Engine) redoChange(count int) {
	for range count {
		if len(e.redo) == 0 {
			e.msg = "Already at newest change"
			return
		}
		s := e.redo[len(e.redo)-1]
		e.redo = e.redo[:len(e.redo)-1]
		e.undo = append(e.undo, snapshot{lines: e.buf.clone(), cur: s.cur})
		old := e.buf.lines
		e.buf.lines = s.lines
		e.cur = e.redoCursor(old, s.cur)
	}
}

// redoCursor puts the cursor where vim does after a redo: on the first line
// the change touched, in the column the cursor had before the change if it
// was on that line (or the line above, as for "o"), else on the first
// non-blank.
func (e *Engine) redoCursor(old [][]rune, before Pos) Pos {
	l := 0
	for l < len(old) && l < len(e.buf.lines) && string(old[l]) == string(e.buf.lines[l]) {
		l++
	}
	if before.Line+1 == l && l > 0 {
		l--
	}
	switch {
	case l > e.buf.last():
		return Pos{e.buf.last(), 0}
	case l == before.Line:
		return before
	}
	return Pos{l, e.buf.firstNonBlank(l)}
}

// replay feeds recorded keys back through the engine.
func (e *Engine) replay(keys []string) Action {
	e.replaying++
	defer func() { e.replaying-- }()
	var act Action
	for _, k := range keys {
		if a := e.HandleKey(k); a.Kind != None {
			act = a
		}
	}
	return act
}
