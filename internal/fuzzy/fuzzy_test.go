package fuzzy

import (
	"strings"
	"testing"
)

func names(ms []Match, c []string) []string {
	var out []string
	for _, m := range ms {
		out = append(out, c[m.Index])
	}
	return out
}

func TestFilter(t *testing.T) {
	statuses := []string{"Open", "Ready for dev", "Selected for Development", "In Progress", "Pull Request", "Test", "Done", "Won't do"}
	cases := []struct {
		q    string
		want string // first result
	}{
		{"prog", "In Progress"},
		{"pr", "In Progress"}, // a run at a word start beats scattered initials
		{"done", "Done"},
		{"sfd", "Selected for Development"},
		{"test", "Test"},
		{"dev ready", "Ready for dev"},
		{"won", "Won't do"},
	}
	for _, c := range cases {
		got := names(Filter(c.q, statuses), statuses)
		if len(got) == 0 || got[0] != c.want {
			t.Errorf("Filter(%q) = %v, want %q first", c.q, got, c.want)
		}
	}
	if got := Filter("zzz", statuses); len(got) != 0 {
		t.Errorf("zzz matched %v", names(got, statuses))
	}
	if got := Filter("", statuses); len(got) != len(statuses) || got[0].Index != 0 {
		t.Errorf("empty query should keep everything in order")
	}
}

func TestSmartCase(t *testing.T) {
	if !Matches("joris", "Joris Bakker") {
		t.Error("lowercase should ignore case")
	}
	if Matches("JORIS", "Joris Bakker") {
		t.Error("capitals should be case sensitive")
	}
}

func TestHighlight(t *testing.T) {
	m, ok := Score("bak", "Joris Bakker")
	if !ok {
		t.Fatal("no match")
	}
	up := func(s string) string { return "[" + s + "]" }
	id := func(s string) string { return s }
	if got := Highlight("Joris Bakker", m.Runes, up, id); got != "Joris [Bak]ker" {
		t.Errorf("Highlight = %q", got)
	}
	if !strings.Contains(Highlight("ab", nil, up, id), "ab") {
		t.Error("no positions should pass text through")
	}
}

func TestContains(t *testing.T) {
	if !Contains("order get", "GET My orders Shop API") || Contains("order", "List products /api/products") {
		t.Fatal("Contains is wrong")
	}
	if Contains("Order", "my orders") || !Contains("Order", "Create Order") {
		t.Fatal("smart case is wrong")
	}
}

// Letters whose lowercase takes more bytes (İ, Ⱥ) don't shift the
// positions, nor crash the word-start check: positions count runes.
func TestLowercaseKeepsPositions(t *testing.T) {
	m, ok := Score("x", "İx")
	if !ok || len(m.Runes) != 1 || m.Runes[0] != 1 {
		t.Fatalf("%v %v", m, ok)
	}
	m, ok = Score("ord", "İİİ Orders")
	if !ok || len(m.Runes) != 3 || m.Runes[0] != 4 {
		t.Fatalf("%v %v", m, ok)
	}
	for _, c := range []string{"İ", "İİx", "ẞx", "ǅx", "ȺȺ x"} {
		Filter("x", []string{c})
		Filter("i", []string{c})
	}
}

// Words typed together, found together, beat the same words found apart.
func TestPhrase(t *testing.T) {
	got := Filter("a product", []string{
		"GET  Get product Shop API / Products {{baseUrl}}/api/products/:id",
		"e.g. A product Shop API / Products / Get product {{baseUrl}}/api/products/:id",
	})
	if len(got) != 2 || got[0].Index != 1 {
		t.Fatalf("%+v", got)
	}
}
