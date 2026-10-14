package app

import (
	"context"
	"errors"
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/shinyvision/posthero/internal/graphql"
	"github.com/shinyvision/posthero/internal/history"
	"github.com/shinyvision/posthero/internal/postman"
	"github.com/shinyvision/posthero/internal/reqdoc"
	"github.com/shinyvision/posthero/internal/run"
	"github.com/shinyvision/posthero/internal/send"
	"github.com/shinyvision/posthero/internal/store"
	"github.com/shinyvision/posthero/internal/ui/modal"
	"github.com/shinyvision/posthero/internal/ui/respview"
	"github.com/shinyvision/posthero/internal/ui/tree"
	"github.com/shinyvision/posthero/internal/vars"
)

// sentMsg is a request that came back (or didn't).
type sentMsg struct {
	colID, envID string
	ex           *run.Execution
	// before and set are the variables as the send started and as its
	// scripts left them; ws is the workspace it was sent in.
	before, set *vars.Set
	ws          string
	seq         int
	// secrets are the values the history must not keep.
	secrets []string
}

// inflight is a request on its way: which send it is, and how to stop it.
type inflight struct {
	seq    int
	cancel context.CancelFunc
}

// sendText sends a request as its text in the editor says, saved or not.
func (m *Model) sendText(colID, itemID, text string) tea.Cmd {
	c, it, parents := m.findItem(colID, itemID)
	if it == nil {
		m.setStatus("this request isn't in the collection any more", true)
		return nil
	}
	item, err := reqdoc.Apply(it, text)
	if err != nil {
		return m.docError(err)
	}
	return m.sendItem(c, item, parents)
}

// docError shows a mistake in the request text and puts the cursor on it.
func (m *Model) docError(err error) tea.Cmd {
	var de *reqdoc.Error
	if errors.As(err, &de) && m.editor.backend != nil {
		m.editor.backend.Goto(de.Line-1, 0)
	}
	m.setStatus(err.Error(), true)
	return nil
}

// sendCurrent sends the request under the cursor in the tree, or the one
// in the editor.
func (m *Model) sendCurrent() tea.Cmd {
	if m.focus == paneTree {
		if n := m.tree.Current(); n != nil && n.Leaf() {
			ref := nodeRef(n)
			if text, ok := m.docText(ref); ok {
				return m.sendDoc(ref, text)
			}
			return nil
		}
		if n := m.tree.Current(); n != nil {
			return m.runFolder(n)
		}
	}
	if m.open == nil {
		m.setStatus("open a request first (⏎ in the tree, or ctrl+p)", true)
		return nil
	}
	text, ok := m.docText(m.open.docRef)
	if !ok {
		return nil
	}
	return m.sendDoc(m.open.docRef, text)
}

// changesData reports whether a request changes something on the server:
// anything but a read, and GraphQL mutations (wherever they are in the
// document, after fragments and queries too).
func changesData(it *postman.Item) bool {
	if r := it.Request; r != nil && r.Body != nil && r.Body.Mode == "graphql" && r.Body.GraphQL != nil {
		for _, op := range graphql.Operations(string(r.Body.GraphQL.Query)) {
			if op == "mutation" {
				return true
			}
		}
		return false
	}
	switch tree.Badge(it) {
	case "GET", "HEAD", "OPT", "QRY":
		return false
	}
	return true
}

// sendItem sends a request item, asking first when it changes things on a
// production environment.
func (m *Model) sendItem(c *store.Col, it *postman.Item, parents []*postman.Item) tea.Cmd {
	colID := c.Summary.ID
	env := m.envName(colID)
	if m.settings.Confirm && isProd(env) && changesData(it) {
		return modal.Show(&modal.Confirm{
			Title: "Send to " + env + "?",
			Body:  fmt.Sprintf("%s changes things, and %s is production.", it.Name, env),
			Yes:   func() tea.Msg { return cmdMsg{func() tea.Cmd { return m.dispatch(c, it, parents) }} },
		})
	}
	return m.dispatch(c, it, parents)
}

// dispatch sends a request in the background.
func (m *Model) dispatch(c *store.Col, it *postman.Item, parents []*postman.Item) tea.Cmd {
	colID := c.Summary.ID
	if f, ok := m.sending[it.ID]; ok {
		f.cancel() // the newer send wins
	}
	envID := m.envFor(colID)
	set := m.scopes(colID)
	before := set.Clone()
	ws := m.st.Workspace.ID
	ctx, cancel := context.WithCancel(m.ctx)
	m.sendSeq++
	seq := m.sendSeq
	m.sending[it.ID] = inflight{seq: seq, cancel: cancel}
	if m.open != nil && m.open.id() == it.ID {
		m.resp.Busy = "sending " + it.Name + "…"
		m.showRun = false
	}
	r := *m.runner
	// The send works on a copy: the collection can change here meanwhile.
	target := run.Snapshot(c.Data, it, parents)
	prompt := m.prompter()
	vault := m.vault
	m.setStatus("sending "+it.Name+"…", false)
	return func() tea.Msg {
		ex := r.Execute(ctx, target, set, run.Options{Prompt: prompt})
		secrets := append(append(before.Secrets(), set.Secrets()...), vaultSecrets(vault)...)
		return sentMsg{colID: colID, envID: envID, ex: ex, before: before, set: set, ws: ws, seq: seq, secrets: secrets}
	}
}

// vaultSecrets are the vault's values (pm.vault, {{vault:name}}), which the
// history must not keep. They come from the system keyring: not for the UI
// goroutine.
func vaultSecrets(v *vault) []string {
	if v == nil {
		return nil
	}
	var out []string
	for _, name := range v.Names() {
		if val, ok := v.Get(name); ok && len(val) >= 4 {
			out = append(out, val)
		}
	}
	return out
}

// sent shows what came back.
func (m *Model) sent(msg sentMsg) tea.Cmd {
	ex := msg.ex
	itemID := ex.Target.Item.ID
	f, ok := m.sending[itemID]
	if !ok || f.seq != msg.seq {
		// A send that a newer one replaced, or one that was stopped.
		return nil
	}
	f.cancel()
	delete(m.sending, itemID)
	if m.st != nil && m.st.Workspace.ID == msg.ws {
		if err := m.st.CommitChanges(msg.before, msg.set, msg.colID, msg.envID); err != nil {
			m.setStatus("couldn't keep the variables scripts set: "+err.Error(), true)
		}
	}
	env := ""
	if m.st != nil {
		if e := m.st.Env(msg.envID); e != nil {
			env = e.Name()
		}
	}
	rec := history.FromExecution(ex, msg.colID, env)
	if errors.Is(ex.Err, context.Canceled) || ex.Err != nil && ex.Err.Error() == "cancelled" {
		m.resp.Busy = ""
		m.setStatus("cancelled", false)
		return nil
	}
	rec.Secrets = msg.secrets // masked in what the history keeps
	_ = m.hist.Add(rec)
	m.last[itemID] = rec
	if m.open != nil && m.open.id() == itemID {
		m.resp.Busy = ""
		m.showRun = false
		m.resp.Show(rec)
	}
	m.updateMarks()
	m.refreshHints()
	m.setStatus(summary(rec), rec.Error != "" || rec.Failed() > 0 || len(rec.ScriptErrs) > 0)
	// A GraphQL server that answered: now its schema can be asked for too.
	if rec.Error == "" && rec.Status < 300 {
		if _, it, _ := m.findItem(msg.colID, itemID); it != nil {
			return m.ensureSchema(msg.colID, it, true)
		}
	}
	return nil
}

// summary is a one-line report of a response for the status bar.
func summary(r *history.Record) string {
	if r.Error != "" {
		line, _, _ := strings.Cut(r.Error, "\n")
		return line
	}
	if r.Skipped {
		return r.Name + " was skipped by its pre-request script"
	}
	parts := []string{fmt.Sprintf("%d %s", r.Status, r.StatusText), respview.Duration(r.Duration), send.Size(r.HeaderSize + r.WireSize)}
	if n := len(r.Tests); n > 0 {
		if f := r.Failed(); f > 0 {
			parts = append(parts, fmt.Sprintf("✗ %d of %d tests failed", f, n))
		} else {
			parts = append(parts, fmt.Sprintf("✓ %d %s", r.Passed(), plural(r.Passed(), "test")))
		}
	}
	if len(r.ScriptErrs) > 0 {
		parts = append(parts, "a script failed")
	}
	return strings.Join(parts, " · ")
}

// cancelSend stops the request on its way, if any.
func (m *Model) cancelSend() bool {
	if m.running != nil {
		m.running()
		m.setStatus("stopping the run…", false)
		return true
	}
	if len(m.sending) == 0 {
		return false
	}
	for id, f := range m.sending {
		f.cancel()
		delete(m.sending, id)
	}
	m.resp.Busy = ""
	m.setStatus("cancelled", false)
	return true
}

// lastRecord is the last response of a request: from this run, else the
// history.
func (m *Model) lastRecord(itemID string) *history.Record {
	if r, ok := m.last[itemID]; ok {
		return r
	}
	if list := m.hist.Of(itemID); len(list) > 0 {
		if r, err := m.hist.Load(list[0].ID); err == nil {
			m.last[itemID] = r
			return r
		}
	}
	return nil
}

// updateMarks refreshes the tree's signs: the open request, drafts, cut
// items and last status codes.
func (m *Model) updateMarks() {
	marks := tree.Marks{Drafts: m.drafts.ids(), Status: map[string]int{}}
	if m.open != nil {
		marks.Open = m.open.id()
	}
	if m.cut != nil {
		marks.Cut = map[string]bool{}
		for _, id := range m.cut.ids {
			marks.Cut[id] = true
		}
	}
	for id, r := range m.last {
		if r.Status > 0 {
			marks.Status[id] = r.Status
		}
	}
	marks.Local = m.localMarks
	m.tree.SetMarks(marks)
}
