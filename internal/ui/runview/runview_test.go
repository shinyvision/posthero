package runview

import (
	"fmt"
	"testing"

	"github.com/shinyvision/posthero/internal/history"
	"github.com/shinyvision/posthero/internal/postman"
)

func filled(n, iterations int) *Model {
	m := New("c", iterations, n)
	for i := range n {
		m.Add(Row{Iteration: i % iterations, Item: &postman.Item{Name: fmt.Sprintf("r%d", i)}, Record: &history.Record{Status: 200}})
	}
	return m
}

// A pane too short or narrow for the list must not crash.
func TestViewTiny(t *testing.T) {
	for _, n := range []int{0, 1, 3, 30} {
		for _, it := range []int{1, 3} {
			for w := range 3 {
				for h := range 3 {
					m := filled(n, it)
					if got := m.View(w, h); len(got) != h {
						t.Errorf("%d rows, %d iterations: View(%d, %d) gave %d lines", n, it, w, h, len(got))
					}
					m.Key("G")
					m.View(w, h)
					m.Key("k")
					m.View(w, h)
				}
			}
		}
	}
}

// <C-d> and <C-u> scroll the view and the cursor together, as in vim.
func TestHalfPage(t *testing.T) {
	m := filled(100, 1)
	m.Key("g")
	m.Key("g")
	m.View(80, 22) // 20 rows of requests
	m.Key("<C-d>")
	m.View(80, 22)
	if m.cursor != 10 || m.top != 10 {
		t.Fatalf("<C-d>: cursor %d top %d, want 10 10", m.cursor, m.top)
	}
	m.Key("<C-u>")
	m.View(80, 22)
	if m.cursor != 0 || m.top != 0 {
		t.Fatalf("<C-u>: cursor %d top %d, want 0 0", m.cursor, m.top)
	}
	m.Key("5")
	m.Key("j")
	if m.cursor != 5 {
		t.Fatalf("5j: cursor %d", m.cursor)
	}
	m.Key("3")
	m.Key("G")
	if m.cursor != 2 {
		t.Fatalf("3G: cursor %d", m.cursor)
	}
}
