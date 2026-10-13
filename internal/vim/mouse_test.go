package vim

import (
	"strings"
	"testing"
)

func TestMouse(t *testing.T) {
	e := New("select foo_bar from t\nwhere x = 1\n;", nil)
	e.Click(Pos{0, 9})
	if e.Cursor() != (Pos{0, 9}) || e.Mode() != Normal {
		t.Fatalf("click: cursor %v mode %v", e.Cursor(), e.Mode())
	}
	e.SelectWord(Pos{0, 9})
	if got := e.SelectedText(); got != "foo_bar" || e.Mode() != Visual {
		t.Fatalf("double click selected %q in %v", got, e.Mode())
	}
	e.Click(Pos{1, 0})
	e.DragTo(Pos{1, 4})
	if got := e.SelectedText(); got != "where" {
		t.Fatalf("drag selected %q", got)
	}
	e.SelectLine(Pos{1, 3})
	if _, _, linewise, ok := e.Selection(); !ok || !linewise {
		t.Fatal("triple click should select the line")
	}
	e.Click(Pos{0, 99})
	if e.Mode() != Normal || e.Cursor() != (Pos{0, 20}) {
		t.Fatalf("click past the end: cursor %v mode %v", e.Cursor(), e.Mode())
	}
}

func TestMouseInsert(t *testing.T) {
	e := New("abc def", nil)
	feed(e, "ix<Esc>u")
	feed(e, "A!")
	e.Click(Pos{0, 2})
	if e.Mode() != Insert || e.Cursor() != (Pos{0, 2}) {
		t.Fatalf("click in insert mode: cursor %v mode %v", e.Cursor(), e.Mode())
	}
	e.DragTo(Pos{0, 5})
	if e.Mode() != Visual || e.SelectedText() != "c de" {
		t.Fatalf("drag from insert: %v %q", e.Mode(), e.SelectedText())
	}
	feed(e, "<Esc>j.")
	if got := e.Text(); got != "abc def!!" {
		t.Fatalf("dot after a drag repeats %q", got)
	}
}

func TestMouseScroll(t *testing.T) {
	var lines []string
	for i := range 50 {
		lines = append(lines, strings.Repeat("x", i%7))
	}
	e := New(strings.Join(lines, "\n"), nil)
	e.SetHeight(10)
	e.Scroll(3)
	if e.Top() != 3 || e.Cursor().Line != 5 {
		t.Fatalf("scroll down: top %d cursor %v", e.Top(), e.Cursor())
	}
	e.Scroll(100)
	if e.Top() != 40 {
		t.Fatalf("scroll to the end: top %d", e.Top())
	}
	e.Scroll(-100)
	if e.Top() != 0 || e.Cursor().Line > 7 {
		t.Fatalf("scroll back: top %d cursor %v", e.Top(), e.Cursor())
	}
	// Keys after scrolling must not jump the view.
	e.Scroll(5)
	top := e.Top()
	feed(e, "l")
	if e.Top() != top {
		t.Fatalf("a key moved the view from %d to %d", top, e.Top())
	}
}
