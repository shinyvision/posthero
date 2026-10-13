package vim

import (
	"strings"
	"unicode"
)

// span is a region an operator acts on. Charwise spans are [s, e); linewise
// spans cover lines s.Line through e.Line.
type span struct {
	s, e     Pos
	linewise bool
}

const shiftWidth = 2

func (e *Engine) operate(c command) {
	sp, ok := e.operatorSpan(c)
	if !ok {
		return
	}
	e.apply(c.op, c.reg, sp)
}

func (e *Engine) operatorSpan(c command) (span, bool) {
	b := &e.buf
	switch {
	case c.key == "line":
		to := min(e.cur.Line+c.n()-1, b.last())
		return span{s: Pos{e.cur.Line, 0}, e: Pos{to, 0}, linewise: true}, true
	case len(c.key) == 2 && (c.key[0] == 'i' || c.key[0] == 'a'):
		sp, ok := e.textObject(c.key, c.n())
		return e.exclusive(sp), ok
	}
	// cw on a word behaves like ce, as in vim, except that it stops at the
	// end of the word under the cursor even when already there.
	if c.op == "c" && (c.key == "w" || c.key == "W") && class(b.at(e.cur), false) != 0 {
		big := c.key == "W"
		q := e.cur
		if nx, _ := b.next(q); class(b.at(nx), big) == class(b.at(q), big) {
			q = e.nextWordEnd(q, big)
		}
		for range c.n() - 1 {
			q = e.nextWordEnd(q, big)
		}
		return span{s: e.cur, e: Pos{q.Line, q.Col + 1}}, true
	}
	m, ok := e.motion(c, true)
	if !ok || m.failed {
		return span{}, false
	}
	if m.linewise {
		s, en := order(e.cur, m.pos)
		return span{s: s, e: en, linewise: true}, true
	}
	s, en := order(e.cur, m.pos)
	if m.inclusive {
		en.Col++
	} else if en.Line > s.Line && en.Col <= e.firstNonBlankOrEnd(en.Line) && (c.key == "w" || c.key == "W") {
		// An exclusive word motion that ends at the start of a later line
		// stops at the end of the previous one ("dw" on the last word).
		en = Pos{en.Line - 1, b.lineLen(en.Line - 1)}
	}
	if m.inclusive {
		return span{s: s, e: en}, true
	}
	return e.exclusive(span{s: s, e: en}), true
}

// exclusive applies vim's rules for a charwise span that ends in column 0
// of a later line (:help exclusive-linewise): the end moves back to the end
// of the line before, and when nothing but white space comes before the
// start the span becomes linewise. That makes "d}" and "di{" on a block
// whose braces have lines of their own delete whole lines.
func (e *Engine) exclusive(sp span) span {
	if sp.linewise || sp.e.Col != 0 || sp.e.Line <= sp.s.Line {
		return sp
	}
	if sp.s.Col <= e.firstNonBlankOrEnd(sp.s.Line) {
		return span{s: Pos{sp.s.Line, 0}, e: Pos{sp.e.Line - 1, 0}, linewise: true}
	}
	return span{s: sp.s, e: Pos{sp.e.Line - 1, e.buf.lineLen(sp.e.Line - 1)}}
}

func (e *Engine) spanText(sp span) Register {
	if sp.linewise {
		return Register{Text: e.buf.linesText(sp.s.Line, sp.e.Line), Linewise: true}
	}
	return Register{Text: e.buf.get(sp.s, sp.e)}
}

// apply runs an operator over a span.
func (e *Engine) apply(op string, reg rune, sp span) {
	b := &e.buf
	switch op {
	case "y":
		e.regs.store(reg, e.spanText(sp), true)
		if sp.linewise {
			e.cur.Line = sp.s.Line
		} else {
			e.cur = sp.s
		}
	case "d":
		if !sp.linewise && sp.s == sp.e {
			// Nothing to delete ("x" on an empty line): the registers
			// keep what they had.
			e.cur = sp.s
			return
		}
		e.beginChange()
		e.regs.store(reg, e.spanText(sp), false)
		if sp.linewise {
			b.removeLines(sp.s.Line, sp.e.Line)
			l := min(sp.s.Line, b.last())
			e.cur = Pos{l, b.firstNonBlank(l)}
		} else {
			b.remove(sp.s, sp.e)
			e.cur = sp.s
		}
	case "c":
		e.beginChange()
		if sp.linewise || sp.s != sp.e {
			e.regs.store(reg, e.spanText(sp), false)
		}
		if sp.linewise {
			indent := b.indent(sp.s.Line)
			if sp.s.Line == 0 && sp.e.Line >= b.last() {
				b.lines = [][]rune{[]rune(indent)}
			} else {
				b.removeLines(sp.s.Line, sp.e.Line)
				b.insertLines(sp.s.Line, indent)
			}
			e.cur = Pos{sp.s.Line, len([]rune(indent))}
		} else {
			b.remove(sp.s, sp.e)
			e.cur = sp.s
		}
		e.enterInsert(Insert)
	case ">", "<":
		e.beginChange()
		for l := sp.s.Line; l <= sp.e.Line; l++ {
			e.shiftLine(l, op == ">")
		}
		e.cur = Pos{sp.s.Line, b.firstNonBlank(sp.s.Line)}
	case "g~", "gu", "gU":
		e.beginChange()
		if sp.linewise {
			sp = span{s: Pos{sp.s.Line, 0}, e: Pos{sp.e.Line, b.lineLen(sp.e.Line)}}
		}
		e.transformCase(sp.s, sp.e, op)
		e.cur = sp.s
	}
}

func (e *Engine) shiftLine(l int, right bool) {
	line := e.buf.lines[l]
	if right {
		if len(line) > 0 {
			e.buf.lines[l] = append([]rune(strings.Repeat(" ", shiftWidth)), line...)
		}
		return
	}
	n := 0
	for n < shiftWidth && n < len(line) && line[n] == ' ' {
		n++
	}
	if n == 0 && len(line) > 0 && line[0] == '\t' {
		n = 1
	}
	e.buf.lines[l] = line[n:]
}

func (e *Engine) transformCase(s, en Pos, op string) {
	for p := s; p.Before(en); {
		line := e.buf.lines[p.Line]
		if p.Col < len(line) {
			r := line[p.Col]
			switch {
			case op == "gu":
				r = unicode.ToLower(r)
			case op == "gU":
				r = unicode.ToUpper(r)
			case unicode.IsUpper(r):
				r = unicode.ToLower(r)
			default:
				r = unicode.ToUpper(r)
			}
			line[p.Col] = r
		}
		next, ok := e.buf.next(p)
		if !ok {
			break
		}
		p = next
	}
}

// put inserts a register after (p) or before (P) the cursor.
func (e *Engine) put(name rune, after bool, count int, cursorAfter bool) {
	if name == 0 {
		name = '"'
	}
	r := e.regs.Get(name)
	if r.Text == "" && !r.Linewise {
		e.msg = "Nothing in register " + string(name)
		return
	}
	e.putRegister(r, after, count, cursorAfter)
}

func (e *Engine) putRegister(r Register, after bool, count int, cursorAfter bool) {
	b := &e.buf
	if r.Linewise && !e.SingleLine {
		text := strings.TrimSuffix(strings.Repeat(r.Text+"\n", count), "\n")
		at := e.cur.Line
		if after {
			at++
		}
		b.insertLines(at, text)
		if cursorAfter {
			l := min(at+strings.Count(text, "\n")+1, b.last())
			e.cur = Pos{l, 0}
		} else {
			e.cur = Pos{at, b.firstNonBlank(at)}
		}
		return
	}
	text := strings.Repeat(r.Text, count)
	if e.SingleLine {
		text = strings.ReplaceAll(text, "\n", " ")
	}
	p := e.cur
	if after && b.lineLen(p.Line) > 0 {
		p.Col++
	}
	end := b.insert(p, text)
	switch {
	case cursorAfter:
		e.cur = end
	case strings.Contains(text, "\n"):
		e.cur = p
	default:
		e.cur = Pos{end.Line, max(0, end.Col-1)}
	}
}

// join joins lines from..to; with spaces it behaves like J, otherwise gJ.
func (e *Engine) join(from, to int, spaces bool) {
	b := &e.buf
	to = min(to, b.last())
	for l := from; l < to; l++ {
		cur := b.lines[from]
		next := b.lines[from+1]
		if spaces {
			trimmed := []rune(strings.TrimLeft(string(next), " \t"))
			sep := " "
			if len(trimmed) == 0 || len(cur) == 0 || cur[len(cur)-1] == ' ' || trimmed[0] == ')' {
				sep = ""
			}
			e.cur = Pos{from, len(cur)}
			next = append([]rune(sep), trimmed...)
		} else {
			e.cur = Pos{from, len(cur)}
		}
		b.lines[from] = append(append([]rune(nil), cur...), next...)
		b.removeLines(from+1, from+1)
	}
}
