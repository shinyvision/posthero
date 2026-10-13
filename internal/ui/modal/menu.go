package modal

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/shinyvision/posthero/internal/mouse"
	"github.com/shinyvision/posthero/internal/ui/theme"
)

// Item is a menu entry. Key is the keyboard shortcut shown next to it, as a
// reminder; it also picks the item while the menu is open.
type Item struct {
	Label, Key string
	Run        func() tea.Cmd
}

// Menu is a context menu, opened with a right click at X, Y.
type Menu struct {
	Title string
	Items []Item
	X, Y  int
	sel   int
	box   string
}

// Anchor implements Anchored.
func (m *Menu) Anchor() (int, int) { return m.X, m.Y }

// Key implements Modal.
func (m *Menu) Key(k string) (bool, tea.Cmd) {
	switch k {
	case "<Esc>", "q":
		return true, nil
	case "j", "<Down>", "<C-n>", "<Tab>":
		m.sel = (m.sel + 1) % len(m.Items)
	case "k", "<Up>", "<C-p>", "<S-Tab>":
		m.sel = (m.sel - 1 + len(m.Items)) % len(m.Items)
	case "<CR>", " ":
		return m.run(m.sel)
	default:
		for i, it := range m.Items {
			if it.Key == k {
				return m.run(i)
			}
		}
	}
	return false, nil
}

func (m *Menu) run(i int) (bool, tea.Cmd) {
	if i < 0 || i >= len(m.Items) || m.Items[i].Run == nil {
		return true, nil
	}
	return true, m.Items[i].Run()
}

// Mouse implements Mouser: dragging highlights, a click or a release over
// an item runs it (so press, drag and release works too), and a click
// outside closes the menu.
func (m *Menu) Mouse(ev mouse.Event) (bool, tea.Cmd) {
	// The box has a border and a title line above the items.
	i := ev.Y - 2
	over := inside(m.box, ev) && i >= 0 && i < len(m.Items)
	switch ev.Action {
	case mouse.Wheel:
		if dy, _ := ev.WheelDelta(); dy != 0 {
			m.sel = (m.sel + dy + len(m.Items)) % len(m.Items)
		}
	case mouse.Drag:
		if over {
			m.sel = i
		}
	case mouse.Press:
		if !inside(m.box, ev) {
			return true, nil
		}
		if over && ev.Button == tea.MouseLeft {
			return m.run(i)
		}
	case mouse.Release:
		// A right press opened the menu; releasing it over an item picks it.
		if over && ev.Button == tea.MouseRight && m.sel == i {
			return m.run(i)
		}
	}
	return false, nil
}

// View implements Modal.
func (m *Menu) View(maxW, _ int) string {
	labelW, keyW := lipgloss.Width(m.Title), 0
	for _, it := range m.Items {
		labelW = max(labelW, lipgloss.Width(it.Label))
		keyW = max(keyW, lipgloss.Width(it.Key))
	}
	w := min(labelW+keyW+3, maxW-4)
	lines := []string{theme.Dim.Render(theme.Truncate(m.Title, w))}
	for i, it := range m.Items {
		gap := max(1, w-lipgloss.Width(it.Label)-lipgloss.Width(it.Key))
		line := theme.Fit(it.Label+strings.Repeat(" ", gap)+theme.Dim.Render(it.Key), w)
		if i == m.sel {
			line = theme.Selected.Render(theme.Fit(it.Label+strings.Repeat(" ", gap)+it.Key, w))
		}
		lines = append(lines, line)
	}
	m.box = lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(theme.Accent).
		Render(strings.Join(lines, "\n"))
	return m.box
}
