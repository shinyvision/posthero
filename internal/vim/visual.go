package vim

import "strings"

func (e *Engine) enterVisual(m Mode) {
	e.mode = m
	e.anchor = e.cur
	e.pending = nil
}

func (e *Engine) exitVisual() {
	e.lastVisual.anchor, e.lastVisual.cur, e.lastVisual.mode = e.anchor, e.cur, e.mode
	s, en := order(e.anchor, e.cur)
	e.marks['<'], e.marks['>'] = s, en
	e.mode = Normal
	e.pending = nil
}

func (e *Engine) reselect() {
	if e.lastVisual.mode == Normal {
		return
	}
	e.mode = e.lastVisual.mode
	e.anchor, e.cur = e.lastVisual.anchor, e.lastVisual.cur
}

// selectionSpan converts the visual selection into an operator span.
func (e *Engine) selectionSpan() span {
	s, en := order(e.anchor, e.cur)
	if e.mode == VisualLine {
		return span{s: Pos{s.Line, 0}, e: Pos{en.Line, 0}, linewise: true}
	}
	if en.Col >= e.buf.lineLen(en.Line) && en.Line < e.buf.last() {
		return span{s: s, e: Pos{en.Line + 1, 0}}
	}
	return span{s: s, e: Pos{en.Line, min(en.Col+1, e.buf.lineLen(en.Line))}}
}

// SelectedText returns the text of the visual selection.
func (e *Engine) SelectedText() string {
	return e.spanText(e.selectionSpan()).Text
}

func (e *Engine) visualKey(key string) Action {
	if key == "<Esc>" || key == "<C-c>" {
		if len(e.pending) > 0 {
			e.pending = nil
			return Action{}
		}
		e.exitVisual()
		return Action{}
	}
	e.pending = append(e.pending, key)
	c, st := e.parse(e.pending, true)
	switch st {
	case incomplete:
		return Action{}
	case invalid:
		e.pending = nil
		return Action{}
	}
	keys := e.pending
	e.pending = nil
	v := e.selectionSize()
	act := e.visualCommand(c)
	if isVisualChange(c.key) && e.replaying == 0 {
		e.recordDot(keys, v)
	}
	if e.mode == Normal {
		e.endChange()
	}
	return act
}

// visualDot is the amount of text a visual change was made on, so that .
// makes the same change on as much text from the cursor.
type visualDot struct {
	mode  Mode // Visual or VisualLine; Normal for a change made otherwise
	lines int  // lines after the first
	col   int  // characters after the first on a single line, else the end column
}

func (e *Engine) selectionSize() visualDot {
	s, en := order(e.anchor, e.cur)
	v := visualDot{mode: e.mode, lines: en.Line - s.Line, col: en.Col}
	if v.lines == 0 {
		v.col = en.Col - s.Col
	}
	return v
}

func isVisualChange(key string) bool {
	switch key {
	case "d", "x", "<Del>", "X", "D", "c", "s", "C", "S", "R", "~", "u", "U", ">", "<", "J", "gJ", "r", "p", "P":
		return true
	}
	return false
}

// repeatVisual is . after a visual change: it selects as much text from the
// cursor as the change was made on and makes it again.
func (e *Engine) repeatVisual() {
	v := e.dotVisual
	e.mode = v.mode
	e.anchor = e.cur
	end := Pos{min(e.cur.Line+v.lines, e.buf.last()), v.col}
	if v.lines == 0 {
		end.Col = e.cur.Col + v.col
	}
	end.Col = min(end.Col, max(0, e.buf.lineLen(end.Line)-1))
	e.cur = end
	e.replay(e.dot)
}

func (e *Engine) visualCommand(c command) Action {
	sp := e.selectionSpan()
	switch c.key {
	case "<CR>":
		text := e.SelectedText()
		e.exitVisual()
		return Action{Kind: SubmitSelection, Text: text}
	case "v", "V":
		m := map[string]Mode{"v": Visual, "V": VisualLine}[c.key]
		if e.mode == m {
			e.exitVisual()
		} else {
			e.mode = m
		}
	case "o", "O":
		e.anchor, e.cur = e.cur, e.anchor
	case "d", "x", "<Del>", "X", "D", "y", "Y", "c", "s", "C", "S", "R":
		op := map[byte]string{'d': "d", 'x': "d", '<': "d", 'X': "d", 'D': "d", 'y': "y", 'Y': "y", 'c': "c", 's': "c", 'C': "c", 'S': "c", 'R': "c"}[c.key[0]]
		if strings.ContainsAny(c.key, "XDYCSR") {
			sp.linewise = true
		}
		e.exitVisual()
		e.apply(op, c.reg, sp)
	case "~", "u", "U":
		op := map[string]string{"~": "g~", "u": "gu", "U": "gU"}[c.key]
		e.exitVisual()
		e.apply(op, c.reg, sp)
	case ">", "<":
		e.exitVisual()
		for range c.n() {
			e.apply(c.key, c.reg, span{s: sp.s, e: Pos{max(sp.e.Line, sp.s.Line), 0}, linewise: true})
		}
		if !sp.linewise && sp.e.Col == 0 && sp.e.Line > sp.s.Line {
			// The selection ended at the start of the next line.
			e.shiftLine(sp.e.Line, c.key == "<")
		}
	case "J", "gJ":
		e.exitVisual()
		e.beginChange()
		e.join(sp.s.Line, max(sp.e.Line, sp.s.Line+1), c.key == "J")
	case "r":
		if isPrintableKey(c.arg) {
			e.exitVisual()
			e.beginChange()
			r := []rune(c.arg)[0]
			for p := sp.s; p.Before(sp.e) || (sp.linewise && p.Line <= sp.e.Line); {
				if p.Col < e.buf.lineLen(p.Line) {
					e.buf.lines[p.Line][p.Col] = r
				}
				next, ok := e.buf.next(p)
				if !ok {
					break
				}
				p = next
			}
			e.cur = sp.s
		}
	case "p", "P":
		reg := c.reg
		if reg == 0 {
			reg = '"'
		}
		r := e.regs.Get(reg)
		replaced := e.spanText(sp)
		e.exitVisual()
		e.beginChange()
		e.apply("d", '_', sp)
		if c.key == "p" {
			e.regs.store('"', replaced, false)
		}
		if sp.linewise {
			// Text replacing whole lines goes on lines of its own.
			r.Linewise = true
		}
		after := sp.linewise && sp.s.Line > e.buf.last()
		e.putRegister(r, after, 1, false)
	case ":":
		e.exitVisual()
		e.enterCmdline(':', "'<,'>")
	case "/", "?":
		e.enterCmdline(c.key[0], "")
	default:
		if len(c.key) == 2 && (c.key[0] == 'i' || c.key[0] == 'a') {
			if obj, ok := e.textObject(c.key, c.n()); ok {
				e.anchor = obj.s
				if obj.linewise {
					e.mode = VisualLine
					e.cur = obj.e
				} else {
					e.cur, _ = e.buf.prev(obj.e)
				}
			}
			return Action{}
		}
		if m, ok := e.motion(c, false); ok && !m.failed {
			e.moveTo(m)
		}
	}
	return Action{}
}
