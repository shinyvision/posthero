package vim

// Mouse support, matching Vim with 'mouse' set: a click moves the cursor
// (ending a visual selection, keeping insert mode), dragging selects, a
// double click selects a word, a triple click a line, and the wheel scrolls.

// Click moves the cursor to p.
func (e *Engine) Click(p Pos) {
	if e.mode == Cmdline {
		return
	}
	if e.mode == Visual || e.mode == VisualLine {
		e.exitVisual()
	}
	e.pending = nil
	e.cur = p
	e.clampCursor()
	e.want = e.cur.Col
}

// DragTo extends a visual selection from where the drag began to p.
func (e *Engine) DragTo(p Pos) {
	if e.mode == Cmdline {
		return
	}
	if e.mode != Visual && e.mode != VisualLine {
		e.mouseVisual(Visual)
	}
	e.cur = p
	e.clampCursor()
	e.want = e.cur.Col
}

// SelectWord selects the word at p in visual mode.
func (e *Engine) SelectWord(p Pos) {
	e.Click(p)
	if e.mode == Cmdline {
		return
	}
	sp, ok := e.wordObject(true, false)
	if !ok {
		return
	}
	e.mouseVisual(Visual)
	e.anchor = sp.s
	e.cur = Pos{sp.e.Line, sp.e.Col - 1}
}

// SelectLine selects line p.Line in visual line mode.
func (e *Engine) SelectLine(p Pos) {
	e.Click(p)
	if e.mode != Cmdline {
		e.mouseVisual(VisualLine)
	}
}

func (e *Engine) mouseVisual(m Mode) {
	if e.mode == Insert || e.mode == Replace {
		cur := e.cur
		// leaveInsert drops the final key, normally the <Esc>.
		e.insertTyped = append(e.insertTyped, "<Esc>")
		if e.recordingDot && e.replaying == 0 {
			e.insertKeys = append(e.insertKeys, "<Esc>")
		}
		e.leaveInsert()
		e.cur = cur
	}
	e.enterVisual(m)
}

// Scroll moves the view by n lines (positive is down), dragging the cursor
// along when it would leave the screen, like <C-e> and <C-y>.
func (e *Engine) Scroll(n int) {
	e.top = clamp(e.top+n, 0, max(0, e.buf.last()-e.height+1))
	lo, hi := e.cursorRange()
	line := clamp(e.cur.Line, lo, hi)
	if line != e.cur.Line {
		e.cur.Line = line
		e.cur.Col = e.want
		if e.want < 0 {
			e.cur.Col = e.buf.lineLen(line)
		}
		e.clampCursor()
	}
}
