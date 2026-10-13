// Package picker is posthero's fuzzy finder, opened for anything chosen from
// a list: a request, an environment, a workspace, a language. Typing
// narrows the list (fuzzy, like fzf or telescope), Enter picks the
// highlighted item, Esc cancels. A list too long to load at once can be
// searched as you type (Source).
package picker

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/shinyvision/posthero/internal/fuzzy"
	"github.com/shinyvision/posthero/internal/mouse"
	"github.com/shinyvision/posthero/internal/ui/lineinput"
	"github.com/shinyvision/posthero/internal/ui/theme"
	"github.com/shinyvision/posthero/internal/vim"
)

// Item is a choice.
type Item struct {
	// Label is what is shown and matched.
	Label string
	// Detail is shown dim after the label and matched too.
	Detail string
	// Search is extra text to match, not shown (e.g. an email).
	Search string
	// Style colors the label (e.g. a status in its category's color).
	Style *lipgloss.Style
	// Current marks today's value with a check.
	Current bool
	// Value is whatever the caller needs back.
	Value any
}

// SourceMsg delivers items searched for a query (see Picker.Source).
type SourceMsg struct {
	Picker *Picker
	Query  string
	Items  []Item
	Err    error
}

// Picker is the modal.
type Picker struct {
	Title string
	// Items to choose from; with Source set, the ones to show before the
	// first search (and to keep at the top, like "me" and "Unassigned").
	Items []Item
	// Placeholder is shown in the empty prompt.
	Placeholder string
	// Pick runs with the chosen item.
	Pick func(Item) tea.Cmd
	// Multi lets Tab mark several items; PickMany then runs with them all
	// (the highlighted one when none is marked).
	Multi    bool
	PickMany func([]Item) tea.Cmd
	// Create offers an item for typed text that matches nothing exactly
	// (a new name); it returns that item, or false to offer nothing.
	Create func(text string) (Item, bool)
	// Source searches elsewhere for the query; its command must deliver a
	// SourceMsg for this picker. Results replace the matches, after Items.
	Source func(query string) tea.Cmd
	// Preselect marks items of a Multi picker to start with.
	Preselect func(Item) bool

	input    *lineinput.Model
	matches  []match
	sel      int
	top      int
	marked   map[int]bool // by index into all()
	toggled  bool         // a mark was changed by hand
	found    []Item       // from Source
	foundFor string       // the query found answers
	asked    string       // query last sent to Source
	loading  bool
	err      string
	box      string
	rows     int // item rows shown last time
}

type match struct {
	item  Item
	index int // into all()
	runes []int
}

// Open returns a command showing the picker.
func (p *Picker) Open() tea.Cmd {
	p.init()
	return tea.Batch(func() tea.Msg { return OpenMsg{p} }, p.search())
}

// OpenMsg asks the app to show a picker.
type OpenMsg struct{ Picker *Picker }

func (p *Picker) init() {
	if p.input != nil {
		return
	}
	p.input = lineinput.New("› ", "", nil)
	p.input.Placeholder = p.Placeholder
	p.marked = map[int]bool{}
	if p.Preselect != nil {
		for k, it := range p.Items {
			if p.Preselect(it) {
				p.marked[k] = true
			}
		}
	}
	p.refilter()
	for k, m := range p.matches {
		if m.item.Current {
			p.sel = k
			break
		}
	}
}

// all is every item: the fixed ones, then what Source found.
func (p *Picker) all() []Item {
	if len(p.found) == 0 {
		return p.Items
	}
	return append(append([]Item(nil), p.Items...), p.found...)
}

func (p *Picker) refilter() {
	q := p.input.Value()
	texts := make([]string, len(p.Items))
	for k, it := range p.Items {
		texts[k] = strings.TrimSpace(it.Label + " " + it.Detail + " " + it.Search)
	}
	p.matches = p.matches[:0]
	for _, fm := range fuzzy.Filter(q, texts) {
		var runes []int
		n := len([]rune(p.Items[fm.Index].Label))
		for _, r := range fm.Runes {
			if r < n {
				runes = append(runes, r)
			}
		}
		p.matches = append(p.matches, match{item: p.Items[fm.Index], index: fm.Index, runes: runes})
	}
	// What Source found for this query already matches, the way it
	// searched: keep its order. Results for an earlier query are narrowed
	// here until the new ones arrive.
	for k, it := range p.found {
		if p.foundFor == q || fuzzy.Matches(q, it.Label+" "+it.Detail+" "+it.Search) {
			p.matches = append(p.matches, match{item: it, index: len(p.Items) + k})
		}
	}
	items := p.all()
	if p.Create != nil && q != "" {
		exact := false
		for _, it := range items {
			if strings.EqualFold(it.Label, q) {
				exact = true
				break
			}
		}
		if it, ok := p.Create(q); ok && !exact {
			p.matches = append(p.matches, match{item: it, index: -1})
		}
	}
	p.sel = max(0, min(p.sel, len(p.matches)-1))
}

// search asks Source for the query when it changed.
func (p *Picker) search() tea.Cmd {
	if p.Source == nil {
		return nil
	}
	q := strings.TrimSpace(p.input.Value())
	if q == p.asked && p.asked != "" {
		return nil
	}
	p.asked, p.loading = q, true
	return p.Source(q)
}

// Update takes search results.
func (p *Picker) Update(msg tea.Msg) tea.Cmd {
	m, ok := msg.(SourceMsg)
	if !ok || m.Picker != p {
		return nil
	}
	if m.Query != p.asked {
		return nil // an older search
	}
	p.loading = false
	p.err = ""
	if m.Err != nil {
		p.err = m.Err.Error()
		return nil
	}
	// Drop found items that repeat a fixed one.
	seen := map[string]bool{}
	for _, it := range p.Items {
		seen[it.Label] = true
	}
	p.found, p.foundFor = p.found[:0], m.Query
	for _, it := range m.Items {
		if !seen[it.Label] {
			p.found = append(p.found, it)
		}
	}
	p.refilter()
	return nil
}

// Key implements modal.Modal.
func (p *Picker) Key(k string) (bool, tea.Cmd) {
	p.init()
	switch k {
	case "<Esc>", "<C-c>":
		return true, nil
	case "<CR>":
		return p.choose()
	case "<C-n>", "<Down>", "<C-j>":
		p.move(1)
		return false, nil
	case "<C-p>", "<Up>", "<C-k>":
		p.move(-1)
		return false, nil
	case "<C-d>", "<PageDown>":
		p.page(max(1, p.rows/2))
		return false, nil
	case "<C-u>", "<PageUp>":
		if k == "<C-u>" && p.input.Mode() == vim.Insert {
			break // deletes what was typed, as in vim and fzf
		}
		p.page(-max(1, p.rows/2))
		return false, nil
	case "<Tab>":
		if p.Multi && p.sel < len(p.matches) {
			idx := p.matches[p.sel].index
			if idx < 0 {
				// A new item (a label nobody used yet): keep it, marked.
				p.Items = append(p.Items, p.matches[p.sel].item)
				p.marked[len(p.Items)-1] = true
				p.input.SetValue("")
				p.refilter()
				return false, nil
			}
			p.marked[idx] = !p.marked[idx]
			p.toggled = true
			p.move(1)
			return false, nil
		}
		p.move(1)
		return false, nil
	case "<S-Tab>":
		p.move(-1)
		return false, nil
	}
	before := p.input.Value()
	act := p.input.Key(k)
	switch act.Kind {
	case vim.Submit:
		return p.choose()
	case vim.Cancel:
		return true, nil
	}
	if p.input.Value() != before {
		p.sel = 0
		p.refilter()
		return false, p.search()
	}
	return false, nil
}

// Paste types pasted text into the prompt.
func (p *Picker) Paste(s string) tea.Cmd {
	p.init()
	p.input.Paste(strings.ReplaceAll(s, "\n", " "))
	p.sel = 0
	p.refilter()
	return p.search()
}

func (p *Picker) move(d int) {
	if len(p.matches) == 0 {
		return
	}
	p.sel = (p.sel + d + len(p.matches)) % len(p.matches)
}

// page moves by d items, stopping at the ends of the list.
func (p *Picker) page(d int) {
	p.sel = max(0, min(len(p.matches)-1, p.sel+d))
}

func (p *Picker) choose() (bool, tea.Cmd) {
	if p.Multi && p.PickMany != nil {
		var items []Item
		all := p.all()
		for idx := range all {
			if p.marked[idx] {
				items = append(items, all[idx])
			}
		}
		switch {
		case len(p.matches) == 1 && p.matches[0].index < 0:
			items = append(items, p.matches[0].item) // ⏎ on a new name that matches nothing adds it
		case len(items) == 0 && !p.toggled && p.sel < len(p.matches):
			// Nothing marked and nothing unmarked: ⏎ takes the highlight.
			items = []Item{p.matches[p.sel].item}
		}
		if len(items) == 0 && !p.toggled {
			return false, nil
		}
		return true, p.PickMany(items) // empty after unmarking everything: none
	}
	if p.sel >= len(p.matches) || p.Pick == nil {
		if len(p.matches) == 0 {
			return false, nil // nothing to pick; keep typing
		}
		return true, nil
	}
	return true, p.Pick(p.matches[p.sel].item)
}

// View implements modal.Modal.
func (p *Picker) View(maxW, maxH int) string {
	p.init()
	w := max(36, ansi.StringWidth(p.Title)+2)
	for _, it := range p.all() {
		w = max(w, ansi.StringWidth(it.Label+"  "+it.Detail)+10)
	}
	w = max(30, min(w, maxW-6, 90))
	p.rows = max(3, min(len(p.matches), maxH-10, 14))
	if len(p.matches) == 0 {
		p.rows = 1
	}
	if p.sel < p.top {
		p.top = p.sel
	}
	if p.sel >= p.top+p.rows {
		p.top = p.sel - p.rows + 1
	}
	p.top = max(0, min(p.top, len(p.matches)-p.rows))
	prompt, _ := p.input.View(w)
	lines := []string{prompt, ""}
	if len(p.matches) == 0 {
		msg := "nothing matches"
		switch {
		case p.loading:
			msg = "searching…"
		case p.err != "":
			msg = p.err
		}
		lines = append(lines, theme.Dim.Render("  "+msg))
	}
	for k := p.top; k < len(p.matches) && k < p.top+p.rows; k++ {
		lines = append(lines, p.line(k, w))
	}
	status := fmt.Sprintf("%d of %d", len(p.matches), len(p.all()))
	if p.loading {
		status = "searching… " + status
	}
	if n := p.markedCount(); n > 0 {
		status = fmt.Sprintf("%d marked · ", n) + status
	}
	lines = append(lines, "", theme.Dim.Render(fmt.Sprintf("%*s", w, status)))
	hint := "⏎ pick · esc cancel · ^n ^p move"
	if p.Multi {
		hint = "tab mark · ⏎ done · esc cancel · ^n ^p move"
	}
	p.box = lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(theme.Accent).
		Padding(0, 1).
		Render(theme.AccentFg.Bold(true).Render(p.Title) + "\n\n" + strings.Join(lines, "\n") + "\n" + theme.Dim.Render(theme.Truncate(hint, w)))
	return p.box
}

func (p *Picker) markedCount() int {
	n := 0
	for _, on := range p.marked {
		if on {
			n++
		}
	}
	return n
}

// line renders one item: a mark column, the label with the matched
// letters highlighted, and the detail.
func (p *Picker) line(k, w int) string {
	m := p.matches[k]
	it := m.item
	style := lipgloss.NewStyle()
	if it.Style != nil {
		style = *it.Style
	}
	hl := lipgloss.NewStyle().Foreground(theme.Yellow).Bold(true)
	label := fuzzy.Highlight(it.Label, m.runes, func(s string) string { return hl.Render(s) }, func(s string) string { return style.Render(s) })
	mark := "  "
	switch {
	case m.index >= 0 && p.marked[m.index]:
		mark = theme.Success.Render("✓ ")
	case it.Current:
		mark = theme.Dim.Render("• ")
	}
	text := mark + label
	if it.Current {
		text += theme.Dim.Render(" (now)")
	}
	if it.Detail != "" {
		text += "  " + theme.Dim.Render(it.Detail)
	}
	text = theme.Fit(text, w-1)
	if k == p.sel {
		return theme.AccentFg.Render("▌") + theme.Selected.Render(ansi.Strip(text))
	}
	return " " + text
}

// Mouse implements modal.Mouser: click an item to pick it; the wheel
// moves; a click outside cancels.
func (p *Picker) Mouse(ev mouse.Event) (bool, tea.Cmd) {
	if dy, _ := ev.WheelDelta(); dy != 0 {
		p.move(dy)
		return false, nil
	}
	if ev.Action != mouse.Press {
		return false, nil
	}
	if !ev.In(lipgloss.Width(p.box), lipgloss.Height(p.box)) {
		return true, nil
	}
	p.toggled = p.toggled || p.Multi
	// Border, title, blank, prompt, blank come before the items.
	k := p.top + ev.Y - 5
	if k >= p.top && k < len(p.matches) && k < p.top+p.rows {
		p.sel = k
		if p.Multi {
			idx := p.matches[k].index
			if idx >= 0 {
				p.marked[idx] = !p.marked[idx]
			}
			return false, nil
		}
		return p.choose()
	}
	return false, nil
}
