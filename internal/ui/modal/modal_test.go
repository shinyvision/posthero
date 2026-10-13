package modal

import (
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/shinyvision/posthero/internal/mouse"
)

func press(x, y int) mouse.Event {
	return mouse.Event{Action: mouse.Press, Button: tea.MouseLeft, X: x, Y: y, Clicks: 1}
}

func TestConfirmMouse(t *testing.T) {
	yes := func() tea.Msg { return "yes" }
	c := &Confirm{Title: "Drop", Body: "Really?", Yes: yes}
	box := c.View(80, 24)
	x, y, ok := find(box, " Yes    No ")
	if !ok {
		t.Fatalf("no buttons in\n%s", box)
	}
	if done, cmd := c.Mouse(press(x+7, y)); !done || cmd != nil {
		t.Fatal("clicking No should cancel")
	}
	if done, _ := c.Mouse(press(0, 1)); done {
		t.Fatal("clicking the body should do nothing")
	}
	if done, cmd := c.Mouse(press(x+1, y)); !done || cmd == nil || cmd() != "yes" {
		t.Fatal("clicking Yes should confirm")
	}
	if done, cmd := c.Mouse(press(-3, y)); !done || cmd != nil {
		t.Fatal("clicking outside should cancel")
	}
}

func TestMenuMouse(t *testing.T) {
	var ran string
	item := func(s string) Item { return Item{Label: s, Run: func() tea.Cmd { ran = s; return nil }} }
	m := &Menu{Title: "t", Items: []Item{item("one"), item("two")}}
	m.View(80, 24)
	m.Mouse(press(3, 3)) // border, title, then the items
	if ran != "two" {
		t.Fatalf("ran %q", ran)
	}
	ran = ""
	// Right press, drag onto an item, release.
	m.Mouse(mouse.Event{Action: mouse.Drag, Button: tea.MouseRight, X: 2, Y: 2})
	if done, _ := m.Mouse(mouse.Event{Action: mouse.Release, Button: tea.MouseRight, X: 2, Y: 2}); !done || ran != "one" {
		t.Fatalf("release ran %q", ran)
	}
	if done, _ := m.Mouse(press(50, 50)); !done {
		t.Fatal("clicking outside should close the menu")
	}
}

func TestPagerMouse(t *testing.T) {
	p := &Pager{Title: "help", Lines: make([]string, 100)}
	p.View(80, 24)
	p.Mouse(mouse.Event{Action: mouse.Wheel, Button: tea.MouseWheelDown})
	if p.top != 3 {
		t.Fatalf("wheel scrolled to %d", p.top)
	}
	if done, _ := p.Mouse(press(1, 1)); done {
		t.Fatal("clicking inside should keep the pager open")
	}
	if done, _ := p.Mouse(press(79, 1)); !done {
		t.Fatal("clicking outside should close the pager")
	}
}

// Dialogs must draw without crashing however small the screen.
func TestTinyViews(t *testing.T) {
	for w := range 3 {
		for h := range 3 {
			mods := []Modal{
				&Confirm{Title: "Drop", Body: "Really?"},
				&Message{Title: "Oops", Body: "it broke", IsError: true},
				&Input{Title: "Name", Value: "日本語", Hint: "a hint"},
				&Choice{Title: "Pick", Body: "which", Options: []Option{{Key: "a", Label: "A"}, {Key: "b", Label: "B"}}},
				&Menu{Title: "Menu", Items: []Item{{Label: "Open", Key: "o"}, {Label: "Close", Key: "c"}}},
				&Keys{Title: "Keys", Groups: []KeyGroup{{Title: "g", Items: []KeyItem{{Key: "x", Label: "do x"}}}}, Footer: "f"},
				&Pager{Title: "Help", Lines: []string{"a", "b"}, Wide: true},
			}
			for _, m := range mods {
				m.View(w, h)
				m.Key("j")
				m.View(w, h)
			}
		}
	}
}
