package lineinput

import (
	"strings"
	"testing"

	"github.com/shinyvision/posthero/internal/ui/theme"
	"github.com/shinyvision/posthero/internal/vim"
)

// The cursor and clicks are in cells: wide characters take two.
func TestWideCharacters(t *testing.T) {
	m := New("> ", "日本語", nil)
	line, x := m.View(40)
	if x != 8 {
		t.Fatalf("cursor at x=%d, want 8 (%q)", x, line)
	}
	m.Key("<Esc>") // normal mode: on 語
	if _, x = m.View(40); x != 6 {
		t.Fatalf("normal mode cursor at x=%d, want 6", x)
	}
	for cell, col := range map[int]int{2: 0, 3: 0, 4: 1, 5: 1, 6: 2, 7: 2} {
		m.Click(cell)
		if got := m.eng.Cursor().Col; got != col {
			t.Errorf("click at x=%d: column %d, want %d", cell, got, col)
		}
	}
}

// Long wide text scrolls so the cursor stays inside the field.
func TestWideScroll(t *testing.T) {
	m := New("> ", strings.Repeat("日本語", 10), nil)
	for _, w := range []int{0, 1, 2, 3, 4, 7, 10, 11} {
		line, x := m.View(w)
		if theme.Width(line) > w || x > w-1 && w > 3 {
			t.Errorf("width %d: line %q is %d cells, cursor at %d", w, line, theme.Width(line), x)
		}
	}
	m.Key("<Esc>")
	m.Key("0")
	m.Key("5")
	m.Key("l") // on the sixth character
	line, x := m.View(10)
	if x < 2 || x > 9 {
		t.Fatalf("cursor at %d in %q", x, line)
	}
	m.Click(x)
	if got := m.eng.Cursor().Col; got != 5 {
		t.Fatalf("clicking the cursor's cell moved it to %d", got)
	}
	if m.Mode() != vim.Normal {
		t.Fatalf("mode %v", m.Mode())
	}
}
