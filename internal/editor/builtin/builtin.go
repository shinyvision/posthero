// Package builtin is the editor used when neither Neovim nor Vim is
// installed: posthero's own vim engine, with highlighting for posthero's
// documents.
package builtin

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"

	tea "charm.land/bubbletea/v2"

	"github.com/shinyvision/posthero/internal/editor"
	"github.com/shinyvision/posthero/internal/keys"
	"github.com/shinyvision/posthero/internal/mouse"
	"github.com/shinyvision/posthero/internal/vim"
)

// buffer is a file open in the editor.
type buffer struct {
	file  string
	eng   *vim.Engine
	saved string // the text as last written, for E37 and modified
	hints map[int]editor.Hint
	// reported is what was last reported as changed.
	reported string
}

// Backend edits files with the built-in engine.
type Backend struct {
	regs      *vim.Registers
	cur       *buffer
	eng       *vim.Engine // the current buffer's
	bufs      map[string]*buffer
	w, h      int
	events    chan editor.Event
	msg       string
	complete  editor.Completer
	pop       *popup // the insert-mode completion menu, when open
	closeOnce sync.Once
	ctrlX     bool // <C-x> was typed in insert mode
	ctrlW     bool // <C-w> was typed in normal mode
	main      bool
	readOnly  bool
}

// Start opens a file in a new engine.
func Start(opts editor.Options, regs *vim.Registers, w, h int) (*Backend, error) {
	b := &Backend{
		regs:     regs,
		bufs:     map[string]*buffer{},
		events:   make(chan editor.Event, 64),
		complete: opts.Complete,
		main:     opts.Main,
		readOnly: opts.ReadOnly,
	}
	if err := b.load(opts.File, nil); err != nil {
		return nil, err
	}
	b.Resize(w, h)
	b.startInsert(opts.Insert)
	return b, nil
}

// load makes file the current buffer, reading it when it isn't open yet.
func (b *Backend) load(file string, text *string) error {
	buf, ok := b.bufs[file]
	if !ok {
		data, err := os.ReadFile(file)
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		content := strings.TrimSuffix(string(data), "\n")
		buf = &buffer{file: file, eng: vim.New(content, b.regs), saved: content, reported: "\x00"}
		b.bufs[file] = buf
	}
	// A draft stands in for the file's text, but a buffer with changes
	// keeps them: they are newer than anything posthero has heard of.
	if text != nil && buf.eng.Text() != *text && (!ok || buf.eng.Text() == buf.saved) {
		buf.eng.SetText(*text)
	}
	b.cur, b.eng = buf, buf.eng
	if b.h > 0 {
		b.eng.SetHeight(b.h - 1)
	}
	return nil
}

// Name implements editor.Backend.
func (b *Backend) Name() string { return "builtin" }

// startInsert puts the cursor where editor.Options.Insert asks and starts
// insert mode.
func (b *Backend) startInsert(mode string) {
	switch mode {
	case "":
		return
	case "i":
		b.eng.HandleKey("i")
		return
	case "A":
		b.eng.StartInsert()
		return
	}
	line, cmd := mode[:len(mode)-1], mode[len(mode)-1:]
	n, err := strconv.Atoi(line)
	if err != nil {
		n = 1
	}
	lines := b.eng.Lines()
	n = max(1, min(n, len(lines)))
	if cmd == "A" {
		b.eng.SetCursor(vim.Pos{Line: n - 1, Col: len([]rune(lines[n-1]))})
		b.eng.HandleKey("a")
		if len(lines[n-1]) == 0 {
			return
		}
		b.eng.HandleKey("<Esc>")
		b.eng.HandleKey("A")
		return
	}
	b.eng.SetCursor(vim.Pos{Line: n - 1})
	b.eng.HandleKey("i")
}

// windowKeys are what may follow <C-w>, and the window command each is.
var windowKeys = map[string]string{
	"h": "h", "<C-h>": "h", "<Left>": "h", "<BS>": "h",
	"j": "j", "<C-j>": "j", "<Down>": "j",
	"k": "k", "<C-k>": "k", "<Up>": "k",
	"l": "l", "<C-l>": "l", "<Right>": "l",
	"w": "w", "<C-w>": "w", "W": "W", "p": "p", "<C-p>": "p", "t": "t", "<C-t>": "t", "b": "b", "<C-b>": "b",
	"o": "o", "<C-o>": "o", "c": "c", "q": "q", "<C-q>": "q",
	"=": "=", "+": "+", "-": "-", "<": "<", ">": ">", "_": "_", "|": "|",
}

// Key implements editor.Backend.
func (b *Backend) Key(k tea.KeyPressMsg) {
	b.msg = ""
	key := keys.Vim(k)
	normal := b.eng.Mode() == vim.Normal && b.eng.Pending() == ""
	if b.main {
		if b.ctrlW {
			b.ctrlW = false
			if cmd, ok := windowKeys[key]; ok {
				editor.Notify(b.events, editor.WinCmd{Key: cmd})
			}
			return
		}
		if normal && key == "<C-w>" {
			b.ctrlW = true
			return
		}
		if normal && key == "K" {
			b.hover("K")
			return
		}
		if b.eng.Mode() == vim.Normal && b.eng.Pending() == "g" && key == "d" {
			b.eng.HandleKey("<Esc>")
			b.hover("gd")
			return
		}
	}
	if b.readOnly && b.eng.Mode() == vim.Normal && strings.Contains("iIaAoOsScCxXdDpPrR.u", key) && key != "" && len(key) == 1 && b.eng.Pending() == "" {
		b.msg = "E21: Cannot make changes, 'modifiable' is off"
		return
	}
	if b.completionKey(key) {
		b.reportChange()
		return
	}
	act := b.eng.HandleKey(key)
	if b.pop != nil {
		b.refilter(key)
	}
	switch act.Kind {
	case vim.Submit:
		if b.main {
			cur := b.eng.Cursor()
			lines := b.eng.Lines()
			editor.Notify(b.events, editor.Send{File: b.cur.file, Lines: lines, Line: cur.Line, Col: byteCol(lines[cur.Line], cur.Col)})
		} else {
			// <CR> in normal mode goes down a line, as in vim.
			b.eng.HandleKey("+")
		}
	case vim.Ex:
		b.ex(act.Text)
	}
	if b.eng.Mode() == vim.Insert && b.pop == nil && key != "<Esc>" && b.autoComplete() {
		b.openMenu(false, true)
	}
	// Changes are reported as they happen, typing in insert mode too: a
	// request sent or switched away from in insert mode is what it shows.
	b.reportChange()
}

func (b *Backend) hover(key string) {
	cur := b.eng.Cursor()
	lines := b.eng.Lines()
	editor.Notify(b.events, editor.Hover{File: b.cur.file, Lines: lines, Line: cur.Line, Col: byteCol(lines[cur.Line], cur.Col), Key: key})
}

// reportChange tells about changes to the current buffer (Main editors).
func (b *Backend) reportChange() {
	if !b.main {
		return
	}
	text := b.eng.Text()
	modified := text != b.cur.saved
	key := text
	if modified {
		key += "\x01"
	}
	if key == b.cur.reported {
		return
	}
	b.cur.reported = key
	editor.Notify(b.events, editor.Changed{File: b.cur.file, Text: text, Modified: modified})
}

// exNames are the ex commands the built-in editor knows, by every name
// Vim accepts for them (without a !).
var exNames = map[string]string{
	"w": "write", "write": "write",
	"up": "update", "update": "update",
	"wq": "wq", "wqa": "wq", "wqall": "wq", "xa": "wq", "xall": "wq",
	"x": "x", "xit": "x", "exi": "x", "exit": "x",
	"q": "quit", "quit": "quit", "qa": "quit", "qall": "quit", "quita": "quit", "quitall": "quit", "clo": "quit", "close": "quit",
	"cq": "cquit", "cquit": "cquit",
	"e": "edit", "edit": "edit",
}

func (b *Backend) ex(cmd string) {
	cmd = strings.TrimSpace(cmd)
	name, bang := strings.CutSuffix(cmd, "!")
	switch exNames[name] {
	case "write":
		b.write()
	case "update":
		if b.modified() {
			b.write()
		}
	case "wq", "x":
		// :x (and ZZ) write only when there are changes; :wq always does.
		if exNames[name] == "wq" || b.modified() {
			b.write()
		}
		if b.main {
			// In a request, posthero quits once the save is done.
			editor.Notify(b.events, editor.WinCmd{Key: "writequit" + bangIf(bang)})
			return
		}
		editor.Notify(b.events, editor.Exited{})
	case "quit":
		if b.main {
			editor.Notify(b.events, editor.WinCmd{Key: "quit" + bangIf(bang)})
			return
		}
		if !bang && b.modified() {
			b.msg = "E37: No write since last change (add ! to override)"
			return
		}
		editor.Notify(b.events, editor.Exited{})
	case "cquit":
		if b.main {
			editor.Notify(b.events, editor.WinCmd{Key: "quit!"})
			return
		}
		editor.Notify(b.events, editor.Exited{})
	case "edit":
		if !bang {
			b.msg = "E492: Not an editor command: " + cmd
			return
		}
		b.eng.SetText(b.cur.saved)
		b.reportChange()
	default:
		b.msg = "E492: Not an editor command: " + cmd
	}
}

// modified reports whether the buffer has changes not written.
func (b *Backend) modified() bool { return b.eng.Text() != b.cur.saved }

func bangIf(bang bool) string {
	if bang {
		return "!"
	}
	return ""
}

func (b *Backend) write() {
	if b.readOnly {
		b.msg = "E45: 'readonly' option is set"
		return
	}
	text := b.eng.Text()
	_ = os.WriteFile(b.cur.file, []byte(text+"\n"), 0o600)
	b.cur.saved = text
	b.msg = fmt.Sprintf("%q written", b.cur.file)
	editor.Notify(b.events, editor.Written{File: b.cur.file, Text: text})
	b.reportChange()
}

// Paste implements editor.Backend.
func (b *Backend) Paste(text string) {
	if b.readOnly {
		return
	}
	b.eng.Paste(text)
	b.reportChange()
}

// Mouse implements editor.Backend.
func (b *Backend) Mouse(ev mouse.Event) {
	if dy, _ := ev.WheelDelta(); dy != 0 {
		b.eng.Scroll(3 * dy)
		return
	}
	if !ev.Left() || ev.Y >= b.h-1 {
		return
	}
	lines := b.eng.Lines()
	p := vim.Pos{Line: b.eng.Top() + ev.Y, Col: ev.X - gutterWidth(len(lines))}
	if p.Line >= len(lines) {
		p.Line = len(lines) - 1
		p.Col = len([]rune(lines[p.Line]))
	}
	p.Col = max(0, p.Col)
	switch {
	case ev.Action == mouse.Drag:
		b.eng.DragTo(p)
	case ev.Clicks == 2:
		b.eng.SelectWord(p)
	case ev.Clicks >= 3:
		b.eng.SelectLine(p)
	default:
		b.eng.Click(p)
	}
}

// Command implements editor.Backend.
func (b *Backend) Command(cmd string) { b.ex(cmd) }

// Resize implements editor.Backend.
func (b *Backend) Resize(w, h int) {
	b.w, b.h = max(1, w), max(1, h)
	b.eng.SetHeight(b.h - 1) // the last line shows mode and command line
}

// Cursor implements editor.Backend.
func (b *Backend) Cursor() (int, int, tea.CursorShape, bool) {
	if line, ok := b.eng.CommandLine(); ok {
		return len([]rune(line)), b.h - 1, tea.CursorBar, true
	}
	p := b.eng.Cursor()
	shape := tea.CursorBlock
	if m := b.eng.Mode(); m == vim.Insert {
		shape = tea.CursorBar
	} else if m == vim.Replace {
		shape = tea.CursorUnderline
	}
	line := []rune(b.eng.Lines()[p.Line])
	x := gutterWidth(len(b.eng.Lines())) + displayWidth(line[:min(p.Col, len(line))])
	return x, p.Line - b.eng.Top(), shape, true
}

// Mode implements editor.Backend.
func (b *Backend) Mode() string { return b.eng.Mode().String() }

// Events implements editor.Backend.
func (b *Backend) Events() <-chan editor.Event { return b.events }

// Close implements editor.Backend: whoever waits for events hears that the
// editor is gone.
func (b *Backend) Close() {
	b.closeOnce.Do(func() {
		select {
		case b.events <- editor.Exited{}:
		default:
		}
	})
}

// Open implements editor.Backend.
func (b *Backend) Open(file string, text *string) {
	if err := b.load(file, text); err != nil {
		b.msg = err.Error()
		return
	}
	b.pop = nil
	b.reportChange()
	editor.Notify(b.events, editor.Redraw{})
}

// Reload implements editor.Backend.
// A buffer with changes posthero hasn't heard of keeps them (it asks only
// when it has no draft: the changes it heard of are none, or undone).
func (b *Backend) Reload(file string) {
	buf, ok := b.bufs[file]
	if !ok || buf.eng.Text() != buf.saved && strings.TrimSuffix(buf.reported, "\x01") != buf.eng.Text() {
		return
	}
	data, err := os.ReadFile(file)
	if err != nil {
		return
	}
	text := strings.TrimSuffix(string(data), "\n")
	buf.saved = text
	if buf.eng.Text() != text {
		buf.eng.SetText(text)
	}
	if buf == b.cur {
		b.reportChange()
	}
	editor.Notify(b.events, editor.Redraw{})
}

// SetHints implements editor.Backend.
func (b *Backend) SetHints(file string, hints []editor.Hint) {
	buf, ok := b.bufs[file]
	if !ok {
		return
	}
	buf.hints = map[int]editor.Hint{}
	for _, h := range hints {
		buf.hints[h.Line] = h
	}
	editor.Notify(b.events, editor.Redraw{})
}

// Goto implements editor.Backend.
func (b *Backend) Goto(line, col int) {
	lines := b.eng.Lines()
	line = max(0, min(line, len(lines)-1))
	b.eng.SetCursor(vim.Pos{Line: line, Col: len([]rune(lines[line][:min(col, len(lines[line]))]))})
	b.eng.CenterCursor()
}

// byteCol converts a rune column of line to a byte column.
func byteCol(line string, col int) int {
	r := []rune(line)
	return len(string(r[:min(col, len(r))]))
}
