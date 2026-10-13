package app

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/shinyvision/posthero/internal/ui/modal"
	"github.com/shinyvision/posthero/internal/ui/respview"
	"github.com/shinyvision/posthero/internal/ui/theme"
	"github.com/shinyvision/posthero/internal/ui/tree"
)

type rect struct{ x, y, w, h int }

func (r rect) visible() bool { return r.w > 0 && r.h > 0 }

func (r rect) contains(x, y int) bool {
	return r.visible() && x >= r.x && x < r.x+r.w && y >= r.y && y < r.y+r.h
}

// layout is where the panes go.
type layout struct {
	tree, editor, resp rect
	statusY            int
}

func (l layout) rect(p pane) rect {
	switch p {
	case paneTree:
		return l.tree
	case paneEditor:
		return l.editor
	}
	return l.resp
}

// treeWidth is the tree's width: as set, else a quarter of the screen.
func (m *Model) treeWidth() int {
	w := m.state.TreeWidth
	if w == 0 {
		w = max(28, min(46, m.w/4))
	}
	return max(16, min(m.w-30, w))
}

// editorShare is the editor's share of the right side's height.
func (m *Model) editorShare() int {
	if s := m.state.EditorShare; s > 0 {
		return max(10, min(90, s))
	}
	return 42
}

func (m *Model) layout() layout {
	w, h := max(m.w, 40), max(m.h, 8)
	l := layout{statusY: h - 1}
	main := h - 1
	if m.zoomed {
		switch m.focus {
		case paneTree:
			l.tree = rect{0, 0, w, main}
		case paneEditor:
			l.editor = rect{0, 0, w, main}
		default:
			l.resp = rect{0, 0, w, main}
		}
		return l
	}
	x := 0
	if !m.state.TreeHidden {
		tw := m.treeWidth()
		if w < 70 {
			tw = w / 3
		}
		l.tree = rect{0, 0, tw, main}
		x = tw
	}
	right := w - x
	eh := max(4, min(main-4, main*m.editorShare()/100))
	l.editor = rect{x, 0, right, eh}
	l.resp = rect{x, eh, right, main - eh}
	return l
}

// View implements tea.Model.
func (m *Model) View() tea.View {
	v := tea.NewView("")
	v.AltScreen = true
	v.ReportFocus = true
	if m.settings.Mouse {
		v.MouseMode = tea.MouseModeCellMotion
	}
	v.WindowTitle = m.windowTitle()
	if m.w == 0 {
		return v
	}
	l := m.layout()
	free := m.overlayFree()
	var left, right []string
	if l.tree.visible() {
		left = append(left, m.treePane(l.tree, m.focus == paneTree && free))
	}
	if l.editor.visible() {
		right = append(right, m.editorBox(l.editor, m.focus == paneEditor && free))
	}
	if l.resp.visible() {
		right = append(right, m.respPane(l.resp, m.focus == paneResp && free))
	}
	var body string
	switch {
	case len(left) > 0 && len(right) > 0:
		body = lipgloss.JoinHorizontal(lipgloss.Top, left[0], strings.Join(right, "\n"))
	case len(left) > 0:
		body = left[0]
	default:
		body = strings.Join(right, "\n")
	}
	base := body + "\n" + m.statusBar(m.w)
	v.Content = base
	v.Cursor = m.cursor(l)

	var layers []*lipgloss.Layer
	if wild := m.wildmenu(m.w); wild != "" && m.cmdline != nil {
		layers = append(layers, lipgloss.NewLayer(wild).X(0).Y(l.statusY-strings.Count(wild, "\n")-1).Z(1))
	}
	if m.edit != nil {
		box, cx, cy, ok, shape := m.edit.View(m.w, m.h)
		ex, ey := center(m.w, m.h, box)
		m.ms.edit = rect{ex, ey, lipgloss.Width(box), lipgloss.Height(box)}
		layers = append(layers, lipgloss.NewLayer(box).X(ex).Y(ey).Z(1))
		v.Cursor = nil
		if ok {
			v.Cursor = tea.NewCursor(ex+cx, ey+cy)
			v.Cursor.Shape = shape
		}
	}
	var overlay string
	ox, oy := 0, 0
	switch {
	case len(m.modals) > 0:
		top := m.modals[len(m.modals)-1]
		overlay = top.View(m.w, m.h)
		ox, oy = center(m.w, m.h, overlay)
		if a, ok := top.(modal.Anchored); ok {
			if ax, ay := a.Anchor(); ax >= 0 && ay >= 0 {
				ox = max(0, min(ax, m.w-lipgloss.Width(overlay)))
				oy = max(0, min(ay, m.h-lipgloss.Height(overlay)))
			}
		}
		v.Cursor = nil
		if c, ok := top.(interface{ Cursor() (int, int) }); ok {
			cx, cy := c.Cursor()
			v.Cursor = tea.NewCursor(ox+cx, oy+cy)
			v.Cursor.Shape = tea.CursorBar
		}
	case m.form != nil:
		box, cx, cy, ok := m.form.View(m.w, m.h)
		overlay = box
		ox, oy = center(m.w, m.h, box)
		v.Cursor = nil
		if ok {
			v.Cursor = tea.NewCursor(ox+cx, oy+cy)
			v.Cursor.Shape = tea.CursorBar
		}
	}
	m.ms.modal = rect{ox, oy, lipgloss.Width(overlay), lipgloss.Height(overlay)}
	if overlay != "" {
		layers = append(layers, lipgloss.NewLayer(overlay).X(ox).Y(oy).Z(2))
	}
	if len(layers) > 0 {
		v.Content = lipgloss.NewCompositor(append([]*lipgloss.Layer{lipgloss.NewLayer(base)}, layers...)...).Render()
	}
	return v
}

func (m *Model) windowTitle() string {
	title := "posthero"
	if m.open != nil {
		if _, it, _ := m.findItem(m.open.colID, m.open.itemID); it != nil {
			title = it.Name + " · posthero"
		}
	}
	return title
}

func (m *Model) overlayFree() bool {
	return m.form == nil && m.edit == nil && len(m.modals) == 0
}

func center(w, h int, box string) (int, int) {
	bw, bh := lipgloss.Width(box), lipgloss.Height(box)
	return max(0, (w-bw)/2), max(0, (h-bh)/3)
}

// treePane draws the tree in a box titled with the workspace.
func (m *Model) treePane(r rect, focused bool) string {
	title := "posthero"
	if m.st != nil {
		title = orName(m.st.Workspace.Name, title)
	}
	footer := ""
	switch {
	case m.st == nil && m.form == nil:
		footer = "connecting…"
	case !m.loaded:
		footer = "loading…"
	case m.cut != nil:
		footer = "p puts it · esc keeps it"
	}
	lines := m.tree.View(r.w-2, r.h-2, focused)
	return theme.Box(title, footer, lines, r.w, r.h, focused)
}

// editorBox draws the editor titled with the request.
func (m *Model) editorBox(r rect, focused bool) string {
	title := "request"
	footer := ""
	if m.open != nil {
		if c, it, parents := m.findItem(m.open.colID, m.open.itemID); it != nil {
			title = it.Name
			var path []string
			for _, p := range parents {
				path = append(path, p.Name)
			}
			if len(path) > 0 {
				title = strings.Join(path, " / ") + " / " + it.Name
			} else if c != nil {
				title = c.Name() + " / " + it.Name
			}
			if _, draft := m.drafts.get(it.ID); draft {
				title += " [+]"
			}
		}
	}
	return m.editor.view(r.w, r.h, focused, title, footer)
}

// respPane draws the response (or the run's list).
func (m *Model) respPane(r rect, focused bool) string {
	iw, ih := r.w-2, r.h-2
	if m.showRun && m.runv != nil {
		return theme.Box(m.runv.Heading(), "⏎ shows a request · n next failure", m.runv.View(iw, ih), r.w, r.h, focused)
	}
	lines := m.resp.View(iw, ih)
	title, footer := "response", ""
	if rec := m.resp.Record(); rec != nil {
		switch {
		case rec.Example:
			title = "example " + theme.Status(rec.Status).Render(fmt.Sprintf("%d %s", rec.Status, rec.StatusText))
		case rec.Error != "":
			title = theme.Error.Render("no response")
		case rec.Skipped:
			title = "skipped"
		default:
			title = theme.Status(rec.Status).Render(fmt.Sprintf("%d %s", rec.Status, rec.StatusText)) + theme.Dim.Render(" · "+respview.Duration(rec.Duration))
		}
		if m.runv != nil && !m.showRun {
			footer = "<BS> back to the run"
		} else if !rec.At.IsZero() && !rec.Example {
			footer = rec.At.Local().Format("15:04:05")
		}
		if jq := m.resp.JQ(); jq != "" {
			footer = "| " + jq + " · esc shows the body"
		}
		if r.h > 4 {
			line, total := m.resp.Position()
			if total > r.h-2 {
				footer = strings.TrimSpace(footer + fmt.Sprintf("  %d/%d", line, total))
			}
		}
	}
	if m.resp.Busy != "" {
		title = theme.Warn.Render(m.spinner() + " " + m.resp.Busy)
	}
	return theme.Box(title, footer, lines, r.w, r.h, focused)
}

func (m *Model) spinner() string {
	frames := []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}
	return frames[m.spin%len(frames)]
}

// statusBar is the bottom line: the command line when it is open, else the
// environment, the login, a message and the mode.
func (m *Model) statusBar(w int) string {
	if m.cmdline != nil {
		line, _ := m.cmdline.View(w)
		return line
	}
	var left []string
	// The environment of what the keys act on now (E, s, S).
	colID := m.currentCol()
	if colID != "" && m.st != nil {
		env := m.envName(colID)
		if env == "" {
			left = append(left, theme.MutedPill.Render("no environment"))
		} else {
			left = append(left, theme.EnvPill(isProd(env)).Render(env))
		}
	}
	if a := m.authLine(); a != "" {
		left = append(left, a)
	}
	if m.local != nil {
		pill := "local"
		if m.localChanges > 0 {
			pill += fmt.Sprintf(" · %d to push", m.localChanges)
		}
		left = append(left, theme.WarnPill.Render(pill))
	}
	msg := m.status
	style := theme.Dim
	if m.isError {
		style = theme.Error
	} else if msg != "" {
		style = theme.Plain
	}
	if note := m.busyNote(); note != "" && (msg == "" || !m.isError) {
		msg, style = m.spinner()+" "+note, theme.Dim
	}
	right := ""
	if m.focus == paneEditor {
		if mode := m.editor.mode(); mode != "" && mode != "NORMAL" {
			right = theme.Bold.Render("-- " + mode + " --")
		}
	}
	if m.keys.pending != "" {
		right = m.keys.pending + " " + right
	}
	right = strings.TrimSpace(right + "  " + theme.Dim.Render("? help"))
	l := strings.Join(left, " ")
	room := w - theme.Width(l) - theme.Width(right) - 3
	text := ""
	if room > 4 && msg != "" {
		text = style.Render(theme.Truncate(msg, room))
	}
	gap := w - theme.Width(l) - theme.Width(text) - theme.Width(right) - 2
	if gap < 1 {
		return theme.Fit(l+" "+text, w)
	}
	return " " + l + " " + text + strings.Repeat(" ", gap) + right
}

// busyNote is what work in progress says.
func (m *Model) busyNote() string {
	for _, n := range m.busy {
		if n != "" {
			return n
		}
	}
	return ""
}

// cursor is where the terminal cursor goes: on the command line, in a
// pane's prompt, or in the editor.
func (m *Model) cursor(l layout) *tea.Cursor {
	switch {
	case m.cmdline != nil:
		_, x := m.cmdline.View(m.w)
		c := tea.NewCursor(x, l.statusY)
		c.Shape = tea.CursorBar
		return c
	case !m.overlayFree():
		return nil
	case m.focus == paneEditor && l.editor.visible():
		x, y, shape, ok := m.editor.cursor()
		if !ok {
			return nil
		}
		c := tea.NewCursor(l.editor.x+1+x, l.editor.y+1+y)
		c.Shape = shape
		return c
	}
	return nil
}

// focusPane moves the keyboard to a pane, opening it when it is closed.
func (m *Model) focusPane(p pane) {
	if m.zoomed && p != m.focus {
		m.zoomed = false
	}
	if p == paneTree && m.state.TreeHidden {
		m.state.TreeHidden = false
	}
	if p != m.focus {
		m.prevFocus = m.focus
	}
	m.focus = p
}

// moveFocus goes to the pane in a direction, like <C-w>hjkl.
func (m *Model) moveFocus(dir string) {
	l := m.layout()
	cur := l.rect(m.focus)
	best, bestDist := pane(-1), 1<<30
	for _, p := range []pane{paneTree, paneEditor, paneResp} {
		r := l.rect(p)
		if p == m.focus || !r.visible() {
			continue
		}
		var ok bool
		var dist int
		switch dir {
		case "h":
			ok, dist = r.x+r.w <= cur.x, cur.x-(r.x+r.w)
		case "l":
			ok, dist = r.x >= cur.x+cur.w, r.x-(cur.x+cur.w)
		case "k":
			ok, dist = r.y+r.h <= cur.y && overlaps(r.x, r.w, cur.x, cur.w), cur.y-(r.y+r.h)
		case "j":
			ok, dist = r.y >= cur.y+cur.h && overlaps(r.x, r.w, cur.x, cur.w), r.y-(cur.y+cur.h)
		}
		if ok && dist < bestDist {
			best, bestDist = p, dist
		}
	}
	// Nothing is below or above the tree: down goes to the response, up
	// to the request.
	if best < 0 && m.focus == paneTree && (dir == "j" || dir == "k") {
		best = map[string]pane{"j": paneResp, "k": paneEditor}[dir]
		if !l.rect(best).visible() {
			best = -1
		}
	}
	if best >= 0 {
		m.focusPane(best)
		return
	}
	if dir == "h" && m.state.TreeHidden {
		m.focusPane(paneTree)
	}
}

func overlaps(a, aw, b, bw int) bool { return a < b+bw && b < a+aw }

// wincmd runs a <C-w> window command on posthero's panes.
func (m *Model) wincmd(k string) tea.Cmd {
	switch k {
	case "<C-h>", "<C-j>", "<C-k>", "<C-l>", "<C-w>", "<C-p>", "<C-o>", "<C-c>", "<C-q>", "<C-t>", "<C-b>":
		k = k[3:4]
	case "<Left>":
		k = "h"
	case "<Down>":
		k = "j"
	case "<Up>":
		k = "k"
	case "<Right>":
		k = "l"
	}
	switch k {
	case "h", "j", "k", "l":
		m.moveFocus(k)
	case "w", "W":
		order := []pane{paneTree, paneEditor, paneResp}
		l := m.layout()
		d := 1
		if k == "W" {
			d = len(order) - 1
		}
		for i := 1; i <= len(order); i++ {
			p := order[(int(m.focus)+i*d)%len(order)]
			if l.rect(p).visible() {
				m.focusPane(p)
				break
			}
		}
	case "p":
		m.focusPane(m.prevFocus)
	case "t":
		m.focusPane(paneTree)
	case "b":
		m.focusPane(paneResp)
	case "o":
		m.zoomed = !m.zoomed
	case "c", "q":
		if m.focus == paneTree {
			m.state.TreeHidden = true
			m.focusPane(paneEditor)
		} else {
			m.setStatus("the editor and response stay; <C-w>o shows one alone", false)
		}
	case "=":
		m.zoomed = false
		m.state.TreeWidth, m.state.EditorShare, m.state.TreeHidden = 0, 0, false
		m.setStatus("panes back to their sizes", false)
	case "+", "-":
		d := 5
		if k == "-" {
			d = -5
		}
		if m.focus == paneResp {
			d = -d
		}
		m.state.EditorShare = m.editorShare() + d
	case "<", ">":
		d := 3
		if k == "<" {
			d = -3
		}
		if m.focus != paneTree {
			d = -d
		}
		m.state.TreeWidth = m.treeWidth() + d
	case "_":
		if m.focus == paneResp {
			m.state.EditorShare = 10
		} else {
			m.state.EditorShare = 90
		}
	case "|":
		m.state.TreeWidth = m.w - 30
	case "quit", "quit!":
		return m.requestQuit(k == "quit!")
	case "writequit", "writequit!":
		// :wq, :x or ZZ in a request: the editor wrote the buffer (when it
		// had something to write) just before. Quit once that save to
		// Postman is done; a request with changes Postman doesn't have
		// (whose buffer had nothing new to write) is saved first. A save
		// that can't happen (a mistake in the text) keeps posthero open.
		m.quitAfterSave, m.quitAfterSaveBang = true, k == "writequit!"
		if m.savesInFlight > 0 || m.open == nil {
			return nil
		}
		if d, ok := m.drafts.get(m.open.itemID); ok {
			return m.saveRequest(m.open.colID, m.open.itemID, d)
		}
		return nil
	case "<Esc>":
	default:
		m.setStatus("not a window command: <C-w>"+k, true)
	}
	return nil
}

// treeRow is the tree row at a screen position, for the mouse.
func (m *Model) treeRow(y int) *tree.Node {
	l := m.layout()
	row := y - l.tree.y - 1 + m.tree.Top()
	rows := m.tree.Rows()
	if row < 0 || row >= len(rows) {
		return nil
	}
	return rows[row]
}
