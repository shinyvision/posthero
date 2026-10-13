// Package runview shows a collection run: every request as it runs, with
// its status, time and tests, and a summary.
package runview

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/shinyvision/posthero/internal/history"
	"github.com/shinyvision/posthero/internal/postman"
	"github.com/shinyvision/posthero/internal/run"
	"github.com/shinyvision/posthero/internal/ui/respview"
	"github.com/shinyvision/posthero/internal/ui/theme"
	"github.com/shinyvision/posthero/internal/ui/tree"
)

// Row is a request of the run, as it finished.
type Row struct {
	Iteration int
	Item      *postman.Item
	Record    *history.Record
}

// Model is the run's list.
type Model struct {
	// Title names what runs: a collection or folder.
	Title      string
	Iterations int
	Total      int // requests per iteration
	rows       []Row
	cursor     int
	top        int
	h          int
	Done       bool
	Summary    run.Summary
	Started    time.Time
	pending    string
	count      int
	scroll     int // lines <C-d> and <C-u> move; 0 is half the list
}

// New starts a run's list.
func New(title string, iterations, total int) *Model {
	return &Model{Title: title, Iterations: max(1, iterations), Total: total, Started: time.Now()}
}

// Add adds a finished request, moving the cursor along while it is at the
// end.
func (m *Model) Add(r Row) {
	follow := m.cursor >= len(m.rows)-1
	m.rows = append(m.rows, r)
	if follow {
		m.cursor = len(m.rows) - 1
	}
}

// Finish marks the run as done.
func (m *Model) Finish(s run.Summary) {
	m.Done, m.Summary = true, s
}

// Current is the row under the cursor.
func (m *Model) Current() *Row {
	if m.cursor < 0 || m.cursor >= len(m.rows) {
		return nil
	}
	return &m.rows[m.cursor]
}

// Rows are the finished requests.
func (m *Model) Rows() []Row { return m.rows }

// Key handles a key; open says to show the row under the cursor.
func (m *Model) Key(k string) (open, pass bool) {
	if m.pending == "g" {
		m.pending = ""
		n := m.count
		m.count = 0
		if k == "g" {
			m.cursor = max(1, n) - 1
			m.clampCursor()
			return false, false
		}
		return false, true
	}
	if k >= "1" && k <= "9" || k == "0" && m.count > 0 {
		m.count = m.count*10 + int(k[0]-'0')
		return false, false
	}
	if k == "g" {
		m.pending = "g" // keeps the count for gg
		return false, false
	}
	typed := m.count
	count := max(1, m.count)
	m.count = 0
	switch k {
	case "j", "<Down>":
		m.cursor += count
	case "k", "<Up>":
		m.cursor -= count
	case "G":
		m.cursor = len(m.rows) - 1
		if typed > 0 {
			m.cursor = typed - 1
		}
	case "<C-d>", "<C-u>":
		if typed > 0 {
			m.scroll = typed
		}
		n := m.scroll
		if n == 0 {
			n = max(1, m.h/2)
		}
		if k == "<C-u>" {
			n = -n
		}
		// Like vim: the view and the cursor move together.
		m.top = max(0, min(m.top+n, len(m.rows)-max(1, m.h)))
		m.cursor += n
	case "]f", "n":
		m.nextFailure(1)
	case "N":
		m.nextFailure(-1)
	case "<CR>", "l":
		return m.Current() != nil, false
	default:
		return false, true
	}
	m.clampCursor()
	return false, false
}

func (m *Model) clampCursor() {
	m.cursor = max(0, min(len(m.rows)-1, m.cursor))
}

func (m *Model) nextFailure(d int) {
	for i := m.cursor + d; i >= 0 && i < len(m.rows); i += d {
		if failed(m.rows[i].Record) {
			m.cursor = i
			return
		}
	}
}

func failed(r *history.Record) bool {
	return r.Error != "" || r.Failed() > 0 || len(r.ScriptErrs) > 0
}

// Heading is the pane's title: progress, or the summary.
func (m *Model) Heading() string {
	if !m.Done {
		return fmt.Sprintf("running %s · %d of %d", m.Title, len(m.rows), m.Total*m.Iterations)
	}
	s := m.Summary
	out := fmt.Sprintf("ran %s · %d %s in %s", m.Title, s.Requests, plural(s.Requests, "request"), respview.Duration(s.Duration))
	switch {
	case s.Loop:
		out += " · stopped: setNextRequest went round in circles"
	case s.Stopped:
		out += " · stopped"
	}
	return out
}

// Totals is a line with the test counts.
func (m *Model) Totals() string {
	pass, fail, errs := 0, 0, 0
	for _, r := range m.rows {
		pass += r.Record.Passed()
		fail += r.Record.Failed()
		if r.Record.Error != "" {
			errs++
		}
	}
	var parts []string
	if pass > 0 {
		parts = append(parts, theme.Success.Render(fmt.Sprintf("✓ %d passed", pass)))
	}
	if fail > 0 {
		parts = append(parts, theme.Error.Render(fmt.Sprintf("✗ %d failed", fail)))
	}
	if errs > 0 {
		parts = append(parts, theme.Error.Render(fmt.Sprintf("%d %s", errs, plural(errs, "error"))))
	}
	if len(parts) == 0 {
		return theme.Dim.Render("no tests")
	}
	return strings.Join(parts, theme.Dim.Render(" · "))
}

// View renders the list as h lines of width w.
func (m *Model) View(w, h int) []string {
	h = max(0, h)
	rows := max(1, h-2)
	m.h = rows
	out := []string{" " + m.Totals(), ""}
	if m.cursor < m.top {
		m.top = m.cursor
	}
	if m.cursor >= m.top+rows {
		m.top = m.cursor - rows + 1
	}
	m.top = max(0, min(m.top, len(m.rows)-1))
	lastIter := -1
	if m.top > 0 {
		lastIter = m.rows[m.top-1].Iteration
	}
	for i := m.top; i < len(m.rows) && len(out) < h; i++ {
		r := m.rows[i]
		if m.Iterations > 1 && r.Iteration != lastIter {
			out = append(out, theme.Dim.Render(fmt.Sprintf(" iteration %d of %d", r.Iteration+1, m.Iterations)))
			lastIter = r.Iteration
			if len(out) >= h {
				break
			}
		}
		line := m.line(r, w)
		if i == m.cursor {
			line = theme.Selected.Render(theme.Fit(theme.Strip(line), w))
		}
		out = append(out, line)
	}
	if !m.Done && len(out) < h {
		out = append(out, theme.Dim.Render(" …"))
	}
	for len(out) < h {
		out = append(out, "")
	}
	return out[:h]
}

func (m *Model) line(r Row, w int) string {
	rec := r.Record
	mark := theme.Success.Render("✓")
	switch {
	case rec.Skipped:
		mark = theme.Dim.Render("-")
	case failed(rec):
		mark = theme.Error.Render("✗")
	}
	badge := tree.Badge(r.Item)
	name := theme.Truncate(r.Item.Name, max(10, w/2-8))
	left := " " + mark + " " + tree.BadgeStyle(badge).Render(fmt.Sprintf("%-4s", badge)) + " " + name
	var right string
	switch {
	case rec.Skipped:
		right = theme.Dim.Render("skipped")
	case rec.Error != "":
		right = theme.Error.Render(theme.Truncate(firstLine(rec.Error), max(10, w/2)))
	default:
		right = theme.Status(rec.Status).Render(strconv.Itoa(rec.Status)) + theme.Dim.Render(fmt.Sprintf(" %6s", respview.Duration(rec.Duration)))
		if n := len(rec.Tests); n > 0 {
			right += fmt.Sprintf("  %d/%d", rec.Passed(), n)
		}
	}
	gap := w - theme.Width(left) - theme.Width(right) - 1
	if gap < 1 {
		return theme.Fit(left+" "+right, w)
	}
	return left + strings.Repeat(" ", gap) + right + " "
}

func firstLine(s string) string {
	l, _, _ := strings.Cut(s, "\n")
	return l
}

func plural(n int, w string) string {
	if n == 1 {
		return w
	}
	return w + "s"
}
