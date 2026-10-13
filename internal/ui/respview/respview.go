// Package respview is the response pane: what came back laid out as a
// page, with the tests, console, headers, what was sent and the timing
// folded above the body, and JSON folded per object and array.
package respview

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/charmbracelet/x/ansi"
	"github.com/itchyny/gojq"

	"github.com/shinyvision/posthero/internal/auth"
	"github.com/shinyvision/posthero/internal/history"
	"github.com/shinyvision/posthero/internal/jsonx"
	"github.com/shinyvision/posthero/internal/ui/lineinput"
	"github.com/shinyvision/posthero/internal/ui/theme"
)

// Model is the response pane.
type Model struct {
	rec    *history.Record
	lines  []line
	closed map[int]bool // folds that are closed, by their first line
	vis    []int        // the visible lines
	cursor int          // index into vis
	top    int          // first visible row
	w, h   int
	wrap   bool

	pending string
	count   int // typed before a command
	scroll  int // lines <C-d> and <C-u> move, set by a count; 0 is half the pane

	prompt   *lineinput.Model
	promptOf byte // '/', '?' or '|'
	query    string
	backward bool
	// origin is the line the cursor was on when the search began, to search
	// from as it is typed, and to go back to on esc.
	origin int

	jq    string
	jqOut []string
	jqErr string

	// Notice is a message for the status bar after a key, cleared after it
	// is read.
	notice string
	// Busy says a request is on its way; the pane shows it.
	Busy string
}

// New returns an empty pane.
func New() *Model { return &Model{closed: map[int]bool{}} }

// Record is what the pane shows.
func (m *Model) Record() *history.Record { return m.rec }

// Show shows a record, starting at the top; nil empties the pane.
func (m *Model) Show(r *history.Record) {
	m.rec = r
	m.jq, m.jqOut, m.jqErr = "", nil, ""
	m.layout(true)
	m.cursor, m.top = 0, 0
	m.jumpToBody()
}

// SetWrap wraps long lines.
func (m *Model) SetWrap(on bool) { m.wrap = on }

// layout builds the lines; fresh resets the folds, otherwise the folds
// above the body stay as they are and the body's start as built (the
// body's lines may have changed, and folds are kept by line).
func (m *Model) layout(fresh bool) {
	if m.rec == nil {
		m.lines, m.vis = nil, nil
		return
	}
	oldBody := m.bodyStart()
	lines, closed := build(m.rec, m.jqOut, m.jqErr)
	m.lines = lines
	if fresh || m.closed == nil {
		m.closed = map[int]bool{}
		for _, c := range closed {
			m.closed[c] = true
		}
	} else {
		from := min(oldBody, m.bodyStart())
		for i := range m.closed {
			if i >= from {
				delete(m.closed, i)
			}
		}
		for _, c := range closed {
			if c >= from {
				m.closed[c] = true
			}
		}
	}
	m.visible()
}

// bodyStart is the first line of the body, or the number of lines when
// there is none.
func (m *Model) bodyStart() int {
	if i := m.firstBody(); i >= 0 {
		return i
	}
	return len(m.lines)
}

// visible works out which lines show, given the folds.
func (m *Model) visible() {
	cur := -1
	if m.cursor < len(m.vis) {
		cur = m.vis[m.cursor]
	}
	m.vis = m.vis[:0]
	for i := 0; i < len(m.lines); i++ {
		m.vis = append(m.vis, i)
		if m.closed[i] && m.lines[i].end > i {
			i = m.lines[i].end
		}
	}
	m.cursor = 0
	if cur >= 0 {
		for k, i := range m.vis {
			if i <= cur {
				m.cursor = k
			}
		}
	}
}

// jumpToBody puts the cursor on the first line of the body, when there is
// room for the lines above it.
func (m *Model) jumpToBody() {
	for k, i := range m.vis {
		if m.lines[i].body {
			m.cursor = k
			return
		}
	}
}

// Notice returns and clears the message of the last key.
func (m *Model) Notice() string {
	n := m.notice
	m.notice = ""
	return n
}

// Capturing reports whether a prompt takes the keys.
func (m *Model) Capturing() bool { return m.prompt != nil }

// Effect is what a key asks the app to do.
type Effect struct {
	// Copy is text to copy, What says what it is.
	Copy, What string
	// Open is a URL to open in the browser.
	Open string
	// Show is text to show in a dialog, titled Title.
	Show, Title string
	// Pass says the key isn't the pane's.
	Pass bool
}

// Key handles a key.
func (m *Model) Key(k string) Effect {
	if m.prompt != nil {
		m.promptKey(k)
		return Effect{}
	}
	if len(m.vis) == 0 {
		if k == "|" || k == "/" {
			return Effect{}
		}
		return Effect{Pass: true}
	}
	if p := m.pending; p != "" {
		m.pending = ""
		typed := m.count
		m.count = 0
		return m.pendingKey(p, k, typed)
	}
	if k >= "1" && k <= "9" || k == "0" && m.count > 0 {
		m.count = m.count*10 + int(k[0]-'0')
		return Effect{}
	}
	switch k {
	case "g", "z", "y", "[", "]":
		m.pending = k // the count waits for the second key
		return Effect{}
	}
	typed := m.count
	count := max(1, m.count)
	m.count = 0
	switch k {
	case "j", "<Down>", "<C-n>":
		m.move(count)
	case "k", "<Up>", "<C-p>":
		m.move(-count)
	case "<C-e>":
		m.Scroll(count)
	case "<C-y>":
		m.Scroll(-count)
	case "G", "<End>":
		m.cursor = len(m.vis) - 1
		if typed > 0 && k == "G" {
			m.cursor = 0
			m.move(typed - 1) // {n}G goes to line n
		}
	case "<Home>":
		m.cursor = 0
	case "<C-d>", "<C-u>":
		if typed > 0 {
			m.scroll = typed
		}
		d := m.scroll
		if d == 0 {
			d = max(1, m.h/2)
		}
		if k == "<C-u>" {
			d = -d
		}
		// Like vim: the view and the cursor move together.
		m.top = max(0, min(m.top+d, len(m.vis)-max(1, m.h)))
		m.move(d)
	case "<C-f>", "<PageDown>", " ":
		m.move(max(1, m.h-1))
	case "<C-b>", "<PageUp>":
		m.move(-max(1, m.h-1))
	case "H":
		m.cursor = m.top
		m.move(min(count, m.shown()) - 1)
	case "L":
		m.cursor = m.top + m.shown() - 1
		m.move(-(min(count, m.shown()) - 1))
	case "M":
		m.cursor = m.top
		m.move((m.shown() - 1) / 2)
	case "<Tab>", "<CR>":
		if k == "<CR>" {
			if l := m.cur(); l.end == 0 {
				return Effect{Pass: true}
			}
		}
		m.toggle()
	case "Y":
		return m.copyBody()
	case "/", "?":
		m.prompt = lineinput.New(k, "", nil)
		m.prompt.Insert()
		m.promptOf = k[0]
		m.origin = m.vis[m.cursor]
	case "|":
		m.prompt = lineinput.New("| jq ", m.jq, nil)
		m.prompt.Insert()
		m.promptOf = '|'
	case "n", "N":
		if m.query == "" {
			m.notice = "no previous search"
			return Effect{}
		}
		m.search(m.query, (k == "N") != m.backward, true)
	case "*":
		if l := m.cur(); l.value != "" {
			m.query = regexp.QuoteMeta(l.value)
			m.backward = false
			m.search(m.query, false, true)
		}
	case "K":
		return m.explain()
	case "<Esc>":
		if m.jq != "" {
			m.SetJQ("")
			return Effect{}
		}
		return Effect{Pass: true}
	default:
		return Effect{Pass: true}
	}
	return Effect{}
}

func (m *Model) pendingKey(p, k string, count int) Effect {
	switch p + k {
	case "gg":
		m.cursor = 0
		m.move(max(1, count) - 1) // {n}gg goes to line n
	case "gx":
		if u := firstURL(m.cur().plain); u != "" {
			return Effect{Open: u}
		}
		m.notice = "no link on this line"
	case "gw":
		m.wrap = !m.wrap
		if m.wrap {
			m.notice = "wrapping long lines (:set wrap keeps it)"
		} else {
			m.notice = "not wrapping long lines"
		}
	case "za", "zA":
		m.toggle()
	case "zo", "zO":
		m.setFold(false, k == "O")
	case "zc", "zC":
		m.setFold(true, k == "C")
	case "zM":
		for i, l := range m.lines {
			if l.end > 0 && (!l.body || i != m.firstBody()) {
				m.closed[i] = true
			}
		}
		m.visible()
	case "zR":
		m.closed = map[int]bool{}
		m.visible()
	case "zt":
		m.top = m.cursor
	case "zz":
		m.top = max(0, m.cursor-m.h/2)
	case "zb":
		m.top = max(0, m.cursor-m.h+1)
	case "yy":
		return Effect{Copy: m.cur().plain, What: "the line"}
	case "yv":
		l := m.cur()
		if l.end > 0 {
			return Effect{Copy: m.foldText(m.vis[m.cursor]), What: "the value"}
		}
		if l.value == "" {
			return Effect{Copy: strings.TrimSpace(l.plain), What: "the line"}
		}
		return Effect{Copy: l.value, What: "the value"}
	case "yp":
		if l := m.cur(); l.path != "" {
			return Effect{Copy: l.path, What: "the path " + l.path}
		}
		m.notice = "no JSON path here"
	case "yb", "yB":
		return m.copyBody()
	case "yh":
		if m.rec == nil {
			return Effect{}
		}
		var b strings.Builder
		for _, h := range m.rec.Headers {
			b.WriteString(h.Name + ": " + h.Value + "\n")
		}
		return Effect{Copy: strings.TrimSuffix(b.String(), "\n"), What: "the headers"}
	case "]]":
		for range max(1, count) {
			m.section(1)
		}
	case "[[":
		for range max(1, count) {
			m.section(-1)
		}
	default:
		return Effect{Pass: true}
	}
	return Effect{}
}

func (m *Model) firstBody() int {
	for i, l := range m.lines {
		if l.body {
			return i
		}
	}
	return -1
}

func (m *Model) copyBody() Effect {
	if m.rec == nil {
		return Effect{}
	}
	if m.jq != "" {
		return Effect{Copy: strings.Join(m.jqOut, "\n"), What: "the jq output"}
	}
	return Effect{Copy: string(m.rec.Body), What: "the body"}
}

// foldText is the text of a fold's lines, as shown unfolded.
func (m *Model) foldText(start int) string {
	end := m.lines[start].end
	var out []string
	indent := len(m.lines[start].plain) - len(strings.TrimLeft(m.lines[start].plain, " "))
	for i := start; i <= end && i < len(m.lines); i++ {
		p := m.lines[i].plain
		if len(p) >= indent {
			p = p[indent:]
		}
		out = append(out, p)
	}
	text := strings.Join(out, "\n")
	// A key before the value isn't part of it.
	if c := strings.Index(out[0], "\": "); c >= 0 && strings.HasPrefix(out[0], "\"") {
		text = strings.TrimSpace(text[c+3:])
	}
	return strings.TrimSuffix(text, ",")
}

func (m *Model) cur() line {
	if m.cursor < 0 || m.cursor >= len(m.vis) {
		return line{}
	}
	return m.lines[m.vis[m.cursor]]
}

func (m *Model) move(d int) {
	m.cursor = max(0, min(len(m.vis)-1, m.cursor+d))
}

// shown is how many lines show from the top, at least one.
func (m *Model) shown() int {
	n, used := 0, 0
	for k := m.top; k < len(m.vis); k++ {
		used += m.rowsOf(k, m.w)
		if used > m.h && n > 0 {
			break
		}
		n++
	}
	return max(1, n)
}

// rowsOf is how many rows visible line k takes: more than one when it
// wraps.
func (m *Model) rowsOf(k, w int) int {
	if !m.wrap {
		return 1
	}
	return max(1, (ansi.StringWidth(m.text(m.vis[k]))+w-1)/max(1, w))
}

// enclosing is the fold the line i is in (its first line), or -1.
func (m *Model) enclosing(i int) int {
	best := -1
	for s := i - 1; s >= 0; s-- {
		if m.lines[s].end >= i {
			best = s
			break
		}
	}
	return best
}

// toggle opens or closes the fold on the cursor's line, or the one it is
// in.
func (m *Model) toggle() {
	i := m.vis[m.cursor]
	if m.lines[i].end > 0 {
		m.closed[i] = !m.closed[i]
		m.visible()
		return
	}
	if s := m.enclosing(i); s >= 0 {
		m.closed[s] = true
		m.visible()
		m.goLine(s)
	}
}

func (m *Model) setFold(close, all bool) {
	i := m.vis[m.cursor]
	s := i
	if m.lines[i].end == 0 {
		if s = m.enclosing(i); s < 0 {
			return
		}
	}
	m.closed[s] = close
	if all {
		for j := s + 1; j <= m.lines[s].end; j++ {
			if m.lines[j].end > 0 {
				m.closed[j] = close
			}
		}
	}
	m.visible()
	if close {
		m.goLine(s)
	}
}

// goLine moves the cursor to line i, opening folds that hide it.
func (m *Model) goLine(i int) {
	for s := m.enclosing(i); s >= 0; s = m.enclosing(s) {
		if m.closed[s] {
			delete(m.closed, s)
		}
	}
	m.visible()
	for k, v := range m.vis {
		if v == i {
			m.cursor = k
			return
		}
	}
}

func (m *Model) section(d int) {
	for k := m.cursor + d; k >= 0 && k < len(m.vis); k += d {
		if m.lines[m.vis[k]].section {
			m.cursor = k
			return
		}
	}
}

func (m *Model) promptKey(k string) {
	switch k {
	case "<Esc>":
		if m.promptOf != '|' {
			m.goLine(m.origin)
		}
		m.prompt = nil
		return
	case "<CR>":
		text := m.prompt.Value()
		of := m.promptOf
		m.prompt = nil
		switch of {
		case '|':
			m.SetJQ(text)
		default:
			if text == "" {
				text = m.query
			}
			m.query, m.backward = text, of == '?'
			m.goLine(m.origin)
			m.search(text, m.backward, true)
		}
		return
	}
	m.prompt.Key(k)
	if m.promptOf != '|' && m.prompt.Value() != "" {
		// As it is typed, the search goes from where it began.
		m.goLine(m.origin)
		m.search(m.prompt.Value(), m.promptOf == '?', true)
	}
}

// search moves to the next line matching q (a regexp, smart case), past
// the cursor when skip is set; folds that hide it open.
func (m *Model) search(q string, backward, skip bool) {
	re, err := compile(q)
	if err != nil {
		m.notice = "E486: Pattern not found: " + q
		return
	}
	start := 0
	if m.cursor < len(m.vis) {
		start = m.vis[m.cursor]
	}
	n := len(m.lines)
	for step := 0; step < n; step++ {
		off := step
		if skip {
			off++
		}
		i := (start + off) % n
		if backward {
			i = ((start-off)%n + n) % n
		}
		if re.MatchString(m.lines[i].plain) {
			m.goLine(i)
			if step+1 >= n-start && !backward && skip {
				m.notice = "search hit BOTTOM, continuing at TOP"
			}
			return
		}
	}
	m.notice = "E486: Pattern not found: " + q
}

// compile makes a search pattern: smart case, and plain text when it isn't
// a valid regexp.
func compile(q string) (*regexp.Regexp, error) {
	flags := "(?i)"
	if strings.ToLower(q) != q {
		flags = ""
	}
	re, err := regexp.Compile(flags + q)
	if err != nil {
		return regexp.Compile(flags + regexp.QuoteMeta(q))
	}
	return re, nil
}

// SetJQ filters the body through a jq expression; empty shows the body.
func (m *Model) SetJQ(expr string) {
	expr = strings.TrimSpace(expr)
	m.jq, m.jqOut, m.jqErr = expr, nil, ""
	if expr != "" && m.rec != nil {
		m.jqOut, m.jqErr = runJQ(expr, m.rec.Body)
		if m.jqOut == nil {
			m.jqOut = []string{}
		}
	}
	m.layout(false)
	m.cursor = 0
	m.jumpToBody()
}

// JQ is the jq filter in use.
func (m *Model) JQ() string { return m.jq }

func runJQ(expr string, body []byte) ([]string, string) {
	q, err := gojq.Parse(expr)
	if err != nil {
		return nil, err.Error()
	}
	code, err := gojq.Compile(q)
	if err != nil {
		return nil, err.Error()
	}
	var input any
	dec := json.NewDecoder(strings.NewReader(string(body)))
	dec.UseNumber()
	if err := dec.Decode(&input); err != nil {
		return nil, "the body isn't JSON"
	}
	input = normalizeNumbers(input)
	var out []string
	iter := code.Run(input)
	for len(out) < 10000 {
		v, ok := iter.Next()
		if !ok {
			break
		}
		if err, isErr := v.(error); isErr {
			return out, err.Error()
		}
		data, err := jsonx.Marshal(v)
		if err != nil {
			return out, err.Error()
		}
		out = append(out, string(data))
	}
	return out, ""
}

// normalizeNumbers turns json.Number into what gojq computes with.
func normalizeNumbers(v any) any {
	switch x := v.(type) {
	case json.Number:
		if i, err := x.Int64(); err == nil {
			return int(i)
		}
		f, _ := x.Float64()
		return f
	case map[string]any:
		for k, e := range x {
			x[k] = normalizeNumbers(e)
		}
	case []any:
		for i, e := range x {
			x[i] = normalizeNumbers(e)
		}
	}
	return v
}

// explain shows the value on the cursor's line in full, a JWT decoded.
func (m *Model) explain() Effect {
	l := m.cur()
	v := l.value
	if v == "" {
		v = strings.TrimSpace(l.plain)
	}
	if l.end > 0 {
		v = m.foldText(m.vis[m.cursor])
	}
	title := "Value"
	if l.path != "" {
		title = l.path
	}
	text := v
	tok := strings.TrimPrefix(strings.TrimSpace(v), "Bearer ")
	if claims, ok := auth.Claims(tok); ok {
		parts := strings.Split(tok, ".")
		head, _ := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[0], "="))
		var pretty strings.Builder
		pretty.WriteString("a JWT\n\nheader\n")
		if h, ok := prettyJSON(string(head)); ok {
			pretty.WriteString(h)
		}
		pretty.WriteString("\n\nclaims\n")
		cj, _ := jsonx.MarshalIndent(claims, "", "  ")
		pretty.Write(cj)
		pretty.WriteString("\n\n" + describeTimes(claims))
		text = pretty.String()
	} else if p, ok := prettyJSON(v); ok {
		text = p
	}
	return Effect{Show: text, Title: title}
}

func describeTimes(claims map[string]any) string {
	var out []string
	keys := []string{"iat", "nbf", "exp"}
	names := map[string]string{"iat": "issued", "nbf": "valid from", "exp": "expires"}
	for _, k := range keys {
		if f, ok := claims[k].(float64); ok {
			t := unixTime(int64(f))
			out = append(out, fmt.Sprintf("%s %s (%s)", names[k], t.Local().Format("2 Jan 2006 15:04:05"), relative(t)))
		}
	}
	return strings.Join(out, "\n")
}

var urlPattern = regexp.MustCompile(`https?://[^\s"'<>]+`)

func firstURL(s string) string { return urlPattern.FindString(s) }

// View renders the pane's content: h lines of width w.
func (m *Model) View(w, h int) []string {
	h = max(0, h)
	total := h
	if m.prompt != nil {
		h = max(0, h-1)
	}
	m.w, m.h = w, h
	var out []string
	switch {
	case m.Busy != "" && m.rec == nil:
		out = append(out, theme.Dim.Render(" "+m.Busy))
	case m.rec == nil:
		out = append(out, "", theme.Dim.Render(" nothing sent yet · ⏎ in the request sends it"))
	}
	if m.rec != nil {
		out = m.rows(w, h)
	}
	for len(out) < h {
		out = append(out, "")
	}
	out = out[:h]
	if m.prompt != nil {
		line, _ := m.prompt.View(w)
		out = append(out, line)
	}
	return out[:min(len(out), total)]
}

// rows lays out the visible lines from the top, wrapped or cut.
func (m *Model) rows(w, h int) []string {
	if m.cursor < m.top {
		m.top = m.cursor
	}
	// With wrapping, a line may take several rows; the cursor's line must
	// fit.
	for {
		used := 0
		for k := m.top; k <= m.cursor && k < len(m.vis); k++ {
			used += m.rowsOf(k, w)
		}
		if used <= h || m.top >= m.cursor {
			break
		}
		m.top++
	}
	var re *regexp.Regexp
	if q := m.activeQuery(); q != "" {
		re, _ = compile(q)
	}
	var out []string
	for k := m.top; k < len(m.vis) && len(out) < h; k++ {
		i := m.vis[k]
		text := m.text(i)
		if re != nil {
			text = mark(text, m.lines[i].plain, re)
		}
		var parts []string
		if m.wrap {
			parts = strings.Split(ansi.Hardwrap(text, w, true), "\n")
		} else {
			parts = []string{ansi.Truncate(text, w, theme.Dim.Render("›"))}
		}
		for _, p := range parts {
			if len(out) >= h {
				break
			}
			p = theme.Fit(p, w)
			if k == m.cursor {
				p = cursorLine(p, w)
			}
			out = append(out, p)
		}
	}
	return out
}

func (m *Model) activeQuery() string {
	if m.prompt != nil && m.promptOf != '|' {
		return m.prompt.Value()
	}
	return m.query
}

// text is a line as shown: its summary when folded.
func (m *Model) text(i int) string {
	l := m.lines[i]
	if m.closed[i] && l.end > 0 {
		return l.summary
	}
	return l.text
}

// cursorLine draws the cursor's line with a background.
func cursorLine(s string, w int) string {
	return "\x1b[48;5;236m" + strings.ReplaceAll(theme.Fit(s, w), "\x1b[0m", "\x1b[0;48;5;236m") + "\x1b[0m"
}

// mark underlines what a search matches in a line.
func mark(text, plain string, re *regexp.Regexp) string {
	locs := re.FindAllStringIndex(plain, -1)
	if len(locs) == 0 || ansi.Strip(text) != plain {
		if len(locs) > 0 {
			return theme.Selected.Render(ansi.Strip(text))
		}
		return text
	}
	var b strings.Builder
	last := 0
	for _, l := range locs {
		b.WriteString(theme.Plain.Render(plain[last:l[0]]))
		b.WriteString(theme.Selected.Render(plain[l[0]:l[1]]))
		last = l[1]
	}
	b.WriteString(plain[last:])
	return b.String()
}

// Click puts the cursor on a row (relative to the content), opening or
// closing a fold when it is clicked twice.
func (m *Model) Click(row int, double bool) {
	k := m.top + row
	if k < 0 || k >= len(m.vis) {
		return
	}
	m.cursor = k
	if double {
		m.toggle()
	}
}

// Scroll moves the view by d lines, like <C-e> and <C-y>: the cursor
// stays unless it would go off the screen.
func (m *Model) Scroll(d int) {
	m.top = max(0, min(len(m.vis)-1, m.top+d))
	if m.cursor < m.top {
		m.cursor = m.top
	}
	if n := m.shown(); m.cursor >= m.top+n {
		m.cursor = m.top + n - 1
	}
	m.cursor = max(0, min(len(m.vis)-1, m.cursor))
}

// Position says where the cursor is, for the border: line and lines.
func (m *Model) Position() (int, int) { return m.cursor + 1, len(m.vis) }

// Headers returns the response's headers sorted, for completion and tests.
func (m *Model) Headers() []string {
	if m.rec == nil {
		return nil
	}
	var out []string
	for _, h := range m.rec.Headers {
		out = append(out, h.Name)
	}
	sort.Strings(out)
	return out
}
