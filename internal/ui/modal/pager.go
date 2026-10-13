package modal

import (
	"fmt"
	"regexp"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/shinyvision/posthero/internal/mouse"
	"github.com/shinyvision/posthero/internal/ui/lineinput"
	"github.com/shinyvision/posthero/internal/ui/theme"
	"github.com/shinyvision/posthero/internal/vim"
)

// Pager shows scrollable text, e.g. help, with vim's search: / and ?
// search forward and backward as you type, n and N repeat, and ]] and [[
// jump between sections.
type Pager struct {
	Title string
	Lines []string
	// Render, when set, lays the text out for a content width on every
	// View and says which lines start sections; Lines is ignored.
	Render func(width int) (lines []string, sections []int)
	// Wide uses most of the screen instead of fitting the text.
	Wide bool
	// Header are lines above the text that stay as it scrolls (tabs, say).
	Header []string
	// Hint replaces the keys shown at the bottom; Status, when set, the
	// line above them (the position in the text).
	Hint, Status string

	top, h   int
	sections []int
	plain    []string // Lines without colors, for searching

	prompt   *lineinput.Model
	query    string
	backward bool // the last search went up (?)
	match    int  // line of the current match, -1 for none
	origin   int  // top when the search started, restored by esc
	pending  string
	typed    int // a count typed before a command
	scroll   int // lines <C-d> and <C-u> move, set by a count; 0 is half a page
	notice   string
	box      string

	re    *regexp.Regexp // the query, compiled
	reFor string
}

// Key implements Modal.
func (p *Pager) Key(k string) (bool, tea.Cmd) {
	p.notice = ""
	if p.prompt != nil {
		p.promptKey(k)
		return false, nil
	}
	if p.pending != "" {
		seq := p.pending + k
		p.pending = ""
		n := max(1, p.typed)
		p.typed = 0
		switch seq {
		case "gg":
			p.top = n - 1 // {n}gg puts line n at the top
		case "]]":
			for range n {
				p.section(1)
			}
		case "[[":
			for range n {
				p.section(-1)
			}
		case "zz":
			p.top = p.match - p.h/2
		}
		p.clamp()
		return false, nil
	}
	if k >= "1" && k <= "9" || k == "0" && p.typed > 0 {
		p.typed = p.typed*10 + int(k[0]-'0')
		return false, nil
	}
	switch k {
	case "g", "]", "[", "z":
		p.pending = k // the count waits for the second key
		return false, nil
	}
	typed, n := p.typed, max(1, p.typed)
	p.typed = 0
	switch k {
	case "q", "<Esc>", "<CR>":
		if k == "<Esc>" && typed > 0 {
			return false, nil // esc drops a count, as in vim
		}
		if k == "<Esc>" && p.query != "" {
			p.query, p.match = "", -1 // the first esc clears the highlight
			return false, nil
		}
		return true, nil
	case "/", "?":
		p.origin, p.backward = p.top, k == "?"
		p.prompt = lineinput.New(k, "", nil)
	case "n", "N":
		for range n {
			p.next((k == "n") != p.backward, true)
		}
	case "j", "<Down>", "<C-e>", "<C-n>":
		p.top += n
	case "k", "<Up>", "<C-y>", "<C-p>":
		p.top -= n
	case "<C-d>", "<C-u>":
		if typed > 0 {
			p.scroll = typed
		}
		d := p.scroll
		if d == 0 {
			d = max(1, p.h/2)
		}
		if k == "<C-u>" {
			d = -d
		}
		p.top += d
	case "<C-f>", "<PageDown>", " ":
		p.top += n * max(1, p.h-1)
	case "<C-b>", "<PageUp>":
		p.top -= n * max(1, p.h-1)
	case "G":
		p.top = len(p.Lines)
		if typed > 0 {
			p.top = typed - 1 // {n}G puts line n at the top
		}
	}
	p.clamp()
	return false, nil
}

func (p *Pager) clamp() {
	p.top = max(0, min(p.top, len(p.Lines)-p.h))
}

// promptKey edits the search; the view follows the first match as the
// query is typed (incsearch).
func (p *Pager) promptKey(k string) {
	act := p.prompt.Key(k)
	switch act.Kind {
	case vim.Submit:
		if q := p.prompt.Value(); q != "" {
			p.query = q
		}
		p.prompt = nil
		if p.match < 0 && p.query != "" {
			p.notice = "E486: Pattern not found: " + p.query
		}
		return
	case vim.Cancel:
		p.prompt, p.query, p.match, p.top = nil, "", -1, p.origin
		return
	}
	if k == "<Esc>" {
		p.prompt, p.query, p.match, p.top = nil, "", -1, p.origin
		return
	}
	p.query = p.prompt.Value()
	p.top = p.origin
	p.match = -1
	if p.query != "" {
		p.next(!p.backward, false)
	}
}

// next moves to the next match below (down) or above the current one,
// wrapping around like vim.
func (p *Pager) next(down, skip bool) {
	if p.query == "" || len(p.plain) == 0 {
		return
	}
	n := len(p.plain)
	from := p.match
	if from < 0 {
		from = p.top
		if !down {
			from = p.top + p.h - 1
		}
		if !skip {
			// Typing a search starts at the top of the screen, as vim
			// starts at the cursor.
			if down {
				from--
			} else {
				from++
			}
		}
	}
	for i := 1; i <= n; i++ {
		j := from + i
		if !down {
			j = from - i
		}
		j = ((j % n) + n) % n
		if p.matches(p.plain[j]) != nil {
			if down && j <= from || !down && j >= from {
				p.notice = "search hit BOTTOM, continuing at TOP"
				if !down {
					p.notice = "search hit TOP, continuing at BOTTOM"
				}
			}
			p.match = j
			if j < p.top || j >= p.top+p.h {
				p.top = j - p.h/3
			}
			p.clamp()
			return
		}
	}
	p.match = -1
	p.notice = "E486: Pattern not found: " + p.query
}

// section jumps to the next (1) or previous (-1) section header.
func (p *Pager) section(d int) {
	if d > 0 {
		for _, s := range p.sections {
			if s > p.top {
				p.top = s
				return
			}
		}
		p.top = len(p.Lines)
		return
	}
	for i := len(p.sections) - 1; i >= 0; i-- {
		if p.sections[i] < p.top {
			p.top = p.sections[i]
			return
		}
	}
	p.top = 0
}

// matches returns the byte ranges where the query occurs in s, ignoring
// case unless it has capitals (smartcase). It matches on s itself:
// lowercasing can change a line's length, so offsets into a lowered copy
// would not fit it.
func (p *Pager) matches(s string) [][]int {
	if p.query == "" {
		return nil
	}
	if p.re == nil || p.reFor != p.query {
		flags := ""
		if strings.ToLower(p.query) == p.query {
			flags = "(?i)"
		}
		p.re, p.reFor = regexp.MustCompile(flags+regexp.QuoteMeta(p.query)), p.query
	}
	return p.re.FindAllStringIndex(s, -1)
}

var (
	matchStyle   = lipgloss.NewStyle().Background(theme.Yellow).Foreground(lipgloss.Color("0"))
	currentStyle = lipgloss.NewStyle().Background(theme.Accent).Foreground(lipgloss.Color("0")).Bold(true)
)

// highlight shows a line with its matches marked; the line loses its
// other colors so the matches stand out.
func (p *Pager) highlight(i int) string {
	plain := p.plain[i]
	ms := p.matches(plain)
	if ms == nil {
		return p.Lines[i]
	}
	style := matchStyle
	if i == p.match {
		style = currentStyle
	}
	var b strings.Builder
	last := 0
	for _, m := range ms {
		b.WriteString(plain[last:m[0]])
		b.WriteString(style.Render(plain[m[0]:m[1]]))
		last = m[1]
	}
	b.WriteString(plain[last:])
	return b.String()
}

// count says which match is current, like vim's [2/7].
func (p *Pager) count() string {
	if p.query == "" {
		return ""
	}
	total, cur := 0, 0
	for i, l := range p.plain {
		n := len(p.matches(l))
		if i == p.match {
			cur = total + 1
		}
		total += n
	}
	if total == 0 {
		return ""
	}
	return fmt.Sprintf("[%d/%d]", cur, total)
}

// View implements Modal.
func (p *Pager) View(maxW, maxH int) string {
	width := 0
	if p.Wide {
		width = max(20, min(maxW-6, 104))
	}
	if p.Render != nil {
		p.Lines, p.sections = p.Render(max(20, width))
	}
	if len(p.plain) != len(p.Lines) || p.Render != nil {
		p.plain = make([]string, len(p.Lines))
		for i, l := range p.Lines {
			p.plain[i] = ansi.Strip(l)
		}
	}
	p.h = max(3, maxH-9-len(p.Header))
	p.clamp()
	end := min(len(p.Lines), p.top+p.h)
	shown := make([]string, 0, p.h+2+len(p.Header))
	shown = append(shown, p.Header...)
	for i := p.top; i < end; i++ {
		if p.query != "" {
			shown = append(shown, p.highlight(i))
		} else {
			shown = append(shown, p.Lines[i])
		}
	}
	for len(shown) < p.h && p.Wide {
		shown = append(shown, "")
	}
	var bottom string
	switch {
	case p.prompt != nil:
		bottom, _ = p.prompt.View(max(20, width))
	case p.notice != "":
		bottom = theme.Warn.Render(p.notice)
	case p.Status != "":
		bottom = p.Status
	default:
		pos := "All"
		switch {
		case len(p.Lines) <= p.h:
		case p.top == 0:
			pos = "Top"
		case end >= len(p.Lines):
			pos = "Bot"
		default:
			pos = fmt.Sprintf("%d%%", 100*p.top/max(1, len(p.Lines)-p.h))
		}
		bottom = theme.Dim.Render(strings.TrimSpace(p.count() + "  " + pos))
	}
	shown = append(shown, "", bottom)
	hint := "/ ? search · n N next · j/k scroll · q close"
	if len(p.sections) > 0 {
		hint = "/ ? search · n N next · ]] [[ sections · j/k scroll · q close"
	}
	if p.Hint != "" {
		hint = p.Hint
	}
	p.box = frameW(p.Title, theme.AccentFg, shown, hint, maxW, width)
	return p.box
}

// Notice shows a message at the bottom until the next key.
func (p *Pager) Notice(s string) { p.notice = s }

// Searching says a search is being typed (it gets every key).
func (p *Pager) Searching() bool { return p.prompt != nil }

// Mouse implements Mouser: the wheel scrolls, a click outside closes.
func (p *Pager) Mouse(ev mouse.Event) (bool, tea.Cmd) {
	if dy, _ := ev.WheelDelta(); dy != 0 {
		p.top = max(0, min(p.top+3*dy, len(p.Lines)-p.h))
		return false, nil
	}
	return ev.Action == mouse.Press && !inside(p.box, ev), nil
}
