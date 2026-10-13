package respview

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/shinyvision/posthero/internal/history"
	"github.com/shinyvision/posthero/internal/script"
	"github.com/shinyvision/posthero/internal/send"
	"github.com/shinyvision/posthero/internal/ui/theme"
)

func record() *history.Record {
	return &history.Record{
		Method: "POST", URL: "http://localhost:8080/graphql", Status: 200, StatusText: "OK", Duration: 143 * time.Millisecond,
		Headers:        []send.Header{{Name: "Content-Type", Value: "application/json"}},
		RequestHeaders: []send.Header{{Name: "Authorization", Value: "Bearer x", Auth: true}},
		Auth:           "OAuth 2.0 from the collection · Shop Token · rachel",
		Body:           []byte(`{"data":{"getUsers":{"rows":[{"id":"u1","email":"ada@example.com","token":"eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiJhZGEiLCJleHAiOjQxMDI0NDQ4MDB9.sig"},{"id":"u2","email":null,"tags":[]}],"total":2}}}`),
		Tests:          []script.Test{{Name: "No GraphQL errors", Passed: true}, {Name: "Sorted", Error: "AssertionError: expected [ 'b', 'a' ] to deeply equal [ 'a', 'b' ]"}},
		Logs:           []script.Log{{Level: "log", Text: "userId set to u1"}},
	}
}

func screen(m *Model) string {
	return ansi.Strip(strings.Join(m.View(70, 40), "\n"))
}

func keys(m *Model, ks ...string) Effect {
	var last Effect
	for _, k := range ks {
		last = m.Key(k)
	}
	return last
}

func TestPage(t *testing.T) {
	m := New()
	m.Show(record())
	s := screen(m)
	for _, want := range []string{
		"tests ✓ 1 passed · ✗ 1 failed", "✗ Sorted", "expected [ 'b', 'a' ]", // failures stay open
		"console · 1 line", "userId set to u1",
		theme.Collapsed + "1 header", theme.Collapsed + "sent POST http://localhost:8080/graphql",
		`"data": {`, `"email": "ada@example.com",`, `"tags": []`, `"total": 2`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("no %q in\n%s", want, s)
		}
	}
	if strings.Contains(s, "Bearer x") {
		t.Error("what was sent isn't folded")
	}
	// The cursor starts on the body.
	if l := m.cur(); !strings.HasPrefix(l.plain, "{") {
		t.Errorf("cursor on %q", l.plain)
	}
}

func TestFoldsPathsAndCopy(t *testing.T) {
	m := New()
	m.Show(record())
	// Down to the first row's email: { "data": { "getUsers": { "rows": [ { "id", "email"
	keys(m, "6", "j")
	if l := m.cur(); l.path != ".data.getUsers.rows[0].email" {
		t.Fatalf("path %q on %q", l.path, l.plain)
	}
	if e := keys(m, "y", "p"); e.Copy != ".data.getUsers.rows[0].email" {
		t.Errorf("yp: %+v", e)
	}
	if e := keys(m, "y", "v"); e.Copy != "ada@example.com" {
		t.Errorf("yv: %+v", e)
	}
	// zc folds the object the cursor is in.
	keys(m, "z", "c")
	if !strings.Contains(screen(m), `{…} 3 keys,`) {
		t.Errorf("not folded:\n%s", screen(m))
	}
	if e := keys(m, "y", "v"); !strings.HasPrefix(e.Copy, "{\n  \"id\": \"u1\"") || strings.HasSuffix(e.Copy, ",") {
		t.Errorf("yv on a fold: %q", e.Copy)
	}
	keys(m, "z", "R")
	keys(m, "z", "M")
	if s := screen(m); !strings.Contains(s, `"data": {…} 1 key`) {
		t.Errorf("zM:\n%s", s)
	}
}

func TestSearchAndJQ(t *testing.T) {
	m := New()
	m.Show(record())
	keys(m, "z", "M")
	// Search opens the folds that hide the match.
	keys(m, "/", "u", "2", "<CR>")
	if l := m.cur(); !strings.Contains(l.plain, `"u2"`) {
		t.Errorf("search went to %q", l.plain)
	}
	m.SetJQ(".data.getUsers.rows[].id")
	s := screen(m)
	if !strings.Contains(s, `"u1"`) || !strings.Contains(s, `"u2"`) || strings.Contains(s, "email") {
		t.Errorf("jq:\n%s", s)
	}
	keys(m, "<Esc>")
	if !strings.Contains(screen(m), "email") {
		t.Error("esc didn't clear the jq filter")
	}
	m.SetJQ(".data[")
	if !strings.Contains(screen(m), "jq: ") {
		t.Errorf("jq error not shown:\n%s", screen(m))
	}
}

func TestExplainJWT(t *testing.T) {
	m := New()
	m.Show(record())
	keys(m, "/", "token", "<CR>")
	e := keys(m, "K")
	if !strings.Contains(e.Show, "a JWT") || !strings.Contains(e.Show, `"sub": "ada"`) || !strings.Contains(e.Show, "expires") {
		t.Errorf("K: %q", e.Show)
	}
}

func TestOtherBodies(t *testing.T) {
	cases := map[string]struct {
		ct, body, want string
	}{
		"xml":    {"application/xml", `<a><b x="1">t</b></a>`, "  <b x=\"1\">t</b>"},
		"text":   {"text/plain", "hello\nworld", "world"},
		"binary": {"image/png", "\x89PNG\x00\x01", "image/png · 6 B"},
		"empty":  {"text/plain", "", "no body"},
	}
	for name, c := range cases {
		m := New()
		m.Show(&history.Record{Status: 200, Headers: []send.Header{{Name: "Content-Type", Value: c.ct}}, Body: []byte(c.body)})
		if s := screen(m); !strings.Contains(s, c.want) {
			t.Errorf("%s:\n%s", name, s)
		}
	}
	m := New()
	m.Show(&history.Record{Error: "couldn't connect to localhost:8080: is it running?"})
	if s := screen(m); !strings.Contains(s, "is it running?") {
		t.Errorf("error:\n%s", s)
	}
}

// long is a record whose body is a JSON array of n numbers, one per line.
func long(n int) *history.Record {
	var b strings.Builder
	b.WriteString("[")
	for i := range n {
		if i > 0 {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, "%d", i)
	}
	b.WriteString("]")
	return &history.Record{Status: 200, Headers: []send.Header{{Name: "Content-Type", Value: "application/json"}}, Body: []byte(b.String())}
}

func TestCounts(t *testing.T) {
	m := New()
	m.Show(long(100))
	m.View(70, 20)
	keys(m, "5", "G")
	if m.cursor != 4 {
		t.Fatalf("5G: line %d", m.cursor+1)
	}
	keys(m, "3", "g", "g")
	if m.cursor != 2 {
		t.Fatalf("3gg: line %d", m.cursor+1)
	}
	keys(m, "G")
	if m.cursor != len(m.vis)-1 {
		t.Fatalf("G: line %d of %d", m.cursor+1, len(m.vis))
	}
	keys(m, "g", "g")
	if m.cursor != 0 {
		t.Fatalf("gg: line %d", m.cursor+1)
	}
}

// <C-d> and <C-u> scroll the view and the cursor together; <C-e> and
// <C-y> scroll the view, moving the cursor only when it would go off it.
func TestScrollKeys(t *testing.T) {
	m := New()
	m.Show(long(100))
	keys(m, "g", "g")
	m.View(70, 20)
	keys(m, "3", "j", "<C-d>")
	m.View(70, 20)
	if m.cursor != 13 || m.top != 10 {
		t.Fatalf("<C-d>: cursor %d top %d, want 13 10", m.cursor, m.top)
	}
	keys(m, "<C-u>")
	m.View(70, 20)
	if m.cursor != 3 || m.top != 0 {
		t.Fatalf("<C-u>: cursor %d top %d, want 3 0", m.cursor, m.top)
	}
	keys(m, "4", "<C-d>", "<C-d>")
	m.View(70, 20)
	if m.cursor != 11 || m.top != 8 {
		t.Fatalf("4<C-d><C-d>: cursor %d top %d, want 11 8", m.cursor, m.top)
	}
	keys(m, "<C-e>")
	m.View(70, 20)
	if m.cursor != 11 || m.top != 9 {
		t.Fatalf("<C-e>: cursor %d top %d, want 11 9", m.cursor, m.top)
	}
	keys(m, "5", "<C-e>")
	m.View(70, 20)
	if m.cursor != 14 || m.top != 14 {
		t.Fatalf("5<C-e>: cursor %d top %d, want 14 14", m.cursor, m.top)
	}
	keys(m, "<C-y>")
	m.View(70, 20)
	if m.cursor != 14 || m.top != 13 {
		t.Fatalf("<C-y>: cursor %d top %d, want 14 13", m.cursor, m.top)
	}
	keys(m, "1", "0", "<C-y>")
	m.View(70, 20)
	if m.cursor != 14 || m.top != 3 {
		t.Fatalf("10<C-y>: cursor %d top %d, want 14 3", m.cursor, m.top)
	}
	keys(m, "M")
	if m.cursor != 3+9 {
		t.Fatalf("M: cursor %d, want 12", m.cursor)
	}
	keys(m, "H")
	if m.cursor != 3 {
		t.Fatalf("H: cursor %d, want 3", m.cursor)
	}
	keys(m, "L")
	if m.cursor != 22 {
		t.Fatalf("L: cursor %d, want 22", m.cursor)
	}
}

// M goes to the middle of what shows, also when that is less than a
// screen.
func TestMShort(t *testing.T) {
	m := New()
	m.Show(&history.Record{Status: 200, Headers: []send.Header{{Name: "Content-Type", Value: "text/plain"}}, Body: []byte("a")})
	m.View(70, 40)
	keys(m, "g", "g", "M")
	if want := (len(m.vis) - 1) / 2; m.cursor != want {
		t.Fatalf("M on %d lines: line %d, want %d", len(m.vis), m.cursor+1, want+1)
	}
}

// A jq filter's output is the body: the cursor starts on it, ]] finds it,
// and folds of the old body don't land on its lines.
func TestJQBody(t *testing.T) {
	m := New()
	m.Show(record())
	m.SetJQ(".data.getUsers.rows[].id")
	if l := m.cur(); l.plain != `"u1"` {
		t.Fatalf("cursor on %q after the jq filter", l.plain)
	}
	keys(m, "g", "g")
	for range 10 {
		keys(m, "]", "]")
	}
	if l := m.cur(); l.plain != `"u1"` {
		t.Fatalf("]] went to %q", l.plain)
	}

	m.Show(record())
	keys(m, "6", "j", "z", "c") // folds the first row
	m.SetJQ(".data")
	for i := range m.closed {
		if m.closed[i] && m.lines[i].body {
			t.Fatalf("line %q of the jq output is folded", m.lines[i].plain)
		}
	}
	if s := screen(m); strings.Contains(s, "…}") || strings.Contains(s, "…]") {
		t.Fatalf("folded:\n%s", s)
	}
	m.SetJQ("")
	if s := screen(m); strings.Contains(s, "…}") {
		t.Fatalf("folded after clearing the filter:\n%s", s)
	}
}

func TestViewTiny(t *testing.T) {
	for w := range 3 {
		for h := range 3 {
			m := New()
			m.Show(record())
			m.View(w, h)
			keys(m, "G", "<C-d>", "<C-u>", "<C-e>", "M", "L", "H")
			m.View(w, h)
			keys(m, "/", "u")
			m.View(w, h)
			m.SetWrap(true)
			if got := m.View(w, h); len(got) != h {
				t.Errorf("View(%d, %d) gave %d lines", w, h, len(got))
			}
		}
	}
}
