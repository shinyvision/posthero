package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/shinyvision/posthero/internal/postman"
	"github.com/shinyvision/posthero/internal/reqdoc"
	"github.com/shinyvision/posthero/internal/send"
	"github.com/shinyvision/posthero/internal/store"
	"github.com/shinyvision/posthero/internal/ui/editmodal"
	"github.com/shinyvision/posthero/internal/ui/modal"
	"github.com/shinyvision/posthero/internal/ui/picker"
	"github.com/shinyvision/posthero/internal/ui/tree"
	"github.com/shinyvision/posthero/internal/vars"
)

// currentCol is the collection of the request in the editor, or under the
// tree's cursor.
func (m *Model) currentCol() string {
	// Only a collection that is there, loaded.
	ok := func(id string) bool {
		if m.st == nil {
			return false
		}
		c := m.st.Col(id)
		return c != nil && c.Data != nil
	}
	if m.focus != paneTree && m.open != nil && ok(m.open.colID) {
		return m.open.colID
	}
	if n := m.tree.Current(); n != nil && ok(n.Col.Summary.ID) {
		return n.Col.Summary.ID
	}
	if m.open != nil && ok(m.open.colID) {
		return m.open.colID
	}
	return ""
}

// pickEnv picks the environment for the current collection.
func (m *Model) pickEnv() tea.Cmd {
	colID := m.currentCol()
	if m.st == nil || colID == "" {
		m.setStatus("pick a collection in the tree first", true)
		return nil
	}
	c := m.st.Col(colID)
	cur := m.envFor(colID)
	type scored struct {
		e     *store.Env
		score int
	}
	var list []scored
	name := normName(c.Name())
	for _, e := range m.st.Environments() {
		s := 0
		if en := normName(e.Name()); strings.HasPrefix(en, name) || name != "" && strings.Contains(en, name) {
			s = 1
		}
		list = append(list, scored{e, s})
	}
	sort.SliceStable(list, func(a, b int) bool { return list[a].score > list[b].score })
	items := []picker.Item{{Label: "No environment", Detail: "only the collection's and global variables", Value: "", Current: cur == ""}}
	for _, s := range list {
		detail := ""
		if isProd(s.e.Name()) {
			detail = "production"
		}
		items = append(items, picker.Item{Label: s.e.Name(), Detail: detail, Value: s.e.Summary.ID, Current: s.e.Summary.ID == cur})
	}
	p := &picker.Picker{Title: "Environment for " + c.Name(), Items: items, Placeholder: "environment", Pick: func(it picker.Item) tea.Cmd {
		id := it.Value.(string)
		return func() tea.Msg { return cmdMsg{func() tea.Cmd { m.setEnv(colID, id); return nil }} }
	}}
	return p.Open()
}

// setEnv uses an environment for a collection.
func (m *Model) setEnv(colID, envID string) {
	m.state.Env[colID] = envID
	m.refreshHints()
	name := "no environment"
	if e := m.st.Env(envID); e != nil {
		name = e.Name()
	}
	if c := m.st.Col(colID); c != nil {
		m.setStatus(name+" for "+c.Name(), false)
	}
}

// envByName finds an environment by name (or the start of it) or id.
func (m *Model) envByName(s string) *store.Env {
	if e := m.st.Env(s); e != nil {
		return e
	}
	low := strings.ToLower(s)
	for _, e := range m.st.Environments() {
		if strings.ToLower(e.Name()) == low {
			return e
		}
	}
	var hits []*store.Env
	for _, e := range m.st.Environments() {
		if strings.Contains(strings.ToLower(e.Name()), low) {
			hits = append(hits, e)
		}
	}
	if len(hits) == 1 {
		return hits[0]
	}
	return nil
}

// varsHeader explains the variables document.
const varsHeader = `# Your values of the variables, on this machine only. :w keeps them.
# key = value; removing a line takes your team's value again. A note under
# a line shows the team's value when yours differs: :share KEY gives yours
# to the team, :reset KEY takes theirs.
`

// editVars edits the current values of the variables a collection's
// requests see, with the cursor on a variable when focus is set.
func (m *Model) editVars(colID, focus string) tea.Cmd {
	if colID == "" {
		colID = m.currentCol()
	}
	if m.st == nil || m.st.Col(colID) == nil || m.st.Col(colID).Data == nil {
		m.setStatus("pick a collection in the tree first", true)
		return nil
	}
	envID := m.envFor(colID)
	set := m.scopes(colID)
	var b strings.Builder
	b.WriteString(varsHeader)
	line := 0
	write := func(sc *vars.Scope, title string, team map[string]string) {
		if sc == nil {
			return
		}
		b.WriteString("\n### " + title + "\n")
		keys := map[string]bool{}
		for _, v := range sc.Vars {
			keys[v.Key] = true
			if v.Key == focus {
				line = strings.Count(b.String(), "\n") + 1
			}
			b.WriteString(v.Key + " = " + oneLine(v.Value) + "\n")
			if t, ok := team[v.Key]; !ok {
				b.WriteString("#   only yours\n")
			} else if t != v.Value {
				b.WriteString("#   team: " + orEmpty(oneLine(t)) + "\n")
			}
		}
		for k, t := range team {
			if !keys[k] {
				b.WriteString("#   " + k + " is unset here; team: " + orEmpty(oneLine(t)) + "\n")
			}
		}
	}
	if sc := set.Scope(vars.Environment); sc != nil {
		if e := m.st.Env(envID); e != nil && e.Data != nil {
			write(sc, "environment "+e.Name(), teamValues(store.EnvInitial(e.Data.Values)))
		}
	}
	c := m.st.Col(colID)
	write(set.Scope(vars.Collection), "collection "+c.Name(), teamValues(store.CollectionInitial(c.Data.Variable)))
	write(set.Scope(vars.Global), "globals", teamValues(store.EnvInitial(m.st.Globals())))
	if focus != "" && line == 0 {
		// A variable nobody set: add it to the environment (or the
		// collection).
		text := b.String()
		where := "### environment"
		if set.Scope(vars.Environment) == nil {
			where = "### collection"
		}
		if i := strings.Index(text, where); i >= 0 {
			end := strings.Index(text[i:], "\n") + i + 1
			text = text[:end] + focus + " = \n" + text[end:]
			line = strings.Count(text[:end], "\n") + 1
		}
		b.Reset()
		b.WriteString(text)
	}
	insert := ""
	if line > 0 {
		insert = fmt.Sprintf("%dA", line)
	}
	return editmodal.Open(editmodal.Request{
		Title: "Variables · " + c.Name(), Hint: ":w keeps them · :q closes", Text: strings.TrimRight(b.String(), "\n"),
		Ext: reqdoc.Ext, Name: "variables", Tall: true, Insert: insert, Complete: m.completerFor(colID),
		Submit: func(text string) tea.Cmd { return m.saveVars(colID, envID, text) },
	})
}

func orEmpty(s string) string {
	if s == "" {
		return "(empty)"
	}
	return s
}

// oneLine writes a value on one line: a backslash as \\, a line break
// as \n, a carriage return as \r. fromLine reads it back.
func oneLine(s string) string {
	return strings.NewReplacer("\\", "\\\\", "\n", "\\n", "\r", "\\r").Replace(s)
}

// fromLine reads a value written by oneLine. A backslash before anything
// else stays, so values typed with single backslashes (a regexp) read as
// typed.
func fromLine(s string) string {
	if !strings.Contains(s, "\\") {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) {
			switch s[i+1] {
			case '\\':
				b.WriteByte('\\')
				i++
				continue
			case 'n':
				b.WriteByte('\n')
				i++
				continue
			case 'r':
				b.WriteByte('\r')
				i++
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

func teamValues(in []store.Initial) map[string]string {
	out := map[string]string{}
	for _, v := range in {
		if !v.Off {
			out[v.Key] = v.Value
		}
	}
	return out
}

// saveVars keeps the variables document's values.
func (m *Model) saveVars(colID, envID, text string) tea.Cmd {
	scopes := map[string]*vars.Scope{}
	var cur *vars.Scope
	for n, l := range strings.Split(text, "\n") {
		t := strings.TrimSpace(l)
		if strings.HasPrefix(t, "### ") {
			kind := strings.Fields(strings.TrimPrefix(t, "### "))[0]
			switch kind {
			case "environment":
				kind = vars.Environment
			case "collection":
				kind = vars.Collection
			case "globals", "global":
				kind = vars.Global
			default:
				return editmodal.Fail(fmt.Errorf("line %d: the sections are environment, collection and globals", n+1))
			}
			cur = &vars.Scope{Kind: kind}
			scopes[kind] = cur
			continue
		}
		if t == "" || strings.HasPrefix(t, "#") {
			continue
		}
		if cur == nil {
			return editmodal.Fail(fmt.Errorf("line %d: a variable goes under ### environment, ### collection or ### globals", n+1))
		}
		k, v, ok := strings.Cut(l, "=")
		if !ok {
			return editmodal.Fail(fmt.Errorf("line %d: write key = value", n+1))
		}
		cur.Set(strings.TrimSpace(k), fromLine(strings.TrimPrefix(v, " ")))
	}
	// A variable left out takes the team's value again: it is in the scope
	// as the team has it.
	fill := func(kind string, team []store.Initial) {
		sc := scopes[kind]
		if sc == nil {
			return
		}
		for _, in := range team {
			if _, ok := sc.Get(in.Key); !ok && !in.Off {
				sc.Set(in.Key, in.Value)
			}
		}
	}
	if e := m.st.Env(envID); e != nil && e.Data != nil {
		fill(vars.Environment, store.EnvInitial(e.Data.Values))
	}
	if c := m.st.Col(colID); c != nil && c.Data != nil {
		fill(vars.Collection, store.CollectionInitial(c.Data.Variable))
	}
	fill(vars.Global, store.EnvInitial(m.st.Globals()))
	var errs []error
	if sc := scopes[vars.Environment]; sc != nil {
		errs = append(errs, m.st.SetCurrent(vars.Environment, envID, sc))
	}
	if sc := scopes[vars.Collection]; sc != nil {
		errs = append(errs, m.st.SetCurrent(vars.Collection, colID, sc))
	}
	if sc := scopes[vars.Global]; sc != nil {
		errs = append(errs, m.st.SetCurrent(vars.Global, "", sc))
	}
	if err := errors.Join(errs...); err != nil {
		return editmodal.Fail(err)
	}
	m.refreshHints()
	return func() tea.Msg { return editmodal.ResultMsg{Then: statusMsg{text: "kept your values"}} }
}

// share gives the team this machine's value of a variable (all of them,
// when key is empty).
func (m *Model) share(key string) tea.Cmd {
	colID := m.currentCol()
	if colID == "" {
		return nil
	}
	envID := m.envFor(colID)
	set := m.scopes(colID)
	type change struct {
		kind, key, value string
	}
	var changes []change
	look := func(kind string, team []store.Initial) {
		sc := set.Scope(kind)
		if sc == nil {
			return
		}
		tv := teamValues(team)
		for _, v := range sc.Vars {
			if key != "" && v.Key != key {
				continue
			}
			if t, ok := tv[v.Key]; !ok || t != v.Value {
				changes = append(changes, change{kind, v.Key, v.Value})
			}
		}
	}
	e := m.st.Env(envID)
	if e != nil && e.Data != nil {
		look(vars.Environment, store.EnvInitial(e.Data.Values))
	}
	c := m.st.Col(colID)
	look(vars.Collection, store.CollectionInitial(c.Data.Variable))
	look(vars.Global, store.EnvInitial(m.st.Globals()))
	if len(changes) == 0 {
		m.setStatus("your values are your team's already", false)
		return nil
	}
	var lines []string
	for _, ch := range changes {
		lines = append(lines, fmt.Sprintf("%s = %s (%s)", ch.key, clip(ch.value, 50), ch.kind))
	}
	return modal.Show(&modal.Confirm{
		Title: "Give your team these values?",
		Body:  strings.Join(lines, "\n"),
		Yes: func() tea.Msg {
			return cmdMsg{func() tea.Cmd {
				var cmds []tea.Cmd
				envChanged, colChanged, globChanged := false, false, false
				var envValues []postman.EnvValue
				if e != nil && e.Data != nil {
					envValues = append(envValues, e.Data.Values...)
				}
				colVars := append([]postman.Variable(nil), c.Data.Variable...)
				globals := append([]postman.EnvValue(nil), m.st.Globals()...)
				for _, ch := range changes {
					switch ch.kind {
					case vars.Environment:
						envValues, envChanged = setEnvValue(envValues, ch.key, ch.value), true
					case vars.Collection:
						colVars, colChanged = setColVar(colVars, ch.key, ch.value), true
					case vars.Global:
						globals, globChanged = setEnvValue(globals, ch.key, ch.value), true
					}
				}
				if envChanged {
					cmds = append(cmds, m.saveEnvValues(e, e.Data.Values, envValues, m.envUndo(e)))
				}
				if colChanged {
					cmds = append(cmds, m.saveColVars(c, c.Data.Variable, colVars, m.colVarsUndo(c)))
				}
				if globChanged {
					cmds = append(cmds, m.saveGlobals(m.st.Globals(), globals, m.globalsUndo()))
				}
				return tea.Batch(cmds...)
			}}
		},
	})
}

func setEnvValue(list []postman.EnvValue, key, value string) []postman.EnvValue {
	for i := range list {
		if list[i].Key == key {
			list[i].Value = postman.Str(value)
			return list
		}
	}
	return append(list, postman.EnvValue{Key: key, Value: postman.Str(value), Type: "default"})
}

func setColVar(list []postman.Variable, key, value string) []postman.Variable {
	for i := range list {
		if list[i].Key == key {
			list[i].Value = postman.Str(value)
			return list
		}
	}
	return append(list, postman.Variable{Key: key, Value: postman.Str(value)})
}

// reset takes the team's value of a variable again (all, when key is
// empty).
func (m *Model) reset(key string) tea.Cmd {
	colID := m.currentCol()
	if colID == "" {
		return nil
	}
	ids := []string{m.envFor(colID), colID, "globals-" + m.st.Workspace.ID}
	n := 0
	for _, id := range ids {
		if id == "" {
			continue
		}
		o := m.st.Values.Get(id)
		if len(o) == 0 {
			continue
		}
		if key == "" {
			n += len(o)
			_ = m.st.Values.Set(id, nil)
			continue
		}
		if _, ok := o[key]; ok {
			cp := store.Overrides{}
			for k, v := range o {
				if k != key {
					cp[k] = v
				}
			}
			_ = m.st.Values.Set(id, cp)
			n++
		}
	}
	m.refreshHints()
	if n == 0 {
		m.setStatus("your values are your team's already", false)
	} else {
		m.setStatus(fmt.Sprintf("took your team's value of %d %s", n, plural(n, "variable")), false)
	}
	return nil
}

// envHeader explains the environment document.
const envHeader = `## Your team's values of %s. :w saves them.
## A line is a key, a colon and the value. A # in front switches a
## variable off; (secret) after the key hides the value on screen. In a
## value, \n is a line break and \\ a backslash. Lines starting with ## are
## notes. Your own values are in :vars.
`

// editEnv edits an environment's team values.
func (m *Model) editEnv(e *store.Env) tea.Cmd {
	if e == nil || e.Data == nil {
		m.setStatus("no environment picked; E picks one", true)
		return nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, envHeader, e.Name())
	b.WriteByte('\n')
	writeEnvLines(&b, e.Data.Values)
	// What the editor started from: only what changes from it is saved, on
	// top of what Postman has then.
	base := append([]postman.EnvValue(nil), e.Data.Values...)
	var submitted []postman.EnvValue
	return editmodal.Open(editmodal.Request{
		Title: "Environment · " + e.Name(), Hint: m.saveHint(), Text: strings.TrimRight(b.String(), "\n"),
		Ext: reqdoc.Ext, Name: "environment", Tall: true,
		Submit: func(text string) tea.Cmd {
			values, err := parseEnvLines(text, base)
			if err != nil {
				return editmodal.Fail(err)
			}
			submitted = values
			return m.saveEnvValues(e, base, values, m.envUndo(e))
		},
		Saved: func() { base = submitted },
	})
}

func writeEnvLines(b *strings.Builder, values []postman.EnvValue) {
	for _, v := range values {
		if !v.On() {
			b.WriteString("# ")
		}
		b.WriteString(v.Key)
		if v.Secret() {
			b.WriteString(" (secret)")
		}
		b.WriteString(": " + oneLine(string(v.Value)) + "\n")
	}
}

// parseEnvLines reads key: value lines into variables, keeping what the
// old ones had that the lines don't say.
func parseEnvLines(text string, old []postman.EnvValue) ([]postman.EnvValue, error) {
	byKey := map[string]postman.EnvValue{}
	for _, v := range old {
		byKey[v.Key] = v
	}
	var out []postman.EnvValue
	for n, l := range strings.Split(text, "\n") {
		// Only the start is trimmed: a value may end in spaces.
		t := strings.TrimLeft(strings.TrimSuffix(l, "\r"), " \t")
		if strings.TrimSpace(t) == "" {
			continue
		}
		off := false
		if strings.HasPrefix(t, "##") {
			continue // help, not a variable
		}
		if strings.HasPrefix(t, "#") {
			// A variable switched off, or a note: a note has no colon, or
			// words before it that aren't a variable here.
			body := strings.TrimLeft(strings.TrimPrefix(t, "#"), " \t")
			k, _, ok := strings.Cut(body, ":")
			key := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(k), "(secret)"))
			_, known := byKey[key]
			if !ok || key == "" || strings.ContainsAny(key, " \t") && !known {
				continue
			}
			off, t = true, body
		}
		k, v, ok := strings.Cut(t, ":")
		if !ok {
			return nil, fmt.Errorf("line %d: write key: value", n+1)
		}
		k = strings.TrimSpace(k)
		secret := strings.HasSuffix(k, "(secret)")
		k = strings.TrimSpace(strings.TrimSuffix(k, "(secret)"))
		ev, ok := byKey[k]
		if !ok {
			ev = postman.EnvValue{Key: k, Type: "default"}
		}
		ev.Value = postman.Str(fromLine(strings.TrimPrefix(v, " ")))
		on := !off
		ev.Enabled = &on
		switch {
		case secret:
			ev.Type = "secret"
		case ev.Type == "secret":
			ev.Type = "default"
		}
		out = append(out, ev)
	}
	return out, nil
}

// envUndo is the undo entry of a change to an environment's team values:
// it puts back the values it has now.
func (m *Model) envUndo(e *store.Env) *undoEntry {
	before := append([]postman.EnvValue(nil), e.Data.Values...)
	return &undoEntry{what: "change " + e.Name(), kind: undoRestore, restore: func() tea.Cmd { return m.saveEnvValues(e, e.Data.Values, before, nil) }}
}

// saveEnvValues saves an environment's team values: what changed from
// base to values, on top of what Postman has now (a teammate's change
// made meanwhile stays). u, when set, is recorded once that worked.
func (m *Model) saveEnvValues(e *store.Env, base, values []postman.EnvValue, u *undoEntry) tea.Cmd {
	data := e.Data.Clone()
	data.Values = values
	if e.Path != "" || m.st.API == nil {
		e.Data = data
		bytes, err := store.EnvFileData(data)
		if err != nil {
			return editmodal.Fail(err)
		}
		path := e.Path
		return m.run("writing "+path, func(context.Context) tea.Msg {
			if path != "" {
				if err := store.WriteFile(path, bytes); err != nil {
					return editmodal.ResultMsg{Err: err}
				}
			}
			return editmodal.ResultMsg{Then: cmdMsg{func() tea.Cmd { m.pushUndoOf(u); m.refreshHints(); return status("saved " + e.Name()) }}}
		})
	}
	api := m.api
	uid := e.Summary.UID
	base = append([]postman.EnvValue(nil), base...)
	return m.run("saving "+e.Name(), func(ctx context.Context) tea.Msg {
		now, err := api.Environment(ctx, uid)
		if err != nil {
			return editmodal.ResultMsg{Err: fmt.Errorf("couldn't read the latest environment; nothing was saved: %w", err)}
		}
		data = now.Clone()
		data.Values = mergeByKey(base, values, now.Values, envKey, sameEnvValue)
		if err := api.PutEnvironment(ctx, uid, data); err != nil {
			return editmodal.ResultMsg{Err: err}
		}
		fresh, err := api.Environment(ctx, uid)
		if err != nil {
			fresh = data
		}
		return editmodal.ResultMsg{Then: cmdMsg{func() tea.Cmd {
			e.Data = fresh
			e.Summary.UpdatedAt = fresh.UpdatedAt
			m.st.PutEnv(e)
			m.pushUndoOf(u)
			m.refreshHints()
			return status("saved " + e.Name() + " " + m.saveTarget())
		}}}
	})
}

// colVarsUndo is the undo entry of a change to a collection's team values.
func (m *Model) colVarsUndo(c *store.Col) *undoEntry {
	before := append([]postman.Variable(nil), c.Data.Variable...)
	return &undoEntry{what: "change the variables of " + c.Name(), kind: undoRestore, restore: func() tea.Cmd { return m.saveColVars(c, c.Data.Variable, before, nil) }}
}

// saveColVars saves a collection's team values; u, when set, is recorded
// once that worked.
func (m *Model) saveColVars(c *store.Col, base, vs []postman.Variable, u *undoEntry) tea.Cmd {
	colID := c.Summary.ID
	if c.Path != "" || m.st.API == nil {
		return m.saveFile(c, func() { c.Data.Variable = vs }, func() tea.Msg { return changedMsg{colID: colID, undo: u, note: "saved the collection's variables"} })
	}
	api := m.api
	uid := c.Summary.UID
	to := m.saveTarget()
	base = append([]postman.Variable(nil), base...)
	return m.run("saving "+c.Name(), func(ctx context.Context) tea.Msg {
		// What changed from base, on top of what Postman has now.
		now, err := api.Collection(ctx, uid)
		if err != nil {
			return changedMsg{err: fmt.Errorf("couldn't read the latest collection; nothing was saved: %w", err)}
		}
		vs = mergeByKey(base, vs, now.Variable, varKey, sameVariable)
		if err := api.PatchCollection(ctx, uid, postman.CollectionPatch{Variable: &vs}); err != nil {
			return changedMsg{err: err}
		}
		col, err := api.Collection(ctx, uid)
		return changedMsg{colID: colID, col: col, err: err, undo: u, note: "saved the collection's variables " + to}
	})
}

// editGlobals edits the workspace's globals.
func (m *Model) editGlobals() tea.Cmd {
	if m.st == nil {
		return nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, envHeader, "the globals of "+orName(m.st.Workspace.Name, "the workspace"))
	b.WriteByte('\n')
	writeEnvLines(&b, m.st.Globals())
	base := append([]postman.EnvValue(nil), m.st.Globals()...)
	var submitted []postman.EnvValue
	return editmodal.Open(editmodal.Request{
		Title: "Globals", Hint: m.saveHint(), Text: strings.TrimRight(b.String(), "\n"),
		Ext: reqdoc.Ext, Name: "globals", Tall: true,
		Submit: func(text string) tea.Cmd {
			values, err := parseEnvLines(text, base)
			if err != nil {
				return editmodal.Fail(err)
			}
			submitted = values
			return m.saveGlobals(base, values, m.globalsUndo())
		},
		Saved: func() { base = submitted },
	})
}

// globalsUndo is the undo entry of a change to the globals.
func (m *Model) globalsUndo() *undoEntry {
	before := append([]postman.EnvValue(nil), m.st.Globals()...)
	return &undoEntry{what: "change the globals", kind: undoRestore, restore: func() tea.Cmd { return m.saveGlobals(m.st.Globals(), before, nil) }}
}

// saveGlobals saves the workspace's globals; u, when set, is recorded
// once that worked.
func (m *Model) saveGlobals(base, values []postman.EnvValue, u *undoEntry) tea.Cmd {
	if m.st.API == nil {
		m.st.SetGlobals(values)
		m.pushUndoOf(u)
		return func() tea.Msg {
			return editmodal.ResultMsg{Then: statusMsg{text: "saved the globals (on this machine)"}}
		}
	}
	api := m.api
	ws := m.st.Workspace.ID
	base = append([]postman.EnvValue(nil), base...)
	return m.run("saving the globals", func(ctx context.Context) tea.Msg {
		// What changed from base, on top of what Postman has now.
		now, err := api.Globals(ctx, ws)
		if err != nil {
			return editmodal.ResultMsg{Err: fmt.Errorf("couldn't read the latest globals; nothing was saved: %w", err)}
		}
		values = mergeByKey(base, values, now, envKey, sameEnvValue)
		if err := api.PutGlobals(ctx, ws, values); err != nil {
			return editmodal.ResultMsg{Err: err}
		}
		return editmodal.ResultMsg{Then: cmdMsg{func() tea.Cmd {
			m.st.SetGlobals(values)
			m.pushUndoOf(u)
			m.refreshHints()
			return status("saved the globals " + m.saveTarget())
		}}}
	})
}

// newEnv makes an environment (a copy of another when from is set), uses
// it and opens it to edit; record makes it a change u takes back (undoing
// a delete makes one too, without recording or editing).
func (m *Model) newEnv(name string, from *store.Env, record bool) tea.Cmd {
	if m.st == nil {
		return nil
	}
	env := &postman.Environment{Name: name}
	if from != nil && from.Data != nil {
		env.Values = append(env.Values, from.Data.Values...)
	}
	colID := m.currentCol()
	if m.st.API == nil {
		id := store.NewID()
		env.ID = id
		e := &store.Env{Summary: postman.Summary{ID: id, UID: id, Name: name}, Data: env}
		m.st.PutEnv(e)
		if colID != "" {
			m.setEnv(colID, id)
		}
		if record {
			m.pushUndo(m.envMadeUndo(e))
		}
		return nil
	}
	api := m.api
	ws := m.st.Workspace.ID
	return m.run("making "+name, func(ctx context.Context) tea.Msg {
		sum, err := api.CreateEnvironment(ctx, ws, env)
		if err != nil {
			return errorMsg{"Not made", err}
		}
		full, err := api.Environment(ctx, sum.UID)
		if err != nil {
			return errorMsg{"Made, but not fetched", err}
		}
		sum.UpdatedAt = full.UpdatedAt
		return cmdMsg{func() tea.Cmd {
			e := &store.Env{Summary: sum, Data: full}
			m.st.PutEnv(e)
			if colID != "" {
				m.setEnv(colID, sum.ID)
			}
			if !record {
				return nil // undoing a delete; the undo says what it did
			}
			m.pushUndo(m.envMadeUndo(e))
			return m.editEnv(e)
		}}
	})
}

// envMadeUndo takes back making an environment: it deletes it.
func (m *Model) envMadeUndo(e *store.Env) undoEntry {
	return undoEntry{what: "make " + e.Name(), kind: undoRestore, restore: func() tea.Cmd { return m.dropEnv(e, nil) }}
}

// renameEnv renames an environment; record makes it a change u takes back.
func (m *Model) renameEnv(e *store.Env, name string, record bool) tea.Cmd {
	old := e.Name()
	data := e.Data.Clone()
	data.Name = name
	var u *undoEntry
	if record {
		u = &undoEntry{what: "rename " + old, kind: undoRestore, restore: func() tea.Cmd { return m.renameEnv(e, old, false) }}
	}
	return m.saveEnvNamed(e, data, u)
}

func (m *Model) saveEnvNamed(e *store.Env, data *postman.Environment, u *undoEntry) tea.Cmd {
	if e.Path != "" || m.st.API == nil {
		e.Data = data
		e.Summary.Name = data.Name
		if e.Path != "" {
			bytes, _ := store.EnvFileData(data)
			_ = store.WriteFile(e.Path, bytes)
		}
		m.pushUndoOf(u)
		return status("renamed to " + data.Name)
	}
	api := m.api
	uid := e.Summary.UID
	name := data.Name
	return m.run("renaming", func(ctx context.Context) tea.Msg {
		now, err := api.Environment(ctx, uid)
		if err != nil {
			return errorMsg{"Not renamed", fmt.Errorf("couldn't read the latest environment; nothing was saved: %w", err)}
		}
		data = now.Clone()
		data.Name = name
		if err := api.PutEnvironment(ctx, uid, data); err != nil {
			return errorMsg{"Not renamed", err}
		}
		return cmdMsg{func() tea.Cmd {
			e.Data = data
			e.Summary.Name = data.Name
			m.st.PutEnv(e)
			m.pushUndoOf(u)
			return status("renamed to " + data.Name)
		}}
	})
}

// deleteEnv deletes an environment after asking.
func (m *Model) deleteEnv(e *store.Env) tea.Cmd {
	return modal.Show(&modal.Confirm{Title: "Delete " + e.Name() + "?", Body: "Your whole team loses it. u brings it back (as a new environment).", Yes: func() tea.Msg {
		return cmdMsg{func() tea.Cmd {
			snapshot := &store.Env{Summary: e.Summary, Data: e.Data.Clone()}
			return m.dropEnv(e, &undoEntry{what: "delete " + e.Name(), kind: undoRestore, restore: func() tea.Cmd {
				return m.newEnv(snapshot.Name(), snapshot, false)
			}})
		}}
	}})
}

// dropEnv deletes an environment without asking; u, when set, is recorded
// once that worked.
func (m *Model) dropEnv(e *store.Env, u *undoEntry) tea.Cmd {
	if m.st.API == nil {
		m.st.RemoveEnv(e.Summary.ID)
		m.pushUndoOf(u)
		m.refreshHints()
		return status("deleted " + e.Name())
	}
	api := m.api
	uid, id, name := e.Summary.UID, e.Summary.ID, e.Name()
	return m.run("deleting "+name, func(ctx context.Context) tea.Msg {
		if err := api.DeleteEnvironment(ctx, uid); err != nil {
			return errorMsg{"Not deleted", err}
		}
		return cmdMsg{func() tea.Cmd {
			m.st.RemoveEnv(id)
			m.pushUndoOf(u)
			m.refreshHints()
			return status("deleted " + name)
		}}
	})
}

// editSettings edits a collection's or folder's own settings: auth,
// scripts, variables (the collection's team values) and description.
func (m *Model) editSettings(n *tree.Node) tea.Cmd {
	if n == nil {
		return nil
	}
	c := n.Col
	if c.Data == nil {
		m.setStatus("still loading", true)
		return nil
	}
	colID := c.Summary.ID
	if n.Kind == tree.Collection {
		base := c.Data.Clone()
		var submitted *postman.Collection
		return editmodal.Open(editmodal.Request{
			Title: "Collection · " + c.Name(), Hint: m.saveHint(), Text: strings.TrimRight(reqdoc.RenderCollection(c.Data), "\n"),
			Ext: reqdoc.Ext, Name: "collection", Tall: true, Complete: m.completerFor(colID),
			Submit: func(text string) tea.Cmd {
				nc, err := reqdoc.ApplyCollection(base, text)
				if err != nil {
					return editmodal.Fail(err)
				}
				submitted = nc
				return m.saveCollectionSettingsFrom(c, base, nc, m.collectionUndo(c))
			},
			Saved: func() { base = submitted },
		})
	}
	it := n.Item
	base := it.Clone()
	var submitted *postman.Item
	return editmodal.Open(editmodal.Request{
		Title: "Folder · " + it.Name, Hint: m.saveHint(), Text: strings.TrimRight(reqdoc.RenderFolder(it), "\n"),
		Ext: reqdoc.Ext, Name: "folder", Tall: true, Complete: m.completerFor(colID),
		Submit: func(text string) tea.Cmd {
			// The folder as it is now, in case the latest is in the tree already.
			cur, _ := c.Data.Find(it.ID)
			if cur == nil {
				return editmodal.Fail(fmt.Errorf("%s isn't in the collection any more", it.Name))
			}
			nf, err := reqdoc.ApplyFolder(base, text)
			if err != nil {
				return editmodal.Fail(err)
			}
			u := &undoEntry{what: "change " + cur.Name, colID: colID, kind: undoSave, item: cur.Clone()}
			submitted = nf
			if c.Path != "" || m.st.API == nil {
				return m.saveFile(c, func() { replaceItem(c, nf) }, func() tea.Msg {
					return editmodal.ResultMsg{Then: changedMsg{colID: colID, undo: u, note: "saved " + nf.Name}}
				})
			}
			return m.saveFolderSettings(c, base, nf, u)
		},
		Saved: func() {
			if submitted != nil {
				base = submitted
			}
		},
	})
}

// collectionUndo is the undo entry of a change to a collection's own
// settings: auth, scripts, variables, description.
func (m *Model) collectionUndo(c *store.Col) *undoEntry {
	before := c.Data.Clone()
	return &undoEntry{what: "change " + c.Name(), kind: undoRestore, restore: func() tea.Cmd { return m.saveCollectionSettings(c, before, nil) }}
}

// saveCollectionSettings saves a collection's own settings; u, when set,
// is recorded once that worked.
func (m *Model) saveCollectionSettings(c *store.Col, nc *postman.Collection, u *undoEntry) tea.Cmd {
	return m.saveCollectionSettingsFrom(c, c.Data.Clone(), nc, u)
}

func (m *Model) saveCollectionSettingsFrom(c *store.Col, base, nc *postman.Collection, u *undoEntry) tea.Cmd {
	colID := c.Summary.ID
	if c.Path != "" || m.st.API == nil {
		return m.saveFile(c, func() {
			c.Data.Auth, c.Data.Event, c.Data.Variable, c.Data.Info.Description = nc.Auth, nc.Event, nc.Variable, nc.Info.Description
		}, func() tea.Msg {
			return editmodal.ResultMsg{Then: changedMsg{colID: colID, undo: u, note: "saved the collection"}}
		})
	}
	api := m.api
	uid := c.Summary.UID
	base, nc = base.Clone(), nc.Clone()
	to := m.saveTarget()
	return m.run("saving "+c.Name(), func(ctx context.Context) tea.Msg {
		now, err := api.Collection(ctx, uid)
		if err != nil {
			return editmodal.ResultMsg{Err: fmt.Errorf("couldn't read the latest collection; nothing was saved: %w", err)}
		}
		p, err := settingsPatch(collectionSettings(base), collectionSettings(nc), collectionSettings(now))
		if err != nil {
			return editmodal.ResultMsg{Err: err}
		}
		patch := postman.CollectionPatch{Description: p.Description, Auth: p.Auth, Event: p.Event}
		if !sameJSON(base.Variable, nc.Variable) {
			vs := mergeByKey(base.Variable, nc.Variable, now.Variable, varKey, sameVariable)
			patch.Variable = &vs
		}
		if patch != (postman.CollectionPatch{}) {
			if err := api.PatchCollection(ctx, uid, patch); err != nil {
				return editmodal.ResultMsg{Err: err}
			}
		}
		col, err := api.Collection(ctx, uid)
		return editmodal.ResultMsg{Then: changedMsg{colID: colID, col: col, err: err, undo: u, note: "saved the collection " + to}}
	})
}

// cookiesHeader explains the cookies document.
const cookiesHeader = `# The cookies posthero keeps and sends, by domain. :w saves them; removing
# a line deletes the cookie.
# domain  name=value  [path=/] [expires=2026-11-01T10:00:00Z] [secure] [httponly]
`

// editCookies edits the cookie jar.
func (m *Model) editCookies() tea.Cmd {
	var b strings.Builder
	b.WriteString(cookiesHeader + "\n")
	for _, c := range m.jar.All() {
		b.WriteString(c.Domain + "  " + c.Name + "=" + c.Value)
		if c.Path != "" && c.Path != "/" {
			b.WriteString("  path=" + c.Path)
		}
		if !c.Expires.IsZero() {
			b.WriteString("  expires=" + c.Expires.UTC().Format(time.RFC3339))
		}
		if c.Secure {
			b.WriteString("  secure")
		}
		if c.HTTPOnly {
			b.WriteString("  httponly")
		}
		b.WriteByte('\n')
	}
	return editmodal.Open(editmodal.Request{
		Title: "Cookies", Hint: ":w saves · :q closes", Text: strings.TrimRight(b.String(), "\n"), Ext: ".txt", Name: "cookies", Tall: true,
		Submit: func(text string) tea.Cmd {
			cookies, err := parseCookies(text)
			if err != nil {
				return editmodal.Fail(err)
			}
			m.jar.Replace(cookies)
			_ = m.jar.Save()
			return func() tea.Msg {
				return editmodal.ResultMsg{Then: statusMsg{text: fmt.Sprintf("kept %d %s", len(cookies), plural(len(cookies), "cookie"))}}
			}
		},
	})
}

func parseCookies(text string) ([]send.Cookie, error) {
	var out []send.Cookie
	for n, l := range strings.Split(text, "\n") {
		t := strings.TrimSpace(l)
		if t == "" || strings.HasPrefix(t, "#") {
			continue
		}
		f := strings.Fields(t)
		if len(f) < 2 || !strings.Contains(f[1], "=") {
			return nil, fmt.Errorf("line %d: write a domain and name=value", n+1)
		}
		name, value, _ := strings.Cut(f[1], "=")
		c := send.Cookie{Domain: f[0], Name: name, Value: value, Path: "/", HostOnly: true}
		for _, a := range f[2:] {
			k, v, _ := strings.Cut(a, "=")
			switch strings.ToLower(k) {
			case "path":
				c.Path = v
			case "expires":
				t, err := time.Parse(time.RFC3339, v)
				if err != nil {
					return nil, fmt.Errorf("line %d: expires is a time like 2026-11-01T10:00:00Z", n+1)
				}
				c.Expires = t
			case "secure":
				c.Secure = true
			case "httponly":
				c.HTTPOnly = true
			case "domain":
				c.HostOnly = false
			}
		}
		out = append(out, c)
	}
	return out, nil
}

// editVault edits the vault: secrets on this machine only.
func (m *Model) editVault() tea.Cmd {
	var b strings.Builder
	b.WriteString("# Your vault: secrets on this machine only (in your system keyring), used as\n# {{vault:name}} and by pm.vault. :w saves them; removing a line deletes it.\n\n")
	names := m.vault.Names()
	sort.Strings(names)
	for _, n := range names {
		v, _ := m.vault.Get(n)
		b.WriteString(n + " = " + oneLine(v) + "\n")
	}
	return editmodal.Open(editmodal.Request{
		Title: "Vault", Hint: ":w saves · :q closes", Text: strings.TrimRight(b.String(), "\n"), Ext: ".txt", Name: "vault", Tall: true,
		Submit: func(text string) tea.Cmd {
			keep := map[string]string{}
			for n, l := range strings.Split(text, "\n") {
				t := strings.TrimSpace(l)
				if t == "" || strings.HasPrefix(t, "#") {
					continue
				}
				k, v, ok := strings.Cut(l, "=")
				if !ok {
					return editmodal.Fail(fmt.Errorf("line %d: write name = value", n+1))
				}
				keep[strings.TrimSpace(k)] = fromLine(strings.TrimPrefix(v, " "))
			}
			for _, n := range m.vault.Names() {
				if _, ok := keep[n]; !ok {
					_ = m.vault.Unset(n)
				}
			}
			for k, v := range keep {
				if err := m.vault.Set(k, v); err != nil {
					return editmodal.Fail(err)
				}
			}
			m.refreshHints()
			return func() tea.Msg { return editmodal.ResultMsg{Then: statusMsg{text: "saved the vault"}} }
		},
	})
}

// mergeByKey applies what changed from base to mine onto theirs: what was
// changed or added in mine wins, what was removed in mine goes, and the
// rest is theirs, so a teammate's change made meanwhile stays.
func mergeByKey[T any](base, mine, theirs []T, key func(T) string, same func(a, b T) bool) []T {
	index := func(list []T) map[string]T {
		out := map[string]T{}
		for _, x := range list {
			if _, ok := out[key(x)]; !ok {
				out[key(x)] = x
			}
		}
		return out
	}
	b, m := index(base), index(mine)
	changed := func(k string) bool {
		bv, inBase := b[k]
		mv, inMine := m[k]
		return inMine && (!inBase || !same(bv, mv))
	}
	removed := func(k string) bool {
		_, inBase := b[k]
		_, inMine := m[k]
		return inBase && !inMine
	}
	var out []T
	seen := map[string]bool{}
	for _, t := range theirs {
		k := key(t)
		seen[k] = true
		switch {
		case removed(k):
		case changed(k):
			out = append(out, m[k])
		default:
			out = append(out, t)
		}
	}
	for _, x := range mine {
		if k := key(x); !seen[k] && changed(k) {
			seen[k] = true
			out = append(out, x)
		}
	}
	return out
}

func envKey(v postman.EnvValue) string { return v.Key }

func sameEnvValue(a, b postman.EnvValue) bool {
	return a.Value == b.Value && a.On() == b.On() && a.Secret() == b.Secret()
}

func varKey(v postman.Variable) string { return v.Key }

func sameVariable(a, b postman.Variable) bool {
	return a.Value == b.Value && a.Disabled == b.Disabled && a.Type == b.Type && descOf(a.Description) == descOf(b.Description)
}

// sameJSON reports whether a and b are the same in Postman's JSON.
func sameJSON(a, b any) bool {
	x, err := json.Marshal(a)
	if err != nil {
		return false
	}
	y, err := json.Marshal(b)
	return err == nil && bytes.Equal(x, y)
}

func descOf(d *postman.Description) string {
	if d == nil {
		return ""
	}
	return d.Content
}
