package vim

import (
	"strconv"
	"strings"
	"unicode"
)

// command is a parsed normal or visual mode command, e.g. "2d3w" or "\"ayy".
type command struct {
	reg   rune
	count int // 0 when no count was typed
	op    string
	key   string
	arg   string
}

func (c command) n() int { return max(1, c.count) }

type parseState int

const (
	incomplete parseState = iota
	invalid
	complete
)

var operators = map[string]bool{"d": true, "c": true, "y": true, ">": true, "<": true, "g~": true, "gu": true, "gU": true}

func needsArg(k string) bool {
	switch k {
	case "f", "F", "t", "T", "r", "m", "'", "`", "q", "@":
		return true
	}
	return false
}

func parseCount(keys []string, i int) (int, int) {
	n := 0
	for i < len(keys) && len(keys[i]) == 1 && keys[i][0] >= '0' && keys[i][0] <= '9' {
		if keys[i] == "0" && n == 0 {
			break
		}
		n = n*10 + int(keys[i][0]-'0')
		i++
	}
	return n, i
}

// readKey reads one command key, joining two-key commands like "gg" or "zz".
func readKey(keys []string, i int) (string, int, parseState) {
	switch keys[i] {
	case "g", "z", "Z", "[", "]", "<C-w>":
		if i+1 >= len(keys) {
			return "", i, incomplete
		}
		return keys[i] + keys[i+1], i + 2, complete
	}
	return keys[i], i + 1, complete
}

func (e *Engine) parse(keys []string, visual bool) (command, parseState) {
	var c command
	i := 0
	if keys[0] == "\"" {
		if len(keys) < 2 {
			return c, incomplete
		}
		c.reg = []rune(keys[1])[0]
		i = 2
	}
	c.count, i = parseCount(keys, i)
	if i >= len(keys) {
		return c, incomplete
	}
	k, i, st := readKey(keys, i)
	if st != complete {
		return c, st
	}
	if operators[k] && !visual {
		c.op = k
		n, j := parseCount(keys, i)
		i = j
		if n > 0 {
			c.count = c.n() * n
		}
		if i >= len(keys) {
			return c, incomplete
		}
		m, j, st := readKey(keys, i)
		if st != complete {
			return c, st
		}
		i = j
		k = m
		if m == c.op || (len(c.op) == 2 && m == c.op[1:]) {
			c.key = "line"
			return c, complete
		}
	}
	if (k == "i" || k == "a") && (c.op != "" || visual) {
		if i >= len(keys) {
			return c, incomplete
		}
		c.key = k + keys[i]
		return c, complete
	}
	c.key = k
	if needsArg(k) && !(k == "q" && e.macroReg != 0) {
		if i >= len(keys) {
			return c, incomplete
		}
		c.arg = keys[i]
	}
	return c, complete
}

func (e *Engine) normalKey(key string) Action {
	if key == "<Esc>" || key == "<C-c>" {
		if len(e.pending) == 0 && key == "<Esc>" {
			return Action{Kind: Cancel}
		}
		e.pending = nil
		return Action{}
	}
	e.pending = append(e.pending, key)
	c, st := e.parse(e.pending, false)
	switch st {
	case incomplete:
		return Action{}
	case invalid:
		e.pending = nil
		return Action{}
	}
	keys := e.pending
	e.pending = nil
	act := e.execute(c)
	if isChange(c) && e.replaying == 0 {
		e.recordDot(keys, visualDot{})
	}
	if e.mode == Normal {
		e.endChange()
	}
	return act
}

// recordDot records a change for ., waiting for the end of the insert when
// the change started one.
func (e *Engine) recordDot(keys []string, v visualDot) {
	if e.mode == Insert || e.mode == Replace {
		e.dotPending, e.dotPendingVisual = keys, v
		e.recordingDot = true
		e.insertKeys = nil
		return
	}
	e.setDot(keys, v)
}

// setDot remembers keys as the change . repeats, and for a visual change
// how much text it was made on. The count is kept apart, as a count typed
// before . replaces it: "2d3w" is stored as "dw" and 6.
func (e *Engine) setDot(keys []string, v visualDot) {
	e.dotVisual = v
	if v.mode != Normal {
		e.dot, e.dotCount = keys, 0
		return
	}
	e.dot, e.dotCount = withoutCount(keys)
}

func withoutCount(keys []string) ([]string, int) {
	var out []string
	i := 0
	if len(keys) >= 2 && keys[0] == "\"" {
		out = append(out, keys[:2]...)
		i = 2
	}
	count, i := parseCount(keys, i)
	if i >= len(keys) {
		return keys, 0
	}
	k, j, st := readKey(keys, i)
	if st != complete {
		return keys, 0
	}
	out = append(out, keys[i:j]...)
	if operators[k] {
		n, l := parseCount(keys, j)
		if n > 0 {
			count = max(1, count) * n
		}
		j = l
	}
	return append(out, keys[j:]...), count
}

// repeat is the . command; count replaces the count of the change.
func (e *Engine) repeat(count int) {
	if e.dot == nil {
		return
	}
	if e.dotVisual.mode != Normal {
		e.repeatVisual()
		return
	}
	if count > 0 {
		e.dotCount = count
	}
	keys := e.dot
	if e.dotCount > 0 {
		at := 0
		if keys[0] == "\"" {
			at = 2
		}
		var digits []string
		for _, d := range strconv.Itoa(e.dotCount) {
			digits = append(digits, string(d))
		}
		keys = append(append(append([]string(nil), keys[:at]...), digits...), keys[at:]...)
	}
	e.replay(keys)
}

func isChange(c command) bool {
	if c.op != "" {
		return c.op != "y"
	}
	switch c.key {
	case "x", "X", "D", "C", "s", "S", "p", "P", "gp", "gP", "J", "gJ", "~", "r", "R",
		"o", "O", "i", "a", "I", "A", "gI", "<C-a>", "<C-x>":
		return true
	}
	return false
}

// rewrites maps shorthand commands to their operator form.
var rewrites = map[string]command{
	"x":     {op: "d", key: "l"},
	"<Del>": {op: "d", key: "l"},
	"X":     {op: "d", key: "h"},
	"D":     {op: "d", key: "$"},
	"C":     {op: "c", key: "$"},
	"s":     {op: "c", key: "l"},
	"S":     {op: "c", key: "line"},
	"Y":     {op: "y", key: "line"},
}

func (e *Engine) execute(c command) Action {
	if r, ok := rewrites[c.key]; ok && c.op == "" {
		r.reg, r.count = c.reg, c.count
		c = r
	}
	if c.op != "" {
		e.operate(c)
		return Action{}
	}
	if m, ok := e.motion(c, false); ok {
		if !m.failed {
			e.moveTo(m)
		}
		return Action{}
	}
	n := c.n()
	if strings.HasPrefix(c.key, "<C-w>") {
		if k, ok := windowKeys[strings.TrimPrefix(c.key, "<C-w>")]; ok {
			return Action{Kind: Window, Text: k}
		}
		return Action{}
	}
	switch c.key {
	case "<CR>":
		return Action{Kind: Submit}
	case "g<CR>":
		return Action{Kind: Explain}
	case "i", "<Insert>":
		e.startInsert(c, e.cur)
	case "a":
		p := e.cur
		if e.buf.lineLen(p.Line) > 0 {
			p.Col++
		}
		e.startInsert(c, p)
	case "I":
		e.startInsert(c, Pos{e.cur.Line, e.firstNonBlankOrEnd(e.cur.Line)})
	case "gI":
		e.startInsert(c, Pos{e.cur.Line, 0})
	case "A":
		e.startInsert(c, Pos{e.cur.Line, e.buf.lineLen(e.cur.Line)})
	case "o", "O":
		e.openLine(c.key == "o")
		e.insertRepeat = n - 1
		e.insertCmd = c.key
	case "R":
		e.beginChange()
		e.enterInsert(Replace)
	case "p", "P", "gp", "gP":
		e.beginChange()
		e.put(c.reg, c.key == "p" || c.key == "gp", n, strings.HasPrefix(c.key, "g"))
	case "u":
		e.undoChange(n)
	case "<C-r>":
		e.redoChange(n)
	case "J", "gJ":
		e.beginChange()
		e.join(e.cur.Line, e.cur.Line+max(1, n-1), c.key == "J")
	case "~":
		e.beginChange()
		end := Pos{e.cur.Line, min(e.cur.Col+n, e.buf.lineLen(e.cur.Line))}
		e.transformCase(e.cur, end, "g~")
		e.cur = end
	case "r":
		e.replaceChars(c.arg, n)
	case "v":
		e.enterVisual(Visual)
	case "V":
		e.enterVisual(VisualLine)
	case "gv":
		e.reselect()
	case ".":
		e.repeat(c.count)
	case "q":
		e.toggleMacro(c.arg)
	case "@":
		e.runMacro(c.arg, n)
	case "m":
		e.marks[[]rune(c.arg)[0]] = e.cur
	case ":":
		e.enterCmdline(':', "")
	case "/", "?":
		e.enterCmdline(c.key[0], "")
	case "<C-a>", "<C-x>":
		if c.key == "<C-x>" {
			n = -n
		}
		e.increment(n)
	case "<C-d>", "<C-u>", "<C-f>", "<C-b>", "<PageDown>", "<PageUp>", "<C-e>", "<C-y>", "zz", "zt", "zb", "z.", "z<CR>", "z-":
		e.scroll(c.key, c.count)
	case "ZZ":
		return Action{Kind: Ex, Text: "x"}
	case "ZQ":
		return Action{Kind: Ex, Text: "q!"}
	}
	return Action{}
}

// windowKeys maps what follows <C-w> to the window command it is.
var windowKeys = map[string]string{
	"h": "h", "<C-h>": "h", "<Left>": "h", "<BS>": "h",
	"j": "j", "<C-j>": "j", "<Down>": "j",
	"k": "k", "<C-k>": "k", "<Up>": "k",
	"l": "l", "<C-l>": "l", "<Right>": "l",
	"w": "w", "<C-w>": "w", "W": "W",
	"p": "p", "<C-p>": "p",
	"t": "t", "<C-t>": "t",
	"b": "b", "<C-b>": "b",
	"o": "o", "<C-o>": "o",
	"c": "c",
	"q": "q", "<C-q>": "q",
	"=": "=", "+": "+", "-": "-", "<": "<", ">": ">",
	"_": "_", "<C-_>": "_", "|": "|",
}

func (e *Engine) firstNonBlankOrEnd(l int) int {
	for i, r := range e.buf.lines[l] {
		if !unicode.IsSpace(r) {
			return i
		}
	}
	return e.buf.lineLen(l)
}

func (e *Engine) startInsert(c command, p Pos) {
	e.cur = p
	e.insertRepeat = c.n() - 1
	e.insertCmd = c.key
	e.beginChange()
	e.enterInsert(Insert)
}

func (e *Engine) openLine(below bool) {
	e.beginChange()
	indent := e.buf.indent(e.cur.Line)
	at := e.cur.Line
	if below {
		at++
	}
	e.buf.insertLines(at, indent)
	e.cur = Pos{at, len([]rune(indent))}
	e.enterInsert(Insert)
}

func (e *Engine) replaceChars(arg string, n int) {
	line := e.buf.lines[e.cur.Line]
	if e.cur.Col+n > len(line) || !isPrintableKey(arg) {
		return
	}
	e.beginChange()
	r := []rune(arg)[0]
	for i := 0; i < n; i++ {
		line[e.cur.Col+i] = r
	}
	e.cur.Col += n - 1
}

func isPrintableKey(k string) bool {
	return k != "" && (len([]rune(k)) == 1)
}

func (e *Engine) toggleMacro(arg string) {
	if e.macroReg != 0 {
		keys := e.macroKeys
		if len(keys) > 0 {
			keys = keys[:len(keys)-1] // drop the q that stopped recording
		}
		e.macros[e.macroReg] = keys
		e.regs.set(e.macroReg, Register{Text: strings.Join(keys, "")}, false)
		e.macroReg = 0
		return
	}
	if r := []rune(arg)[0]; unicode.IsLetter(r) || unicode.IsDigit(r) {
		e.macroReg = unicode.ToLower(r)
		e.macroKeys = nil
	}
}

func (e *Engine) runMacro(arg string, n int) {
	r := []rune(arg)[0]
	if r == '@' {
		r = e.lastMacro
	}
	keys, ok := e.macros[r]
	if !ok {
		// A register filled by yanking can be executed too.
		for _, ch := range e.regs.Get(r).Text {
			keys = append(keys, string(ch))
		}
	}
	e.lastMacro = r
	for range n {
		e.replay(keys)
	}
}

func (e *Engine) increment(delta int) {
	line := e.buf.lines[e.cur.Line]
	s := e.cur.Col
	for s < len(line) && !unicode.IsDigit(line[s]) {
		s++
	}
	if s == len(line) {
		return
	}
	for s > 0 && unicode.IsDigit(line[s-1]) {
		s--
	}
	if s > 0 && line[s-1] == '-' {
		s--
	}
	en := s + 1
	for en < len(line) && unicode.IsDigit(line[en]) {
		en++
	}
	v, err := strconv.Atoi(string(line[s:en]))
	if err != nil {
		return
	}
	e.beginChange()
	repl := []rune(strconv.Itoa(v + delta))
	e.buf.lines[e.cur.Line] = append(append(append([]rune(nil), line[:s]...), repl...), line[en:]...)
	e.cur.Col = s + len(repl) - 1
}

func (e *Engine) scroll(key string, count int) {
	// move scrolls the view by d lines and the cursor by cursor lines, then
	// keeps the cursor 'scrolloff' lines inside the view.
	move := func(d, cursor int) {
		e.top = clamp(e.top+d, 0, max(0, e.buf.last()-e.height+1))
		e.cur.Line = clamp(e.cur.Line+cursor, 0, e.buf.last())
		lo, hi := e.cursorRange()
		e.cur.Line = clamp(e.cur.Line, lo, hi)
		e.cur.Col = e.buf.firstNonBlank(e.cur.Line)
	}
	n := max(1, count)
	if count > 0 && (key == "<C-d>" || key == "<C-u>") {
		// A count sets 'scroll' for later <C-d> and <C-u> too.
		e.scrollLines = count
	}
	half := e.scrollLines
	if half == 0 {
		half = max(1, e.height/2)
	}
	switch key {
	case "<C-d>":
		move(half, half)
	case "<C-u>":
		move(-half, -half)
	case "<C-f>", "<PageDown>":
		move(n*max(1, e.height-2), 0)
	case "<C-b>", "<PageUp>":
		move(-n*max(1, e.height-2), 0)
	case "<C-e>":
		e.Scroll(n)
	case "<C-y>":
		e.Scroll(-n)
	case "zz", "z.":
		e.top = max(0, e.cur.Line-(e.height-1)/2)
	case "zt", "z<CR>":
		e.top = e.cur.Line
	case "zb", "z-":
		e.top = max(0, e.cur.Line-e.height+1)
	}
}

// cursorRange returns the lines the cursor can be on without the view
// scrolling: 'scrolloff' lines away from its edges, unless the view shows
// the start or end of the buffer.
func (e *Engine) cursorRange() (int, int) {
	off := min(scrollOff, (e.height-1)/2)
	lo := e.top + off
	if e.top == 0 {
		lo = 0
	}
	bottom := e.top + e.height - 1
	hi := bottom - off
	if bottom >= e.buf.last() {
		hi = e.buf.last()
	}
	return lo, max(lo, hi)
}
