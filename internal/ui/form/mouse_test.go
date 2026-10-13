package form

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/shinyvision/posthero/internal/mouse"
)

// clickOn clicks the first place the box shows text.
func clickOn(t *testing.T, f *Form, text string) (tea.Cmd, bool) {
	t.Helper()
	box, _, _, _ := f.View(120, 40)
	for y, line := range strings.Split(ansi.Strip(box), "\n") {
		if i := strings.Index(line, text); i >= 0 {
			x := ansi.StringWidth(line[:i])
			return f.Mouse(mouse.Event{Action: mouse.Press, Button: tea.MouseLeft, X: x, Y: y, Clicks: 1})
		}
	}
	t.Fatalf("%q is not on screen:\n%s", text, ansi.Strip(box))
	return nil, false
}

func TestFormMouse(t *testing.T) {
	var submitted bool
	f := &Form{Title: "t"}
	f.Rows = []*Row{
		{Label: "Name", Fields: []*Field{NewText("name", "orders", 20)}},
		{Label: "Type", Fields: []*Field{NewSelect("type", "", []string{"bigint", "integer", "text"}, 20)}},
		{Label: "Null", Fields: []*Field{NewToggle("nullable", true, 8)}},
		{Label: "cols", Item: true, Fields: []*Field{NewText("col", "", 10), NewToggle("pk", false, 6)}},
		{Fields: []*Field{NewButton("Save", nil), NewCancel()}},
	}
	f.Submit = func(*Form) tea.Cmd { submitted = true; return nil }

	// Click into the name, between "ord" and "ers", and type.
	clickOn(t, f, "orders")
	box, _, _, _ := f.View(120, 40)
	x, y := -1, -1
	for i, line := range strings.Split(ansi.Strip(box), "\n") {
		if j := strings.Index(line, "orders"); j >= 0 {
			x, y = ansi.StringWidth(line[:j])+3, i
		}
	}
	f.Mouse(mouse.Event{Action: mouse.Press, Button: tea.MouseLeft, X: x, Y: y})
	f.Key("_")
	if got := f.Field("name").Value(); got != "ord_ers" {
		t.Fatalf("typing after a click gave %q", got)
	}

	// Focus the dropdown by its label, then pick an option by clicking it.
	clickOn(t, f, "Type")
	clickOn(t, f, "integer")
	if got := f.Field("type").Value(); got != "integer" {
		t.Fatalf("picked %q", got)
	}

	clickOn(t, f, "○ no")
	if f.Field("nullable").On() {
		t.Fatal("clicking “no” should turn the toggle off")
	}
	clickOn(t, f, "○ no") // the pk toggle in the item row
	if !f.Field("pk").On() {
		t.Fatal("clicking a toggle in a row should flip it")
	}

	clickOn(t, f, "Save")
	if !submitted {
		t.Fatal("clicking Save should submit")
	}
	f.pending = false
	if _, closed := clickOn(t, f, "Cancel"); !closed {
		t.Fatal("clicking Cancel should close the form")
	}
}
