package vim

import (
	"strings"
	"unicode"
)

func (e *Engine) enterInsert(m Mode) {
	e.mode = m
	e.pending = nil
	e.insertTyped = nil
	e.replaced = nil
	e.clampCursor()
}

func (e *Engine) insertKey(key string) Action {
	b := &e.buf
	if e.pendingInsertReg {
		e.pendingInsertReg = false
		if isPrintableKey(key) {
			r := e.regs.Get([]rune(key)[0])
			text := r.Text
			if r.Linewise {
				text += "\n"
			}
			if e.SingleLine {
				text = strings.ReplaceAll(text, "\n", " ")
			}
			e.cur = b.insert(e.cur, text)
		}
		return Action{}
	}
	switch key {
	case "<Esc>", "<C-c>", "<C-[>":
		e.leaveInsert()
	case "<CR>", "<C-m>":
		if e.SingleLine {
			return Action{Kind: Submit}
		}
		indent := b.indent(e.cur.Line)
		if e.cur.Col < len([]rune(indent)) {
			indent = indent[:e.cur.Col]
		}
		e.cur = b.insert(e.cur, "\n"+indent)
	case "<BS>":
		if p, ok := b.prev(e.cur); ok {
			if e.mode == Replace {
				// Put back what was typed over; before where replacing
				// started only move left.
				if n := len(e.replaced); n > 0 && e.replaced[n-1].pos == p {
					r := e.replaced[n-1]
					e.replaced = e.replaced[:n-1]
					if r.appended {
						b.remove(p, e.cur)
					} else {
						b.lines[p.Line][p.Col] = r.orig
					}
				}
				e.cur = p
			} else {
				b.remove(p, e.cur)
				e.cur = p
			}
		}
	case "<Del>":
		if p, ok := b.next(e.cur); ok {
			b.remove(e.cur, p)
		}
	case "<C-w>":
		start := e.cur
		line := b.lines[e.cur.Line]
		col := min(e.cur.Col, len(line))
		for col > 0 && unicode.IsSpace(line[col-1]) {
			col--
		}
		if col > 0 {
			c := class(line[col-1], false)
			for col > 0 && class(line[col-1], false) == c {
				col--
			}
		}
		if col == start.Col && start.Line > 0 {
			p, _ := b.prev(start)
			b.remove(p, start)
			e.cur = p
			return Action{}
		}
		b.remove(Pos{e.cur.Line, col}, start)
		e.cur.Col = col
	case "<C-u>":
		b.remove(Pos{e.cur.Line, 0}, e.cur)
		e.cur.Col = 0
	case "<C-r>":
		e.pendingInsertReg = true
	case "<Tab>":
		e.cur = b.insert(e.cur, strings.Repeat(" ", shiftWidth))
	case "<Left>":
		e.cur.Col = max(0, e.cur.Col-1)
	case "<Right>":
		e.cur.Col = min(b.lineLen(e.cur.Line), e.cur.Col+1)
	case "<Up>":
		if e.cur.Line > 0 {
			e.cur.Line--
		}
	case "<Down>":
		if e.cur.Line < b.last() {
			e.cur.Line++
		}
	case "<Home>":
		e.cur.Col = 0
	case "<End>":
		e.cur.Col = b.lineLen(e.cur.Line)
	default:
		if !isPrintableKey(key) && !(len(key) > 1 && key[0] != '<') {
			return Action{}
		}
		if e.mode == Replace && e.cur.Col < b.lineLen(e.cur.Line) {
			e.replaced = append(e.replaced, replacedCh{pos: e.cur, orig: b.lines[e.cur.Line][e.cur.Col]})
			b.lines[e.cur.Line][e.cur.Col] = []rune(key)[0]
			e.cur.Col++
			return Action{}
		}
		if e.mode == Replace {
			for i := range []rune(key) {
				e.replaced = append(e.replaced, replacedCh{pos: Pos{e.cur.Line, e.cur.Col + i}, appended: true})
			}
		}
		e.cur = b.insert(e.cur, key)
	}
	return Action{}
}

// replacedCh is a character replace mode typed over or appended, so that
// <BS> can undo it.
type replacedCh struct {
	pos      Pos
	orig     rune
	appended bool
}

func (e *Engine) leaveInsert() {
	keys := e.insertTyped
	if len(keys) > 0 {
		keys = keys[:len(keys)-1] // without the <Esc>
	}
	if e.insertRepeat > 0 {
		n := e.insertRepeat
		e.insertRepeat = 0
		for range n {
			if e.insertCmd == "o" || e.insertCmd == "O" {
				e.openLine(e.insertCmd == "o")
			}
			e.replay(keys)
		}
	}
	e.insertRepeat = 0
	e.mode = Normal
	if e.cur.Col > 0 {
		e.cur.Col--
	}
	e.want = e.cur.Col
	e.endChange()
	if e.recordingDot && e.replaying == 0 {
		e.setDot(append(append([]string(nil), e.dotPending...), e.insertKeys...), e.dotPendingVisual)
		e.recordingDot = false
	}
}
