package form

import (
	tea "charm.land/bubbletea/v2"

	"github.com/shinyvision/posthero/internal/mouse"
	"github.com/shinyvision/posthero/internal/vim"
)

// Row is a line of fields. Header rows only show their fields' labels as
// column titles; Item rows belong to the repeatable list (table columns).
type Row struct {
	Label  string
	Fields []*Field
	Header bool
	Item   bool
}

// ResultMsg is the outcome of Submit: on success the form closes and Then
// is delivered; on failure the error is shown in the form.
type ResultMsg struct {
	Err  error
	Then tea.Msg
}

// Fail returns a command reporting a validation error.
func Fail(err error) tea.Cmd { return func() tea.Msg { return ResultMsg{Err: err} } }

// OpenMsg asks the app to show a form.
type OpenMsg struct{ Form *Form }

// Open returns a command opening f.
func Open(f *Form) tea.Cmd { return func() tea.Msg { return OpenMsg{f} } }

// Form is a dialog.
type Form struct {
	Title    string
	Subtitle string
	Rows     []*Row
	// NewItem creates a row for the repeatable list (ctrl+a); nil disables
	// adding and removing rows.
	NewItem func() *Row
	// Submit validates and applies the form; its command yields a ResultMsg.
	Submit func(*Form) tea.Cmd
	// Busy replaces "saving…" while Submit runs.
	Busy string
	// Verb names what ctrl+s does in the hints; empty is "save".
	Verb string
	// OnCancel, set by Submit, stops the work Submit started: esc esc calls
	// it while the form is busy. Submit's command then reports the
	// cancellation as its error, which keeps the form open, so a second esc
	// esc closes it.
	OnCancel func()

	row, col   int
	err        string
	pending    bool
	started    bool
	escArmed   bool   // one esc of the pair that cancels a busy form
	cancelling bool   // OnCancel was called; waiting for Submit to stop
	zones      []zone // clickable spans from the last View
}

func (f *Form) start() {
	if f.started {
		return
	}
	f.started = true
	f.row, f.col = -1, 0
	f.moveRow(1)
}

func (f *Form) current() *Field {
	if f.row < 0 || f.row >= len(f.Rows) {
		return nil
	}
	fs := f.Rows[f.row].Fields
	if f.col >= len(fs) {
		return nil
	}
	return fs[f.col]
}

func (f *Form) focusable(r int) bool {
	return r >= 0 && r < len(f.Rows) && !f.Rows[r].Header && len(f.Rows[r].Fields) > 0
}

func (f *Form) moveRow(d int) {
	for r := f.row + d; r >= 0 && r < len(f.Rows); r += d {
		if f.focusable(r) {
			f.row = r
			f.col = min(f.col, len(f.Rows[r].Fields)-1)
			f.current().focus(nil)
			return
		}
	}
}

// moveField moves linearly through all fields, wrapping around.
func (f *Form) moveField(d int) {
	type pos struct{ r, c int }
	var all []pos
	cur := 0
	for r, row := range f.Rows {
		if !f.focusable(r) {
			continue
		}
		for c := range row.Fields {
			if r == f.row && c == f.col {
				cur = len(all)
			}
			all = append(all, pos{r, c})
		}
	}
	if len(all) == 0 {
		return
	}
	p := all[(cur+d+len(all))%len(all)]
	f.row, f.col = p.r, p.c
	f.current().focus(nil)
}

func (f *Form) moveCol(d int) {
	n := len(f.Rows[f.row].Fields)
	f.col = max(0, min(n-1, f.col+d))
	f.current().focus(nil)
}

// Values helpers for Submit functions.

// Field returns the first field with the given label.
func (f *Form) Field(label string) *Field {
	for _, r := range f.Rows {
		if r.Header {
			continue
		}
		for _, fl := range r.Fields {
			if fl.Label == label {
				return fl
			}
		}
	}
	return nil
}

// Items returns the repeatable rows.
func (f *Form) Items() []*Row {
	var out []*Row
	for _, r := range f.Rows {
		if r.Item {
			out = append(out, r)
		}
	}
	return out
}

// AddItem appends a row to the repeatable list after the focused one.
func (f *Form) AddItem() {
	if f.NewItem == nil {
		return
	}
	at := len(f.Rows)
	for i, r := range f.Rows {
		if r.Item {
			at = i + 1
		}
	}
	if f.row >= 0 && f.Rows[f.row].Item {
		at = f.row + 1
	}
	f.Rows = append(f.Rows[:at], append([]*Row{f.NewItem()}, f.Rows[at:]...)...)
	f.row, f.col = at, 0
	f.current().focus(nil)
}

func (f *Form) removeItem() {
	if f.NewItem == nil || !f.Rows[f.row].Item || len(f.Items()) <= 1 {
		return
	}
	f.Rows = append(f.Rows[:f.row], f.Rows[f.row+1:]...)
	f.row--
	f.moveRow(1)
	if !f.Rows[f.row].Item {
		f.moveRow(-1)
	}
}

func (f *Form) submit() tea.Cmd {
	if f.pending || f.Submit == nil {
		return nil
	}
	f.err = ""
	f.OnCancel = nil
	f.escArmed, f.cancelling = false, false
	cmd := f.Submit(f)
	f.pending = cmd != nil
	return cmd
}

// busyKey handles a key while Submit runs: esc esc cancels when it can.
func (f *Form) busyKey(k string) {
	if k != "<Esc>" || f.OnCancel == nil || f.cancelling {
		f.escArmed = false
		return
	}
	if !f.escArmed {
		f.escArmed = true
		return
	}
	f.escArmed, f.cancelling = false, true
	f.OnCancel()
}

// Key handles a key in Vim notation. closed reports the form was cancelled.
func (f *Form) Key(k string) (cmd tea.Cmd, closed bool) {
	f.start()
	if f.pending {
		f.busyKey(k)
		return nil, false
	}
	fl := f.current()
	switch k {
	case "<C-s>":
		return f.submit(), false
	case "<Tab>":
		f.moveField(1)
		return nil, false
	case "<S-Tab>":
		f.moveField(-1)
		return nil, false
	case "<C-a>":
		f.AddItem()
		return nil, false
	case "<C-x>":
		f.removeItem()
		return nil, false
	}
	if fl == nil {
		return nil, k == "<Esc>"
	}
	normal := !fl.editable() || fl.input.Mode() == vim.Normal
	if fl.Kind == Select {
		switch {
		case k == "<Down>" || k == "<C-n>" || (normal && k == "j"):
			fl.moveOption(1)
			return nil, false
		case k == "<Up>" || k == "<C-p>" || (normal && k == "k"):
			fl.moveOption(-1)
			return nil, false
		}
	} else if normal || k == "<Down>" || k == "<Up>" {
		switch k {
		case "j", "<Down>":
			f.moveRow(1)
			return nil, false
		case "k", "<Up>":
			f.moveRow(-1)
			return nil, false
		}
	}
	if !fl.editable() {
		return f.controlKey(fl, k)
	}
	switch fl.typeKey(k).Kind {
	case vim.Submit:
		if fl.Kind == Select {
			fl.pick()
		}
		f.moveField(1)
	case vim.Cancel:
		return nil, true
	}
	return nil, false
}

func (f *Form) controlKey(fl *Field, k string) (tea.Cmd, bool) {
	switch k {
	case "<Esc>", "q":
		return nil, true
	case "h", "<Left>":
		if fl.Kind == Toggle && len(f.Rows[f.row].Fields) == 1 {
			fl.on = true // stacked toggles read "yes  no"
		} else {
			f.moveCol(-1)
		}
	case "l", "<Right>":
		if fl.Kind == Toggle && len(f.Rows[f.row].Fields) == 1 {
			fl.on = false
		} else {
			f.moveCol(1)
		}
	case " ", "<CR>", "x":
		if fl.Kind == Toggle {
			fl.on = !fl.on
		} else if fl.Cancel {
			return nil, true
		} else if fl.Action != nil {
			return fl.Action(f), false
		} else if fl.Kind == Button {
			return f.submit(), false
		}
	case "y":
		if fl.Kind == Toggle {
			fl.on = true
		}
	case "n":
		if fl.Kind == Toggle {
			fl.on = false
		}
	}
	return nil, false
}

// Mouse handles a mouse event relative to the dialog's top-left corner:
// clicks focus fields, place the text cursor, flip toggles, pick dropdown
// options and press buttons; the wheel moves through a dropdown.
func (f *Form) Mouse(ev mouse.Event) (cmd tea.Cmd, closed bool) {
	f.start()
	if f.pending {
		return nil, false
	}
	if dy, _ := ev.WheelDelta(); dy != 0 {
		if fl := f.current(); fl != nil && fl.Kind == Select {
			fl.moveOption(dy)
		}
		return nil, false
	}
	if ev.Action != mouse.Press || ev.Button != tea.MouseLeft {
		return nil, false
	}
	x, y := ev.X-padX, ev.Y-padY
	for _, z := range f.zones {
		if z.y == y && x >= z.x0 && x < z.x1 {
			return f.click(z, x)
		}
	}
	return nil, false
}

func (f *Form) click(z zone, x int) (tea.Cmd, bool) {
	fl := f.Rows[z.row].Fields[z.col]
	if z.what == zOption {
		fl.sel = z.n
		fl.pick()
		f.moveField(1)
		return nil, false
	}
	if z.row != f.row || z.col != f.col {
		f.row, f.col = z.row, z.col
		fl.focus(nil)
	}
	switch z.what {
	case zText:
		fl.input.Click(x - z.n)
	case zYes:
		fl.on = true
	case zNo:
		fl.on = false
	case zToggle:
		fl.on = !fl.on
	case zField:
		if fl.Kind == Button {
			return f.controlKey(fl, "<CR>")
		}
	}
	return nil, false
}

// Paste inserts text into the focused field.
func (f *Form) Paste(s string) {
	f.start()
	if fl := f.current(); fl != nil && fl.editable() {
		fl.input.Paste(s)
		fl.typed = true
	}
}

// Update handles the Submit result. closed reports the form is done.
func (f *Form) Update(msg tea.Msg) (tea.Cmd, bool) {
	r, ok := msg.(ResultMsg)
	if !ok {
		return nil, false
	}
	f.pending = false
	f.escArmed, f.cancelling = false, false
	if r.Err != nil {
		f.err = r.Err.Error()
		return nil, false
	}
	if r.Then == nil {
		return nil, true
	}
	return func() tea.Msg { return r.Then }, true
}
