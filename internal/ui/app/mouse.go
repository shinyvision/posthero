package app

import (
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/shinyvision/posthero/internal/mouse"
	"github.com/shinyvision/posthero/internal/ui/modal"
	"github.com/shinyvision/posthero/internal/ui/tree"
)

// doubleClick is the longest gap between the presses of a double click.
const doubleClick = 400 * time.Millisecond

// mouseState tracks clicks across mouse messages, and where the last View
// drew the dialogs.
type mouseState struct {
	held   tea.MouseButton
	last   time.Time
	lastAt tea.Mouse
	clicks int
	target pane // the pane that got the press, for drags

	modal, edit rect
}

func (s *mouseState) clickCount(p tea.Mouse) int {
	now := time.Now()
	if p == s.lastAt && now.Sub(s.last) < doubleClick {
		s.clicks++
	} else {
		s.clicks = 1
	}
	s.last, s.lastAt = now, p
	return s.clicks
}

// mouse routes a mouse message: dialogs first, then the pane under the
// pointer, which a click also focuses.
func (m *Model) mouse(msg tea.MouseMsg) tea.Cmd {
	p := msg.Mouse()
	ev := mouse.Event{Button: p.Button, Mod: p.Mod, X: p.X, Y: p.Y, ScreenX: p.X, ScreenY: p.Y}
	switch msg.(type) {
	case tea.MouseClickMsg:
		ev.Action = mouse.Press
		ev.Clicks = m.ms.clickCount(p)
		m.ms.held = p.Button
	case tea.MouseReleaseMsg:
		ev.Action = mouse.Release
		ev.Button = m.ms.held
		m.ms.held = tea.MouseNone
	case tea.MouseMotionMsg:
		if m.ms.held == tea.MouseNone {
			return nil
		}
		ev.Action = mouse.Drag
		ev.Button = m.ms.held
	case tea.MouseWheelMsg:
		ev.Action = mouse.Wheel
	}
	if n := len(m.modals); n > 0 {
		top := m.modals[n-1]
		if mm, ok := top.(modal.Mouser); ok {
			r := m.ms.modal
			done, cmd := mm.Mouse(ev.Offset(r.x, r.y))
			if done {
				m.modals = m.modals[:n-1]
			}
			return cmd
		}
		return nil
	}
	if m.form != nil {
		r := m.ms.modal
		cmd, closed := m.form.Mouse(ev.Offset(r.x, r.y))
		if closed {
			m.form = nil
			if f := m.formClosed; f != nil {
				m.formClosed = nil
				f()
			}
			if m.api == nil && m.st == nil {
				return m.quitWithoutLogin()
			}
		}
		return cmd
	}
	if m.edit != nil {
		r := m.ms.edit
		m.edit.Mouse(ev.Offset(r.x, r.y))
		return nil
	}
	if m.cmdline != nil && ev.Action == mouse.Press {
		m.cmdline = nil
	}
	l := m.layout()
	at := paneTree
	switch {
	case ev.Action == mouse.Drag || ev.Action == mouse.Release:
		at = m.ms.target
	case l.editor.contains(ev.X, ev.Y):
		at = paneEditor
	case l.resp.contains(ev.X, ev.Y):
		at = paneResp
	case l.tree.contains(ev.X, ev.Y):
		at = paneTree
	default:
		return nil
	}
	if ev.Action == mouse.Press {
		m.ms.target = at
		if m.focus != at {
			m.focusPane(at)
		}
	}
	dy, _ := ev.WheelDelta()
	switch at {
	case paneTree:
		return m.treeMouse(ev, dy)
	case paneEditor:
		m.editor.mouse(ev.Offset(l.editor.x+1, l.editor.y+1))
		return nil
	case paneResp:
		inner := ev.Offset(l.resp.x+1, l.resp.y+1)
		if m.showRun && m.runv != nil {
			switch {
			case ev.Action == mouse.Wheel && dy != 0:
				k := "j"
				if dy < 0 {
					k = "k"
				}
				for range 3 {
					m.runv.Key(k)
				}
			case ev.Action == mouse.Press && ev.Left() && ev.Clicks >= 2:
				return m.showRunStep()
			}
			return nil
		}
		switch {
		case ev.Action == mouse.Wheel && dy != 0:
			m.resp.Scroll(dy * 3)
		case ev.Action == mouse.Press && ev.Left():
			m.resp.Click(inner.Y, ev.Clicks == 2)
		case ev.Action == mouse.Press && ev.Button == tea.MouseRight:
			return m.menuAt(ev.X, ev.Y)
		}
	}
	return nil
}

// treeMouse: a click picks a row (and shows the request), a double click
// opens it or folds the folder, a right click shows what can be done.
func (m *Model) treeMouse(ev mouse.Event, dy int) tea.Cmd {
	switch {
	case ev.Action == mouse.Wheel && dy != 0:
		before := m.tree.Current()
		m.tree.Scroll(dy * 3)
		if n := m.tree.Current(); n != nil && n != before {
			return m.preview(n)
		}
	case ev.Action == mouse.Press:
		n := m.treeRow(ev.Y)
		if n == nil {
			return nil
		}
		m.tree.Select(n.ID())
		switch {
		case ev.Button == tea.MouseRight:
			return m.menuAt(ev.X, ev.Y)
		case !ev.Left():
		case n.Kind != tree.Request:
			if ev.Clicks == 1 {
				m.tree.Toggle()
			}
		case ev.Clicks >= 2:
			return m.openItem(n.Col.Summary.ID, n.Item.ID, true)
		default:
			return m.preview(n)
		}
	}
	return nil
}

// menuAt opens the actions menu at the pointer.
func (m *Model) menuAt(x, y int) tea.Cmd {
	cmd := m.menu()
	if cmd == nil {
		return nil
	}
	return func() tea.Msg {
		msg := cmd()
		if s, ok := msg.(modal.ShowMsg); ok {
			if menu, ok := s.Modal.(*modal.Menu); ok {
				menu.X, menu.Y = x, y
			}
		}
		return msg
	}
}
