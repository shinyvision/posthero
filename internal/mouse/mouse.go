// Package mouse describes mouse events in the coordinates of whatever
// receives them, so panes, dialogs and editors need not know where on the
// screen they are drawn.
package mouse

import tea "charm.land/bubbletea/v2"

// Action is what the mouse did.
type Action int

// Actions.
const (
	Press Action = iota
	Release
	// Drag is motion with a button held.
	Drag
	Wheel
)

// Event is a mouse event relative to its receiver's top-left corner. Drags
// and releases go to whatever received the press, so they can fall outside
// it (negative or beyond its size).
type Event struct {
	Action Action
	Button tea.MouseButton
	Mod    tea.KeyMod
	X, Y   int
	// ScreenX and ScreenY are absolute, for placing popups at the pointer.
	ScreenX, ScreenY int
	// Clicks counts quick presses on the same cell: 2 for a double click.
	Clicks int
}

// Offset returns the event relative to a receiver drawn at (x, y).
func (e Event) Offset(x, y int) Event {
	e.X, e.Y = e.X-x, e.Y-y
	return e
}

// In reports whether the event falls within a w×h area.
func (e Event) In(w, h int) bool {
	return e.X >= 0 && e.Y >= 0 && e.X < w && e.Y < h
}

// Clamp moves the event into a w×h area, e.g. for a drag that left it.
func (e Event) Clamp(w, h int) Event {
	e.X = max(0, min(e.X, w-1))
	e.Y = max(0, min(e.Y, h-1))
	return e
}

// Left reports a left-button press or drag.
func (e Event) Left() bool {
	return e.Button == tea.MouseLeft && (e.Action == Press || e.Action == Drag)
}

// WheelDelta returns the vertical and horizontal scroll of a wheel event in
// steps: positive is down or right. Shift turns vertical scrolling sideways.
func (e Event) WheelDelta() (dy, dx int) {
	if e.Action != Wheel {
		return 0, 0
	}
	switch e.Button {
	case tea.MouseWheelUp:
		dy = -1
	case tea.MouseWheelDown:
		dy = 1
	case tea.MouseWheelLeft:
		dx = -1
	case tea.MouseWheelRight:
		dx = 1
	}
	if e.Mod.Contains(tea.ModShift) && dx == 0 {
		dy, dx = 0, dy
	}
	return dy, dx
}
