// Package textdiff compares two texts line by line, for showing what
// someone else changed.
package textdiff

import "strings"

// Kind says whether a line is in both texts, only the old or only the new.
type Kind byte

// Kinds of lines.
const (
	Same    Kind = ' '
	Removed Kind = '-'
	Added   Kind = '+'
)

// Line is a line of the comparison.
type Line struct {
	Kind Kind
	Text string
}

// Lines compares old and new: the result reads as new, with old's lines
// that are gone marked Removed and new's own lines marked Added. It finds
// the longest common run of lines, which is plenty for requests. Lines the
// texts start and end with alike are matched first; a middle too big to
// compare line by line (a large body changed all over) shows as removed,
// then added.
func Lines(old, new string) []Line {
	a, b := split(old), split(new)
	head := 0
	for head < len(a) && head < len(b) && a[head] == b[head] {
		head++
	}
	tail := 0
	for tail < len(a)-head && tail < len(b)-head && a[len(a)-1-tail] == b[len(b)-1-tail] {
		tail++
	}
	var out []Line
	for _, l := range a[:head] {
		out = append(out, Line{Same, l})
	}
	out = append(out, middle(a[head:len(a)-tail], b[head:len(b)-tail])...)
	for _, l := range a[len(a)-tail:] {
		out = append(out, Line{Same, l})
	}
	return out
}

// maxCells bounds the comparison table: 4M cells is 32 MB.
const maxCells = 4 << 20

func middle(a, b []string) []Line {
	var out []Line
	if (len(a)+1)*(len(b)+1) > maxCells {
		for _, l := range a {
			out = append(out, Line{Removed, l})
		}
		for _, l := range b {
			out = append(out, Line{Added, l})
		}
		return out
	}
	// lcs[i][j] is the longest common subsequence of a[i:] and b[j:].
	lcs := make([][]int, len(a)+1)
	for i := range lcs {
		lcs[i] = make([]int, len(b)+1)
	}
	for i := len(a) - 1; i >= 0; i-- {
		for j := len(b) - 1; j >= 0; j-- {
			if a[i] == b[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else {
				lcs[i][j] = max(lcs[i+1][j], lcs[i][j+1])
			}
		}
	}
	i, j := 0, 0
	for i < len(a) && j < len(b) {
		switch {
		case a[i] == b[j]:
			out = append(out, Line{Same, a[i]})
			i++
			j++
		case lcs[i+1][j] >= lcs[i][j+1]:
			out = append(out, Line{Removed, a[i]})
			i++
		default:
			out = append(out, Line{Added, b[j]})
			j++
		}
	}
	for ; i < len(a); i++ {
		out = append(out, Line{Removed, a[i]})
	}
	for ; j < len(b); j++ {
		out = append(out, Line{Added, b[j]})
	}
	return out
}

// Changed reports whether the comparison has any difference.
func Changed(lines []Line) bool {
	for _, l := range lines {
		if l.Kind != Same {
			return true
		}
	}
	return false
}

func split(s string) []string {
	s = strings.TrimSuffix(s, "\n")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

// Merge merges two versions of a text changed from the same base, by line:
// what only one side changed is taken; where both changed the same lines
// differently, both are kept between git's conflict markers, mine first.
// It returns the text and how many conflicts it has.
func Merge(base, mine, theirs, mineLabel, theirsLabel string) (string, int) {
	b, m, t := split(base), split(mine), split(theirs)
	bm, bt := matches(b, m), matches(b, t)
	var out []string
	conflicts := 0
	pb, pm, pt := 0, 0, 0
	chunk := func(eb, em, et int) {
		cb, cm, ct := b[pb:eb], m[pm:em], t[pt:et]
		switch {
		case equal(cm, cb):
			out = append(out, ct...)
		case equal(ct, cb), equal(cm, ct):
			out = append(out, cm...)
		case len(cb) == len(cm) && len(cb) == len(ct):
			// Lines changed in place: line by line, so changes to lines
			// next to each other don't conflict.
			var cfm, cft []string
			flush := func() {
				if len(cfm) > 0 {
					conflicts++
					out = append(out, "<<<<<<< "+mineLabel)
					out = append(out, cfm...)
					out = append(out, "=======")
					out = append(out, cft...)
					out = append(out, ">>>>>>> "+theirsLabel)
					cfm, cft = nil, nil
				}
			}
			for i := range cb {
				switch {
				case cm[i] == cb[i]:
					flush()
					out = append(out, ct[i])
				case ct[i] == cb[i], cm[i] == ct[i]:
					flush()
					out = append(out, cm[i])
				default:
					cfm, cft = append(cfm, cm[i]), append(cft, ct[i])
				}
			}
			flush()
		default:
			conflicts++
			out = append(out, "<<<<<<< "+mineLabel)
			out = append(out, cm...)
			out = append(out, "=======")
			out = append(out, ct...)
			out = append(out, ">>>>>>> "+theirsLabel)
		}
	}
	for i := range b {
		mi, okm := bm[i]
		ti, okt := bt[i]
		if !okm || !okt || mi < pm || ti < pt {
			continue
		}
		// A base line both sides kept: what is between this and the last
		// one is a chunk to merge.
		chunk(i, mi, ti)
		out = append(out, b[i])
		pb, pm, pt = i+1, mi+1, ti+1
	}
	chunk(len(b), len(m), len(t))
	text := strings.Join(out, "\n")
	if len(out) > 0 && (strings.HasSuffix(mine, "\n") || strings.HasSuffix(theirs, "\n")) {
		text += "\n"
	}
	return text, conflicts
}

// HasConflicts says a text still has conflict markers in it, and on which
// line (1-based) the first one is.
func HasConflicts(text string) (int, bool) {
	for i, l := range strings.Split(text, "\n") {
		if strings.HasPrefix(l, "<<<<<<< ") || l == "=======" || strings.HasPrefix(l, ">>>>>>> ") {
			return i + 1, true
		}
	}
	return 0, false
}

// matches pairs the lines of a with the same lines of b, in order (the
// longest common run): index in a → index in b.
func matches(a, b []string) map[int]int {
	out := map[int]int{}
	head := 0
	for head < len(a) && head < len(b) && a[head] == b[head] {
		out[head] = head
		head++
	}
	tail := 0
	for tail < len(a)-head && tail < len(b)-head && a[len(a)-1-tail] == b[len(b)-1-tail] {
		out[len(a)-1-tail] = len(b) - 1 - tail
		tail++
	}
	ma, mb := a[head:len(a)-tail], b[head:len(b)-tail]
	if (len(ma)+1)*(len(mb)+1) > maxCells {
		return out // too big to compare line by line: the middle is one chunk
	}
	lcs := make([][]int, len(ma)+1)
	for i := range lcs {
		lcs[i] = make([]int, len(mb)+1)
	}
	for i := len(ma) - 1; i >= 0; i-- {
		for j := len(mb) - 1; j >= 0; j-- {
			if ma[i] == mb[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else {
				lcs[i][j] = max(lcs[i+1][j], lcs[i][j+1])
			}
		}
	}
	for i, j := 0, 0; i < len(ma) && j < len(mb); {
		switch {
		case ma[i] == mb[j]:
			out[head+i] = head + j
			i++
			j++
		case lcs[i+1][j] >= lcs[i][j+1]:
			i++
		default:
			j++
		}
	}
	return out
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
