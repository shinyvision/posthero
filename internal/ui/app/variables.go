package app

import (
	"fmt"
	"sort"
	"strings"
	"sync"

	tea "charm.land/bubbletea/v2"

	"github.com/shinyvision/posthero/internal/editor"
	"github.com/shinyvision/posthero/internal/graphql"
	"github.com/shinyvision/posthero/internal/postman"
	"github.com/shinyvision/posthero/internal/reqdoc"
	"github.com/shinyvision/posthero/internal/send"
	"github.com/shinyvision/posthero/internal/store"
	"github.com/shinyvision/posthero/internal/ui/modal"
	"github.com/shinyvision/posthero/internal/vars"
)

// envFor is the environment picked for a collection: the one picked
// before, else one whose name starts with the collection's.
func (m *Model) envFor(colID string) string {
	if m.st == nil {
		return ""
	}
	if id, ok := m.state.Env[colID]; ok {
		if id == "" || m.st.Env(id) != nil {
			return id
		}
	}
	if c := m.st.Col(colID); c != nil {
		if e := guessEnv(c.Name(), m.st.Environments()); e != nil {
			return e.Summary.ID
		}
	}
	return ""
}

// guessEnv picks the environment that goes with a collection by name:
// "Shop - Local" for "Shop", preferring local ones.
func guessEnv(colName string, envs []*store.Env) *store.Env {
	var best *store.Env
	bestScore := 0
	for _, e := range envs {
		if score, _ := envScore(colName, e); score > bestScore {
			best, bestScore = e, score
		}
	}
	return best
}

// envScore says how well an environment's name fits a collection's to pick
// it by itself: more for a local or dev one, less for production (0 is
// never). ours says the name is the collection's at all.
func envScore(colName string, e *store.Env) (score int, ours bool) {
	name := normName(colName)
	en := normName(e.Name())
	fw := firstWord(strings.ToLower(e.Name()))
	switch {
	case strings.HasPrefix(en, name):
		score = 3
	case name != "" && strings.Contains(en, name), len(fw) > 2 && strings.HasPrefix(name, fw):
		score = 2
	default:
		return 0, false
	}
	low := strings.ToLower(e.Name())
	if strings.Contains(low, "local") {
		score += 2
	} else if strings.Contains(low, "dev") {
		score++
	}
	if isProd(e.Name()) {
		score -= 2
	}
	return score, true
}

func normName(s string) string {
	return strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			return r
		}
		if r >= 'A' && r <= 'Z' {
			return r + 32
		}
		return -1
	}, s)
}

func firstWord(s string) string {
	for i, r := range s {
		if r < 'a' || r > 'z' {
			return s[:i]
		}
	}
	return s
}

// isProd reports whether an environment's name says production.
func isProd(name string) bool {
	// Every word of the name, not just the first place a word turns up:
	// "Products - Prod" is production.
	words := strings.FieldsFunc(strings.ToLower(name), func(r rune) bool { return r < 'a' || r > 'z' })
	for _, w := range words {
		switch w {
		case "prod", "prd", "production", "live":
			return true
		}
	}
	return false
}

// envName is the name of the environment used for a collection.
func (m *Model) envName(colID string) string {
	if m.st == nil {
		return ""
	}
	if e := m.st.Env(m.envFor(colID)); e != nil {
		return e.Name()
	}
	return ""
}

// scopes are the variables a request of a collection sees now.
func (m *Model) scopes(colID string) *vars.Set {
	set := m.st.Scopes(colID, m.envFor(colID), nil, nil)
	set.Vault = m.vault.Get
	return set
}

// refreshHints shows what the open request's variables are, next to them
// in the editor.
func (m *Model) refreshHints() {
	m.updateCompletions()
	if m.open == nil || m.editor.backend == nil || m.st == nil {
		return
	}
	file := m.open.file
	if !m.settings.ShowValues {
		m.editor.backend.SetHints(file, nil)
		return
	}
	text, ok := m.docText(m.open.docRef)
	if !ok {
		return
	}
	m.editor.backend.SetHints(file, hints(text, m.scopes(m.open.colID)))
}

// hints are the notes after lines with {{variables}}: the request line
// gets the URL as it will be sent, other lines the variables' values.
func hints(text string, set *vars.Set) []editor.Hint {
	var out []editor.Hint
	lines := strings.Split(text, "\n")
	first := -1
	for i, l := range lines {
		t := strings.TrimSpace(l)
		if t == "" || strings.HasPrefix(t, "#") {
			continue
		}
		first = i
		break
	}
	if first >= 0 {
		if h, ok := urlHint(text, set); ok {
			h.Line = first
			out = append(out, h)
		}
	}
	inScript := false
	for i, l := range lines {
		if i == first {
			continue
		}
		if strings.HasPrefix(l, "### ") {
			name := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(l, "###")))
			// Scripts and text say {{variables}} as they are; so does an
			// example's response, which came back that way.
			inScript = strings.HasPrefix(name, "pre") || strings.HasPrefix(name, "test") || strings.HasPrefix(name, "description") || strings.HasPrefix(name, "response")
			continue
		}
		if inScript || !strings.Contains(l, "{{") {
			continue
		}
		names := vars.Names(l)
		if len(names) == 0 {
			continue
		}
		var parts, missing []string
		for _, n := range names {
			switch {
			case strings.HasPrefix(n, "$"):
				parts = append(parts, n+" is made up each time")
			case strings.HasPrefix(n, "vault:"):
				if _, ok := set.Vault(strings.TrimPrefix(n, "vault:")); ok {
					parts = append(parts, n+" = ••••")
				} else {
					missing = append(missing, n)
				}
			default:
				v, _, ok := set.Lookup(n)
				switch {
				case !ok:
					missing = append(missing, n)
				case v.Secret:
					parts = append(parts, n+" = ••••")
				case len(names) == 1:
					parts = append(parts, "= "+clip(set.Resolve(v.Value), 60))
				default:
					parts = append(parts, n+" = "+clip(set.Resolve(v.Value), 30))
				}
			}
		}
		if len(missing) > 0 {
			out = append(out, editor.Hint{Line: i, Text: "not set: " + braces(missing), Kind: "missing"})
			continue
		}
		out = append(out, editor.Hint{Line: i, Text: strings.Join(parts, " · "), Kind: "value"})
	}
	return out
}

// urlHint is the note after the request line: the URL as it will be sent,
// when variables, query lines or path variables make it differ from what
// the line says.
func urlHint(text string, set *vars.Set) (editor.Hint, bool) {
	it, err := reqdoc.Apply(&postman.Item{}, reqdoc.RequestPart(text))
	if err != nil || it.Request == nil || it.Request.URL == nil {
		return editor.Hint{}, false
	}
	u := it.Request.URL
	written := strings.TrimSpace(u.Raw)
	missing := map[string]bool{}
	resolved := send.ResolveURL(u, func(s string) string {
		out, miss := set.ResolveReport(s)
		for _, m := range miss {
			missing[m] = true
		}
		return out
	})
	if len(missing) > 0 {
		var names []string
		for n := range missing {
			names = append(names, n)
		}
		sort.Strings(names)
		return editor.Hint{Text: "not set: " + braces(names), Kind: "missing"}, true
	}
	if resolved == "" || resolved == written {
		return editor.Hint{}, false
	}
	return editor.Hint{Text: "→ " + clip(mask(resolved, set), 90), Kind: "value"}, true
}

func braces(names []string) string {
	out := make([]string, len(names))
	for i, n := range names {
		out[i] = "{{" + n + "}}"
	}
	return strings.Join(out, ", ")
}

// mask hides the values of secret variables in text.
func mask(text string, set *vars.Set) string {
	for _, s := range set.Secrets() {
		text = strings.ReplaceAll(text, s, "••••")
	}
	return text
}

func clip(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len([]rune(s)) <= n {
		return s
	}
	return string([]rune(s)[:n-1]) + "…"
}

// complete offers variables after {{, and header names at the start of a
// header line.
// completions is what the editors offer, built on the UI thread: the
// editors ask for it from goroutines of their own.
type completions struct {
	mu sync.Mutex
	// vars are the {{variables}} of the open request's collection.
	vars []editor.Item
	// schema is the open GraphQL request's server's schema, when known.
	schema *graphql.Schema
}

// varItems are the {{variables}} a collection's requests see, for
// completion.
func (m *Model) varItems(colID string) []editor.Item {
	if m.st == nil {
		return nil
	}
	var items []editor.Item
	set := m.scopes(colID)
	seen := map[string]bool{}
	for _, sc := range set.Scopes {
		for _, v := range sc.Vars {
			if seen[v.Key] {
				continue
			}
			seen[v.Key] = true
			info := clip(v.Value, 60)
			if v.Secret {
				info = "••••"
			}
			items = append(items, editor.Item{Word: "{{" + v.Key + "}}", Abbr: v.Key, Menu: scopeLabel(sc), Info: info})
		}
	}
	return items
}

// updateCompletions rebuilds what the editors offer, after the open
// request or the variables changed.
func (m *Model) updateCompletions() {
	colID := ""
	if m.open != nil {
		colID = m.open.colID
	}
	items := m.varItems(colID)
	schema := m.openSchema()
	m.compl.mu.Lock()
	m.compl.vars, m.compl.schema = items, schema
	m.compl.mu.Unlock()
}

// completerFor completes the {{variables}} of one collection, for an
// editor of its own (its settings, :vars): what it offers is fixed when the
// editor opens.
func (m *Model) completerFor(colID string) editor.Completer {
	items := m.varItems(colID)
	vault := m.vault
	return func(lines []string, row, col int) (int, []editor.Item) {
		return completeVars(items, vault, lines, row, col)
	}
}

// complete answers the request pane's completion: {{variables}}, and in a
// GraphQL query what the server's schema has there. It runs on the
// editor's goroutine, so it reads only what updateCompletions built.
func (m *Model) complete(lines []string, row, col int) (int, []editor.Item) {
	m.compl.mu.Lock()
	items, schema := m.compl.vars, m.compl.schema
	m.compl.mu.Unlock()
	if start, out := completeVars(items, m.vault, lines, row, col); start >= 0 {
		return start, out
	}
	if start, out, ok := completeGraphQL(schema, lines, row, col); ok {
		return start, out
	}
	return -1, nil
}

// completeVars completes a {{variable}} being typed from all.
func completeVars(all []editor.Item, vault *vault, lines []string, row, col int) (int, []editor.Item) {
	if row < 0 || row >= len(lines) {
		return -1, nil
	}
	before := lines[row][:min(col, len(lines[row]))]
	at := strings.LastIndex(before, "{{")
	if at < 0 || strings.Contains(before[at:], "}}") {
		return -1, nil
	}
	typed := strings.ToLower(before[at+2:])
	var items []editor.Item
	for _, it := range all {
		if strings.Contains(strings.ToLower(it.Abbr), typed) {
			items = append(items, it)
		}
	}
	sort.SliceStable(items, func(a, b int) bool {
		pa := strings.HasPrefix(strings.ToLower(items[a].Abbr), typed)
		pb := strings.HasPrefix(strings.ToLower(items[b].Abbr), typed)
		return pa && !pb
	})
	if strings.HasPrefix(typed, "$") || typed == "" {
		for _, n := range vars.DynamicNames() {
			if strings.HasPrefix(strings.ToLower(n), typed) || typed == "" && (n == "$guid" || n == "$timestamp" || n == "$isoTimestamp" || n == "$randomEmail") {
				items = append(items, editor.Item{Word: "{{" + n + "}}", Abbr: n, Menu: "random"})
			}
		}
	}
	if strings.HasPrefix("vault:", typed) || strings.HasPrefix(typed, "vault:") {
		for _, n := range vault.Names() {
			items = append(items, editor.Item{Word: "{{vault:" + n + "}}", Abbr: "vault:" + n, Menu: "vault"})
		}
	}
	return at, items
}

func scopeLabel(sc *vars.Scope) string {
	switch sc.Kind {
	case vars.Environment:
		return "env"
	case vars.Collection:
		return "collection"
	case vars.Global:
		return "global"
	case vars.Local:
		return "local"
	case vars.Data:
		return "data"
	}
	return sc.Kind
}

// hover explains the {{variable}} under the cursor (K), or opens it to
// edit (gd).
func (m *Model) hover(colID string, ev editor.Hover) tea.Cmd {
	if ev.Line < 0 || ev.Line >= len(ev.Lines) {
		return nil
	}
	name, _, _, ok := vars.At(ev.Lines[ev.Line], ev.Col)
	if !ok {
		if info, ok := graphqlInfo(m.openSchema(), ev.Lines, ev.Line, ev.Col); ok {
			// The name as the title; its whole signature and description below.
			title, _, _ := strings.Cut(info, "\n")
			if i := strings.IndexAny(title, "(:"); i > 0 {
				title = title[:i]
			}
			return modal.Show(&modal.Message{Title: title, Body: info})
		}
		if ev.Key == "K" {
			return m.explainLine(colID, ev)
		}
		m.setStatus("no {{variable}} under the cursor", false)
		return nil
	}
	if ev.Key == "gd" {
		return m.editVars(colID, name)
	}
	return modal.Show(&modal.Message{Title: "{{" + name + "}}", Body: m.describeVar(colID, name)})
}

// explainLine says what K on a line without a variable means: the
// request's auth on the request line.
func (m *Model) explainLine(colID string, ev editor.Hover) tea.Cmd {
	if m.open == nil {
		return nil
	}
	c, it, parents := m.findItem(colID, m.open.itemID)
	if it == nil || c == nil || c.Data == nil {
		return nil
	}
	a, from := send.EffectiveAuth(it, parents, c.Data)
	body := "This request sends no auth."
	if a != nil {
		body = authSummary(a, from, m.scopes(colID))
	}
	return modal.Show(&modal.Message{Title: it.Name, Body: body})
}

// describeVar says what a variable is: its value in each scope, which one
// wins, and the team's value.
func (m *Model) describeVar(colID, name string) string {
	set := m.scopes(colID)
	switch {
	case strings.HasPrefix(name, "$"):
		v, ok := vars.Dynamic(name)
		if !ok {
			return "posthero doesn't know this dynamic variable."
		}
		return "A dynamic variable: made up anew every time it is sent, like\n\n" + v
	case strings.HasPrefix(name, "vault:"):
		if _, ok := m.vault.Get(strings.TrimPrefix(name, "vault:")); ok {
			return "A secret in your vault (on this machine only). :vault edits the vault."
		}
		return "Not in your vault. :vault adds it."
	}
	var lines []string
	won := false
	for _, sc := range set.Scopes {
		v, ok := sc.Get(name)
		if !ok {
			continue
		}
		val := v.Value
		if v.Secret {
			val = "•••• (secret)"
		}
		label := scopeLabel(sc)
		if sc.Name != "" {
			label += " " + sc.Name
		}
		mark := "  "
		if !won {
			mark, won = "→ ", true
		}
		lines = append(lines, fmt.Sprintf("%s%s: %s", mark, label, clip(val, 200)))
	}
	if len(lines) == 0 {
		return "Not set anywhere: not in the environment " + orNone(m.envName(colID)) + ", the collection or the globals.\n\ngd adds it to your values (:vars)."
	}
	out := strings.Join(lines, "\n")
	if team := m.teamValue(colID, name); team != nil {
		if v, _, _ := set.Lookup(name); *team != v.Value {
			out += "\n\nyour team's value is " + clip(*team, 200) + " (:reset takes it)"
		}
	}
	return out + "\n\ngd edits it (:vars)"
}

func orNone(s string) string {
	if s == "" {
		return "(none picked)"
	}
	return s
}

// teamValue is a variable's shared (initial) value in the scope that has
// it.
func (m *Model) teamValue(colID, name string) *string {
	if e := m.st.Env(m.envFor(colID)); e != nil && e.Data != nil {
		for _, v := range e.Data.Values {
			if v.Key == name {
				s := string(v.Value)
				return &s
			}
		}
	}
	if c := m.st.Col(colID); c != nil && c.Data != nil {
		for _, v := range c.Data.Variable {
			if v.Key == name {
				s := string(v.Value)
				return &s
			}
		}
	}
	for _, v := range m.st.Globals() {
		if v.Key == name {
			s := string(v.Value)
			return &s
		}
	}
	return nil
}

// findItem finds a collection and an item in it.
func (m *Model) findItem(colID, itemID string) (*store.Col, *postman.Item, []*postman.Item) {
	if m.st == nil {
		return nil, nil, nil
	}
	c := m.st.Col(colID)
	if c == nil || c.Data == nil {
		return c, nil, nil
	}
	it, parents := c.Data.Find(itemID)
	return c, it, parents
}
