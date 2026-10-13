package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/shinyvision/posthero/internal/postman"
	"github.com/shinyvision/posthero/internal/reqdoc"
	"github.com/shinyvision/posthero/internal/store"
	"github.com/shinyvision/posthero/internal/textdiff"
	"github.com/shinyvision/posthero/internal/ui/editmodal"
	"github.com/shinyvision/posthero/internal/ui/modal"
	"github.com/shinyvision/posthero/internal/ui/picker"
	"github.com/shinyvision/posthero/internal/ui/theme"
	"github.com/shinyvision/posthero/internal/ui/tree"
)

// startedMsg says who the API key belongs to and which workspaces there
// are.
type startedMsg struct {
	me         postman.User
	workspaces []postman.Workspace
	err        error
}

// start asks Postman who we are and which workspaces there are.
func (m *Model) start() tea.Cmd {
	api := m.remote
	return m.run("connecting to Postman", func(ctx context.Context) tea.Msg {
		me, err := api.Me(ctx)
		if err != nil {
			return startedMsg{err: err}
		}
		ws, err := api.Workspaces(ctx)
		return startedMsg{me: me, workspaces: ws, err: err}
	})
}

func (m *Model) started(msg startedMsg) tea.Cmd {
	if msg.err != nil {
		if postman.IsUnauthorized(msg.err) {
			m.form = m.loginForm("Postman didn't accept the API key. Make a new one and paste it here.")
			return nil
		}
		m.setStatus(msg.err.Error(), true)
		if m.st != nil && m.st.Cached() {
			m.setStatus("offline: showing the workspace as it was last fetched ("+msg.err.Error()+")", true)
			return m.afterLoad()
		}
		return modal.Show(&modal.Choice{
			Title: "Can't reach Postman", Body: msg.err.Error(), Danger: true,
			Options: []modal.Option{
				{Key: "r", Label: "Try again", Run: func() tea.Msg { return cmdMsg{m.start} }},
				{Key: "q", Label: "Quit", Run: tea.Quit},
			},
		})
	}
	m.me, m.workspaces = msg.me, msg.workspaces
	var ws postman.Workspace
	ok := false
	if m.st != nil {
		ws, ok = m.findWorkspace(m.st.Workspace.ID) // an id, or a name given with -w
	}
	if !ok {
		ws, ok = m.findWorkspace(m.settings.Workspace)
	}
	if !ok {
		ws, ok = m.findWorkspace(m.state.Workspace)
	}
	if !ok && len(m.workspaces) == 1 {
		ws, ok = m.workspaces[0], true
	}
	if !ok {
		return m.pickWorkspace("Which workspace does your team use?")
	}
	if m.st == nil || m.st.Workspace.ID != ws.ID {
		m.openWorkspace(ws)
	} else {
		m.st.Workspace = ws
		m.state.WorkspaceName = ws.Name
	}
	cmds := []tea.Cmd{m.refresh(true)}
	if m.loaded {
		cmds = append(cmds, m.afterLoad())
	}
	return tea.Batch(cmds...)
}

// findWorkspace finds a workspace by id or name.
func (m *Model) findWorkspace(s string) (postman.Workspace, bool) {
	if s == "" {
		return postman.Workspace{}, false
	}
	for _, ws := range m.workspaces {
		if ws.ID == s {
			return ws, true
		}
	}
	for _, ws := range m.workspaces {
		if strings.EqualFold(ws.Name, s) {
			return ws, true
		}
	}
	return postman.Workspace{}, false
}

// pickWorkspace asks which workspace to open.
func (m *Model) pickWorkspace(title string) tea.Cmd {
	var items []picker.Item
	for _, ws := range m.workspaces {
		detail := ws.Visibility
		if ws.Type == "personal" || ws.Visibility == "personal" {
			detail = "only you"
		}
		items = append(items, picker.Item{Label: ws.Name, Detail: detail, Value: ws, Current: m.st != nil && m.st.Workspace.ID == ws.ID})
	}
	p := &picker.Picker{Title: title, Items: items, Placeholder: "workspace", Pick: func(it picker.Item) tea.Cmd {
		ws := it.Value.(postman.Workspace)
		return func() tea.Msg { return cmdMsg{func() tea.Cmd { return m.switchWorkspace(ws) }} }
	}}
	return p.Open()
}

// switchWorkspace opens another workspace.
func (m *Model) switchWorkspace(ws postman.Workspace) tea.Cmd {
	if m.st != nil && m.st.Workspace.ID == ws.ID {
		return nil
	}
	m.open = nil
	m.resp.Show(nil)
	// What runs, what was cut and what u would take back belong to the old
	// workspace.
	m.cancelSend()
	m.undo, m.cut = nil, nil
	m.runv, m.showRun = nil, false
	m.openWorkspace(ws)
	m.setStatus("workspace "+ws.Name, false)
	m.refreshing = false // a refresh of the old one doesn't count
	return tea.Batch(m.refresh(true), m.afterLoad())
}

// fetchedMsg is what a refresh found.
type fetchedMsg struct {
	ws  string
	f   store.Fetched
	err error
	// seq says which refresh it is: only the last one counts.
	seq int
}

// progressMsg is part of the first load.
type progressMsg struct {
	ws string
	p  store.Progress
}

func (m *Model) progress(msg progressMsg) tea.Cmd {
	if m.st == nil || msg.ws != m.st.Workspace.ID || m.loaded {
		return nil
	}
	if m.st.ApplyProgress(msg.p) {
		m.tree.SetData(m.st.Collections())
		if m.state.Cursor != "" {
			m.tree.Select(m.state.Cursor)
		}
		m.updateMarks()
	}
	return nil
}

// refresh asks Postman what changed; force fetches the globals too.
func (m *Model) refresh(force bool) tea.Cmd {
	if m.st == nil || m.st.API == nil || m.refreshing {
		return nil
	}
	m.refreshing = true
	m.refreshSeq++
	seq := m.refreshSeq
	m.lastRefresh = time.Now()
	plan := m.st.Plan(force)
	note := ""
	if !m.loaded {
		note = "fetching " + orName(m.st.Workspace.Name, "the workspace")
	}
	run := m.run
	if note == "" {
		// Quiet: refreshes in the background don't spin.
		run = func(_ string, f func(context.Context) tea.Msg) tea.Cmd {
			ctx := m.ctx
			return func() tea.Msg {
				c, cancel := context.WithTimeout(ctx, 2*time.Minute)
				defer cancel()
				return f(c)
			}
		}
	}
	ws := m.st.Workspace.ID
	if !m.loaded && m.send != nil {
		// The first load shows the collections as they come in.
		send := m.send
		plan.Progress = func(pr store.Progress) { send(progressMsg{ws: ws, p: pr}) }
	}
	return run(note, func(ctx context.Context) tea.Msg {
		f, err := store.Fetch(ctx, plan)
		return fetchedMsg{ws: ws, f: f, err: err, seq: seq}
	})
}

func orName(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

func (m *Model) fetched(msg fetchedMsg) tea.Cmd {
	if msg.seq != m.refreshSeq {
		return nil // a refresh that a newer one replaced
	}
	m.refreshing = false
	if m.st == nil || msg.ws != m.st.Workspace.ID {
		return nil
	}
	if msg.err != nil {
		if !errors.Is(msg.err, context.Canceled) {
			m.setStatus("couldn't refresh from Postman: "+msg.err.Error(), true)
		}
		return nil
	}
	first := !m.loaded
	ch := m.st.Apply(msg.f)
	m.loaded = true
	m.dropOpenIfGone()
	m.pruneDrafts()
	if len(msg.f.Errors) > 0 {
		m.setStatus(msg.f.Errors[0].Error(), true)
	}
	if ch.Any() || first {
		m.tree.SetData(m.st.Collections())
		m.updateMarks()
	}
	// The open request changed in Postman.
	if m.open != nil {
		for _, id := range ch.Cols {
			if id != m.open.colID {
				continue
			}
			c, it, _ := m.findItem(m.open.colID, m.open.itemID)
			switch {
			case it == nil && c != nil && c.Data != nil:
				m.setStatus("the open request was deleted in Postman; your text is still in the editor: yank it into a new request (a in the tree)", true)
			case it != nil:
				if _, draft := m.drafts.get(it.ID); draft {
					if m.editor.texts[m.open.file] != "" && strings.TrimSuffix(reqdoc.Render(it), "\n") != m.drafts.textOr(it.ID) {
						m.setStatus("someone changed this request in Postman; your changes are kept · :e! takes theirs", false)
					}
				} else {
					m.editor.changedOnServer(m.open.colID, it)
				}
			}
		}
	}
	if ch.Any() {
		m.refreshHints()
	}
	if first {
		return tea.Batch(m.afterLoad(), m.recount())
	}
	return nil
}

// dropOpenIfGone lets go of the open request when its collection is gone
// (deleted here or by a teammate).
func (m *Model) dropOpenIfGone() {
	if m.open == nil || m.st != nil && m.st.Col(m.open.colID) != nil {
		return
	}
	m.open = nil
	m.resp.Show(nil)
	m.setStatus("the open request's collection was deleted", true)
}

// afterLoad opens where posthero was left: the open request and the tree's
// cursor, once the collections are in.
func (m *Model) afterLoad() tea.Cmd {
	if m.st == nil {
		return nil
	}
	m.tree.SetData(m.st.Collections())
	var cmds []tea.Cmd
	if m.opts.Open != "" {
		target := m.opts.Open
		m.opts.Open = ""
		if c, it := m.lookup(target); it != nil {
			return m.openItem(c.Summary.ID, it.ID, true)
		}
		m.setStatus("no request "+target+" in this workspace", true)
	}
	if m.open == nil && m.state.Open != "" {
		colID, itemID, _ := strings.Cut(m.state.Open, "/")
		if _, it, _ := m.findItem(colID, itemID); it != nil {
			cmds = append(cmds, m.openItem(colID, itemID, false))
		}
	}
	if m.state.Cursor != "" {
		m.tree.Select(m.state.Cursor)
	}
	m.updateMarks()
	return tea.Batch(cmds...)
}

// lookup finds a request by id, name or a Postman link to it.
func (m *Model) lookup(s string) (*store.Col, *postman.Item) {
	s = strings.TrimSpace(s)
	if i := strings.LastIndex(s, "/request/"); i >= 0 {
		s = s[i+len("/request/"):]
	}
	if c, it, _ := m.st.Find(s); it != nil {
		return c, it
	}
	if i := strings.Index(s, "-"); i > 0 {
		if c, it, _ := m.st.Find(s[i+1:]); it != nil {
			return c, it
		}
	}
	for _, c := range m.st.Collections() {
		if c.Data == nil {
			continue
		}
		for _, loc := range c.Data.Requests() {
			if strings.EqualFold(loc.Item.Name, s) || strings.EqualFold(loc.Path(), s) {
				return c, loc.Item
			}
		}
	}
	return nil, nil
}

// openItem opens a request in the editor and its last response in the
// response pane; focus moves the keyboard to the editor.
func (m *Model) openItem(colID, itemID string, focus bool) tea.Cmd {
	c, it, _ := m.findItem(colID, itemID)
	if it == nil {
		m.setStatus("that request isn't there any more", true)
		return nil
	}
	if it.Folder {
		m.tree.Select(itemID)
		return nil
	}
	m.editor.open(c.Summary.ID, it)
	m.state.AddRecent(c.Summary.ID + "/" + it.ID)
	m.tree.Select(itemID)
	m.showRun = false
	if rec := m.lastRecord(itemID); rec != nil {
		m.resp.Show(rec)
	} else {
		m.resp.Show(nil)
	}
	if _, busy := m.sending[itemID]; busy {
		m.resp.Busy = "sending " + it.Name + "…"
	} else {
		m.resp.Busy = ""
	}
	if focus {
		m.focusPane(paneEditor)
	}
	m.updateMarks()
	m.refreshHints()
	return tea.Batch(m.editor.ensure(), m.ensureSchema(c.Summary.ID, it, false))
}

// savedMsg is how saving a request went.
type savedMsg struct {
	colID     string
	item      *postman.Item // what was saved
	before    *postman.Item // what it was
	updatedAt string
	conflict  *store.Conflict
	text      string
	err       error
}

// saveRequest saves a request's text to Postman (or its file).
func (m *Model) saveRequest(colID, itemID, text string) tea.Cmd {
	return m.save(colID, itemID, text, false)
}

func (m *Model) save(colID, itemID, text string, force bool) tea.Cmd {
	c, it, _ := m.findItem(colID, itemID)
	if it == nil {
		m.quitAfterSave = false
		m.setStatus("this request was deleted in Postman; yank the text into a new request (a in the tree) to keep it", true)
		return nil
	}
	item, err := reqdoc.Apply(it, text)
	if err != nil {
		m.quitAfterSave = false // :wq stays: the text needs fixing first
		m.drafts.set(itemID, text, m.editor.baseOf(it))
		m.updateMarks()
		return m.docError(err)
	}
	if reqdoc.Render(item) == reqdoc.Render(it) {
		m.drafts.drop(itemID)
		m.updateMarks()
		m.setStatus("no changes to save", false)
		return nil
	}
	before := it.Clone()
	m.savesInFlight++ // until saved() hears how it went
	if c.Path != "" || m.st.API == nil {
		return m.saveFile(c, func() { replaceItem(c, item) }, func() tea.Msg {
			return savedMsg{colID: colID, item: item, before: before, text: text}
		})
	}
	api := m.api
	// Changed in Postman since the version this text started from?
	known := m.editor.baseOf(it)
	if b := m.drafts.base(itemID); b != "" {
		known = b
	}
	return m.run("saving "+item.Name, func(ctx context.Context) tea.Msg {
		if !force {
			conflict, err := store.CheckRequest(ctx, api, c.Summary.UID, item.ID, known)
			if err != nil {
				return savedMsg{colID: colID, item: item, before: before, text: text, err: err}
			}
			if conflict != nil {
				return savedMsg{colID: colID, item: item, before: before, text: text, conflict: conflict}
			}
		}
		at, err := api.UpdateRequest(ctx, c.Summary.UID, item)
		return savedMsg{colID: colID, item: item, before: before, updatedAt: at, text: text, err: err}
	})
}

// saveFile changes a collection opened from a file and writes it. A
// failure is reported the way then would report success: to the editor
// that is waiting for it, or in a dialog.
func (m *Model) saveFile(c *store.Col, change func(), then func() tea.Msg) tea.Cmd {
	fail := func(err error) tea.Msg {
		switch x := then().(type) {
		case editmodal.ResultMsg:
			return editmodal.ResultMsg{Err: err}
		case savedMsg:
			x.err = err
			return x
		}
		return errorMsg{"Not saved", err}
	}
	change()
	data, err := store.FileData(c.Data)
	if err != nil {
		return func() tea.Msg { return fail(err) }
	}
	path := c.Path
	if path == "" {
		// A collection only posthero knows (the API is gone): nothing to write.
		return then
	}
	return m.run("writing "+path, func(context.Context) tea.Msg {
		if err := store.WriteFile(path, data); err != nil {
			return fail(err)
		}
		return then()
	})
}

func (m *Model) saved(msg savedMsg) tea.Cmd {
	m.savesInFlight = max(0, m.savesInFlight-1)
	if msg.conflict != nil || msg.err != nil {
		m.quitAfterSave = false // :wq stays when the save didn't happen
	}
	switch {
	case msg.conflict != nil:
		return m.showConflict(msg)
	case msg.err != nil:
		m.drafts.set(msg.item.ID, msg.text, msg.before.UpdatedAt)
		m.updateMarks()
		m.setStatus("not saved: "+msg.err.Error(), true)
		return nil
	}
	c, _, _ := m.findItem(msg.colID, msg.item.ID)
	if c == nil {
		return nil
	}
	msg.item.UpdatedAt = msg.updatedAt
	replaceItem(c, msg.item)
	m.st.Touched(msg.colID, "")
	m.editor.bases[msg.item.ID] = msg.updatedAt
	// What was typed after the save stays a draft, on top of this version.
	if d, ok := m.drafts.get(msg.item.ID); ok && strings.TrimRight(d, "\n") != strings.TrimRight(msg.text, "\n") {
		m.drafts.rebase(msg.item.ID, msg.updatedAt)
	} else {
		m.drafts.drop(msg.item.ID)
	}
	m.pushUndo(undoEntry{what: "save " + msg.item.Name, colID: msg.colID, kind: undoSave, item: msg.before})
	m.tree.SetData(m.st.Collections())
	m.updateMarks()
	m.refreshHints()
	where := m.saveTarget()
	if c.Path != "" {
		where = "to " + c.Path
	}
	m.setStatus(fmt.Sprintf("saved %s %s · u in the tree undoes it", msg.item.Name, where), false)
	return nil
}

// takeTheirs drops the user's changes for what Postman has now; the text
// is kept in the unnamed register, so p puts it back.
// showConflict asks what to do when someone changed the request in
// Postman after posthero fetched it.
func (m *Model) showConflict(msg savedMsg) tea.Cmd {
	return modal.Show(m.conflictChoice(msg))
}

func (m *Model) conflictChoice(msg savedMsg) *modal.Choice {
	who := "someone"
	if msg.conflict.By != "" && fmt.Sprint(m.me.ID) == msg.conflict.By {
		who = "you (elsewhere)"
	}
	return &modal.Choice{
		Title: "Changed in Postman since you opened it",
		Body:  fmt.Sprintf("%s changed %s in Postman (%s), after posthero fetched it.", who, msg.item.Name, when(msg.conflict.At)),
		Options: []modal.Option{
			{Key: "d", Label: "Show the difference", Run: func() tea.Msg {
				return cmdMsg{func() tea.Cmd { return m.showDifference(msg) }}
			}},
			{Key: "o", Label: "Overwrite theirs", Run: func() tea.Msg {
				return cmdMsg{func() tea.Cmd { return m.save(msg.colID, msg.item.ID, msg.text, true) }}
			}},
			{Key: "t", Label: "Take theirs", Run: func() tea.Msg {
				return cmdMsg{func() tea.Cmd { return m.takeTheirs(msg.colID, msg.item.ID, msg.text) }}
			}},
			{Key: "c", Label: "Cancel", Run: nil},
		},
	}
}

// showDifference fetches the request as it is in Postman and shows how
// yours differs, then asks again.
func (m *Model) showDifference(msg savedMsg) tea.Cmd {
	c := m.st.Col(msg.colID)
	if c == nil || m.api == nil {
		return nil
	}
	api, uid := m.api, c.Summary.UID
	return m.run("fetching their version", func(ctx context.Context) tea.Msg {
		col, err := api.Collection(ctx, uid)
		if err != nil {
			return errorMsg{"Couldn't fetch their version", err}
		}
		theirs, _ := col.Find(msg.item.ID)
		if theirs == nil {
			return errorMsg{"Their version is gone", errors.New("the request was deleted in Postman")}
		}
		var lines []string
		for _, l := range textdiff.Lines(reqdoc.Render(theirs), msg.text) {
			switch l.Kind {
			case textdiff.Removed:
				lines = append(lines, theme.Error.Render("- "+l.Text))
			case textdiff.Added:
				lines = append(lines, theme.Success.Render("+ "+l.Text))
			default:
				lines = append(lines, theme.Dim.Render("  ")+l.Text)
			}
		}
		pager := &modal.Pager{Title: "- theirs  + yours · q goes back", Lines: lines, Wide: true}
		// The pager on top of the question, which q goes back to.
		return multiMsg{modal.ShowMsg{Modal: m.conflictChoice(msg)}, modal.ShowMsg{Modal: pager}}
	})
}

func (m *Model) takeTheirs(colID, itemID, text string) tea.Cmd {
	m.regs.Yank(text)
	m.drafts.drop(itemID)
	return tea.Batch(m.reloadCollection(colID, itemID, "your version is in the \" register"), func() tea.Msg { return nil })
}

// replaceItem puts an item in its place in a collection, keeping the
// pointer the tree holds.
func replaceItem(c *store.Col, it *postman.Item) {
	if c.Data == nil {
		return
	}
	if old, _ := c.Data.Find(it.ID); old != nil {
		*old = *it.Clone()
	}
}

// when writes a timestamp from Postman for people.
func when(at string) string {
	t, err := time.Parse(time.RFC3339, at)
	if err != nil {
		return at
	}
	return t.Local().Format("2 Jan 15:04")
}

// reloadCollection fetches a collection again, then selects an item.
func (m *Model) reloadCollection(colID, selectID, note string) tea.Cmd {
	c := m.st.Col(colID)
	if c == nil || m.st.API == nil {
		return nil
	}
	api := m.api
	uid := c.Summary.UID
	return m.run("fetching "+c.Name(), func(ctx context.Context) tea.Msg {
		col, err := api.Collection(ctx, uid)
		return changedMsg{colID: colID, col: col, err: err, selectID: selectID, note: note}
	})
}

// changedMsg is a collection after posthero changed it: fetched again, so
// what posthero shows is what Postman has.
type changedMsg struct {
	colID    string
	col      *postman.Collection
	err      error
	selectID string
	note     string
	undo     *undoEntry
	// open opens the selected request (a new one) in the editor.
	open bool
	// dropDrafts are requests whose drafts go (they were deleted).
	dropDrafts []string
}

func (m *Model) changed(msg changedMsg) tea.Cmd {
	if msg.err == nil || msg.undo != nil {
		for _, id := range msg.dropDrafts {
			m.drafts.drop(id)
		}
	}
	if msg.err != nil {
		m.setStatus(msg.err.Error(), true)
		if msg.undo != nil {
			// The change was made; only fetching the result failed.
			m.pushUndo(*msg.undo)
			return m.refresh(true)
		}
		return nil
	}
	c := m.st.Col(msg.colID)
	if c == nil {
		return nil
	}
	if msg.col != nil {
		c.Data = msg.col
		c.Summary.UpdatedAt = msg.col.Info.UpdatedAt
		m.st.Touched(msg.colID, "")
	}
	if msg.undo != nil {
		m.pushUndo(*msg.undo)
	}
	m.tree.SetData(m.st.Collections())
	if msg.selectID != "" {
		m.tree.Select(msg.selectID)
	}
	m.updateMarks()
	if msg.note != "" {
		m.setStatus(msg.note, false)
	}
	var cmd tea.Cmd
	if m.open != nil && m.open.colID == msg.colID {
		if _, it, _ := m.findItem(msg.colID, m.open.itemID); it != nil {
			m.editor.changedOnServer(msg.colID, it)
		}
	}
	if msg.open && msg.selectID != "" {
		cmd = m.openItem(msg.colID, msg.selectID, true)
	}
	return cmd
}

// treeNode is the node under the tree's cursor, or the open request.
func (m *Model) treeNode() *tree.Node {
	if m.focus != paneTree && m.open != nil {
		c, it, parents := m.findItem(m.open.colID, m.open.itemID)
		if it != nil {
			return &tree.Node{Kind: tree.Request, Col: c, Item: it, Parents: parents}
		}
	}
	return m.tree.Current()
}

// textOr is a draft's text, or "".
func (d *drafts) textOr(id string) string {
	t, _ := d.get(id)
	return t
}
