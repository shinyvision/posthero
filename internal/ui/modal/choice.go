package modal

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/shinyvision/posthero/internal/mouse"
	"github.com/shinyvision/posthero/internal/ui/theme"
)

// Option is one answer of a Choice.
type Option struct {
	Key   string // the key that picks it, shown in the button
	Label string
	Run   tea.Cmd
}

// Choice asks a question with several answers, like vim's confirm()
// dialogs: each answer has a key, h/l or tab move between them, ⏎ picks
// the highlighted one and esc cancels (runs nothing).
type Choice struct {
	Title, Body string
	Options     []Option
	// Cancel runs when the question is put away (esc, q); may be nil.
	Cancel tea.Cmd
	Danger bool // drawn in the warning color
	sel    int
	box    string
}

// Key implements Modal. An option's own key wins over q for cancel.
func (c *Choice) Key(k string) (bool, tea.Cmd) {
	for _, o := range c.Options {
		if o.Key == k {
			return true, o.Run
		}
	}
	switch k {
	case "<Esc>", "q":
		return true, c.Cancel
	case "h", "<Left>", "<S-Tab>", "k", "<Up>":
		c.sel = (c.sel - 1 + len(c.Options)) % len(c.Options)
	case "l", "<Right>", "<Tab>", "j", "<Down>":
		c.sel = (c.sel + 1) % len(c.Options)
	case "<CR>", " ":
		return true, c.Options[c.sel].Run
	default:
		for _, o := range c.Options {
			if strings.EqualFold(o.Key, k) {
				return true, o.Run
			}
		}
	}
	return false, nil
}

func (c *Choice) button(i int) string {
	o := c.Options[i]
	text := "[" + o.Key + "] " + o.Label
	if i == c.sel {
		return theme.AccentPill.Render(text)
	}
	return theme.MutedPill.Render(text)
}

// View implements Modal.
func (c *Choice) View(maxW, _ int) string {
	var buttons []string
	for i := range c.Options {
		buttons = append(buttons, c.button(i))
	}
	row := strings.Join(buttons, "  ")
	if lipgloss.Width(row) > maxW-8 {
		row = strings.Join(buttons, "\n")
	}
	color := theme.AccentFg
	if c.Danger {
		color = theme.Warn
	}
	c.box = frame(c.Title, color, []string{c.Body, "", row}, "a key or ⏎ picks · h/l move · esc cancels", maxW)
	return c.box
}

// Mouse implements Mouser: clicking a button picks it, a click outside
// cancels.
func (c *Choice) Mouse(ev mouse.Event) (bool, tea.Cmd) {
	if ev.Action != mouse.Press || ev.Button != tea.MouseLeft {
		return false, nil
	}
	if !inside(c.box, ev) {
		return true, nil
	}
	for i, o := range c.Options {
		text := " [" + o.Key + "] " + o.Label + " "
		if x, y, ok := find(c.box, text); ok && ev.Y == y && ev.X >= x && ev.X < x+lipgloss.Width(text) {
			return true, c.Options[i].Run
		}
	}
	return false, nil
}
