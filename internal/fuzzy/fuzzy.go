// Package fuzzy ranks text against what someone typed, the way fzf and
// telescope do: the typed characters must appear in order, and matches at
// the start of words, consecutive runs and exact prefixes score higher.
// Several words in the query must each match, in any order.
package fuzzy

import (
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Match is a candidate that matched.
type Match struct {
	Index int   // position in the candidates given
	Score int   // higher is better
	Runes []int // rune positions in the candidate that matched, for highlighting
}

// Filter returns the candidates matching query, best first; ties keep their
// original order. An empty query matches everything in order.
func Filter(query string, candidates []string) []Match {
	words := strings.Fields(query)
	out := make([]Match, 0, len(candidates))
	for i, c := range candidates {
		if m, ok := score(words, c); ok {
			m.Index = i
			out = append(out, m)
		}
	}
	if len(words) > 0 {
		sort.SliceStable(out, func(a, b int) bool { return out[a].Score > out[b].Score })
	}
	return out
}

// Matches reports whether text matches query.
func Matches(query, text string) bool {
	_, ok := score(strings.Fields(query), text)
	return ok
}

// Contains reports whether every word of query appears in text as it is
// typed (smart case), not just as scattered letters.
func Contains(query, text string) bool {
	lower := strings.ToLower(text)
	for _, w := range strings.Fields(query) {
		hay := lower
		if hasUpper(w) {
			hay = text
		} else {
			w = strings.ToLower(w)
		}
		if !strings.Contains(hay, w) {
			return false
		}
	}
	return true
}

// Score is the score of text for query, and whether it matches at all.
func Score(query, text string) (Match, bool) {
	return score(strings.Fields(query), text)
}

func score(words []string, text string) (Match, bool) {
	var m Match
	if len(words) == 0 {
		return m, true
	}
	runes := []rune(text)
	lower := []rune(strings.ToLower(text))
	for _, w := range words {
		s, pos, ok := scoreWord(w, runes, lower)
		if !ok {
			return Match{}, false
		}
		m.Score += s
		m.Runes = append(m.Runes, pos...)
	}
	sort.Ints(m.Runes)
	// Several words found as typed, together, beat the same words apart:
	// "a product" is the example "A product" before "Get product" (whose
	// URL has an a).
	if len(words) > 1 {
		phrase, hay := strings.Join(words, " "), strings.Join(strings.Fields(text), " ")
		if !hasUpper(phrase) {
			phrase, hay = strings.ToLower(phrase), strings.ToLower(hay)
		}
		if strings.Contains(hay, phrase) {
			m.Score += 30 * len(words)
		}
	}
	// Shorter candidates win ties: "Done" beats "Done (old)" for "done".
	m.Score -= len(runes) / 8
	return m, true
}

// scoreWord matches one query word: smart case (capitals in the word make
// it case sensitive), preferring the best of an exact substring and an
// in-order scatter.
func scoreWord(w string, runes, lower []rune) (int, []int, bool) {
	q := []rune(w)
	hay := runes
	if !hasUpper(w) {
		q = []rune(strings.ToLower(w))
		hay = lower
	}
	if len(q) == 0 {
		return 0, nil, true
	}
	if i := indexRunes(hay, q); i >= 0 {
		s := 100 + 10*len(q)
		if i == 0 {
			s += 60
		} else if boundary(runes, i) {
			s += 40
		}
		if len(q) == len(hay) {
			s += 50
		}
		pos := make([]int, len(q))
		for k := range q {
			pos[k] = i + k
		}
		return s, pos, true
	}
	// Scattered: each rune in order, preferring word starts.
	pos := make([]int, 0, len(q))
	s, j, prev := 0, 0, -2
	for _, r := range q {
		found := -1
		for k := j; k < len(hay); k++ {
			if hay[k] == r {
				if found < 0 {
					found = k
				}
				if boundary(runes, k) {
					found = k
					break
				}
				if k == prev+1 {
					break
				}
			}
		}
		if found < 0 {
			return 0, nil, false
		}
		switch {
		case found == prev+1:
			s += 8
		case boundary(runes, found):
			s += 6
		default:
			s += 1
		}
		pos = append(pos, found)
		prev, j = found, found+1
	}
	return s, pos, true
}

// boundary reports whether rune i starts a word.
func boundary(r []rune, i int) bool {
	if i == 0 {
		return true
	}
	p, c := r[i-1], r[i]
	switch {
	case !unicode.IsLetter(p) && !unicode.IsDigit(p):
		return true
	case unicode.IsLower(p) && unicode.IsUpper(c):
		return true
	case unicode.IsLetter(p) != unicode.IsLetter(c):
		return true
	}
	return false
}

func hasUpper(s string) bool {
	for _, r := range s {
		if unicode.IsUpper(r) {
			return true
		}
	}
	return false
}

func indexRunes(hay, needle []rune) int {
	if len(needle) > len(hay) {
		return -1
	}
outer:
	for i := 0; i+len(needle) <= len(hay); i++ {
		for k, r := range needle {
			if hay[i+k] != r {
				continue outer
			}
		}
		return i
	}
	return -1
}

// Highlight wraps the runes of text at positions in style, for showing a
// match; plain text outside them is passed through other.
func Highlight(text string, positions []int, style, other func(string) string) string {
	if len(positions) == 0 {
		return other(text)
	}
	var b strings.Builder
	set := make(map[int]bool, len(positions))
	for _, p := range positions {
		set[p] = true
	}
	var run []rune
	in := false
	flush := func() {
		if len(run) == 0 {
			return
		}
		if in {
			b.WriteString(style(string(run)))
		} else {
			b.WriteString(other(string(run)))
		}
		run = run[:0]
	}
	i := 0
	for len(text) > 0 {
		r, size := utf8.DecodeRuneInString(text)
		text = text[size:]
		if set[i] != in {
			flush()
			in = set[i]
		}
		run = append(run, r)
		i++
	}
	flush()
	return b.String()
}
