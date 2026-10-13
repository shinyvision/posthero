package vim

import (
	"strings"
	"unicode"
)

// Pos is a position in the buffer; Col counts runes.
type Pos struct{ Line, Col int }

// Before reports whether p comes before q.
func (p Pos) Before(q Pos) bool {
	return p.Line < q.Line || (p.Line == q.Line && p.Col < q.Col)
}

func order(a, b Pos) (Pos, Pos) {
	if b.Before(a) {
		return b, a
	}
	return a, b
}

// buffer is the text being edited: always at least one line.
type buffer struct {
	lines [][]rune
}

func newBuffer(text string) buffer {
	parts := strings.Split(text, "\n")
	b := buffer{lines: make([][]rune, len(parts))}
	for i, p := range parts {
		b.lines[i] = []rune(p)
	}
	return b
}

func (b *buffer) text() string {
	parts := make([]string, len(b.lines))
	for i, l := range b.lines {
		parts[i] = string(l)
	}
	return strings.Join(parts, "\n")
}

func (b *buffer) clone() [][]rune {
	out := make([][]rune, len(b.lines))
	for i, l := range b.lines {
		out[i] = append([]rune(nil), l...)
	}
	return out
}

func (b *buffer) lineLen(l int) int { return len(b.lines[l]) }
func (b *buffer) last() int         { return len(b.lines) - 1 }

// at returns the rune at p, or '\n' past the end of a line.
func (b *buffer) at(p Pos) rune {
	if p.Line < 0 || p.Line >= len(b.lines) || p.Col >= len(b.lines[p.Line]) || p.Col < 0 {
		return '\n'
	}
	return b.lines[p.Line][p.Col]
}

// next advances one rune, treating line ends as a '\n' position. It returns
// false at the end of the buffer.
func (b *buffer) next(p Pos) (Pos, bool) {
	if p.Col < len(b.lines[p.Line]) {
		return Pos{p.Line, p.Col + 1}, true
	}
	if p.Line < b.last() {
		return Pos{p.Line + 1, 0}, true
	}
	return p, false
}

func (b *buffer) prev(p Pos) (Pos, bool) {
	if p.Col > 0 {
		return Pos{p.Line, min(p.Col-1, len(b.lines[p.Line]))}, true
	}
	if p.Line > 0 {
		return Pos{p.Line - 1, len(b.lines[p.Line-1])}, true
	}
	return p, false
}

// get returns the text in [s, e). An end of {line+1, 0} includes the newline.
func (b *buffer) get(s, e Pos) string {
	s, e = order(s, e)
	if e.Line > b.last() {
		e = Pos{b.last(), len(b.lines[b.last()])}
	}
	if s.Line == e.Line {
		l := b.lines[s.Line]
		return string(l[min(s.Col, len(l)):min(e.Col, len(l))])
	}
	var sb strings.Builder
	sb.WriteString(string(b.lines[s.Line][min(s.Col, len(b.lines[s.Line])):]))
	for l := s.Line + 1; l < e.Line; l++ {
		sb.WriteByte('\n')
		sb.WriteString(string(b.lines[l]))
	}
	sb.WriteByte('\n')
	sb.WriteString(string(b.lines[e.Line][:min(e.Col, len(b.lines[e.Line]))]))
	return sb.String()
}

// remove deletes [s, e).
func (b *buffer) remove(s, e Pos) {
	s, e = order(s, e)
	if e.Line > b.last() {
		e = Pos{b.last(), len(b.lines[b.last()])}
	}
	s.Col = min(s.Col, len(b.lines[s.Line]))
	e.Col = min(e.Col, len(b.lines[e.Line]))
	head := b.lines[s.Line][:s.Col]
	tail := b.lines[e.Line][e.Col:]
	merged := append(append([]rune(nil), head...), tail...)
	b.lines = append(b.lines[:s.Line], append([][]rune{merged}, b.lines[e.Line+1:]...)...)
}

// insert puts text at p and returns the position just after it.
func (b *buffer) insert(p Pos, text string) Pos {
	p.Col = min(p.Col, len(b.lines[p.Line]))
	parts := strings.Split(text, "\n")
	line := b.lines[p.Line]
	head := append([]rune(nil), line[:p.Col]...)
	tail := append([]rune(nil), line[p.Col:]...)
	if len(parts) == 1 {
		r := []rune(parts[0])
		b.lines[p.Line] = append(append(head, r...), tail...)
		return Pos{p.Line, p.Col + len(r)}
	}
	newLines := make([][]rune, len(parts))
	newLines[0] = append(head, []rune(parts[0])...)
	for i := 1; i < len(parts)-1; i++ {
		newLines[i] = []rune(parts[i])
	}
	lastPart := []rune(parts[len(parts)-1])
	newLines[len(parts)-1] = append(append([]rune(nil), lastPart...), tail...)
	b.lines = append(b.lines[:p.Line], append(newLines, b.lines[p.Line+1:]...)...)
	return Pos{p.Line + len(parts) - 1, len(lastPart)}
}

// insertLines inserts whole lines before index at.
func (b *buffer) insertLines(at int, text string) {
	parts := strings.Split(text, "\n")
	newLines := make([][]rune, len(parts))
	for i, p := range parts {
		newLines[i] = []rune(p)
	}
	b.lines = append(b.lines[:at], append(newLines, b.lines[at:]...)...)
}

// removeLines deletes lines [from, to] and keeps at least one line.
func (b *buffer) removeLines(from, to int) {
	b.lines = append(b.lines[:from], b.lines[to+1:]...)
	if len(b.lines) == 0 {
		b.lines = [][]rune{{}}
	}
}

func (b *buffer) linesText(from, to int) string {
	parts := make([]string, 0, to-from+1)
	for l := from; l <= to; l++ {
		parts = append(parts, string(b.lines[l]))
	}
	return strings.Join(parts, "\n")
}

func (b *buffer) firstNonBlank(l int) int {
	for i, r := range b.lines[l] {
		if !unicode.IsSpace(r) {
			return i
		}
	}
	return max(0, len(b.lines[l])-1)
}

func (b *buffer) indent(l int) string {
	var sb strings.Builder
	for _, r := range b.lines[l] {
		if r != ' ' && r != '\t' {
			break
		}
		sb.WriteRune(r)
	}
	return sb.String()
}

// Character classes for word motions: 0 blank, 1 punctuation, 2 word.
func class(r rune, bigWord bool) int {
	switch {
	case r == '\n' || unicode.IsSpace(r):
		return 0
	case bigWord:
		return 2
	case r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r):
		return 2
	}
	return 1
}
