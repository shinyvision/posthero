package modal

import (
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func pagerWith(n int) *Pager {
	var lines []string
	for i := range n {
		lines = append(lines, fmt.Sprintf("line %d", i))
	}
	lines[10] = "Alpha one"
	lines[50] = "alpha two"
	lines[90] = "ALPHA three"
	p := &Pager{Title: "t", Lines: lines}
	p.View(80, 30)
	return p
}

func typeKeys(p *Pager, keys ...string) {
	for _, k := range keys {
		p.Key(k)
	}
	p.View(80, 30)
}

func TestPagerSearch(t *testing.T) {
	p := pagerWith(100)
	// Typing moves to the first match as you go (incsearch).
	typeKeys(p, "/", "a", "l")
	if p.match != 10 {
		t.Fatalf("incsearch: match %d", p.match)
	}
	typeKeys(p, "<CR>", "n")
	if p.match != 50 {
		t.Fatalf("n: match %d", p.match)
	}
	typeKeys(p, "n", "n")
	if p.match != 10 || !strings.Contains(p.notice, "BOTTOM") {
		t.Fatalf("n wraps: match %d notice %q", p.match, p.notice)
	}
	typeKeys(p, "N")
	if p.match != 90 {
		t.Fatalf("N wraps back: %d", p.match)
	}
	if c := p.count(); c != "[3/3]" {
		t.Fatalf("count %s", c)
	}
	// Capitals make it case sensitive.
	typeKeys(p, "/", "A", "L", "P", "H", "A", "<CR>")
	if p.match != 90 || p.count() != "[1/1]" {
		t.Fatalf("smartcase: %d %s", p.match, p.count())
	}
	// esc while typing goes back to where the search started.
	p.top = 0
	typeKeys(p, "/", "t", "h", "r", "e", "e")
	if p.top == 0 {
		t.Fatal("incsearch did not scroll to the match")
	}
	typeKeys(p, "<Esc>")
	if p.top != 0 || p.query != "" || p.prompt != nil {
		t.Fatalf("esc: top %d query %q", p.top, p.query)
	}
	typeKeys(p, "/", "z", "z", "z", "<CR>")
	if !strings.HasPrefix(p.notice, "E486") {
		t.Fatalf("not found: %q", p.notice)
	}
}

func TestPagerHighlightAndClose(t *testing.T) {
	p := pagerWith(100)
	typeKeys(p, "/", "two", "<CR>")
	typeKeys(p, "/", "t", "w", "o", "<CR>")
	view := ansi.Strip(p.View(80, 30))
	if !strings.Contains(view, "alpha two") || !strings.Contains(view, "[1/1]") {
		t.Fatalf("view:\n%s", view)
	}
	// The first esc clears the search, the second closes.
	if done, _ := p.Key("<Esc>"); done || p.query != "" {
		t.Fatal("esc should clear the search first")
	}
	if done, _ := p.Key("<Esc>"); !done {
		t.Fatal("esc should close")
	}
}

func TestPagerSections(t *testing.T) {
	p := &Pager{Title: "t", Render: func(w int) ([]string, []int) {
		return make([]string, 200), []int{20, 80, 150}
	}}
	p.View(80, 30)
	typeKeys(p, "]", "]")
	if p.top != 20 {
		t.Fatalf("]]: %d", p.top)
	}
	typeKeys(p, "]", "]")
	if p.top != 80 {
		t.Fatalf("]] again: %d", p.top)
	}
	typeKeys(p, "[", "[")
	if p.top != 20 {
		t.Fatalf("[[: %d", p.top)
	}
}

// Lowercasing changes the byte length of some letters; search must not
// slice the line at the lowered text's offsets.
func TestPagerSearchUnicode(t *testing.T) {
	for _, l := range []string{"ȺȺ abc", "İİİİ abc", "ﬃ abc ȺȺȺ"} {
		p := &Pager{Title: "t", Lines: []string{"x", l, "y"}}
		p.View(80, 30)
		typeKeys(p, "/", "a", "b", "c", "<CR>")
		if p.match != 1 || p.count() != "[1/1]" {
			t.Fatalf("%q: match %d %s", l, p.match, p.count())
		}
		if view := ansi.Strip(p.View(80, 30)); !strings.Contains(view, l) {
			t.Fatalf("%q not shown:\n%s", l, view)
		}
	}
	// Smart case still works on such text.
	p := &Pager{Title: "t", Lines: []string{"ⱥ", "Ⱥ"}}
	p.View(80, 30)
	typeKeys(p, "/", "Ⱥ", "<CR>")
	if p.match != 1 || p.count() != "[1/1]" {
		t.Fatalf("smartcase: match %d %s", p.match, p.count())
	}
}

func TestPagerCounts(t *testing.T) {
	p := pagerWith(100)
	typeKeys(p, "5", "j")
	if p.top != 5 {
		t.Fatalf("5j: top %d", p.top)
	}
	typeKeys(p, "2", "k")
	if p.top != 3 {
		t.Fatalf("2k: top %d", p.top)
	}
	typeKeys(p, "5", "0", "G")
	if p.top != 49 {
		t.Fatalf("50G: top %d", p.top)
	}
	typeKeys(p, "1", "0", "g", "g")
	if p.top != 9 {
		t.Fatalf("10gg: top %d", p.top)
	}
	typeKeys(p, "g", "g")
	if p.top != 0 {
		t.Fatalf("gg: top %d", p.top)
	}
	typeKeys(p, "3", "<C-d>")
	if p.top != 3 {
		t.Fatalf("3<C-d>: top %d", p.top)
	}
	typeKeys(p, "<C-d>") // the count sticks, like 'scroll'
	if p.top != 6 {
		t.Fatalf("<C-d> after 3<C-d>: top %d", p.top)
	}
	typeKeys(p, "G")
	if p.top != 100-p.h {
		t.Fatalf("G: top %d", p.top)
	}
}

func TestPagerTiny(t *testing.T) {
	for w := range 3 {
		for h := range 3 {
			p := pagerWith(100)
			p.View(w, h)
			typeKeys(p, "G", "/", "a", "<CR>")
			p.View(w, h)
		}
	}
}
