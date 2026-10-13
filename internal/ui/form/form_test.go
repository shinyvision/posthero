package form

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

func typeKeys(f *Form, keys ...string) (closed bool) {
	for _, k := range keys {
		if len(k) > 1 && !strings.HasPrefix(k, "<") {
			for _, r := range k {
				_, closed = f.Key(string(r))
			}
			continue
		}
		_, closed = f.Key(k)
	}
	return closed
}

func TestFormFlow(t *testing.T) {
	var submitted map[string]string
	f := &Form{Title: "t"}
	f.Rows = []*Row{
		{Label: "Name", Fields: []*Field{NewText("name", "", 20)}},
		{Label: "Type", Fields: []*Field{NewSelect("type", "", []string{"bigint", "integer", "text", "timestamp with time zone"}, 20)}},
		{Label: "Null", Fields: []*Field{NewToggle("nullable", true, 8)}},
		{Fields: []*Field{NewButton("Save", nil)}},
	}
	f.Submit = func(f *Form) tea.Cmd {
		submitted = map[string]string{"name": f.Field("name").Value(), "type": f.Field("type").Value()}
		if !f.Field("nullable").On() {
			submitted["nullable"] = "no"
		}
		return nil
	}
	// Type a name, Enter to the dropdown, filter by "time", pick it.
	typeKeys(f, "orders", "<CR>", "time", "<CR>")
	if got := f.Field("type").Value(); got != "timestamp with time zone" {
		t.Fatalf("type = %q", got)
	}
	if box, _, _, _ := f.View(120, 40); !strings.Contains(ansi.Strip(box), "orders") {
		t.Errorf("view misses the name:\n%s", box)
	}
	// Now on the toggle: switch it off, move to the button and press it.
	typeKeys(f, " ", "j", "<CR>")
	if submitted["name"] != "orders" || submitted["nullable"] != "no" {
		t.Fatalf("submitted %v", submitted)
	}
	if closed := typeKeys(f, "<Esc>"); !closed {
		t.Error("esc on a button should cancel the form")
	}
}

func TestItems(t *testing.T) {
	item := func() *Row { return &Row{Item: true, Fields: []*Field{NewText("col", "", 10)}} }
	f := &Form{Rows: []*Row{{Fields: []*Field{NewText("name", "", 10)}}, item()}, NewItem: item}
	typeKeys(f, "<Tab>", "<C-a>", "<C-a>")
	if n := len(f.Items()); n != 3 {
		t.Fatalf("items = %d", n)
	}
	typeKeys(f, "<C-x>")
	if n := len(f.Items()); n != 2 {
		t.Fatalf("items after remove = %d", n)
	}
}

func TestCancelBusy(t *testing.T) {
	cancelled := 0
	f := &Form{Title: "t", Busy: "exporting…"}
	f.Rows = []*Row{{Fields: []*Field{NewButton("Export", nil)}}}
	f.Submit = func(f *Form) tea.Cmd {
		f.OnCancel = func() { cancelled++ }
		return func() tea.Msg { return nil }
	}
	typeKeys(f, "<CR>")
	if !f.pending {
		t.Fatal("not busy")
	}
	hint := func() string { box, _, _, _ := f.View(120, 40); return ansi.Strip(box) }
	if !strings.Contains(hint(), "esc esc cancels") {
		t.Fatalf("hint:\n%s", hint())
	}
	// Other keys are ignored and disarm a single esc.
	if typeKeys(f, "<Esc>", "j", "<Esc>") || cancelled != 0 {
		t.Fatalf("cancelled %d", cancelled)
	}
	if !strings.Contains(hint(), "again to cancel") {
		t.Fatalf("armed hint:\n%s", hint())
	}
	typeKeys(f, "<Esc>")
	if cancelled != 1 || !strings.Contains(hint(), "cancelling") {
		t.Fatalf("cancelled %d:\n%s", cancelled, hint())
	}
	typeKeys(f, "<Esc>", "<Esc>")
	if cancelled != 1 {
		t.Fatal("cancelled twice")
	}
	// The cancelled result keeps the form open with the message.
	if _, closed := f.Update(ResultMsg{Err: errorString("export cancelled")}); closed || !strings.Contains(hint(), "export cancelled") {
		t.Fatalf("closed %v:\n%s", closed, hint())
	}
	if !typeKeys(f, "<Esc>") {
		t.Fatal("esc after cancelling should close the form")
	}
}

type errorString string

func (e errorString) Error() string { return string(e) }

func TestMaskedValueStaysHidden(t *testing.T) {
	tok := NewText("Token", "s3cret-token", 30)
	tok.SetMask(true)
	f := &Form{Title: "t", Rows: []*Row{{Label: "Name", Fields: []*Field{NewText("Name", "", 30)}}, {Label: "Token", Fields: []*Field{tok}}}}
	box, _, _, _ := f.View(100, 40)
	if strings.Contains(box, "s3cret") {
		t.Fatalf("token shown in the clear:\n%s", box)
	}
}
