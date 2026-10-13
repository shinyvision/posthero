package vim

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

type searchSpec struct {
	re      *regexp.Regexp
	forward bool
	word    bool   // only whole keywords match, as for * and #
	text    string // the pattern as typed, for messages
}

// hit reports whether the match m in line counts. For * and # the match must
// be a whole keyword; that is checked here because Go's \b is ASCII only.
func (s searchSpec) hit(line string, m []int) bool {
	if !s.word {
		return true
	}
	if r, _ := utf8.DecodeLastRuneInString(line[:m[0]]); m[0] > 0 && class(r, false) == 2 {
		return false
	}
	if r, _ := utf8.DecodeRuneInString(line[m[1]:]); m[1] < len(line) && class(r, false) == 2 {
		return false
	}
	return true
}

func (e *Engine) enterCmdline(kind byte, initial string) {
	e.cmdVisual = Normal
	if kind != ':' && (e.mode == Visual || e.mode == VisualLine) {
		// A search typed in visual mode extends the selection.
		e.cmdVisual = e.mode
	}
	e.mode = Cmdline
	e.cmdKind = kind
	e.cmd = []rune(initial)
}

func (e *Engine) cmdlineKey(key string) Action {
	switch key {
	case "<Esc>", "<C-c>":
		e.mode = e.cmdVisual
	case "<CR>":
		e.mode = e.cmdVisual
		text := string(e.cmd)
		if e.cmdKind == ':' {
			return e.ex(text)
		}
		e.search(text, e.cmdKind == '/')
	case "<BS>":
		if len(e.cmd) == 0 {
			e.mode = e.cmdVisual
			return Action{}
		}
		e.cmd = e.cmd[:len(e.cmd)-1]
	case "<C-u>":
		e.cmd = nil
	case "<C-w>":
		i := len(e.cmd)
		for i > 0 && unicode.IsSpace(e.cmd[i-1]) {
			i--
		}
		for i > 0 && !unicode.IsSpace(e.cmd[i-1]) {
			i--
		}
		e.cmd = e.cmd[:i]
	default:
		if key == "<Space>" {
			key = " "
		}
		if isPrintableKey(key) || !strings.HasPrefix(key, "<") {
			e.cmd = append(e.cmd, []rune(key)...)
		}
	}
	return Action{}
}

// --- search ---

func (e *Engine) search(pattern string, forward bool) {
	if pattern == "" {
		if e.lastSearch.re == nil {
			e.msg = "No previous search pattern"
			return
		}
		e.lastSearch.forward = forward
	} else {
		e.lastSearch = searchSpec{re: compileVim(pattern), forward: forward, text: pattern}
	}
	if m := e.searchNext(false, 1); !m.failed {
		e.moveTo(m)
	}
}

// searchWord is * and #. Like vim it searches for the keyword under or
// after the cursor, else the non-blank word under or after it, and only a
// keyword has to match as a whole word.
func (e *Engine) searchWord(forward bool, n int) motionResult {
	line := e.buf.lines[e.cur.Line]
	s, en, ok := wordAt(line, e.cur.Col, func(r rune) bool { return class(r, false) == 2 })
	keyword := ok
	if !ok {
		s, en, ok = wordAt(line, e.cur.Col, func(r rune) bool { return class(r, false) != 0 })
	}
	if !ok {
		e.msg = "No string under cursor"
		return motionResult{failed: true}
	}
	word := string(line[s:en])
	text := word
	if keyword {
		text = `\<` + word + `\>`
	}
	e.lastSearch = searchSpec{re: regexp.MustCompile(regexp.QuoteMeta(word)), forward: forward, word: keyword, text: text}
	// Search from the start of the word, so # does not find the word the
	// cursor is in.
	cur := e.cur
	e.cur.Col = s
	m := e.searchNext(false, n)
	e.cur = cur
	return m
}

// wordAt finds the run of characters in wordChar under col, or else the
// first one after it on the line.
func wordAt(line []rune, col int, wordChar func(rune) bool) (int, int, bool) {
	s := min(col, len(line))
	for s < len(line) && !wordChar(line[s]) {
		s++
	}
	if s == len(line) {
		return 0, 0, false
	}
	for s > 0 && wordChar(line[s-1]) && class(line[s-1], false) == class(line[s], false) {
		s--
	}
	en := s
	for en < len(line) && wordChar(line[en]) && class(line[en], false) == class(line[s], false) {
		en++
	}
	return s, en, true
}

func (e *Engine) searchNext(reverse bool, n int) motionResult {
	s := e.lastSearch
	if s.re == nil {
		e.msg = "No previous search pattern"
		return motionResult{failed: true}
	}
	forward := s.forward != reverse
	p := e.cur
	for range n {
		q, ok := e.findMatch(s, p, forward)
		if !ok {
			e.msg = "Pattern not found: " + s.text
			return motionResult{failed: true}
		}
		p = q
	}
	return motionResult{pos: p}
}

func (e *Engine) findMatch(s searchSpec, from Pos, forward bool) (Pos, bool) {
	total := len(e.buf.lines)
	for i := 0; i <= total; i++ {
		var l int
		if forward {
			l = (from.Line + i) % total
		} else {
			l = ((from.Line-i)%total + total) % total
		}
		line := string(e.buf.lines[l])
		var matches [][]int
		for _, m := range s.re.FindAllStringIndex(line, -1) {
			if s.hit(line, m) {
				matches = append(matches, m)
			}
		}
		if forward {
			for _, m := range matches {
				col := utf8.RuneCountInString(line[:m[0]])
				if i > 0 || col > from.Col {
					return Pos{l, col}, true
				}
			}
		} else {
			for j := len(matches) - 1; j >= 0; j-- {
				col := utf8.RuneCountInString(line[:matches[j][0]])
				if i > 0 || col < from.Col {
					return Pos{l, col}, true
				}
			}
		}
	}
	return from, false
}

// compileVim translates the common parts of Vim's "magic" regex syntax to
// Go's: \( \) \| \+ \? \= \{ become operators, bare ones become literals, and
// \< \> become word boundaries. A pattern starting with \v is used as is.
func compileVim(p string) *regexp.Regexp {
	prefix := ""
	if strings.HasPrefix(p, `\c`) {
		prefix, p = "(?i)", p[2:]
	}
	var goRe string
	if strings.HasPrefix(p, `\v`) {
		goRe = p[2:]
	} else {
		var b strings.Builder
		for i := 0; i < len(p); i++ {
			c := p[i]
			if c == '\\' && i+1 < len(p) {
				i++
				switch n := p[i]; n {
				case '(', ')', '|', '+', '?', '{', '}':
					b.WriteByte(n)
				case '=':
					b.WriteByte('?')
				case '<', '>':
					b.WriteString(`\b`)
				default:
					b.WriteByte('\\')
					b.WriteByte(n)
				}
				continue
			}
			switch c {
			case '(', ')', '|', '+', '?', '{', '}':
				b.WriteByte('\\')
			}
			b.WriteByte(c)
		}
		goRe = b.String()
	}
	re, err := regexp.Compile(prefix + goRe)
	if err != nil {
		re = regexp.MustCompile(prefix + regexp.QuoteMeta(p))
	}
	return re
}

// --- ex commands ---

var rangeRe = regexp.MustCompile(`^(%|'<,'>|[.$]|\d+)(?:,([.$]|\d+))?`)

func (e *Engine) ex(text string) Action {
	text = strings.TrimSpace(strings.TrimPrefix(text, ":"))
	from, to := e.cur.Line, e.cur.Line
	hasRange := false
	if m := rangeRe.FindStringSubmatch(text); m != nil {
		hasRange = true
		text = strings.TrimSpace(text[len(m[0]):])
		switch m[1] {
		case "%":
			from, to = 0, e.buf.last()
		case "'<,'>":
			from, to = e.marks['<'].Line, e.marks['>'].Line
		default:
			from = e.lineAddr(m[1])
			to = from
		}
		if m[2] != "" {
			to = e.lineAddr(m[2])
		}
		from, to = clamp(min(from, to), 0, e.buf.last()), clamp(max(from, to), 0, e.buf.last())
	}
	switch {
	case text == "" && hasRange:
		e.cur = Pos{to, e.buf.firstNonBlank(to)}
	case text == "":
	case strings.HasPrefix(text, "s") && len(text) > 1 && !unicode.IsLetter(rune(text[1])):
		e.substitute(text[1:], from, to)
	case text == "d" || text == "delete":
		e.beginChange()
		e.apply("d", 0, span{s: Pos{from, 0}, e: Pos{to, 0}, linewise: true})
		e.endChange()
	case text == "y" || text == "yank":
		e.regs.store(0, Register{Text: e.buf.linesText(from, to), Linewise: true}, true)
	case text == "u" || text == "undo":
		e.undoChange(1)
	case text == "red" || text == "redo":
		e.redoChange(1)
	case text == "noh" || text == "nohlsearch":
	default:
		return Action{Kind: Ex, Text: text}
	}
	return Action{}
}

func (e *Engine) lineAddr(s string) int {
	switch s {
	case ".":
		return e.cur.Line
	case "$":
		return e.buf.last()
	}
	n, _ := strconv.Atoi(s)
	return n - 1
}

// substitute implements :s/pattern/replacement/flags.
func (e *Engine) substitute(arg string, from, to int) {
	if arg == "" {
		return
	}
	delim := arg[0]
	parts := splitUnescaped(arg[1:], delim)
	if len(parts) < 2 {
		parts = append(parts, "")
	}
	flags := ""
	if len(parts) > 2 {
		flags = parts[2]
	}
	var spec searchSpec
	switch {
	case parts[0] != "":
		pattern := parts[0]
		if strings.Contains(flags, "i") {
			pattern = `\c` + pattern
		}
		spec = searchSpec{re: compileVim(pattern), forward: e.lastSearch.forward || e.lastSearch.re == nil, text: parts[0]}
		// Like a search, :s sets the pattern n and N look for.
		e.lastSearch = spec
	case e.lastSearch.re != nil:
		// An empty pattern means the last search pattern.
		spec = e.lastSearch
	default:
		e.msg = "No previous regular expression"
		return
	}
	re := spec.re
	repl := vimReplacement(parts[1])
	all := strings.Contains(flags, "g")
	e.beginChange()
	count, lines := 0, 0
	for l := from; l <= to; l++ {
		line := string(e.buf.lines[l])
		var out []byte
		last, matched := 0, false
		for _, m := range re.FindAllStringSubmatchIndex(line, -1) {
			if !spec.hit(line, m) {
				continue
			}
			matched = true
			out = append(out, line[last:m[0]]...)
			out = re.ExpandString(out, repl, line, m)
			last = m[1]
			count++
			if !all {
				break
			}
		}
		if !matched {
			continue
		}
		out = append(out, line[last:]...)
		lines++
		var split [][]rune
		for _, part := range strings.Split(string(out), "\n") {
			split = append(split, []rune(part))
		}
		e.buf.lines = append(e.buf.lines[:l], append(split, e.buf.lines[l+1:]...)...)
		added := len(split) - 1
		l += added
		to += added
		e.cur = Pos{l, e.buf.firstNonBlank(l)}
	}
	e.endChange()
	switch {
	case count == 0:
		e.msg = "Pattern not found: " + spec.text
	case lines > 1:
		e.msg = fmt.Sprintf("%d substitutions on %d lines", count, lines)
	}
}

func splitUnescaped(s string, delim byte) []string {
	var parts []string
	var cur strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) && s[i+1] == delim {
			cur.WriteByte(delim)
			i++
			continue
		}
		if s[i] == delim {
			parts = append(parts, cur.String())
			cur.Reset()
			continue
		}
		cur.WriteByte(s[i])
	}
	return append(parts, cur.String())
}

// vimReplacement converts & and \1 into Go's ${0} and ${1}.
func vimReplacement(r string) string {
	var b strings.Builder
	for i := 0; i < len(r); i++ {
		switch c := r[i]; {
		case c == '\\' && i+1 < len(r):
			i++
			if n := r[i]; n >= '0' && n <= '9' {
				b.WriteString("${" + string(n) + "}")
			} else if n == 'n' || n == 'r' {
				b.WriteByte('\n')
			} else if n == '$' {
				b.WriteString("$$")
			} else {
				b.WriteByte(n)
			}
		case c == '&':
			b.WriteString("${0}")
		case c == '$':
			b.WriteString("$$")
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}
