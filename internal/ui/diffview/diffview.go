// Package diffview shows what a push couldn't push without asking, one
// tab per conflict: Postman's version against yours, line by line. tab and
// shift-tab go from tab to tab; c (or :e) merges the two in the editor; :w
// pushes yours; :q closes a tab (q asks first) and :qq all of them (Q asks
// first), leaving what they show here, unpushed.
package diffview

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/shinyvision/posthero/internal/mouse"
	"github.com/shinyvision/posthero/internal/textdiff"
	"github.com/shinyvision/posthero/internal/ui/lineinput"
	"github.com/shinyvision/posthero/internal/ui/modal"
	"github.com/shinyvision/posthero/internal/ui/theme"
	"github.com/shinyvision/posthero/internal/vim"
)

// Tab is one conflict.
type Tab struct {
	// Key is the conflict's.
	Key string
	// Name is where it is ("Facilo / Roles / Get roles"); What says how it
	// conflicts ("changed here and in Postman").
	Name, What string
	// The documents of the three versions; an empty one is deleted. Mine
	// becomes the merged text once merged.
	Base, Mine, Theirs string
	// Merged says Mine was merged in the editor.
	Merged bool
	// Busy says it is being pushed.
	Busy bool
}

// CanMerge says both sides have it, so there is something to merge.
func (t *Tab) CanMerge() bool { return t.Mine != "" && t.Theirs != "" }

// MergeText is what the editor shows to merge: the two versions with git's
// conflict markers where both changed the same lines, or, once merged,
// the merged text.
func (t *Tab) MergeText() string {
	if t.Merged {
		return t.Mine
	}
	text, _ := textdiff.Merge(t.Base, t.Mine, t.Theirs, "yours", "Postman")
	return text
}

// Messages to the app.
type (
	// PushMsg asks to push a tab's version (:w).
	PushMsg struct {
		View *View
		Tab  *Tab
	}
	// EditMsg asks to merge a tab in the editor (c, :e).
	EditMsg struct {
		View *View
		Tab  *Tab
	}
)

// View is the tabs.
type View struct {
	tabs    []*Tab
	cur     int
	pagers  map[*Tab]*modal.Pager
	prompt  *lineinput.Model
	confirm string // "tab" or "all": waiting for y
}

// New shows tabs.
func New(tabs []*Tab) *View {
	return &View{tabs: tabs, pagers: map[*Tab]*modal.Pager{}}
}

// Len is how many tabs are open.
func (v *View) Len() int { return len(v.tabs) }

// Current is the tab shown.
func (v *View) Current() *Tab {
	if len(v.tabs) == 0 {
		return nil
	}
	return v.tabs[v.cur]
}

// Remove closes a tab (pushed, say). It reports whether none are left.
func (v *View) Remove(t *Tab) bool {
	for i, x := range v.tabs {
		if x == t {
			v.tabs = append(v.tabs[:i], v.tabs[i+1:]...)
			delete(v.pagers, t)
			if v.cur >= len(v.tabs) {
				v.cur = max(0, len(v.tabs)-1)
			}
			break
		}
	}
	return len(v.tabs) == 0
}

// Changed shows a tab again after its versions changed (merged, say).
func (v *View) Changed(t *Tab) { delete(v.pagers, t) }

// Notice shows a message at the bottom until the next key.
func (v *View) Notice(s string) {
	if p := v.pager(); p != nil {
		p.Notice(s)
	}
}

// Key implements modal.Modal.
func (v *View) Key(k string) (bool, tea.Cmd) {
	t := v.Current()
	if t == nil {
		return true, nil
	}
	p := v.pager()
	if v.confirm != "" {
		what := v.confirm
		v.confirm = ""
		p.Status = ""
		if k == "y" || k == "Y" {
			if what == "all" {
				return true, nil
			}
			return v.Remove(t), nil
		}
		return false, nil
	}
	if v.prompt != nil {
		return v.promptKey(k)
	}
	if p.Searching() {
		p.Key(k)
		return false, nil
	}
	switch k {
	case "<Tab>":
		v.cur = (v.cur + 1) % len(v.tabs)
		return false, nil
	case "<S-Tab>":
		v.cur = (v.cur - 1 + len(v.tabs)) % len(v.tabs)
		return false, nil
	case ":":
		v.prompt = lineinput.New(":", "", nil)
		return false, nil
	case "q":
		v.confirm = "tab"
		return false, nil
	case "Q":
		v.confirm = "all"
		return false, nil
	case "c":
		return v.edit(t)
	case "<Esc>", "<CR>":
		p.Key(k) // clears a search; doesn't close
		return false, nil
	}
	p.Key(k)
	return false, nil
}

func (v *View) promptKey(k string) (bool, tea.Cmd) {
	act := v.prompt.Key(k)
	switch {
	case act.Kind == vim.Cancel, k == "<Esc>":
		v.prompt = nil
		return false, nil
	case act.Kind != vim.Submit:
		return false, nil
	}
	cmd := strings.TrimSpace(v.prompt.Value())
	v.prompt = nil
	t := v.Current()
	switch cmd {
	case "":
		return false, nil
	case "q", "q!", "quit", "quit!", "clo", "close":
		return v.Remove(t), nil
	case "qq", "qq!", "qa", "qa!", "qall", "qall!":
		return true, nil
	case "e", "e!", "edit":
		return v.edit(t)
	case "w", "write", "wq", "x":
		if t.Busy {
			v.Notice("pushing " + t.Name + " already")
			return false, nil
		}
		if line, ok := textdiff.HasConflicts(t.Mine); ok {
			v.Notice(fmt.Sprintf("there are conflict markers left (line %d): c merges", line))
			return false, nil
		}
		t.Busy = true
		view := v
		return false, func() tea.Msg { return PushMsg{View: view, Tab: t} }
	}
	v.Notice("E492: Not an editor command: " + cmd)
	return false, nil
}

func (v *View) edit(t *Tab) (bool, tea.Cmd) {
	if !t.CanMerge() {
		v.Notice("nothing to merge: one side deleted it · :w pushes yours, :q leaves it")
		return false, nil
	}
	view := v
	return false, func() tea.Msg { return EditMsg{View: view, Tab: t} }
}

// pager is the current tab's text, scrolled where it was left.
func (v *View) pager() *modal.Pager {
	t := v.Current()
	if t == nil {
		return nil
	}
	p := v.pagers[t]
	if p == nil {
		p = &modal.Pager{Wide: true, Lines: diffLines(t)}
		v.pagers[t] = p
	}
	return p
}

// diffLines is a tab's versions compared: Postman's lines marked -, yours
// marked +.
func diffLines(t *Tab) []string {
	var out []string
	if line, ok := textdiff.HasConflicts(t.Mine); ok {
		out = append(out, theme.Warn.Render(fmt.Sprintf("conflict markers left from line %d: c merges again", line)), "")
	}
	for _, l := range textdiff.Lines(t.Theirs, t.Mine) {
		switch l.Kind {
		case textdiff.Removed:
			out = append(out, theme.Error.Render("- "+l.Text))
		case textdiff.Added:
			out = append(out, theme.Success.Render("+ "+l.Text))
		default:
			out = append(out, theme.Dim.Render("  ")+l.Text)
		}
	}
	if len(out) == 0 {
		out = append(out, theme.Dim.Render("(both deleted it)"))
	}
	return out
}

// View implements modal.Modal.
func (v *View) View(maxW, maxH int) string {
	t := v.Current()
	if t == nil {
		return ""
	}
	p := v.pager()
	width := max(20, min(maxW-6, 104))
	status := t.What
	if t.Merged {
		status += " · merged"
	}
	if t.Busy {
		status += " · pushing…"
	}
	p.Header = []string{
		v.tabBar(width),
		"",
		theme.Heading.Render(ansi.Truncate(t.Name, width, "…")),
		theme.Dim.Render(status) + "   " + theme.Error.Render("- Postman") + "  " + theme.Success.Render("+ yours"),
		"",
	}
	p.Title = fmt.Sprintf("Conflicts · %d of %d", v.cur+1, len(v.tabs))
	p.Hint = "tab next · c merge · :w push yours · :q leave it (q asks) · :qq leave all (Q asks)"
	switch {
	case v.prompt != nil:
		p.Status, _ = v.prompt.View(width)
	case v.confirm == "tab":
		p.Status = theme.Warn.Render("Close this diff? Yours stays here, not pushed (y/n)")
	case v.confirm == "all":
		p.Status = theme.Warn.Render(fmt.Sprintf("Close all %d diffs? Yours stay here, not pushed (y/n)", len(v.tabs)))
	default:
		p.Status = ""
	}
	return p.View(maxW, maxH)
}

// tabBar names the tabs, the current one highlighted, as many as fit.
func (v *View) tabBar(width int) string {
	labels := make([]string, len(v.tabs))
	for i, t := range v.tabs {
		name := t.Name
		if j := strings.LastIndex(name, " / "); j >= 0 {
			name = name[j+3:]
		}
		label := fmt.Sprintf("%d %s", i+1, ansi.Truncate(name, 24, "…"))
		if i == v.cur {
			labels[i] = theme.AccentPill.Render(label)
		} else {
			labels[i] = theme.MutedPill.Render(label)
		}
	}
	// Around the current one, while they fit.
	from, to := v.cur, v.cur+1
	used := ansi.StringWidth(labels[v.cur])
	for {
		grew := false
		if to < len(labels) && used+1+ansi.StringWidth(labels[to]) <= width-4 {
			used += 1 + ansi.StringWidth(labels[to])
			to++
			grew = true
		}
		if from > 0 && used+1+ansi.StringWidth(labels[from-1]) <= width-4 {
			from--
			used += 1 + ansi.StringWidth(labels[from])
			grew = true
		}
		if !grew {
			break
		}
	}
	bar := strings.Join(labels[from:to], " ")
	if from > 0 {
		bar = theme.Dim.Render("… ") + bar
	}
	if to < len(labels) {
		bar += theme.Dim.Render(" …")
	}
	return bar
}

// Mouse implements modal.Mouser: the wheel scrolls.
func (v *View) Mouse(ev mouse.Event) (bool, tea.Cmd) {
	if p := v.pager(); p != nil {
		if dy, _ := ev.WheelDelta(); dy != 0 {
			_, cmd := p.Mouse(ev)
			return false, cmd
		}
	}
	return false, nil
}
