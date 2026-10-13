package modal

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/shinyvision/posthero/internal/mouse"
	"github.com/shinyvision/posthero/internal/ui/lineinput"
	"github.com/shinyvision/posthero/internal/ui/theme"
	"github.com/shinyvision/posthero/internal/vim"
)

// Input asks for one line of text, edited with vim keys. ⏎ submits from
// either mode; esc goes to normal mode, and again cancels.
type Input struct {
	Title string
	// Hint is shown dim under the field.
	Hint   string
	Value  string
	Submit func(text string) tea.Cmd
	// Regs are the registers for yanking and putting; nil keeps them
	// private.
	Regs *vim.Registers

	input *lineinput.Model
	box   string
	w     int
}

func (in *Input) init() {
	if in.input == nil {
		in.input = lineinput.New("", in.Value, in.Regs)
	}
}

// Key implements Modal.
func (in *Input) Key(k string) (bool, tea.Cmd) {
	in.init()
	switch in.input.Key(k).Kind {
	case vim.Submit:
		return true, in.Submit(strings.TrimSpace(in.input.Value()))
	case vim.Cancel:
		return true, nil
	}
	if k == "<C-c>" {
		return true, nil
	}
	return false, nil
}

// Paste types pasted text.
func (in *Input) Paste(s string) {
	in.init()
	in.input.Paste(strings.ReplaceAll(s, "\n", " "))
}

// Cursor is where the text cursor is, relative to the box.
func (in *Input) Cursor() (x, y int) {
	in.init()
	_, cx := in.input.View(in.w)
	// Border and padding, then the title and a blank line.
	return 2 + cx, 3
}

// View implements Modal.
func (in *Input) View(maxW, _ int) string {
	in.init()
	in.w = max(30, min(maxW-8, max(60, ansi.StringWidth(in.Value)+4)))
	field, _ := in.input.View(in.w)
	mode := "insert"
	if in.input.Mode() != vim.Insert {
		mode = "normal"
	}
	lines := []string{theme.AccentFg.Bold(true).Render(in.Title), "", field}
	if in.Hint != "" {
		lines = append(lines, theme.Dim.Render(theme.Truncate(in.Hint, in.w)))
	}
	lines = append(lines, "", theme.Dim.Render(theme.Truncate("⏎ save · esc "+map[string]string{"insert": "normal mode", "normal": "cancel"}[mode], in.w)))
	in.box = boxStyle().Render(strings.Join(lines, "\n"))
	return in.box
}

// Mouse implements Mouser: a click outside cancels.
func (in *Input) Mouse(ev mouse.Event) (bool, tea.Cmd) {
	if ev.Action == mouse.Press && !inside(in.box, ev) {
		return true, nil
	}
	return false, nil
}
