package vim

import "unicode"

// textObject resolves "iw", "a(", "i\"", "ip" and friends.
func (e *Engine) textObject(key string, count int) (span, bool) {
	inner := key[0] == 'i'
	switch obj := key[1:]; obj {
	case "w", "W":
		return e.wordObject(inner, obj == "W")
	case "\"", "'", "`":
		return e.quoteObject(inner, []rune(obj)[0])
	case "(", ")", "b":
		return e.bracketObject(inner, '(', ')', count)
	case "[", "]":
		return e.bracketObject(inner, '[', ']', count)
	case "{", "}", "B":
		return e.bracketObject(inner, '{', '}', count)
	case "<", ">":
		return e.bracketObject(inner, '<', '>', count)
	case "p":
		return e.paragraphObject(inner)
	}
	return span{}, false
}

func (e *Engine) wordObject(inner, big bool) (span, bool) {
	line := e.buf.lines[e.cur.Line]
	if len(line) == 0 {
		return span{}, false
	}
	col := min(e.cur.Col, len(line)-1)
	c := class(line[col], big)
	s, en := col, col
	for s > 0 && class(line[s-1], big) == c {
		s--
	}
	for en+1 < len(line) && class(line[en+1], big) == c {
		en++
	}
	en++ // exclusive
	if inner {
		return span{s: Pos{e.cur.Line, s}, e: Pos{e.cur.Line, en}}, true
	}
	if c == 0 {
		// aw on white space is the white space and the word after it,
		// which may be on a later line.
		q := e.nextWordEnd(Pos{e.cur.Line, en - 1}, big)
		if class(e.buf.at(q), big) == 0 {
			return span{}, false
		}
		return span{s: Pos{e.cur.Line, s}, e: Pos{q.Line, q.Col + 1}}, true
	}
	// Trailing white space is included, or else the white space before
	// the word unless it is the indent.
	if en < len(line) && class(line[en], big) == 0 {
		for en < len(line) && unicode.IsSpace(line[en]) {
			en++
		}
	} else {
		ws := s
		for ws > 0 && unicode.IsSpace(line[ws-1]) {
			ws--
		}
		if ws > 0 {
			s = ws
		}
	}
	return span{s: Pos{e.cur.Line, s}, e: Pos{e.cur.Line, en}}, true
}

func (e *Engine) quoteObject(inner bool, q rune) (span, bool) {
	line := e.buf.lines[e.cur.Line]
	var quotes []int
	for i, r := range line {
		if r == q && (i == 0 || line[i-1] != '\\') {
			quotes = append(quotes, i)
		}
	}
	col := e.cur.Col
	for i := 0; i+1 < len(quotes); i += 2 {
		s, en := quotes[i], quotes[i+1]
		if col > en {
			continue
		}
		if inner {
			return span{s: Pos{e.cur.Line, s + 1}, e: Pos{e.cur.Line, en}}, true
		}
		// Trailing white space is included, or else leading white space.
		end := en + 1
		for end < len(line) && unicode.IsSpace(line[end]) {
			end++
		}
		if end == en+1 {
			for s > 0 && unicode.IsSpace(line[s-1]) {
				s--
			}
		}
		return span{s: Pos{e.cur.Line, s}, e: Pos{e.cur.Line, end}}, true
	}
	return span{}, false
}

func (e *Engine) bracketObject(inner bool, open, close rune, count int) (span, bool) {
	b := &e.buf
	// Walk outwards to the count-th unmatched opening bracket.
	p := e.cur
	if b.at(p) == close {
		if q, ok := e.scanPair(p, close, open, false); ok {
			p = q
		}
	}
	var start Pos
	found := false
	depth := 0
	for q, ok := p, true; ok; q, ok = b.prev(q) {
		switch b.at(q) {
		case close:
			if q != p {
				depth++
			}
		case open:
			if depth == 0 {
				count--
				if count == 0 {
					start, found = q, true
				}
			} else {
				depth--
			}
		}
		if found {
			break
		}
	}
	if !found {
		return span{}, false
	}
	end, ok := e.scanPair(start, open, close, true)
	if !ok {
		return span{}, false
	}
	if !inner {
		return span{s: start, e: Pos{end.Line, end.Col + 1}}, true
	}
	// Leave out the brackets, and the indent before a closing bracket that
	// starts its line, the way vim does: the inside of "{\n  foo\n}" then
	// ends in column 0 of the last line, which operators take linewise.
	s := b.incl(start)
	q := end
	sol := q.Col == 0
	q, _ = b.decl(q)
	for e.inIndent(q) {
		sol = true
		var r int
		if q, r = b.decl(q); r != 0 {
			break
		}
	}
	switch {
	case sol:
		return span{s: s, e: b.incl(q)}, true
	case !q.Before(s):
		return span{s: s, e: Pos{q.Line, q.Col + 1}}, true
	}
	// Nothing between the brackets.
	return span{s: s, e: s}, true
}

// inIndent reports whether p is on the white space a line starts with.
func (e *Engine) inIndent(p Pos) bool {
	return p.Col < e.firstNonBlankOrEnd(p.Line)
}

// inc, incl, dec and decl step through the buffer like vim's functions of
// those names, so that text objects end where vim's do. inc returns 1 when
// it moved to the next line and 2 when it moved onto the end of a line;
// incl and decl step over line ends.
func (b *buffer) inc(p Pos) (Pos, int) {
	if n := len(b.lines[p.Line]); p.Col < n {
		if p.Col++; p.Col < n {
			return p, 0
		}
		return p, 2
	}
	if p.Line < b.last() {
		return Pos{p.Line + 1, 0}, 1
	}
	return p, -1
}

func (b *buffer) incl(p Pos) Pos {
	q, r := b.inc(p)
	if r >= 1 && q.Col > 0 {
		q, _ = b.inc(q)
	}
	return q
}

func (b *buffer) dec(p Pos) (Pos, int) {
	if p.Col > 0 {
		return Pos{p.Line, min(p.Col-1, len(b.lines[p.Line]))}, 0
	}
	if p.Line > 0 {
		return Pos{p.Line - 1, len(b.lines[p.Line-1])}, 1
	}
	return p, -1
}

func (b *buffer) decl(p Pos) (Pos, int) {
	q, r := b.dec(p)
	if r == 1 && q.Col > 0 {
		q, r = b.dec(q)
	}
	return q, r
}

func (e *Engine) paragraphObject(inner bool) (span, bool) {
	b := &e.buf
	blank := func(l int) bool { return len([]rune(trimSpace(b.lines[l]))) == 0 }
	want := blank(e.cur.Line)
	s, en := e.cur.Line, e.cur.Line
	for s > 0 && blank(s-1) == want {
		s--
	}
	for en < b.last() && blank(en+1) == want {
		en++
	}
	if !inner {
		for en < b.last() && blank(en+1) != want {
			en++
		}
	}
	return span{s: Pos{s, 0}, e: Pos{en, 0}, linewise: true}, true
}

func trimSpace(r []rune) []rune {
	for len(r) > 0 && unicode.IsSpace(r[0]) {
		r = r[1:]
	}
	return r
}
