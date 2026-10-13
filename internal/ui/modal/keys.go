package modal

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/shinyvision/posthero/internal/mouse"
	"github.com/shinyvision/posthero/internal/ui/theme"
)

// KeyGroup is a titled column of a key menu.
type KeyGroup struct {
	Title string
	Items []KeyItem
}

// KeyItem is an entry of a key menu: pressing Key runs it.
type KeyItem struct {
	Key, Label string
	Run        func() tea.Cmd
}

// Keys is a which-key style menu: every action with its key, in columns.
// Pressing a key runs its action; esc closes.
type Keys struct {
	Title  string
	Groups []KeyGroup
	// Footer is a dim line under the columns.
	Footer string
	box    string
	spots  []spot // where items were drawn, for clicks
}

type spot struct {
	x, y, w int
	item    KeyItem
}

// Key implements Modal.
func (k *Keys) Key(key string) (bool, tea.Cmd) {
	switch key {
	case "<Esc>", "q", "<C-c>", " ":
		return true, nil
	}
	for _, g := range k.Groups {
		for _, it := range g.Items {
			if it.Key == key {
				if it.Run == nil {
					return true, nil
				}
				return true, it.Run()
			}
		}
	}
	return false, nil
}

// View implements Modal.
func (k *Keys) View(maxW, _ int) string {
	colW := 0
	for _, g := range k.Groups {
		colW = max(colW, ansi.StringWidth(g.Title))
		for _, it := range g.Items {
			colW = max(colW, ansi.StringWidth(it.Key)+2+ansi.StringWidth(it.Label))
		}
	}
	colW += 3
	perRow := max(1, min(len(k.Groups), (maxW-6)/colW))
	var blocks []string
	k.spots = nil
	y := 2 // border and title line
	for start := 0; start < len(k.Groups); start += perRow {
		row := k.Groups[start:min(start+perRow, len(k.Groups))]
		height := 0
		for _, g := range row {
			height = max(height, len(g.Items)+1)
		}
		lines := make([]string, height)
		for c, g := range row {
			col := make([]string, height)
			col[0] = theme.Bold.Render(g.Title)
			for r, it := range g.Items {
				col[r+1] = theme.AccentFg.Bold(true).Render(it.Key) + "  " + it.Label
				k.spots = append(k.spots, spot{x: 2 + c*colW, y: y + 1 + r + 1, w: colW, item: it})
			}
			for r := range lines {
				lines[r] += theme.Fit(col[r], colW)
			}
		}
		y += height + 1
		blocks = append(blocks, strings.Join(lines, "\n"))
	}
	body := theme.AccentFg.Bold(true).Render(k.Title) + "\n\n" + strings.Join(blocks, "\n\n")
	if k.Footer != "" {
		body += "\n\n" + theme.Dim.Render(k.Footer)
	}
	k.box = boxStyle().Render(body)
	return k.box
}

// Mouse implements Mouser: click an item to run it; a click outside
// closes.
func (k *Keys) Mouse(ev mouse.Event) (bool, tea.Cmd) {
	if ev.Action != mouse.Press {
		return false, nil
	}
	if !inside(k.box, ev) {
		return true, nil
	}
	for _, s := range k.spots {
		if ev.Y == s.y && ev.X >= s.x && ev.X < s.x+s.w && s.item.Run != nil {
			return true, s.item.Run()
		}
	}
	return false, nil
}
