// Package lineinput is a single-line text field with vim keys, used for
// prompts and form fields so that editing text works the same everywhere.
package lineinput

import (
	"strings"

	"github.com/shinyvision/posthero/internal/ui/theme"
	"github.com/shinyvision/posthero/internal/vim"
)

// Model is a single-line vim input. It starts in insert mode; <Esc> goes to
// normal mode, <Esc> again cancels and <CR> submits from either mode.
type Model struct {
	Prompt      string
	Placeholder string
	Mask        bool
	eng         *vim.Engine
	scroll      int
}

// New returns an input holding value, ready for typing at its end.
func New(prompt, value string, regs *vim.Registers) *Model {
	eng := vim.New(value, regs)
	eng.SingleLine = true
	eng.StartInsert()
	return &Model{Prompt: prompt, eng: eng}
}

// Key handles a key in Vim notation. Submit and Cancel are passed through.
func (m *Model) Key(k string) vim.Action {
	return m.eng.HandleKey(k)
}

// Paste inserts text.
func (m *Model) Paste(s string) { m.eng.Paste(s) }

// Value returns the text.
func (m *Model) Value() string { return m.eng.Text() }

// SetValue replaces the text.
func (m *Model) SetValue(s string) {
	m.eng.SetText(s)
	m.eng.StartInsert()
}

// Mode returns the editing mode.
func (m *Model) Mode() vim.Mode { return m.eng.Mode() }

// Insert puts the field in insert mode at the end of its text.
func (m *Model) Insert() {
	m.eng.StartInsert()
	m.scroll = 0 // until the next View, the text shows from its start
}

// Click moves the cursor to cell x of the line View rendered, prompt
// included, keeping the mode. Cells are counted in display width, so a
// wide character takes two.
func (m *Model) Click(x int) {
	runes := m.shown()
	cell := x - theme.Width(m.Prompt)
	col := min(m.scroll, len(runes))
	for col < len(runes) {
		w := runeWidth(runes[col])
		if cell < w {
			break
		}
		cell -= w
		col++
	}
	m.eng.Click(vim.Pos{Col: col})
}

// shown is the text as drawn: masked or not.
func (m *Model) shown() []rune {
	runes := []rune(m.eng.Text())
	if m.Mask {
		runes = []rune(strings.Repeat("•", len(runes)))
	}
	return runes
}

// View renders the prompt and text in width cells and returns the cursor
// column relative to the start of the line, in cells.
func (m *Model) View(width int) (string, int) {
	runes := m.shown()
	promptW := theme.Width(m.Prompt)
	avail := max(1, width-promptW-1)
	cur := min(m.eng.Cursor().Col, len(runes))
	if cur < m.scroll {
		m.scroll = cur
	}
	m.scroll = max(0, min(m.scroll, len(runes)))
	// The text before the cursor and the cursor's own cell must fit.
	cursorW := 1
	if cur < len(runes) {
		cursorW = max(1, runeWidth(runes[cur]))
	}
	for m.scroll < cur && cells(runes[m.scroll:cur])+cursorW > avail {
		m.scroll++
	}
	end, used := m.scroll, 0
	for end < len(runes) && used+runeWidth(runes[end]) <= avail {
		used += runeWidth(runes[end])
		end++
	}
	visible := runes[m.scroll:end]
	text := string(visible)
	if len(runes) == 0 && m.Placeholder != "" {
		text = theme.Dim.Render(theme.Truncate(m.Placeholder, avail))
	}
	if s, e, _, ok := m.eng.Selection(); ok {
		text = highlight(visible, s.Col-m.scroll, e.Col-m.scroll)
	}
	return theme.Fit(m.Prompt+text, width), promptW + cells(runes[m.scroll:cur])
}

func runeWidth(r rune) int { return theme.Width(string(r)) }

// cells is the display width of runes.
func cells(r []rune) int { return theme.Width(string(r)) }

func highlight(r []rune, s, e int) string {
	s, e = max(0, s), min(len(r)-1, e)
	if s > e {
		return string(r)
	}
	return string(r[:s]) + theme.Selected.Render(string(r[s:e+1])) + string(r[e+1:])
}
