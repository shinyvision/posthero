// Package modal provides dialogs drawn over the panes: confirmations,
// errors and the help screen. Any pane can open one by returning Show(...).
package modal

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/shinyvision/posthero/internal/keys"
	"github.com/shinyvision/posthero/internal/mouse"
	"github.com/shinyvision/posthero/internal/ui/theme"
)

// Modal is a dialog that owns the keyboard while it is open.
type Modal interface {
	// Key handles a key; done closes the modal.
	Key(key string) (done bool, cmd tea.Cmd)
	// View renders the dialog within the given bounds.
	View(maxW, maxH int) string
}

// Mouser is a modal that handles the mouse. Events are relative to the
// dialog's top-left corner and may fall outside it.
type Mouser interface {
	Mouse(ev mouse.Event) (done bool, cmd tea.Cmd)
}

// Anchored is a modal drawn at a screen position (e.g. under the pointer)
// instead of centered.
type Anchored interface {
	Anchor() (x, y int)
}

// ShowMsg asks the app to open a modal.
type ShowMsg struct{ Modal Modal }

// Show returns a command opening m.
func Show(m Modal) tea.Cmd { return func() tea.Msg { return ShowMsg{m} } }

// KeyString converts a key press for Modal.Key.
func KeyString(k tea.KeyPressMsg) string { return keys.Vim(k) }

func frame(title string, color lipgloss.Style, body []string, hint string, maxW int) string {
	return frameW(title, color, body, hint, maxW, 20)
}

// frameW is frame with a minimum content width.
func frameW(title string, color lipgloss.Style, body []string, hint string, maxW, minW int) string {
	w := minW
	for _, l := range body {
		for _, line := range strings.Split(l, "\n") {
			w = max(w, ansi.StringWidth(line))
		}
	}
	w = min(max(w, ansi.StringWidth(hint)), maxW-6)
	var lines []string
	for _, l := range body {
		lines = append(lines, wrap(l, w)...)
	}
	lines = append(lines, "", theme.Dim.Render(hint))
	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(color.GetForeground()).
		Padding(0, 1).
		Width(w + 4).
		Render(color.Bold(true).Render(title) + "\n\n" + strings.Join(lines, "\n"))
}

func wrap(s string, w int) []string {
	var out []string
	for _, line := range strings.Split(s, "\n") {
		out = append(out, strings.Split(ansi.Wrap(line, w, " "), "\n")...)
	}
	return out
}

// Confirm asks a yes/no question. y confirms, n or <Esc> cancels, and <CR>
// activates the highlighted choice, which starts on "No".
type Confirm struct {
	Title, Body string
	Yes         tea.Cmd
	No          tea.Cmd // when the answer is no; may be nil
	yes         bool
	box         string
}

// Key implements Modal.
func (c *Confirm) Key(k string) (bool, tea.Cmd) {
	switch k {
	case "y", "Y":
		return true, c.Yes
	case "n", "N", "<Esc>", "q":
		return true, c.No
	case "h", "l", "<Tab>", "<Left>", "<Right>", "<S-Tab>":
		c.yes = !c.yes
	case "<CR>":
		if c.yes {
			return true, c.Yes
		}
		return true, c.No
	}
	return false, nil
}

// View implements Modal.
func (c *Confirm) View(maxW, _ int) string {
	no, yes := theme.AccentPill.Render("No"), theme.MutedPill.Render("Yes")
	if c.yes {
		no, yes = theme.MutedPill.Render("No"), theme.AccentPill.Render("Yes")
	}
	body := []string{c.Body, "", yes + "  " + no}
	c.box = frame(c.Title, theme.Warn, body, "y yes · n/esc no · h/l choose · enter select", maxW)
	return c.box
}

// Mouse implements Mouser: click Yes or No; a click outside means no.
func (c *Confirm) Mouse(ev mouse.Event) (bool, tea.Cmd) {
	if ev.Action != mouse.Press || ev.Button != tea.MouseLeft {
		return false, nil
	}
	if !inside(c.box, ev) {
		return true, c.No
	}
	x, y, ok := find(c.box, " Yes    No ")
	if !ok || ev.Y != y {
		return false, nil
	}
	switch {
	case ev.X >= x && ev.X < x+5:
		return true, c.Yes
	case ev.X >= x+7 && ev.X < x+11:
		return true, c.No
	}
	return false, nil
}

// Message shows an error or notice until dismissed.
type Message struct {
	Title, Body string
	IsError     bool
}

// Key implements Modal.
func (m *Message) Key(k string) (bool, tea.Cmd) {
	switch k {
	case "<CR>", "<Esc>", "q", " ":
		return true, nil
	}
	return false, nil
}

// Mouse implements Mouser: any click closes the message.
func (m *Message) Mouse(ev mouse.Event) (bool, tea.Cmd) {
	return ev.Action == mouse.Press && ev.Button == tea.MouseLeft, nil
}

// View implements Modal.
func (m *Message) View(maxW, _ int) string {
	style := theme.AccentFg
	if m.IsError {
		style = theme.Error
	}
	return frame(m.Title, style, []string{m.Body}, "enter/esc to close", maxW)
}

// Error returns a command showing an error dialog.
func Error(title, body string) tea.Cmd {
	return Show(&Message{Title: title, Body: body, IsError: true})
}

// inside reports whether ev falls on the rendered box.
func inside(box string, ev mouse.Event) bool {
	return ev.In(lipgloss.Width(box), lipgloss.Height(box))
}

// find locates plain text in a rendered box, in cells.
func find(box, text string) (x, y int, ok bool) {
	for y, line := range strings.Split(ansi.Strip(box), "\n") {
		if i := strings.Index(line, text); i >= 0 {
			return ansi.StringWidth(line[:i]), y, true
		}
	}
	return 0, 0, false
}

// boxStyle is the frame of dialogs drawn by hand.
func boxStyle() lipgloss.Style {
	return lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(theme.Accent).Padding(0, 1)
}
