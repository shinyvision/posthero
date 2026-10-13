package builtin

import (
	"regexp"
	"strings"

	"github.com/shinyvision/posthero/internal/editor"
	"github.com/shinyvision/posthero/internal/vim"
)

// popupRows is the most completion candidates shown at once.
const popupRows = 8

// popup is vim's insert-mode completion menu.
type popup struct {
	start  vim.Pos // where the text being completed starts
	leader string  // what was typed before the menu opened or since
	items  []editor.Item
	sel    int // -1 shows the leader itself
	top    int // first candidate shown
	// held says the selected candidate isn't in the text yet: a menu that
	// opened while typing highlights the first match without inserting it,
	// like vim's completeopt=noinsert.
	held bool
}

// completionKey handles keys that open, move or close the completion menu,
// reporting whether the key was used up. <C-x><C-o>, <C-n> and <C-p> open
// it.
func (b *Backend) completionKey(key string) bool {
	if b.eng.Mode() != vim.Insert {
		b.pop, b.ctrlX = nil, false
		return false
	}
	if b.ctrlX {
		b.ctrlX = false
		if key == "<C-o>" || key == "<C-n>" || key == "<C-p>" {
			b.open(key == "<C-p>")
			return true
		}
		return false
	}
	if b.pop == nil {
		switch key {
		case "<C-n>", "<C-p>":
			b.open(key == "<C-p>")
			return true
		case "<C-x>":
			b.ctrlX = true
			return true
		}
		return false
	}
	switch key {
	case "<C-n>", "<Down>", "<Tab>":
		b.move(1)
	case "<C-p>", "<Up>", "<S-Tab>":
		b.move(-1)
	case "<C-y>":
		if b.pop.held && b.pop.sel >= 0 {
			b.put(b.pop.items[b.pop.sel].Word)
		}
		b.pop = nil
	case "<C-e>":
		b.put(b.pop.leader)
		b.pop = nil
	case "<CR>":
		selected := b.pop.sel >= 0
		if b.pop.held && selected {
			b.put(b.pop.items[b.pop.sel].Word)
		}
		b.pop = nil
		return selected
	default:
		if key == "<BS>" || len([]rune(key)) == 1 {
			return false // typed; refilter narrows or widens the menu
		}
		b.pop = nil
		return false
	}
	return true
}

// variableBefore is what opens the menu on its own while typing: a {{
// that isn't closed yet.
var variableBefore = regexp.MustCompile(`\{\{[^{}]*$`)

// autoComplete reports whether the text before the cursor asks for the menu
// without a key, as the Neovim and Vim backends do.
func (b *Backend) autoComplete() bool {
	lines := b.eng.Lines()
	cur := b.eng.Cursor()
	before := string([]rune(lines[cur.Line])[:cur.Col])
	return variableBefore.MatchString(before)
}

// open starts completing the text before the cursor; last selects the last
// candidate (<C-p>) instead of the first.
func (b *Backend) open(last bool) { b.openMenu(last, false) }

// openMenu opens the menu; auto says it opened while typing.
func (b *Backend) openMenu(last, auto bool) {
	start, leader, items, ok := b.candidates()
	if !ok || len(items) == 0 {
		b.msg = "-- Pattern not found"
		return
	}
	b.pop = &popup{start: start, leader: leader, items: items, sel: -1}
	if auto {
		b.pop.sel, b.pop.held = 0, true
		return
	}
	if last {
		b.pop.sel = len(items) - 1
		b.put(items[b.pop.sel].Word)
	}
}

// candidates asks the Completer about the text before the cursor.
func (b *Backend) candidates() (vim.Pos, string, []editor.Item, bool) {
	if b.complete == nil {
		return vim.Pos{}, "", nil, false
	}
	lines := b.eng.Lines()
	cur := b.eng.Cursor()
	line := lines[cur.Line]
	col := byteCol(line, cur.Col)
	start, items := b.complete(lines, cur.Line, col)
	if start < 0 {
		return vim.Pos{}, "", nil, false
	}
	start = min(start, col)
	return vim.Pos{Line: cur.Line, Col: len([]rune(line[:start]))}, line[start:col], items, true
}

// refilter follows typing while the menu is open: the menu shows what
// matches the text typed so far, and closes when nothing does.
func (b *Backend) refilter(key string) {
	cur := b.eng.Cursor()
	if b.eng.Mode() != vim.Insert || cur.Line != b.pop.start.Line || cur.Col < b.pop.start.Col {
		b.pop = nil
		return
	}
	if key != "<BS>" && len([]rune(key)) != 1 {
		return
	}
	start, leader, items, ok := b.candidates()
	if !ok || len(items) == 0 || start != b.pop.start {
		b.pop = nil
		return
	}
	b.pop.leader, b.pop.items, b.pop.top = leader, items, 0
	if b.pop.held {
		b.pop.sel = 0
	} else {
		b.pop.sel = -1
	}
}

// move selects the next or previous candidate and puts it in the text; past
// either end comes the leader, as in vim.
func (b *Backend) move(d int) {
	p := b.pop
	p.held = false
	n := len(p.items)
	p.sel += d
	switch {
	case p.sel >= n:
		p.sel = -1
	case p.sel < -1:
		p.sel = n - 1
	}
	if p.sel < 0 {
		b.put(p.leader)
		return
	}
	b.put(p.items[p.sel].Word)
}

// put replaces the text being completed with text.
func (b *Backend) put(text string) {
	b.eng.Replace(b.pop.start, b.eng.Cursor(), strings.ReplaceAll(text, "\n", " "))
}
