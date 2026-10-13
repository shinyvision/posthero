package picker

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

func typ(p *Picker, s string) {
	for _, r := range s {
		p.Key(string(r))
	}
}

func TestPick(t *testing.T) {
	var got string
	p := &Picker{Title: "Move to", Items: []Item{{Label: "Open"}, {Label: "In Progress"}, {Label: "Test", Current: true}, {Label: "Done"}}, Pick: func(it Item) tea.Cmd {
		got = it.Label
		return nil
	}}
	p.init()
	if p.matches[p.sel].item.Label != "Test" {
		t.Fatal("the current value starts highlighted")
	}
	typ(p, "prog")
	if done, _ := p.Key("<CR>"); !done || got != "In Progress" {
		t.Fatalf("picked %q", got)
	}
	view := ansi.Strip(p.View(100, 40))
	if !strings.Contains(view, "Move to") {
		t.Fatal(view)
	}
}

func TestMultiAndCreate(t *testing.T) {
	var got []string
	p := &Picker{Items: []Item{{Label: "backend", Value: "backend"}, {Label: "portal", Value: "portal"}}, Multi: true,
		Preselect: func(it Item) bool { return it.Label == "backend" },
		PickMany: func(items []Item) tea.Cmd {
			for _, it := range items {
				got = append(got, it.Value.(string))
			}
			return nil
		},
		Create: func(text string) (Item, bool) { return Item{Label: text, Value: text}, true },
	}
	p.init()
	typ(p, "urgent")
	if len(p.matches) != 1 || p.matches[0].item.Label != "urgent" {
		t.Fatalf("create offer: %+v", p.matches)
	}
	p.Key("<Tab>") // keeps the new label, marked
	typ(p, "por")
	p.Key("<Tab>") // marks portal
	if done, _ := p.Key("<CR>"); !done {
		t.Fatal("not done")
	}
	if strings.Join(got, ",") != "backend,portal,urgent" {
		t.Fatalf("got %v", got)
	}
	// ⏎ on a new label adds it to what is marked.
	got = nil
	p = &Picker{Items: []Item{{Label: "backend", Value: "backend"}}, Multi: true,
		Preselect: func(Item) bool { return true },
		PickMany: func(items []Item) tea.Cmd {
			for _, it := range items {
				got = append(got, it.Value.(string))
			}
			return nil
		},
		Create: func(text string) (Item, bool) { return Item{Label: text, Value: text}, true },
	}
	p.init()
	typ(p, "infra")
	p.Key("<CR>")
	if strings.Join(got, ",") != "backend,infra" {
		t.Fatalf("got %v", got)
	}
}

func TestSource(t *testing.T) {
	p := &Picker{Items: []Item{{Label: "You"}}}
	asked := ""
	p.Source = func(q string) tea.Cmd {
		asked = q
		return nil
	}
	p.init()
	typ(p, "ja")
	if asked != "ja" {
		t.Fatalf("asked %q", asked)
	}
	p.Update(SourceMsg{Picker: p, Query: "j", Items: []Item{{Label: "old"}}})
	if len(p.all()) != 1 {
		t.Fatal("results for an older query must be ignored")
	}
	p.Update(SourceMsg{Picker: p, Query: "ja", Items: []Item{{Label: "Jaap"}, {Label: "You"}}})
	if len(p.all()) != 2 {
		t.Fatalf("found items: %+v", p.all())
	}
}

func TestUnmarkEverything(t *testing.T) {
	got := []string{"unset"}
	p := &Picker{Items: []Item{{Label: "backend", Value: "backend"}}, Multi: true,
		Preselect: func(Item) bool { return true },
		PickMany: func(items []Item) tea.Cmd {
			got = nil
			for _, it := range items {
				got = append(got, it.Value.(string))
			}
			return nil
		},
	}
	p.init()
	p.Key("<Tab>") // unmark backend
	if done, _ := p.Key("<CR>"); !done || len(got) != 0 {
		t.Fatalf("unmarking everything should pick none, got %v", got)
	}
}

// <C-d> and <C-u> page through the list and stop at its ends; only ^n
// and ^p wrap around.
func TestPageStopsAtEnds(t *testing.T) {
	p := &Picker{Title: "t"}
	for i := range 20 {
		p.Items = append(p.Items, Item{Label: string(rune('a' + i))})
	}
	p.View(100, 40) // 14 rows: <C-d> moves 7
	p.Key("<C-d>")
	p.Key("<C-d>")
	if p.sel != 14 {
		t.Fatalf("<C-d><C-d>: %d", p.sel)
	}
	p.Key("<C-d>")
	if p.sel != 19 {
		t.Fatalf("<C-d> past the end: %d", p.sel)
	}
	p.Key("<PageUp>")
	p.Key("<PageUp>")
	p.Key("<PageUp>")
	if p.sel != 0 {
		t.Fatalf("<PageUp> past the start: %d", p.sel)
	}
	p.Key("<C-p>")
	if p.sel != 19 {
		t.Fatalf("<C-p> wraps: %d", p.sel)
	}
}

func TestViewTiny(t *testing.T) {
	for w := range 3 {
		for h := range 3 {
			p := &Picker{Title: "t", Items: []Item{{Label: "日本語"}, {Label: "b"}}}
			p.View(w, h)
			p.Key("<C-d>")
			p.View(w, h)
		}
	}
}
