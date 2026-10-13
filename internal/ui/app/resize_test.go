package app

import (
	"testing"

	tea "charm.land/bubbletea/v2"
)

// ctrl+shift+hjkl move the borders between the panes, from any pane (the
// editor too), and so does dragging them with the mouse.
func TestResizePanes(t *testing.T) {
	d := newDriver(t)
	start := d.m.layout()
	tw, eh := start.tree.w, start.editor.h
	size := func(what string, wantTree, wantEditor int) {
		t.Helper()
		l := d.m.layout()
		if l.tree.w != wantTree || l.editor.h != wantEditor {
			t.Fatalf("%s: tree %d wide, editor %d high; want %d and %d", what, l.tree.w, l.editor.h, wantTree, wantEditor)
		}
		if l.resp.y != l.editor.h || l.editor.h+l.resp.h != 44 || l.editor.x != l.tree.w || l.editor.w != 160-l.tree.w {
			t.Fatalf("%s: the panes don't fit: %+v", what, l)
		}
	}
	d.keys("<C-S-l>", "<C-S-l>")
	size("ctrl+shift+l twice", tw+4, eh)
	d.keys("<C-S-h>")
	size("ctrl+shift+h", tw+2, eh)
	d.keys("<C-S-j>")
	size("ctrl+shift+j", tw+2, eh+1)
	d.keys("<C-S-k>", "<C-S-k>")
	size("ctrl+shift+k twice", tw+2, eh-1)

	// In the editor, in insert mode too.
	d.keys("<C-l>")
	if d.m.focus != paneEditor {
		t.Fatalf("focus on %v", d.m.focus)
	}
	d.keys("i", "<C-S-j>", "<C-S-l>", "<Esc>")
	size("in the editor", tw+4, eh)
	if d.m.focus != paneEditor {
		t.Fatalf("resizing moved the focus to %v", d.m.focus)
	}

	// The response and the request keep a few rows; the tree a few cells,
	// the request more.
	for range 60 {
		d.handle(keyMsg("<C-S-j>"))
		d.handle(keyMsg("<C-S-l>"))
	}
	size("as far as they go", 130, 40)
	for range 80 {
		d.handle(keyMsg("<C-S-k>"))
		d.handle(keyMsg("<C-S-h>"))
	}
	size("as far back", 16, 4)

	// posthero remembers the sizes.
	if d.m.state.TreeWidth != 16 || d.m.state.EditorShare <= 0 {
		t.Fatalf("state: %d, %v", d.m.state.TreeWidth, d.m.state.EditorShare)
	}
	d.keys("<C-w>=")
	size("<C-w>=", tw, eh)

	// The mouse drags the border between the tree and the request, and the
	// line between the request and the response.
	d.m.settings.Mouse = true
	drag := func(x, y, toX, toY int) {
		d.handle(tea.MouseClickMsg{X: x, Y: y, Button: tea.MouseLeft})
		d.handle(tea.MouseMotionMsg{X: (x + toX) / 2, Y: (y + toY) / 2, Button: tea.MouseLeft})
		d.handle(tea.MouseMotionMsg{X: toX, Y: toY, Button: tea.MouseLeft})
		d.handle(tea.MouseReleaseMsg{X: toX, Y: toY, Button: tea.MouseLeft})
		d.settle()
	}
	drag(tw-1, 10, tw+11, 12)
	size("dragging the tree's border", tw+12, eh)
	drag(tw+12, 20, tw+2, 20)
	size("dragging the request's border", tw+2, eh)
	drag(tw+30, eh, tw+30, eh-6)
	size("dragging the response's border", tw+2, eh-6)
	drag(tw+30, eh-7, tw+40, eh-4)
	size("dragging the request's bottom border", tw+2, eh-3)
	// A click on a pane is a click, not a drag.
	before := d.m.layout()
	drag(tw+30, eh+5, tw+20, eh+10)
	if after := d.m.layout(); after != before {
		t.Fatalf("a drag in the response moved the borders: %+v", after)
	}

	// With a pane zoomed there are no borders to move.
	d.keys("<C-w>o", "<C-S-l>")
	d.see("<C-w>o shows them all")
	d.keys("<C-w>o")
	size("after the zoom", tw+2, eh-3)
}
