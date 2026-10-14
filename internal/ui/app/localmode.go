package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/shinyvision/posthero/internal/local"
	"github.com/shinyvision/posthero/internal/postman"
	"github.com/shinyvision/posthero/internal/reqdoc"
	"github.com/shinyvision/posthero/internal/store"
	"github.com/shinyvision/posthero/internal/textdiff"
	"github.com/shinyvision/posthero/internal/ui/diffview"
	"github.com/shinyvision/posthero/internal/ui/editmodal"
	"github.com/shinyvision/posthero/internal/ui/modal"
)

// Local mode (:set local) keeps a workspace on this machine: posthero
// changes a copy of it instead of Postman, until :push sends the changes
// (asking about what the team changed too) and :pull takes the team's in.
// It is remembered per workspace.

type (
	localFetchedMsg struct {
		ws  postman.Workspace
		f   store.Fetched
		err error
	}
	localStartedMsg struct {
		ws  postman.Workspace
		c   *local.Copy
		err error
	}
	localDirtyMsg   struct{ c *local.Copy }
	localCheckMsg   struct{ c *local.Copy }
	localChangesMsg struct {
		c       *local.Copy
		changes []local.Change
	}
	pulledMsg struct {
		c   *local.Copy
		p   local.Pulled
		err error
	}
	planMsg struct {
		c    *local.Copy
		plan *local.Plan
		err  error
	}
	pushedMsg struct {
		c     *local.Copy
		res   local.Pushed
		err   error
		diffs []*local.Conflict
	}
	diffPushedMsg struct {
		view *diffview.View
		tab  *diffview.Tab
		res  local.Pushed
		err  error
	}
)

// localUpdate handles local mode's messages.
func (m *Model) localUpdate(msg tea.Msg) (tea.Cmd, bool) {
	switch msg := msg.(type) {
	case localFetchedMsg:
		return m.localFetched(msg), true
	case localStartedMsg:
		return m.localStarted(msg), true
	case localDirtyMsg:
		if msg.c != m.local {
			return nil, true
		}
		m.localDirty = true
		if m.localChecking {
			return nil, true
		}
		m.localChecking = true
		c := msg.c
		return tea.Tick(200*time.Millisecond, func(time.Time) tea.Msg { return localCheckMsg{c} }), true
	case localCheckMsg:
		if msg.c != m.local {
			m.localChecking = false
			return nil, true
		}
		m.localDirty = false
		c := msg.c
		return func() tea.Msg { return localChangesMsg{c: c, changes: c.Changes()} }, true
	case localChangesMsg:
		if msg.c != m.local {
			return nil, true
		}
		m.localChecking = false
		m.setLocalChanges(msg.changes)
		if m.localDirty {
			return func() tea.Msg { return localDirtyMsg{msg.c} }, true
		}
		return nil, true
	case pulledMsg:
		return m.pulled(msg), true
	case planMsg:
		return m.planned(msg), true
	case pushedMsg:
		return m.pushed(msg), true
	case diffview.PushMsg:
		return m.pushTab(msg.View, msg.Tab), true
	case diffview.EditMsg:
		return m.mergeTab(msg.View, msg.Tab), true
	case diffPushedMsg:
		return m.tabPushed(msg), true
	}
	return nil, false
}

// saveTarget says where a change went, for the status bar: to Postman,
// or in local mode to the local copy.
func (m *Model) saveTarget() string {
	if m.local != nil {
		return "locally (:push sends it)"
	}
	return "to Postman"
}

// saveHint is an editor dialog's hint for :w.
func (m *Model) saveHint() string {
	if m.local != nil {
		return ":w saves locally · :q closes"
	}
	return ":w saves to Postman · :q closes"
}

// localDir is where the open workspace's local copy lives.
func (m *Model) localDir(wsID string) string {
	if m.opts.StateDir == "" {
		return ""
	}
	return local.Dir(m.opts.StateDir, wsID)
}

// isLocal says the workspace is in local mode, for :set.
func (m *Model) isLocal() bool { return m.local != nil }

// setLocal switches local mode on or off for the open workspace.
func (m *Model) setLocal(on bool) tea.Cmd {
	switch {
	case on && m.local != nil:
		m.setStatus("local mode is on already: :push sends your changes, :pull takes the team's", false)
		return nil
	case !on && m.local == nil:
		m.setStatus("local mode is off: changes go to Postman as you make them", false)
		return nil
	case m.st == nil || m.remote == nil:
		m.setStatus("open a workspace first", true)
		return nil
	case m.st.API == nil:
		m.setStatus("these are files on this machine already: :w saves to them", true)
		return nil
	case m.localDir(m.st.Workspace.ID) == "":
		m.setStatus("local mode needs posthero's state directory", true)
		return nil
	}
	if on {
		return m.enterLocal()
	}
	changes := m.local.Changes()
	if len(changes) == 0 {
		return m.leaveLocal()
	}
	return modal.Show(&modal.Choice{
		Title: "Leave local mode?",
		Body: fmt.Sprintf("%s here %s in Postman yet. :push sends %s first.",
			nOf(len(changes), "change"), isOrAre(len(changes)), itOrThem(len(changes))),
		Danger: true,
		Options: []modal.Option{
			{Key: "s", Label: "Stay local", Run: func() tea.Msg { return statusMsg{text: "still local · :push sends your changes"} }},
			{Key: "d", Label: "Drop them, leave", Run: func() tea.Msg { return cmdMsg{m.leaveLocal} }},
		},
	})
}

// nOf is a count and the word for it: "1 change", "3 changes".
func nOf(n int, w string) string { return fmt.Sprintf("%d %s", n, plural(n, w)) }

func isOrAre(n int) string {
	if n == 1 {
		return "isn't"
	}
	return "aren't"
}

func itOrThem(n int) string {
	if n == 1 {
		return "it"
	}
	return "them"
}

// enterLocal makes a local copy of the workspace from what Postman has now
// (fetched first, so it is current) and works on it from then on.
func (m *Model) enterLocal() tea.Cmd {
	plan := m.st.Plan(true)
	plan.API = m.remote
	ws := m.st.Workspace
	return m.run("making a local copy of "+orName(ws.Name, "the workspace"), func(ctx context.Context) tea.Msg {
		f, err := store.Fetch(ctx, plan)
		return localFetchedMsg{ws: ws, f: f, err: err}
	})
}

func (m *Model) localFetched(msg localFetchedMsg) tea.Cmd {
	if m.st == nil || m.st.Workspace.ID != msg.ws.ID || m.local != nil {
		return nil
	}
	if msg.err == nil && len(msg.f.Errors) > 0 {
		msg.err = errors.Join(msg.f.Errors...)
	}
	if msg.err != nil {
		m.setStatus("no local copy: Postman's version couldn't be fetched: "+msg.err.Error(), true)
		return nil
	}
	m.st.Apply(msg.f)
	m.tree.SetData(m.st.Collections())
	var cols []local.Remote
	for _, c := range m.st.Collections() {
		if c.Data == nil {
			m.setStatus("no local copy: "+c.Name()+" didn't load; try again", true)
			return nil
		}
		cols = append(cols, local.Remote{UID: c.Summary.UID, UpdatedAt: c.Summary.UpdatedAt, Data: c.Data.Clone()})
	}
	var envs []local.RemoteEnv
	for _, e := range m.st.Environments() {
		if e.Data == nil {
			m.setStatus("no local copy: the environment "+e.Name()+" didn't load; try again", true)
			return nil
		}
		envs = append(envs, local.RemoteEnv{UID: e.Summary.UID, UpdatedAt: e.Summary.UpdatedAt, Data: e.Data.Clone()})
	}
	globals := append([]postman.EnvValue(nil), m.st.Globals()...)
	dir, ws := m.localDir(msg.ws.ID), msg.ws
	return m.run("making a local copy of "+orName(ws.Name, "the workspace"), func(context.Context) tea.Msg {
		c, err := local.Start(dir, ws, cols, envs, globals)
		return localStartedMsg{ws: ws, c: c, err: err}
	})
}

func (m *Model) localStarted(msg localStartedMsg) tea.Cmd {
	if msg.err != nil {
		m.setStatus("no local copy: "+msg.err.Error(), true)
		return nil
	}
	if m.st == nil || m.st.Workspace.ID != msg.ws.ID {
		_ = msg.c.Remove()
		return nil
	}
	if m.state.Local == nil {
		m.state.Local = map[string]bool{}
	}
	m.state.Local[msg.ws.ID] = true
	m.saveState()
	cmd := m.useLocal(msg.c)
	m.setStatus("local mode: changes stay on this machine · :push sends them to Postman, :pull takes the team's", false)
	return cmd
}

// useLocal makes posthero work on a local copy: the workspace is read again
// from it.
func (m *Model) useLocal(c *local.Copy) tea.Cmd {
	m.local, m.localChanges, m.localMarks = c, 0, nil
	m.api = c.Client()
	m.hearLocal(c)
	return m.reopenWorkspace()
}

// reopenWorkspace reads the open workspace again from where posthero now
// works on it (Postman, or the local copy). What u would take back was
// done to the other, and goes.
func (m *Model) reopenWorkspace() tea.Cmd {
	ws := m.st.Workspace
	m.undo, m.cut = nil, nil
	m.openWorkspace(ws)
	m.loaded = false
	cmds := []tea.Cmd{m.refresh(true)}
	if m.local != nil {
		c := m.local
		cmds = append(cmds, func() tea.Msg { return localDirtyMsg{c} })
	}
	return tea.Batch(cmds...)
}

// leaveLocal goes back to working on Postman, throwing the local copy
// away.
func (m *Model) leaveLocal() tea.Cmd {
	if m.local == nil || m.st == nil {
		return nil
	}
	c := m.local
	delete(m.state.Local, m.st.Workspace.ID)
	m.saveState()
	m.local, m.localChanges, m.localMarks = nil, 0, nil
	m.api = m.remote
	_ = c.Remove()
	cmd := m.reopenWorkspace()
	m.updateMarks()
	m.setStatus("local mode off: changes go to Postman as you make them", false)
	return cmd
}

// openLocal opens a workspace's local copy, if it is in local mode.
func (m *Model) openLocal(ws postman.Workspace) {
	m.local, m.localChanges, m.localMarks = nil, 0, nil
	m.api = m.remote
	if !m.state.Local[ws.ID] {
		return
	}
	dir := m.localDir(ws.ID)
	if dir == "" || !local.Exists(dir) {
		delete(m.state.Local, ws.ID)
		m.setStatus("LOCAL MODE IS OFF: the local copy of "+orName(ws.Name, "this workspace")+" is gone; changes now go to Postman", true)
		return
	}
	c, err := local.Open(dir)
	if err != nil {
		// Not local, and saying so: changes go to Postman now. The copy
		// stays on disk, for whoever wants to look at it.
		delete(m.state.Local, ws.ID)
		m.setStatus("LOCAL MODE IS OFF: the local copy of "+orName(ws.Name, "this workspace")+" can't be read ("+err.Error()+"); changes now go to Postman", true)
		return
	}
	m.local = c
	m.api = c.Client()
	m.hearLocal(c)
}

// hearLocal counts the local changes again whenever posthero changes the
// copy. (send is set once the program runs, before any change.)
func (m *Model) hearLocal(c *local.Copy) {
	c.OnChange(func() {
		if send := m.send; send != nil {
			send(localDirtyMsg{c})
		}
	})
}

// setLocalChanges shows what the local copy has that Postman doesn't.
func (m *Model) setLocalChanges(changes []local.Change) {
	m.localChanges = len(changes)
	m.localMarks = map[string]bool{}
	for _, ch := range changes {
		if ch.Col != "" {
			m.localMarks[ch.Col] = true
		}
		if ch.Item != "" {
			m.localMarks[ch.Item] = true
		}
	}
	m.updateMarks()
}

// pull takes in what the team changed (:pull).
func (m *Model) pull() tea.Cmd {
	if m.local == nil {
		m.setStatus("not in local mode: posthero shows Postman as it is (:refresh fetches now)", false)
		return nil
	}
	c, api := m.local, m.remote
	return m.run("pulling the team's changes", func(ctx context.Context) tea.Msg {
		p, err := c.Pull(ctx, api)
		return pulledMsg{c: c, p: p, err: err}
	})
}

func (m *Model) pulled(msg pulledMsg) tea.Cmd {
	if msg.c != m.local {
		return nil
	}
	if msg.err != nil {
		m.setStatus("couldn't pull: "+msg.err.Error(), true)
		return nil
	}
	text := "pulled: nothing new from the team"
	if msg.p.Took > 0 {
		text = "pulled " + nOf(msg.p.Took, "change") + " from the team"
	}
	if msg.p.Kept > 0 {
		text += " · " + nOf(msg.p.Kept, "change") + " of yours to push"
	}
	m.setStatus(text, false)
	return tea.Batch(m.refresh(true), m.recount())
}

// recount counts what is left to push again: a pull or push changes the
// base too, which the copy doesn't hear of.
func (m *Model) recount() tea.Cmd {
	c := m.local
	if c == nil {
		return nil
	}
	return func() tea.Msg { return localDirtyMsg{c} }
}

// push sends the local changes to Postman (:push): it asks about each
// conflict first (overwrite, show the diff, or skip), then shows the
// diffs asked for, a tab each.
func (m *Model) push() tea.Cmd {
	if m.local == nil {
		m.setStatus("not in local mode: changes go to Postman as you make them", false)
		return nil
	}
	c, api := m.local, m.remote
	return m.run("checking what to push", func(ctx context.Context) tea.Msg {
		p, err := c.Plan(ctx, api)
		return planMsg{c: c, plan: p, err: err}
	})
}

func (m *Model) planned(msg planMsg) tea.Cmd {
	if msg.c != m.local {
		return nil
	}
	if msg.err != nil {
		m.setStatus("couldn't push: "+msg.err.Error(), true)
		return nil
	}
	if msg.plan.Changes() == 0 && len(msg.plan.Conflicts) == 0 {
		m.setStatus("nothing to push: Postman has everything", false)
		return nil
	}
	return m.ask(msg.c, msg.plan, 0, map[string]bool{}, nil)
}

// ask asks about conflict i of the plan, then the next; after the last it
// pushes.
func (m *Model) ask(c *local.Copy, p *local.Plan, i int, overwrite map[string]bool, diffs []*local.Conflict) tea.Cmd {
	if i == len(p.Conflicts) {
		return m.runPush(c, p, overwrite, diffs)
	}
	cf := p.Conflicts[i]
	next := func(over bool, diff bool) tea.Cmd {
		return func() tea.Msg {
			return cmdMsg{func() tea.Cmd {
				if over {
					overwrite[cf.Key] = true
				}
				d := diffs
				if diff {
					d = append(d, cf)
				}
				return m.ask(c, p, i+1, overwrite, d)
			}}
		}
	}
	title := cf.What
	title = strings.ToUpper(title[:1]) + title[1:]
	if len(p.Conflicts) > 1 {
		title += fmt.Sprintf(" (%d of %d)", i+1, len(p.Conflicts))
	}
	over := "Overwrite"
	switch cf.What {
	case "deleted here, changed in Postman":
		over = "Delete it anyway"
	case "changed here, deleted in Postman":
		over = "Make it again"
	}
	return modal.Show(&modal.Choice{
		Title: title,
		Body:  cf.Name + "\n\nShow diff leaves it here for now and compares the two once every question is answered.",
		Options: []modal.Option{
			{Key: "o", Label: over, Run: next(true, false)},
			{Key: "d", Label: "Show diff", Run: next(false, true)},
			{Key: "s", Label: "Skip", Run: next(false, false)},
		},
		Cancel: func() tea.Msg { return statusMsg{text: "push stopped: nothing was sent"} },
	})
}

func (m *Model) runPush(c *local.Copy, p *local.Plan, overwrite map[string]bool, diffs []*local.Conflict) tea.Cmd {
	api := m.remote
	return m.run("pushing to Postman", func(ctx context.Context) tea.Msg {
		res, err := c.Push(ctx, api, p, overwrite, "")
		return pushedMsg{c: c, res: res, err: err, diffs: diffs}
	})
}

func (m *Model) pushed(msg pushedMsg) tea.Cmd {
	if msg.c != m.local {
		return nil
	}
	m.followIDs(msg.res.IDs)
	text := "pushed " + nOf(msg.res.Pushed, "change") + " to Postman"
	if msg.res.Pushed == 0 {
		text = "pushed nothing"
	}
	if t := msg.res.Pulled.Took; t > 0 {
		text += " · took in " + nOf(t, "change") + " from the team"
	}
	if n := len(msg.diffs); n > 0 {
		text += " · " + nOf(n, "diff") + " to look at"
	}
	cmds := []tea.Cmd{m.refresh(true), m.recount()}
	if msg.err != nil {
		m.setStatus(text+" · "+msg.err.Error(), true)
	} else {
		m.setStatus(text, false)
	}
	if len(msg.diffs) > 0 {
		cmds = append(cmds, m.showDiffs(msg.diffs))
	}
	return tea.Batch(cmds...)
}

// showDiffs opens the diff tabs, one per conflict.
func (m *Model) showDiffs(conflicts []*local.Conflict) tea.Cmd {
	if m.conflicts == nil {
		m.conflicts = map[string]*local.Conflict{}
	}
	var tabs []*diffview.Tab
	for _, cf := range conflicts {
		base, mine, theirs := conflictDocs(cf)
		m.conflicts[cf.Key] = cf
		tabs = append(tabs, &diffview.Tab{Key: cf.Key, Name: cf.Name, What: cf.What, Base: base, Mine: mine, Theirs: theirs})
	}
	return modal.Show(diffview.New(tabs))
}

// conflictDocs are a conflict's three versions as documents; a side that
// deleted it is empty.
func conflictDocs(cf *local.Conflict) (base, mine, theirs string) {
	doc := func(it *postman.Item, c *postman.Collection, e *postman.Environment) string {
		switch cf.Kind {
		case "request":
			if it != nil {
				return strings.TrimSuffix(reqdoc.Render(it), "\n")
			}
		case "folder":
			if it != nil {
				return strings.TrimSuffix(reqdoc.RenderFolder(it), "\n")
			}
		case "collection":
			if c != nil {
				return strings.TrimSuffix(reqdoc.RenderCollection(c), "\n")
			}
		case "environment", "globals":
			if e != nil {
				var b strings.Builder
				writeEnvLines(&b, e.Values)
				return strings.TrimSuffix(b.String(), "\n")
			}
		}
		return ""
	}
	return doc(cf.Base, cf.BaseCol, cf.BaseEnv), doc(cf.Mine, cf.MineCol, cf.MineEnv), doc(cf.Theirs, cf.TheirsCol, cf.TheirsEnv)
}

// mergeTab opens a tab's two versions in the editor, with conflict
// markers, to merge by hand (c, :e). When the editor closes, the diff
// shows the merged text against Postman's.
func (m *Model) mergeTab(view *diffview.View, tab *diffview.Tab) tea.Cmd {
	if m.edit != nil {
		view.Notice("finish the open editor first")
		return nil
	}
	m.hideModal(view)
	m.afterEdit = func() tea.Cmd {
		view.Changed(tab)
		m.modals = append(m.modals, view)
		if line, ok := textdiff.HasConflicts(tab.Mine); ok {
			view.Notice(fmt.Sprintf("conflict markers left (line %d): c merges again", line))
		} else if tab.Merged {
			view.Notice("merged · :w pushes it")
		}
		return nil
	}
	return editmodal.Open(editmodal.Request{
		Title: "Merge " + tab.Name,
		Hint:  "keep what should stay between <<<<<<< and >>>>>>> · :wq",
		Text:  tab.MergeText(),
		Ext:   reqdoc.Ext,
		Name:  "merge",
		Tall:  true,
		Submit: func(text string) tea.Cmd {
			tab.Mine, tab.Merged = strings.TrimRight(text, "\n"), true
			return func() tea.Msg { return editmodal.ResultMsg{} }
		},
	})
}

// hideModal takes a modal off the screen, keeping it.
func (m *Model) hideModal(md modal.Modal) {
	for i, x := range m.modals {
		if x == md {
			m.modals = append(m.modals[:i], m.modals[i+1:]...)
			return
		}
	}
}

// pushTab pushes a tab's version (yours, or the merged text) to Postman
// (:w in the diff).
func (m *Model) pushTab(view *diffview.View, tab *diffview.Tab) tea.Cmd {
	cf := m.conflicts[tab.Key]
	if m.local == nil || cf == nil {
		tab.Busy = false
		view.Notice("not in local mode any more")
		return nil
	}
	c, api, remote := m.local, m.api, m.remote
	ws := m.st.Workspace.ID
	colUID, envUID := "", ""
	if col := m.st.Col(cf.Col); col != nil {
		colUID = col.Summary.UID
	}
	if e := m.st.Env(cf.Env); e != nil {
		envUID = e.Summary.UID
	}
	text, merged := tab.Mine, tab.Merged
	return m.run("pushing "+tab.Name, func(ctx context.Context) tea.Msg {
		if merged {
			if err := applyMerged(ctx, api, ws, colUID, envUID, cf, text); err != nil {
				return diffPushedMsg{view: view, tab: tab, err: err}
			}
		}
		p, err := c.Plan(ctx, remote)
		if err != nil {
			return diffPushedMsg{view: view, tab: tab, err: err}
		}
		res, err := c.Push(ctx, remote, p, map[string]bool{cf.Key: true}, cf.Key)
		return diffPushedMsg{view: view, tab: tab, res: res, err: err}
	})
}

// applyMerged puts a merged document into the local copy.
func applyMerged(ctx context.Context, api postman.API, ws, colUID, envUID string, cf *local.Conflict, text string) error {
	switch cf.Kind {
	case "request", "folder":
		col, err := api.Collection(ctx, colUID)
		if err != nil {
			return err
		}
		it, _ := col.Find(cf.Item)
		if it == nil {
			return fmt.Errorf("%s isn't here any more", cf.Name)
		}
		if cf.Kind == "folder" {
			got, err := reqdoc.ApplyFolder(it, text)
			if err != nil {
				return err
			}
			return api.UpdateFolder(ctx, colUID, got)
		}
		got, err := reqdoc.Apply(it, text)
		if err != nil {
			return err
		}
		_, err = api.UpdateRequest(ctx, colUID, got)
		return err
	case "collection":
		col, err := api.Collection(ctx, colUID)
		if err != nil {
			return err
		}
		got, err := reqdoc.ApplyCollection(col, text)
		if err != nil {
			return err
		}
		desc := ""
		if got.Info.Description != nil {
			desc = got.Info.Description.Content
		}
		auth, ev, vars := got.Auth, got.Event, got.Variable
		return api.PatchCollection(ctx, colUID, postman.CollectionPatch{Name: &got.Info.Name, Description: &desc, Auth: &auth, Event: &ev, Variable: &vars})
	case "environment":
		e, err := api.Environment(ctx, envUID)
		if err != nil {
			return err
		}
		vals, err := parseEnvLines(text, e.Values)
		if err != nil {
			return err
		}
		e.Values = vals
		return api.PutEnvironment(ctx, envUID, e)
	case "globals":
		old, err := api.Globals(ctx, ws)
		if err != nil {
			return err
		}
		vals, err := parseEnvLines(text, old)
		if err != nil {
			return err
		}
		return api.PutGlobals(ctx, ws, vals)
	}
	return fmt.Errorf("posthero can't merge a %s", cf.Kind)
}

func (m *Model) tabPushed(msg diffPushedMsg) tea.Cmd {
	msg.tab.Busy = false
	if msg.err != nil {
		msg.view.Notice("not pushed: " + msg.err.Error())
		m.setStatus("not pushed: "+msg.err.Error(), true)
		return tea.Batch(m.refresh(true), m.recount())
	}
	m.followIDs(msg.res.IDs)
	delete(m.conflicts, msg.tab.Key)
	text := "pushed " + msg.tab.Name
	if msg.res.Pushed == 0 {
		text = msg.tab.Name + ": Postman has it like this already"
	}
	m.setStatus(text, false)
	if msg.view.Remove(msg.tab) {
		m.hideModal(msg.view)
	}
	return tea.Batch(m.refresh(true), m.recount())
}

// followIDs moves what posthero keeps by id (drafts, this machine's
// values, picked environments, the open request) to the ids Postman gave
// what was made locally.
func (m *Model) followIDs(ids map[string]string) {
	if len(ids) == 0 {
		return
	}
	for from, to := range ids {
		if d, ok := m.drafts.get(from); ok {
			m.drafts.set(to, d, m.drafts.base(from))
			m.drafts.drop(from)
		}
		if m.st != nil {
			if o := m.st.Values.Get(from); len(o) > 0 {
				_ = m.st.Values.Set(to, o)
				_ = m.st.Values.Set(from, nil)
			}
		}
		if r, ok := m.last[from]; ok {
			m.last[to] = r
			delete(m.last, from)
		}
	}
	env := map[string]string{}
	for col, e := range m.state.Env {
		if to, ok := ids[col]; ok {
			col = to
		}
		if to, ok := ids[e]; ok {
			e = to
		}
		env[col] = e
	}
	m.state.Env = env
	if o := m.open; o != nil {
		ref := o.docRef
		for _, id := range []*string{&ref.colID, &ref.itemID, &ref.exID} {
			if to, ok := ids[*id]; ok {
				*id = to
			}
		}
		if ref != o.docRef {
			m.open = &openRequest{docRef: ref, file: o.file}
			m.editor.files[o.file] = ref
		}
	}
	m.saveState()
	m.updateMarks()
}
