package nvim

import (
	"strconv"
	"strings"
	"sync"

	tea "charm.land/bubbletea/v2"
)

// attr is a highlight definition from hl_attr_define: the colors and
// styles for 'termguicolors' (rgb) and for 256 colors (cterm).
type attr struct {
	rgbFg, rgbBg, ctermFg, ctermBg int // -1 when unset
	rgbStyle, ctermStyle           style
}

type style struct {
	bold, italic, underline, strike, reverse bool
}

type cell struct {
	text string
	hl   int
}

type modeInfo struct {
	name  string
	shape string
}

// screen mirrors Neovim's grid from ext_linegrid redraw events.
type screen struct {
	mu        sync.Mutex
	w, h      int
	cells     [][]cell
	attrs     map[int]attr
	defaults  attr
	rgb       bool // termguicolors: use rgb attributes instead of cterm ones
	curRow    int
	curCol    int
	modes     []modeInfo
	mode      int
	busy      bool
	lines     []string // rendered at the last flush
	onFlush   func()
	dirtyRows map[int]bool
}

func newScreen(w, h int) *screen {
	s := &screen{attrs: map[int]attr{}, defaults: attr{rgbFg: -1, rgbBg: -1, ctermFg: -1, ctermBg: -1}}
	s.resize(w, h)
	return s
}

func (s *screen) resize(w, h int) {
	cells := make([][]cell, h)
	for y := range cells {
		cells[y] = make([]cell, w)
		for x := range cells[y] {
			cells[y][x] = cell{text: " "}
			if y < len(s.cells) && x < len(s.cells[y]) {
				cells[y][x] = s.cells[y][x]
			}
		}
	}
	s.w, s.h, s.cells = w, h, cells
}

// handle receives a batch of redraw events.
func (s *screen) handle(updates ...[]any) {
	s.mu.Lock()
	flushed := false
	for _, u := range updates {
		if len(u) == 0 {
			continue
		}
		name, _ := u[0].(string)
		for _, a := range u[1:] {
			args, _ := a.([]any)
			if s.apply(name, args) {
				flushed = true
			}
		}
		if name == "flush" {
			flushed = true
		}
	}
	if flushed {
		s.lines = s.renderLocked()
	}
	s.mu.Unlock()
	if flushed && s.onFlush != nil {
		s.onFlush()
	}
}

func (s *screen) apply(name string, args []any) bool {
	switch name {
	case "grid_resize":
		if num(args, 0) == 1 {
			s.resize(num(args, 1), num(args, 2))
		}
	case "default_colors_set":
		// The rgb colors are -1 when unset (ext_termcolors); the cterm
		// ones are the color number plus one, 0 when unset.
		s.defaults = attr{rgbFg: num(args, 0), rgbBg: num(args, 1), ctermFg: num(args, 3) - 1, ctermBg: num(args, 4) - 1}
	case "hl_attr_define":
		s.attrs[num(args, 0)] = parseAttr(mapArg(args, 1), mapArg(args, 2))
	case "grid_clear":
		for y := range s.cells {
			for x := range s.cells[y] {
				s.cells[y][x] = cell{text: " "}
			}
		}
	case "grid_cursor_goto":
		s.curRow, s.curCol = num(args, 1), num(args, 2)
	case "grid_line":
		s.gridLine(args)
	case "grid_scroll":
		s.scroll(num(args, 1), num(args, 2), num(args, 3), num(args, 4), num(args, 5))
	case "mode_info_set":
		s.modes = nil
		list, _ := arg(args, 1).([]any)
		for _, m := range list {
			mm, _ := m.(map[string]any)
			name, _ := mm["name"].(string)
			shape, _ := mm["cursor_shape"].(string)
			s.modes = append(s.modes, modeInfo{name: name, shape: shape})
		}
	case "mode_change":
		s.mode = num(args, 1)
	case "option_set":
		if n, _ := arg(args, 0).(string); n == "termguicolors" {
			s.rgb, _ = arg(args, 1).(bool)
		}
	case "busy_start":
		s.busy = true
	case "busy_stop":
		s.busy = false
	}
	return false
}

func (s *screen) gridLine(args []any) {
	if num(args, 0) != 1 {
		return
	}
	row, col := num(args, 1), num(args, 2)
	if row < 0 || row >= s.h {
		return
	}
	cells, _ := arg(args, 3).([]any)
	hl := 0
	for _, c := range cells {
		parts, _ := c.([]any)
		text, _ := arg(parts, 0).(string)
		if len(parts) > 1 {
			hl = num(parts, 1)
		}
		repeat := 1
		if len(parts) > 2 {
			repeat = num(parts, 2)
		}
		for range repeat {
			if col < s.w {
				s.cells[row][col] = cell{text: text, hl: hl}
			}
			col++
		}
	}
}

func (s *screen) scroll(top, bot, left, right, rows int) {
	bot, right = min(bot, s.h), min(right, s.w)
	if rows > 0 {
		for y := top; y < bot-rows; y++ {
			copy(s.cells[y][left:right], s.cells[y+rows][left:right])
		}
	} else {
		for y := bot - 1; y >= top-rows; y-- {
			copy(s.cells[y][left:right], s.cells[y+rows][left:right])
		}
	}
}

func (s *screen) render() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.lines == nil {
		return make([]string, s.h)
	}
	return s.lines
}

func (s *screen) renderLocked() []string {
	lines := make([]string, s.h)
	var b strings.Builder
	for y, row := range s.cells {
		b.Reset()
		last := -1
		for _, c := range row {
			if c.text == "" {
				continue // right half of a double-width character
			}
			if c.hl != last {
				b.WriteString(s.sgr(c.hl))
				last = c.hl
			}
			b.WriteString(c.text)
		}
		b.WriteString("\x1b[0m")
		lines[y] = b.String()
	}
	return lines
}

func (s *screen) sgr(hl int) string {
	a, ok := s.attrs[hl]
	if !ok {
		a = attr{rgbFg: -1, rgbBg: -1, ctermFg: -1, ctermBg: -1}
	}
	st := a.ctermStyle
	fg, bg := a.ctermFg, a.ctermBg
	dfg, dbg := s.defaults.ctermFg, s.defaults.ctermBg
	if s.rgb {
		st = a.rgbStyle
		fg, bg, dfg, dbg = a.rgbFg, a.rgbBg, s.defaults.rgbFg, s.defaults.rgbBg
	}
	codes := []string{"0"}
	if st.bold {
		codes = append(codes, "1")
	}
	if st.italic {
		codes = append(codes, "3")
	}
	if st.underline {
		codes = append(codes, "4")
	}
	if st.reverse {
		codes = append(codes, "7")
	}
	if st.strike {
		codes = append(codes, "9")
	}
	if fg < 0 {
		fg = dfg
	}
	if bg < 0 {
		bg = dbg
	}
	if fg >= 0 {
		codes = append(codes, color(fg, s.rgb, false))
	}
	if bg >= 0 {
		codes = append(codes, color(bg, s.rgb, true))
	}
	return "\x1b[" + strings.Join(codes, ";") + "m"
}

func color(c int, rgb, bg bool) string {
	if rgb {
		p := "38;2;"
		if bg {
			p = "48;2;"
		}
		return p + strconv.Itoa(c>>16&0xff) + ";" + strconv.Itoa(c>>8&0xff) + ";" + strconv.Itoa(c&0xff)
	}
	base := 30
	if bg {
		base = 40
	}
	switch {
	case c < 8:
		return strconv.Itoa(base + c)
	case c < 16:
		return strconv.Itoa(base + 60 + c - 8)
	}
	return strconv.Itoa(base+8) + ";5;" + strconv.Itoa(c)
}

func (s *screen) cursor() (int, int, tea.CursorShape, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	shape := tea.CursorBlock
	if s.mode >= 0 && s.mode < len(s.modes) {
		switch s.modes[s.mode].shape {
		case "vertical":
			shape = tea.CursorBar
		case "horizontal":
			shape = tea.CursorUnderline
		}
	}
	return s.curCol, s.curRow, shape, !s.busy
}

func (s *screen) modeName() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.mode < 0 || s.mode >= len(s.modes) {
		return ""
	}
	switch name := s.modes[s.mode].name; {
	case name == "insert":
		return "INSERT"
	case name == "visual":
		return "VISUAL"
	case name == "replace":
		return "REPLACE"
	case strings.HasPrefix(name, "cmdline"):
		return "COMMAND"
	case name == "operator":
		return "PENDING"
	}
	return "NORMAL"
}

func parseAttr(rgb, cterm map[string]any) attr {
	a := attr{rgbFg: -1, rgbBg: -1, ctermFg: -1, ctermBg: -1}
	if v, ok := rgb["foreground"]; ok {
		a.rgbFg = toInt(v)
	}
	if v, ok := rgb["background"]; ok {
		a.rgbBg = toInt(v)
	}
	if v, ok := cterm["foreground"]; ok {
		a.ctermFg = toInt(v)
	}
	if v, ok := cterm["background"]; ok {
		a.ctermBg = toInt(v)
	}
	a.rgbStyle, a.ctermStyle = parseStyle(rgb), parseStyle(cterm)
	return a
}

func parseStyle(m map[string]any) style {
	flag := func(k string) bool {
		v, _ := m[k].(bool)
		return v
	}
	return style{
		bold: flag("bold"), italic: flag("italic"), reverse: flag("reverse"), strike: flag("strikethrough"),
		underline: flag("underline") || flag("undercurl") || flag("underdouble") || flag("underdotted") || flag("underdashed"),
	}
}

func arg(args []any, i int) any {
	if i < len(args) {
		return args[i]
	}
	return nil
}

func num(args []any, i int) int { return toInt(arg(args, i)) }

func mapArg(args []any, i int) map[string]any {
	m, _ := arg(args, i).(map[string]any)
	return m
}

func toInt(v any) int {
	switch n := v.(type) {
	case int64:
		return int(n)
	case uint64:
		return int(n)
	case int:
		return n
	case int8:
		return int(n)
	case int16:
		return int(n)
	case int32:
		return int(n)
	case uint8:
		return int(n)
	case uint16:
		return int(n)
	case uint32:
		return int(n)
	case float64:
		return int(n)
	}
	return -1
}
