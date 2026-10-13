package form

import (
	"strconv"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/shinyvision/posthero/internal/ui/theme"
)

const (
	gap          = 2
	dropdownRows = 6
	minWidth     = 56
	// The box has a border and padding of 1 line and 2 columns.
	padX, padY = 3, 2
)

var (
	focusBar    = theme.AccentFg.Render("┃ ")
	noBar       = "  "
	labelFocus  = theme.AccentFg.Bold(true)
	labelPlain  = lipgloss.NewStyle().Bold(true)
	promptStyle = theme.AccentFg.Render("› ")
	keyStyle    = lipgloss.NewStyle().Foreground(theme.Muted).Bold(true)
	buttonPlain = lipgloss.NewStyle().Foreground(theme.Muted).Padding(0, 1)
)

// view accumulates lines and remembers where the cursor goes and what can
// be clicked.
type view struct {
	lines  []string
	cx, cy int
	cursor bool
	zones  []zone
}

// zone is a clickable span of a line, in content coordinates.
type zone struct {
	y, x0, x1 int // x1 is exclusive
	row, col  int
	what      zoneKind
	n         int // the dropdown option, or the text column of x0
}

type zoneKind int

const (
	zField  zoneKind = iota // focus the field
	zText                   // focus and put the text cursor under the click
	zOption                 // pick dropdown option n
	zYes                    // set a toggle
	zNo
	zToggle // flip a toggle
)

func (v *view) add(s ...string) { v.lines = append(v.lines, s...) }

// zone makes x0..x1 of the next added line clickable.
func (v *view) zone(x0, x1, row, col int, what zoneKind, n int) {
	v.zones = append(v.zones, zone{y: len(v.lines), x0: x0, x1: x1, row: row, col: col, what: what, n: n})
}

func (v *view) setCursor(x int) {
	v.cx, v.cy, v.cursor = x, len(v.lines), true
}

// View renders the form for a screen of sw×sh cells and returns the cursor
// position inside the box.
func (f *Form) View(sw, sh int) (box string, cx, cy int, cursor bool) {
	f.start()
	w := f.width(sw)
	v := &view{}
	v.add(theme.AccentFg.Bold(true).Render(f.Title))
	if f.Subtitle != "" {
		for _, l := range strings.Split(ansi.Wrap(f.Subtitle, w, " "), "\n") {
			v.add(theme.Dim.Render(l))
		}
	}
	v.add("")
	for ri, r := range f.Rows {
		focused := ri == f.row
		switch {
		case r.Header:
			v.add(noBar + f.headerLine(r))
		case len(r.Fields) == 0:
			v.add("")
		case r.Fields[0].Kind == Button:
			x := len(noBar)
			for ci, fl := range r.Fields {
				v.zone(x, x+fl.Width, ri, ci, zField, 0)
				x += fl.Width + 1
			}
			v.add(noBar + f.buttonLine(r, focused))
		case len(r.Fields) == 1 && !r.Item:
			f.block(v, ri, focused, w)
		default:
			f.gridRow(v, ri, focused)
		}
	}
	if f.err != "" {
		v.add("")
		for i, l := range strings.Split(ansi.Wrap(f.err, w-2, " "), "\n") {
			mark := "  "
			if i == 0 {
				mark = "✗ "
			}
			v.add(theme.Error.Render(mark + l))
		}
	}
	v.add("", f.hints(w))
	box = lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(theme.Accent).
		Padding(1, 2).
		Render(strings.Join(v.lines, "\n"))
	f.zones = v.zones
	return box, v.cx + padX, v.cy + padY, v.cursor
}

func (f *Form) width(sw int) int {
	w := minWidth
	for _, r := range f.Rows {
		if len(r.Fields) > 1 || r.Item || r.Header {
			total := len(noBar)
			for i, fl := range r.Fields {
				if i > 0 {
					total += gap
				}
				total += fl.Width
			}
			w = max(w, total)
		}
	}
	return max(30, min(w, sw-10))
}

// block renders a labelled field on its own lines.
func (f *Form) block(v *view, ri int, focused bool, w int) {
	r := f.Rows[ri]
	fl := r.Fields[0]
	bar, label := noBar, labelPlain
	if focused {
		bar, label = focusBar, labelFocus
	}
	v.zone(0, w, ri, 0, zField, 0)
	v.add(bar + label.Render(r.Label))
	if focused && fl.Description != "" {
		v.zone(0, w, ri, 0, zField, 0)
		v.add(bar + theme.Dim.Render(theme.Truncate(fl.Description, w-2)))
	}
	text := len(noBar) + 2
	switch fl.Kind {
	case Toggle:
		// "  ● yes   ○ no"
		v.zone(text, text+5, ri, 0, zYes, 0)
		v.zone(text+8, text+12, ri, 0, zNo, 0)
		v.add(bar + toggleOptions(fl.on, focused))
	default:
		v.zone(text, w, ri, 0, zText, text)
		if focused {
			view, cur := fl.input.View(w - 4)
			v.setCursor(text + cur)
			v.add(bar + promptStyle + ansi.Strip(view))
			if fl.Kind == Select {
				f.dropdownLines(v, fl, ri, 0, bar+"  ", text, w-4)
			}
		} else {
			v.add(bar + "  " + valueOrPlaceholder(fl, w-4))
		}
	}
	v.add("")
}

// dropdownLines adds a select field's options, each clickable.
func (f *Form) dropdownLines(v *view, fl *Field, ri, ci int, prefix string, x, w int) {
	top := dropdownTop(fl)
	for i, l := range dropdown(fl, w) {
		if opt := top + i; opt < len(fl.matches()) && i < dropdownRows {
			v.zone(x, x+w, ri, ci, zOption, opt)
		}
		v.add(prefix + l)
	}
}

func toggleOptions(on, focused bool) string {
	sel, other := lipgloss.NewStyle(), theme.Dim
	if focused {
		sel = labelFocus
	}
	yes, no := sel.Render("● yes"), other.Render("○ no")
	if !on {
		yes, no = other.Render("○ yes"), sel.Render("● no")
	}
	return "  " + yes + "   " + no
}

func valueOrPlaceholder(fl *Field, w int) string {
	if v := fl.Value(); v != "" {
		if fl.input != nil && fl.input.Mask {
			return theme.Truncate(strings.Repeat("•", len([]rune(v))), w)
		}
		return theme.Truncate(v, w)
	}
	if fl.Placeholder != "" {
		return theme.Dim.Italic(true).Render(theme.Truncate(fl.Placeholder, w))
	}
	return theme.Dim.Render("—")
}

func (f *Form) headerLine(r *Row) string {
	var parts []string
	for _, fl := range r.Fields {
		parts = append(parts, theme.Dim.Bold(true).Render(pad(strings.ToUpper(fl.Label), fl.Width)))
	}
	return strings.Join(parts, strings.Repeat(" ", gap))
}

// gridRow renders a row of several fields, e.g. one column of a new table.
func (f *Form) gridRow(v *view, ri int, focused bool) {
	r := f.Rows[ri]
	bar := noBar
	if focused {
		bar = focusBar
	}
	var b strings.Builder
	b.WriteString(bar)
	x := len(noBar)
	var open *Field
	openX, openCol := 0, 0
	for ci, fl := range r.Fields {
		if ci > 0 {
			b.WriteString(strings.Repeat(" ", gap))
			x += gap
		}
		cell := focused && ci == f.col
		if fl.Kind == Toggle {
			v.zone(x, x+fl.Width, ri, ci, zToggle, 0)
		} else {
			v.zone(x, x+fl.Width, ri, ci, zText, x)
		}
		switch fl.Kind {
		case Toggle:
			text := "○ no"
			if fl.on {
				text = "● yes"
			}
			style := lipgloss.NewStyle()
			if !fl.on {
				style = theme.Dim
			}
			if cell {
				style = labelFocus.Reverse(true)
			}
			b.WriteString(style.Render(pad(text, fl.Width)))
		default:
			if cell {
				view, cur := fl.input.View(fl.Width)
				v.setCursor(x + cur)
				b.WriteString(theme.AccentFg.Underline(true).Render(ansi.Strip(view)))
				if fl.Kind == Select {
					open, openX, openCol = fl, x, ci
				}
			} else {
				b.WriteString(pad(ansi.Strip(valueOrPlaceholder(fl, fl.Width)), fl.Width))
			}
		}
		x += fl.Width
	}
	v.add(b.String())
	if open != nil {
		f.dropdownLines(v, open, ri, openCol, bar+strings.Repeat(" ", openX-len(noBar)), openX, max(open.Width, 24))
	}
}

// dropdown lists the options matching a select field's text.
func dropdown(fl *Field, w int) []string {
	opts := fl.matches()
	if len(opts) == 0 {
		return []string{theme.Dim.Italic(true).Render(theme.Truncate("no match · “"+fl.Value()+"” is used as typed", w))}
	}
	top := dropdownTop(fl)
	var out []string
	for i := top; i < len(opts) && i < top+dropdownRows; i++ {
		if i == fl.sel {
			out = append(out, labelFocus.Render(theme.Truncate("▸ "+opts[i], w)))
		} else {
			out = append(out, theme.Dim.Render(theme.Truncate("  "+opts[i], w)))
		}
	}
	if more := len(opts) - (top + dropdownRows); more > 0 {
		out = append(out, theme.Dim.Render("  … "+itoa(more)+" more"))
	}
	return out
}

// dropdownTop is the first option a dropdown shows.
func dropdownTop(fl *Field) int {
	return max(0, min(fl.sel-dropdownRows/2, len(fl.matches())-dropdownRows))
}

func (f *Form) buttonLine(r *Row, focused bool) string {
	var parts []string
	for ci, fl := range r.Fields {
		if focused && ci == f.col {
			parts = append(parts, theme.AccentPill.Render(fl.Label))
		} else {
			parts = append(parts, buttonPlain.Render(fl.Label))
		}
	}
	return strings.Join(parts, " ")
}

func (f *Form) hints(w int) string {
	if f.pending {
		busy := f.Busy
		if busy == "" {
			busy = "saving…"
		}
		switch {
		case f.cancelling:
			return theme.Warn.Render("cancelling…")
		case f.escArmed:
			return theme.Warn.Render(busy) + theme.Dim.Render(" · ") + keyStyle.Render("esc") + " " + theme.Dim.Render("again to cancel")
		case f.OnCancel != nil:
			return theme.Warn.Render(busy) + theme.Dim.Render(" · ") + keyStyle.Render("esc esc") + " " + theme.Dim.Render("cancels")
		}
		return theme.Warn.Render(busy)
	}
	verb := f.Verb
	if verb == "" {
		verb = "save"
	}
	pairs := [][2]string{{"tab", "next"}, {"⏎", "choose"}, {"ctrl+s", verb}, {"esc esc", "cancel"}}
	if f.NewItem != nil {
		pairs = append(pairs, [2]string{"ctrl+a", "add column"}, [2]string{"ctrl+x", "remove column"})
	}
	var lines []string
	line, lineW := "", 0
	for _, p := range pairs {
		item := keyStyle.Render(p[0]) + " " + theme.Dim.Render(p[1])
		iw := ansi.StringWidth(item)
		if lineW > 0 && lineW+3+iw > w {
			lines = append(lines, line)
			line, lineW = "", 0
		}
		if lineW > 0 {
			line += theme.Dim.Render(" · ")
			lineW += 3
		}
		line += item
		lineW += iw
	}
	return strings.Join(append(lines, line), "\n")
}

func pad(s string, w int) string {
	r := []rune(s)
	if len(r) > w {
		return string(r[:max(0, w-1)]) + "…"
	}
	return s + strings.Repeat(" ", w-len(r))
}

func itoa(n int) string { return strconv.Itoa(n) }
