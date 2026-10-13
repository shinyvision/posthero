package builtin

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/charmbracelet/x/ansi"

	"github.com/shinyvision/posthero/internal/editor"
	"github.com/shinyvision/posthero/internal/vim"
)

// ANSI styles use the terminal's own palette.
const (
	reset     = "\x1b[0m"
	keyword   = "\x1b[0;35m"
	str       = "\x1b[0;32m"
	number    = "\x1b[0;33m"
	comment   = "\x1b[0;3;90m"
	ident     = "\x1b[0;36m"
	plain     = "\x1b[0m"
	gutter    = "\x1b[0;90m"
	selection = "\x1b[0;7m"
	errStyle  = "\x1b[0;31m"
	pmenu     = "\x1b[0;30;45m" // vim's Pmenu
	pmenuSel  = "\x1b[0;30;47m" // vim's PmenuSel
	modeStyle = "\x1b[0;1m"
	hintStyle = "\x1b[0;90m"
)

func gutterWidth(lines int) int { return len(strconv.Itoa(lines)) + 2 }

func displayWidth(r []rune) int { return len(r) }

// Render implements editor.Backend.
func (b *Backend) Render() []string {
	lines := b.eng.Lines()
	styles := highlight(strings.Join(lines, "\n"))
	selStart, selEnd, linewise, selecting := b.eng.Selection()
	gw := gutterWidth(len(lines))
	out := make([]string, 0, b.h)
	off := 0
	lineOffsets := make([]int, len(lines))
	for i, l := range lines {
		lineOffsets[i] = off
		off += len(l) + 1
	}
	for row := 0; row < b.h-1; row++ {
		li := b.eng.Top() + row
		if li >= len(lines) {
			out = append(out, gutter+pad("~", b.w)+reset)
			continue
		}
		var sb strings.Builder
		sb.WriteString(gutter)
		sb.WriteString(fmt.Sprintf("%*d ", gw-1, li+1))
		width := gw
		last := ""
		byteOff := lineOffsets[li]
		for col, r := range []rune(lines[li]) {
			if width >= b.w {
				break
			}
			st := styles[byteOff]
			if selecting && inSelection(vim.Pos{Line: li, Col: col}, selStart, selEnd, linewise) {
				st = selection
			}
			if st != last {
				sb.WriteString(st)
				last = st
			}
			sb.WriteRune(r)
			byteOff += len(string(r))
			width++
		}
		if selecting && linewise && li >= selStart.Line && li <= selEnd.Line && width < b.w {
			sb.WriteString(selection + " ")
			width++
		}
		if h, ok := b.cur.hints[li]; ok && width+3 < b.w {
			style := hintStyle
			if h.Kind == "missing" {
				style = number
			}
			text := pad("  "+h.Text, b.w-width)
			sb.WriteString(style + text)
			width += len([]rune(text))
		}
		sb.WriteString(reset)
		sb.WriteString(strings.Repeat(" ", max(0, b.w-width)))
		out = append(out, sb.String())
	}
	if b.pop != nil {
		b.drawPopup(out, gw)
	}
	return append(out, b.statusLine())
}

// drawPopup draws the completion menu over the text rows, below the word
// being completed or above it when there is no room below.
func (b *Backend) drawPopup(rows []string, gw int) {
	p := b.pop
	n := min(popupRows, len(p.items))
	if p.sel >= 0 {
		if p.sel < p.top {
			p.top = p.sel
		}
		if p.sel >= p.top+n {
			p.top = p.sel - n + 1
		}
	}
	p.top = max(0, min(p.top, len(p.items)-n))
	wordW, kindW := 0, 0
	for _, it := range p.items[p.top : p.top+n] {
		wordW = max(wordW, len([]rune(label(it))))
		kindW = max(kindW, len([]rune(it.Menu)))
	}
	w := min(1+wordW+2+kindW+1, b.w)
	x := max(0, min(gw+p.start.Col, b.w-w))
	y := p.start.Line - b.eng.Top() + 1
	if y+n > len(rows) {
		y = p.start.Line - b.eng.Top() - n
	}
	if y < 0 {
		y, n = 0, min(n, len(rows))
	}
	for i := 0; i < n && y+i < len(rows); i++ {
		it := p.items[p.top+i]
		style := pmenu
		if p.top+i == p.sel {
			style = pmenuSel
		}
		cell := style + pad(" "+pad(label(it), wordW)+"  "+it.Menu, w) + reset
		row := rows[y+i]
		rows[y+i] = ansi.Cut(row, 0, x) + cell + ansi.Cut(row, x+w, b.w)
	}
}

func (b *Backend) statusLine() string {
	if line, ok := b.eng.CommandLine(); ok {
		return plain + pad(line, b.w) + reset
	}
	if msg := b.eng.Message(); msg != "" {
		return errStyle + pad(msg, b.w) + reset
	}
	if b.msg != "" {
		return plain + pad(b.msg, b.w) + reset
	}
	mode := ""
	switch b.eng.Mode() {
	case vim.Insert, vim.Replace, vim.Visual, vim.VisualLine:
		mode = "-- " + b.eng.Mode().String() + " --"
	}
	if r := b.eng.Recording(); r != 0 {
		mode += " recording @" + string(r)
	}
	right := b.eng.Pending()
	return modeStyle + pad(mode, b.w-len(right)) + plain + right + reset
}

// label is how a candidate shows in the menu.
func label(it editor.Item) string {
	if it.Abbr != "" {
		return it.Abbr
	}
	return it.Word
}

func inSelection(p, s, e vim.Pos, linewise bool) bool {
	if linewise {
		return p.Line >= s.Line && p.Line <= e.Line
	}
	return !p.Before(s) && !e.Before(p)
}

func pad(s string, w int) string {
	r := []rune(s)
	if len(r) >= w {
		return string(r[:max(0, w)])
	}
	return s + strings.Repeat(" ", w-len(r))
}

// highlight returns an ANSI style for every byte of src, for posthero's
// documents: the method, header and field names before the first blank
// line, "### section" lines, comments, {{variables}}, and JSON strings and
// numbers. An example's ### response has a head too: its headers.
func highlight(src string) []string {
	styles := make([]string, len(src)+1)
	for i := range styles {
		styles[i] = plain
	}
	set := func(from, to int, st string) {
		for i := from; i < to && i < len(styles); i++ {
			styles[i] = st
		}
	}
	off := 0
	head := true
	first := true
	for _, line := range strings.Split(src, "\n") {
		end := off + len(line)
		trimmed := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "###"):
			set(off, end, keyword)
			// The response's headers come right under it.
			head = strings.EqualFold(strings.Fields(line + " x")[1], "response")
		case trimmed == "":
			if !first {
				head = false
			}
		case strings.HasPrefix(trimmed, "#") && (head || strings.HasPrefix(line, "#")):
			set(off, end, comment)
		case head && first:
			if sp := strings.IndexByte(line, ' '); sp > 0 && strings.ToUpper(line[:sp]) == line[:sp] {
				set(off, off+sp, keyword)
			}
			first = false
		case head:
			if c := strings.IndexAny(line, ":="); c > 0 {
				set(off, off+c, ident)
			}
		default:
			jsonish(line, off, set)
		}
		if trimmed != "" {
			first = false
		}
		variables(line, off, set)
		off = end + 1
	}
	return styles
}

// jsonish marks strings and numbers in a line starting at byte off.
func jsonish(line string, off int, set func(from, to int, st string)) {
	for i := 0; i < len(line); i++ {
		c := line[i]
		switch {
		case c == '"':
			j := i + 1
			for j < len(line) && (line[j] != '"' || line[j-1] == '\\') {
				j++
			}
			set(off+i, off+min(j+1, len(line)), str)
			i = j
		case c >= '0' && c <= '9' && (i == 0 || strings.IndexByte(" :,[", line[i-1]) >= 0):
			j := i
			for j < len(line) && (line[j] >= '0' && line[j] <= '9' || line[j] == '.') {
				j++
			}
			set(off+i, off+j, number)
			i = j - 1
		}
	}
}

// variables marks {{variables}}.
func variables(line string, off int, set func(from, to int, st string)) {
	for i := 0; i+1 < len(line); i++ {
		if line[i] != '{' || line[i+1] != '{' {
			continue
		}
		j := strings.Index(line[i+2:], "}}")
		if j < 0 {
			return
		}
		set(off+i, off+i+2+j+2, number)
		i += j + 3
	}
}
