package vim

import "unicode"

type motionResult struct {
	pos       Pos
	linewise  bool
	inclusive bool
	vertical  bool // keeps the desired column
	failed    bool
}

type findSpec struct {
	kind string // f F t T
	char rune
}

// motion evaluates c.key as a motion. ok is false when the key is not a
// motion at all.
func (e *Engine) motion(c command, forOp bool) (motionResult, bool) {
	n := c.n()
	p := e.cur
	b := &e.buf
	switch c.key {
	case "h", "<Left>", "<BS>":
		return motionResult{pos: Pos{p.Line, max(0, p.Col-n)}, failed: p.Col == 0 && !forOp}, true
	case "l", "<Right>", " ":
		limit := b.lineLen(p.Line)
		if !forOp {
			limit = max(0, limit-1)
		}
		return motionResult{pos: Pos{p.Line, min(p.Col+n, limit)}}, true
	// Moving down from the last line or up from the first fails, so an
	// operator does nothing, as in vim; a count that goes too far stops at
	// the end.
	case "j", "<Down>", "<C-n>", "<C-j>":
		return failIf(e.vertical(p.Line+n), p.Line == b.last()), true
	case "k", "<Up>", "<C-p>":
		return failIf(e.vertical(p.Line-n), p.Line == 0), true
	case "+", "<C-m>":
		return failIf(e.lineStart(p.Line+n), p.Line == b.last()), true
	case "-":
		return failIf(e.lineStart(p.Line-n), p.Line == 0), true
	case "_":
		return e.lineStart(p.Line + n - 1), true
	case "0", "<Home>":
		return motionResult{pos: Pos{p.Line, 0}}, true
	case "^":
		return motionResult{pos: Pos{p.Line, b.firstNonBlank(p.Line)}}, true
	case "$", "<End>":
		l := clamp(p.Line+n-1, 0, b.last())
		e.want = -1
		return motionResult{pos: Pos{l, max(0, b.lineLen(l)-1)}, inclusive: true, vertical: true}, true
	case "g_":
		l := clamp(p.Line+n-1, 0, b.last())
		col := b.lineLen(l) - 1
		for col > 0 && unicode.IsSpace(b.lines[l][col]) {
			col--
		}
		return motionResult{pos: Pos{l, max(0, col)}, inclusive: true}, true
	case "|":
		return motionResult{pos: Pos{p.Line, min(n-1, max(0, b.lineLen(p.Line)-1))}}, true
	case "w", "W", "<S-Right>":
		q := p
		for range n {
			q = e.nextWordStart(q, c.key == "W")
		}
		return motionResult{pos: q}, true
	case "b", "B", "<S-Left>":
		q := p
		for range n {
			q = e.prevWordStart(q, c.key == "B")
		}
		return motionResult{pos: q}, true
	case "e", "E":
		q := p
		for range n {
			q = e.nextWordEnd(q, c.key == "E")
		}
		return motionResult{pos: q, inclusive: true}, true
	case "ge", "gE":
		q := p
		for range n {
			q = e.prevWordEnd(q, c.key == "gE")
		}
		return motionResult{pos: q, inclusive: true}, true
	case "gg":
		return e.lineStart(max(c.count, 1) - 1), true
	case "G":
		if c.count == 0 {
			return e.lineStart(b.last()), true
		}
		return e.lineStart(c.count - 1), true
	case "f", "F", "t", "T":
		if !isPrintableKey(c.arg) {
			return motionResult{failed: true}, true
		}
		e.lastFind = findSpec{kind: c.key, char: []rune(c.arg)[0]}
		return e.find(e.lastFind, n, false), true
	case ";", ",":
		f := e.lastFind
		if f.kind == "" {
			return motionResult{failed: true}, true
		}
		if c.key == "," {
			f.kind = map[string]string{"f": "F", "F": "f", "t": "T", "T": "t"}[f.kind]
		}
		return e.find(f, n, true), true
	case "%":
		return e.matchPair(), true
	case "}":
		return motionResult{pos: e.paragraph(n, true)}, true
	case "{":
		return motionResult{pos: e.paragraph(n, false)}, true
	case "H", "L":
		// The count-th line from the top or bottom, kept 'scrolloff'
		// lines from the edge unless an operator is pending.
		l := min(e.top+n-1, b.last())
		if c.key == "L" {
			l = max(e.top, min(e.top+e.height-1, b.last())-n+1)
		}
		if !forOp {
			lo, hi := e.cursorRange()
			l = clamp(l, lo, hi)
		}
		return e.lineStart(l), true
	case "M":
		shown := min(e.height, b.last()-e.top+1)
		return e.lineStart(e.top + (shown+1)/2 - 1), true
	case "n", "N":
		return e.searchNext(c.key == "N", n), true
	case "*", "#":
		return e.searchWord(c.key == "*", n), true
	case "'", "`":
		m, ok := e.marks[[]rune(c.arg)[0]]
		if !ok || m.Line > b.last() {
			return motionResult{failed: true}, true
		}
		if c.key == "'" {
			return e.lineStart(m.Line), true
		}
		return motionResult{pos: m}, true
	}
	return motionResult{}, false
}

func failIf(m motionResult, failed bool) motionResult {
	m.failed = m.failed || failed
	return m
}

func (e *Engine) moveTo(m motionResult) {
	e.cur = m.pos
	if !m.vertical {
		e.want = m.pos.Col
	}
}

func (e *Engine) vertical(line int) motionResult {
	line = clamp(line, 0, e.buf.last())
	col := e.want
	if col < 0 || col > e.buf.lineLen(line)-1 {
		col = max(0, e.buf.lineLen(line)-1)
	}
	if e.want < 0 {
		col = max(0, e.buf.lineLen(line)-1)
	}
	return motionResult{pos: Pos{line, col}, linewise: true, vertical: true}
}

func (e *Engine) lineStart(line int) motionResult {
	line = clamp(line, 0, e.buf.last())
	return motionResult{pos: Pos{line, e.buf.firstNonBlank(line)}, linewise: true}
}

func (e *Engine) isEmptyLine(p Pos) bool { return e.buf.lineLen(p.Line) == 0 }

func (e *Engine) nextWordStart(p Pos, big bool) Pos {
	b := &e.buf
	q := p
	ok := true
	if c := class(b.at(q), big); c != 0 {
		for ok && class(b.at(q), big) == c {
			q, ok = b.next(q)
		}
	}
	for ok && class(b.at(q), big) == 0 {
		if q != p && q.Col == 0 && e.isEmptyLine(q) {
			return q
		}
		q, ok = b.next(q)
	}
	return q
}

func (e *Engine) nextWordEnd(p Pos, big bool) Pos {
	b := &e.buf
	q, ok := b.next(p)
	for ok && class(b.at(q), big) == 0 {
		q, ok = b.next(q)
	}
	c := class(b.at(q), big)
	for {
		nx, ok := b.next(q)
		if !ok || class(b.at(nx), big) != c {
			return q
		}
		q = nx
	}
}

func (e *Engine) prevWordStart(p Pos, big bool) Pos {
	b := &e.buf
	q, ok := b.prev(p)
	for ok && class(b.at(q), big) == 0 {
		if q.Col == 0 && e.isEmptyLine(q) {
			return q
		}
		q, ok = b.prev(q)
	}
	c := class(b.at(q), big)
	for {
		pv, ok := b.prev(q)
		if !ok || class(b.at(pv), big) != c || pv.Line != q.Line {
			return q
		}
		q = pv
	}
}

func (e *Engine) prevWordEnd(p Pos, big bool) Pos {
	b := &e.buf
	q := p
	ok := true
	if c := class(b.at(q), big); c != 0 {
		for ok && class(b.at(q), big) == c {
			q, ok = b.prev(q)
		}
	}
	for ok && class(b.at(q), big) == 0 {
		if q.Col == 0 && e.isEmptyLine(q) {
			return q
		}
		q, ok = b.prev(q)
	}
	return q
}

func (e *Engine) find(f findSpec, n int, repeat bool) motionResult {
	line := e.buf.lines[e.cur.Line]
	col := e.cur.Col
	forward := f.kind == "f" || f.kind == "t"
	if repeat && (f.kind == "t" || f.kind == "T") {
		// Repeating t/T must not get stuck right before the same character.
		if forward {
			col++
		} else {
			col--
		}
	}
	for n > 0 {
		if forward {
			col++
		} else {
			col--
		}
		if col < 0 || col >= len(line) {
			return motionResult{failed: true}
		}
		if line[col] == f.char {
			n--
		}
	}
	switch f.kind {
	case "t":
		col--
	case "T":
		col++
	}
	return motionResult{pos: Pos{e.cur.Line, col}, inclusive: forward}
}

var pairs = map[rune]rune{'(': ')', '[': ']', '{': '}', ')': '(', ']': '[', '}': '{'}

func (e *Engine) matchPair() motionResult {
	b := &e.buf
	line := b.lines[e.cur.Line]
	col := e.cur.Col
	for col < len(line) {
		if _, ok := pairs[line[col]]; ok {
			break
		}
		col++
	}
	if col >= len(line) {
		return motionResult{failed: true}
	}
	open := line[col]
	forward := open == '(' || open == '[' || open == '{'
	if q, ok := e.scanPair(Pos{e.cur.Line, col}, open, pairs[open], forward); ok {
		return motionResult{pos: q, inclusive: true}
	}
	return motionResult{failed: true}
}

// scanPair finds the bracket closing (or opening) the one at p.
func (e *Engine) scanPair(p Pos, open, close rune, forward bool) (Pos, bool) {
	b := &e.buf
	depth := 0
	q, ok := p, true
	for ok {
		switch b.at(q) {
		case open:
			depth++
		case close:
			depth--
			if depth == 0 {
				return q, true
			}
		}
		if forward {
			q, ok = b.next(q)
		} else {
			q, ok = b.prev(q)
		}
	}
	return p, false
}

func (e *Engine) paragraph(n int, forward bool) Pos {
	b := &e.buf
	l := e.cur.Line
	step := 1
	if !forward {
		step = -1
	}
	for range n {
		// Skip blank lines, then the paragraph, landing on the next blank.
		for l+step >= 0 && l+step <= b.last() && b.lineLen(l) == 0 {
			l += step
		}
		for l+step >= 0 && l+step <= b.last() && b.lineLen(l) > 0 {
			l += step
		}
	}
	if forward && b.lineLen(l) > 0 {
		return Pos{l, b.lineLen(l)}
	}
	return Pos{l, 0}
}
