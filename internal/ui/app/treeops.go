package app

import (
	"context"
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/shinyvision/posthero/internal/postman"
	"github.com/shinyvision/posthero/internal/store"
	"github.com/shinyvision/posthero/internal/ui/modal"
	"github.com/shinyvision/posthero/internal/ui/picker"
	"github.com/shinyvision/posthero/internal/ui/tree"
)

// undoKind is what an undo entry takes back.
type undoKind int

const (
	undoSave undoKind = iota
	undoCreate
	undoDelete
	undoRename
	undoMove
	// undoRestore takes back any other change with restore.
	undoRestore
)

// undoEntry is a change to take back with u: the change, and what was
// there before.
type undoEntry struct {
	what  string
	colID string
	kind  undoKind
	// item is the item as it was (save, delete) or the item made (create).
	item *postman.Item
	// parent and next say where it was: in which folder (empty: the
	// collection), before which item (empty: at the end).
	parent, next string
	oldName      string
	folder       bool
	// at is the collection a moved item is in now.
	at string
	// restore takes the change back (undoRestore): it saves what was there
	// before, without recording that as a change to take back.
	restore func() tea.Cmd
}

// pushUndoOf records an undo entry when there is one.
func (m *Model) pushUndoOf(u *undoEntry) {
	if u != nil {
		m.pushUndo(*u)
	}
}

func (m *Model) pushUndo(u undoEntry) {
	m.undo = append(m.undo, u)
	if len(m.undo) > 100 {
		m.undo = m.undo[1:]
	}
}

// cutState is items cut (dd) or copied (yy), waiting for p.
type cutState struct {
	colID string
	ids   []string
	copy  bool
	// undoing says the move takes another back: it isn't one to undo.
	undoing bool
}

// placeOf says where an item is: its folder and the item after it.
func placeOf(c *postman.Collection, id string) (parent, next string) {
	p, _ := c.ParentOf(id)
	list := c.Item
	if p != nil {
		parent, list = p.ID, p.Item
	}
	for i, it := range list {
		if it.ID == id && i+1 < len(list) {
			next = list[i+1].ID
		}
	}
	return parent, next
}

// ownerOf is the owner part of a collection's uid, which item uids share.
func ownerOf(c *store.Col) string {
	owner, _, _ := strings.Cut(c.Summary.UID, "-")
	if owner == "" || len(c.Summary.UID) < 37 {
		return c.Summary.Owner
	}
	return owner
}

func itemUID(c *store.Col, it *postman.Item) string {
	if it.UID != "" {
		return it.UID
	}
	return ownerOf(c) + "-" + it.ID
}

// container is the folder new things go in for a node: the folder itself,
// the folder a request is in, or the collection's top.
func container(n *tree.Node) (parent *postman.Item, after *postman.Item) {
	switch n.Kind {
	case tree.Folder:
		return n.Item, nil
	case tree.Request:
		if len(n.Parents) > 0 {
			return n.Parents[len(n.Parents)-1], n.Item
		}
		return nil, n.Item
	}
	return nil, nil
}

// newRequest asks for a name and makes a request next to the node.
func (m *Model) newRequest(n *tree.Node) tea.Cmd {
	if n == nil {
		m.setStatus("pick a collection or folder in the tree first", true)
		return nil
	}
	parent, after := container(n)
	where := n.Col.Name()
	if parent != nil {
		where = parent.Name
	}
	return modal.Show(&modal.Input{Title: "New request in " + where, Hint: "a name; the request opens in the editor", Regs: m.regs, Submit: func(name string) tea.Cmd {
		name = strings.TrimSpace(name)
		if name == "" {
			return nil
		}
		raw := commonBase(n.Col.Data, parent)
		it := &postman.Item{Name: name, Request: &postman.Request{Method: "GET", URL: postman.ParseURL(raw)}}
		return m.create(n.Col, parent, after, it)
	}})
}

// commonBase is how the requests near a new one start their URLs, such as
// "{{baseUrl}}/", so a new request starts the same way.
func commonBase(c *postman.Collection, parent *postman.Item) string {
	if c == nil {
		return ""
	}
	count := map[string]int{}
	tally := func(it *postman.Item, weight int) {
		if it.Request == nil || it.Request.URL == nil {
			return
		}
		raw := it.Request.URL.Raw
		end := len(raw)
		if i := strings.Index(raw, "://"); i >= 0 {
			if j := strings.IndexByte(raw[i+3:], '/'); j >= 0 {
				end = i + 3 + j
			}
		} else if j := strings.IndexByte(raw, '/'); j >= 0 {
			end = j
		}
		if base := raw[:end]; base != "" && !strings.ContainsAny(base, "?# ") {
			count[base] += weight
		}
	}
	c.Walk(func(it *postman.Item, _ []*postman.Item) bool {
		tally(it, 1)
		return true
	})
	if parent != nil {
		for _, it := range parent.Item {
			tally(it, 10)
		}
	}
	best, n := "", 0
	for base, k := range count {
		if k > n || k == n && base < best {
			best, n = base, k
		}
	}
	if best == "" {
		return ""
	}
	return best + "/"
}

// newFolder asks for a name and makes a folder.
func (m *Model) newFolder(n *tree.Node) tea.Cmd {
	if n == nil {
		m.setStatus("pick a collection or folder in the tree first", true)
		return nil
	}
	parent, _ := container(n)
	return modal.Show(&modal.Input{Title: "New folder", Regs: m.regs, Submit: func(name string) tea.Cmd {
		name = strings.TrimSpace(name)
		if name == "" {
			return nil
		}
		return m.create(n.Col, parent, nil, &postman.Item{Name: name, Folder: true})
	}})
}

// create makes an item in a folder (nil: the collection's top), after
// another item when after is set.
func (m *Model) create(c *store.Col, parent, after *postman.Item, it *postman.Item) tea.Cmd {
	colID := c.Summary.ID
	parentID := ""
	if parent != nil {
		parentID = parent.ID
	}
	if c.Path != "" || m.st.API == nil {
		it.ID = store.NewID()
		return m.saveFile(c, func() {
			index := -1
			if after != nil {
				for i, x := range *c.Data.Container(parent) {
					if x.ID == after.ID {
						index = i + 1
					}
				}
			}
			c.Data.Insert(parent, index, it)
		}, func() tea.Msg {
			u := undoEntry{what: "make " + it.Name, colID: colID, kind: undoCreate, item: it}
			return changedMsg{colID: colID, selectID: it.ID, open: !it.Folder, undo: &u, note: "made " + it.Name}
		})
	}
	api := m.api
	uid := c.Summary.UID
	owner := ownerOf(c)
	afterUID := ""
	if after != nil {
		afterUID = itemUID(c, after)
	}
	parentUID := ""
	if parent != nil {
		parentUID = itemUID(c, parent)
	}
	return m.run("making "+it.Name, func(ctx context.Context) tea.Msg {
		var id string
		var err error
		if it.Folder {
			id, err = api.CreateFolder(ctx, uid, parentID, it)
		} else {
			id, err = api.CreateRequest(ctx, uid, parentID, it)
			if err == nil && afterUID != "" {
				_, err = api.Transfer(ctx, false, []string{owner + "-" + id}, postman.Place{Collection: uid, Folder: parentUID, Position: "after", Next: afterUID}, false)
			}
		}
		if err != nil {
			return changedMsg{err: fmt.Errorf("couldn't make %s: %w", it.Name, err)}
		}
		col, err := api.Collection(ctx, uid)
		made := it.Clone()
		made.ID = id
		u := undoEntry{what: "make " + it.Name, colID: colID, kind: undoCreate, item: made, folder: it.Folder}
		return changedMsg{colID: colID, col: col, err: err, selectID: id, open: !it.Folder, undo: &u, note: "made " + it.Name}
	})
}

// rename asks for a new name.
func (m *Model) rename(n *tree.Node) tea.Cmd {
	if n == nil {
		return nil
	}
	old := n.Name()
	return modal.Show(&modal.Input{Title: "Rename", Value: old, Regs: m.regs, Submit: func(name string) tea.Cmd {
		name = strings.TrimSpace(name)
		if name == "" || name == old {
			return nil
		}
		return m.setName(n.Col, n.Item, name, true)
	}})
}

// setName renames an item (nil: the collection).
func (m *Model) setName(c *store.Col, it *postman.Item, name string, record bool) tea.Cmd {
	colID := c.Summary.ID
	var u *undoEntry
	if record {
		old := c.Name()
		id := ""
		if it != nil {
			old, id = it.Name, it.ID
		}
		u = &undoEntry{what: "rename " + old, colID: colID, kind: undoRename, oldName: old, item: &postman.Item{ID: id}}
	}
	if c.Path != "" || m.st.API == nil {
		return m.saveFile(c, func() {
			if it == nil {
				c.Data.Info.Name = name
				c.Summary.Name = name
			} else {
				it.Name = name
			}
		}, func() tea.Msg { return changedMsg{colID: colID, undo: u, note: "renamed to " + name} })
	}
	api := m.api
	uid := c.Summary.UID
	var changed *postman.Item
	if it != nil {
		changed = it.Clone()
		changed.Name = name
	}
	return m.run("renaming", func(ctx context.Context) tea.Msg {
		var err error
		switch {
		case changed == nil:
			err = api.PatchCollection(ctx, uid, postman.CollectionPatch{Name: &name})
		case changed.Folder:
			err = api.UpdateFolder(ctx, uid, changed)
		default:
			_, err = api.UpdateRequest(ctx, uid, changed)
		}
		if err != nil {
			return changedMsg{err: fmt.Errorf("couldn't rename: %w", err)}
		}
		col, err := api.Collection(ctx, uid)
		sel := ""
		if changed != nil {
			sel = changed.ID
		}
		return changedMsg{colID: colID, col: col, err: err, selectID: sel, undo: u, note: "renamed to " + name}
	})
}

// deleteNode asks, then deletes an item.
func (m *Model) deleteNode(n *tree.Node) tea.Cmd {
	if n == nil {
		return nil
	}
	if n.Kind == tree.Collection {
		return modal.Show(&modal.Confirm{
			Title: "Delete the collection " + n.Name() + "?",
			Body:  "Everyone in the workspace loses it. u can't bring a whole collection back.",
			Yes:   func() tea.Msg { return cmdMsg{func() tea.Cmd { return m.deleteCollection(n.Col) }} },
		})
	}
	body := "u brings it back."
	if n.Kind == tree.Folder {
		count := 0
		var walk func([]*postman.Item)
		walk = func(list []*postman.Item) {
			for _, it := range list {
				if it.Folder {
					walk(it.Item)
				} else {
					count++
				}
			}
		}
		walk(n.Item.Item)
		body = fmt.Sprintf("With the %d %s in it. u brings it all back.", count, plural(count, "request"))
	}
	return modal.Show(&modal.Confirm{
		Title: "Delete " + n.Name() + "?",
		Body:  body,
		Yes:   func() tea.Msg { return cmdMsg{func() tea.Cmd { return m.remove(n.Col, n.Item) }} },
	})
}

func plural(n int, w string) string {
	if n == 1 {
		return w
	}
	return w + "s"
}

// remove deletes an item.
func (m *Model) remove(c *store.Col, it *postman.Item) tea.Cmd {
	colID := c.Summary.ID
	parent, next := placeOf(c.Data, it.ID)
	u := &undoEntry{what: "delete " + it.Name, colID: colID, kind: undoDelete, item: it.Clone(), parent: parent, next: next, folder: it.Folder}
	if c.Path != "" || m.st.API == nil {
		return m.saveFile(c, func() { c.Data.Remove(it.ID) }, func() tea.Msg {
			return changedMsg{colID: colID, undo: u, dropDrafts: itemIDs(it), note: "deleted " + it.Name + " · u brings it back"}
		})
	}
	api := m.api
	uid := c.Summary.UID
	id, folder := it.ID, it.Folder
	return m.run("deleting "+it.Name, func(ctx context.Context) tea.Msg {
		var err error
		if folder {
			err = api.DeleteFolder(ctx, uid, id)
		} else {
			err = api.DeleteRequest(ctx, uid, id)
		}
		if err != nil {
			return changedMsg{err: fmt.Errorf("couldn't delete: %w", err)}
		}
		col, err := api.Collection(ctx, uid)
		return changedMsg{colID: colID, col: col, err: err, undo: u, dropDrafts: itemIDs(u.item), note: "deleted " + u.item.Name + " · u brings it back"}
	})
}

func (m *Model) deleteCollection(c *store.Col) tea.Cmd {
	if m.st.API == nil {
		m.setStatus("collections opened from files are deleted with the file", true)
		return nil
	}
	return m.dropCollection(c, &undoEntry{what: "delete " + c.Name(), kind: undoRestore, restore: func() tea.Cmd {
		// Back as it was, as a new collection: Postman gives it new ids.
		return m.importCollection(c.Data.Clone(), false)
	}}, "deleted the collection "+c.Name()+" · u brings it back")
}

// dropCollection deletes a collection without asking; u, when set, is
// recorded once that worked.
func (m *Model) dropCollection(c *store.Col, u *undoEntry, note string) tea.Cmd {
	api := m.api
	uid, id, name := c.Summary.UID, c.Summary.ID, c.Name()
	return m.run("deleting "+name, func(ctx context.Context) tea.Msg {
		if err := api.DeleteCollection(ctx, uid); err != nil {
			return errorMsg{"Not deleted", err}
		}
		return cmdMsg{func() tea.Cmd {
			m.st.RemoveCol(id)
			m.dropOpenIfGone()
			m.tree.SetData(m.st.Collections())
			m.pushUndoOf(u)
			m.setStatus(note, false)
			return nil
		}}
	})
}

// copyCollectionTo asks which workspace to copy a collection to (yy on a
// collection), then copies it there. To another workspace, its
// environments can go along: variables like {{BackendEndpoint}} come from
// them, and environments belong to a workspace.
func (m *Model) copyCollectionTo(c *store.Col) tea.Cmd {
	if c.Path != "" || m.st.API == nil {
		m.setStatus("this collection is a file; copy the file to copy it", true)
		return nil
	}
	return m.pickWorkspaceFor("Copy "+c.Name()+" to which workspace?", func(ws postman.Workspace) tea.Cmd {
		if m.local != nil && ws.ID != m.st.Workspace.ID {
			m.setStatus("local mode: nothing goes to Postman until :push (or :set nolocal)", true)
			return nil
		}
		envs := m.envsOf(c)
		if ws.ID == m.st.Workspace.ID || len(envs) == 0 {
			return m.copyCollection(c, ws, nil)
		}
		names := make([]string, len(envs))
		for i, e := range envs {
			names[i] = e.Name()
		}
		list := strings.Join(names, ", ")
		return modal.Show(&modal.Choice{
			Title: "Copy " + c.Name() + " to " + ws.Name,
			Body:  "Its variables come from " + list + ", which stays behind in this workspace unless it goes along (with your current values).",
			Options: []modal.Option{
				{Key: "y", Label: "With " + list, Run: func() tea.Msg { return cmdMsg{func() tea.Cmd { return m.copyCollection(c, ws, envs) }} }},
				{Key: "n", Label: "Only the collection", Run: func() tea.Msg { return cmdMsg{func() tea.Cmd { return m.copyCollection(c, ws, nil) }} }},
			},
		})
	})
}

// pickWorkspaceFor asks for a workspace to put something in.
func (m *Model) pickWorkspaceFor(title string, then func(postman.Workspace) tea.Cmd) tea.Cmd {
	var items []picker.Item
	for _, ws := range m.workspaces {
		detail := ws.Visibility
		if ws.Type == "personal" || ws.Visibility == "personal" {
			detail = "only you"
		}
		if ws.ID == m.st.Workspace.ID {
			detail = "this one: a copy next to it"
		}
		items = append(items, picker.Item{Label: ws.Name, Detail: detail, Value: ws, Current: ws.ID == m.st.Workspace.ID})
	}
	if len(items) == 0 {
		m.setStatus("no workspaces known yet; try again in a moment", true)
		return nil
	}
	p := &picker.Picker{Title: title, Items: items, Placeholder: "workspace", Pick: func(it picker.Item) tea.Cmd {
		ws := it.Value.(postman.Workspace)
		return func() tea.Msg { return cmdMsg{func() tea.Cmd { return then(ws) }} }
	}}
	return p.Open()
}

// envsOf are the environments of this workspace a collection goes with:
// the one picked for it, and those named after it.
func (m *Model) envsOf(c *store.Col) []*store.Env {
	var out []*store.Env
	picked := m.st.Env(m.envFor(c.Summary.ID))
	for _, e := range m.st.Environments() {
		if _, ours := envScore(c.Name(), e); ours || e == picked {
			out = append(out, e)
		}
	}
	return out
}

// copyCollection copies a collection, as Postman has it now, to a
// workspace: under its own name in another workspace, as "NAME Copy" next
// to it in this one. envs go along (to another workspace). Current values
// (yours, on this machine) go with them. u takes the copies back.
func (m *Model) copyCollection(c *store.Col, ws postman.Workspace, envs []*store.Env) tea.Cmd {
	if c.Path != "" || m.st.API == nil {
		m.setStatus("this collection is a file; copy the file to copy it", true)
		return nil
	}
	api := m.api
	here := ws.ID == m.st.Workspace.ID
	from, name, uid, colID := m.st.Workspace.ID, c.Name(), c.Summary.UID, c.Summary.ID
	where := "workspace " + ws.Name
	type envCopy struct{ from, to postman.Summary }
	var envFrom []postman.Summary
	if !here {
		for _, e := range envs {
			envFrom = append(envFrom, e.Summary)
		}
	}
	return m.run("copying "+name+" to "+ws.Name, func(ctx context.Context) tea.Msg {
		col, err := api.Collection(ctx, uid)
		if err != nil {
			return errorMsg{"Not copied", err}
		}
		col.Info.PostmanID = ""
		col.Walk(func(it *postman.Item, _ []*postman.Item) bool {
			it.ID, it.UID = "", ""
			for _, r := range it.Response {
				r.ID, r.UID = "", ""
			}
			return true
		})
		if here {
			col.Info.Name += " Copy"
		}
		sum, err := api.CreateCollection(ctx, ws.ID, col)
		if err != nil {
			return errorMsg{"Not copied", err}
		}
		var made []envCopy
		var envErr error
		for _, e := range envFrom {
			env, err := api.Environment(ctx, e.UID)
			if err == nil {
				env.ID, env.UID, env.Owner, env.CreatedAt, env.UpdatedAt = "", "", "", "", ""
				var to postman.Summary
				if to, err = api.CreateEnvironment(ctx, ws.ID, env); err == nil {
					made = append(made, envCopy{e, to})
					continue
				}
			}
			envErr = fmt.Errorf("%s: %w", e.Name, err)
		}
		var full *postman.Collection
		if here {
			if full, err = api.Collection(ctx, sum.UID); err != nil {
				return errorMsg{"Copied, but not fetched", err}
			}
			sum.UpdatedAt = full.Info.UpdatedAt
		}
		return cmdMsg{func() tea.Cmd {
			// Your current values go along: they live on this machine, by id.
			if o := m.st.Values.Get(colID); len(o) > 0 {
				_ = m.st.Values.Set(sum.ID, o)
			}
			for _, e := range made {
				if o := m.st.Values.Get(e.from.ID); len(o) > 0 {
					_ = m.st.Values.Set(e.to.ID, o)
				}
			}
			what := "copy " + name + " to " + where
			note := "copied " + name + " to " + where
			for i, e := range made {
				if i == 0 {
					note += " with "
				} else {
					note += ", "
				}
				note += e.from.Name
			}
			if here {
				made := &store.Col{Summary: sum, Data: full}
				m.st.PutCol(made)
				m.tree.SetData(m.st.Collections())
				m.tree.Select(sum.ID)
				m.pushUndo(undoEntry{what: "copy " + name, kind: undoRestore, restore: func() tea.Cmd {
					return m.dropCollection(made, nil, "took back: copy "+name)
				}})
				m.setStatus("copied "+name+" · "+col.Info.Name, false)
				return nil
			}
			if m.st.Workspace.ID == from {
				// The copies are in the other workspace: u here takes them back.
				m.pushUndo(undoEntry{what: what, kind: undoRestore, restore: func() tea.Cmd {
					return m.run("taking back the copy of "+name, func(ctx context.Context) tea.Msg {
						if err := api.DeleteCollection(ctx, sum.UID); err != nil {
							return errorMsg{"Not taken back", err}
						}
						for _, e := range made {
							if err := api.DeleteEnvironment(ctx, e.to.UID); err != nil {
								return errorMsg{"Not all taken back", err}
							}
						}
						return statusMsg{text: "took back: " + what}
					})
				}})
			}
			if envErr != nil {
				m.setStatus(note+" · an environment wasn't copied: "+envErr.Error(), true)
				return nil
			}
			m.setStatus(note+" · u takes it back", false)
			return nil
		}}
	})
}

// copyEnvTo copies an environment, as Postman has it now, to a workspace
// (:env copy without a name): under its own name in another workspace, as
// "NAME Copy" in this one. Your current values go along; u takes it back.
func (m *Model) copyEnvTo(e *store.Env) tea.Cmd {
	if m.st.API == nil {
		m.setStatus("environments from files are copied as files", true)
		return nil
	}
	return m.pickWorkspaceFor("Copy "+e.Name()+" to which workspace?", func(ws postman.Workspace) tea.Cmd {
		if m.local != nil && ws.ID != m.st.Workspace.ID {
			m.setStatus("local mode: nothing goes to Postman until :push (or :set nolocal)", true)
			return nil
		}
		api := m.api
		here := ws.ID == m.st.Workspace.ID
		from, src := m.st.Workspace.ID, e.Summary
		return m.run("copying "+src.Name+" to "+ws.Name, func(ctx context.Context) tea.Msg {
			env, err := api.Environment(ctx, src.UID)
			if err != nil {
				return errorMsg{"Not copied", err}
			}
			env.ID, env.UID, env.Owner, env.CreatedAt, env.UpdatedAt = "", "", "", "", ""
			if here {
				env.Name += " Copy"
			}
			to, err := api.CreateEnvironment(ctx, ws.ID, env)
			if err != nil {
				return errorMsg{"Not copied", err}
			}
			var full *postman.Environment
			if here {
				if full, err = api.Environment(ctx, to.UID); err != nil {
					return errorMsg{"Copied, but not fetched", err}
				}
				to.UpdatedAt = full.UpdatedAt
			}
			return cmdMsg{func() tea.Cmd {
				if o := m.st.Values.Get(src.ID); len(o) > 0 {
					_ = m.st.Values.Set(to.ID, o)
				}
				if here {
					made := &store.Env{Summary: to, Data: full}
					m.st.PutEnv(made)
					m.pushUndo(m.envMadeUndo(made))
					m.setStatus("copied "+src.Name+" · "+env.Name, false)
					return nil
				}
				what := "copy " + src.Name + " to workspace " + ws.Name
				if m.st.Workspace.ID == from {
					m.pushUndo(undoEntry{what: what, kind: undoRestore, restore: func() tea.Cmd {
						return m.run("taking back the copy of "+src.Name, func(ctx context.Context) tea.Msg {
							if err := api.DeleteEnvironment(ctx, to.UID); err != nil {
								return errorMsg{"Not taken back", err}
							}
							return statusMsg{text: "took back: " + what}
						})
					}})
				}
				m.setStatus("copied "+src.Name+" to workspace "+ws.Name+" · u takes it back", false)
				return nil
			}}
		})
	})
}

// cutNode marks the node to move with p (dd), or to copy (yy).
func (m *Model) cutNode(n *tree.Node, copy bool) {
	if n == nil || n.Item == nil {
		m.setStatus("a collection stays where it is; yy copies it, to this workspace or another", true)
		return
	}
	m.cut = &cutState{colID: n.Col.Summary.ID, ids: []string{n.Item.ID}, copy: copy}
	m.updateMarks()
	if copy {
		m.setStatus("copied "+n.Name()+" · p puts a copy below the cursor, P above, esc forgets it", false)
	} else {
		m.setStatus("cut "+n.Name()+" · p puts it below the cursor (into a folder), P above, esc keeps it where it was", false)
	}
}

// put moves (or copies) the cut items to the node: into a folder or
// collection, or next to a request (above with P).
func (m *Model) put(n *tree.Node, above bool) tea.Cmd {
	if m.cut == nil {
		m.setStatus("nothing to put; dd cuts and yy copies an item first", true)
		return nil
	}
	if n == nil {
		return nil
	}
	src := m.st.Col(m.cut.colID)
	if src == nil || src.Data == nil {
		m.cut = nil
		return nil
	}
	it, _ := src.Data.Find(m.cut.ids[0])
	if it == nil {
		m.cut = nil
		m.updateMarks()
		return nil
	}
	dst := n.Col
	var parent *postman.Item
	place := postman.Place{Collection: dst.Summary.UID, Position: "end"}
	switch n.Kind {
	case tree.Collection:
		if above {
			place.Position = "start"
		}
	case tree.Folder:
		if n.Item.ID == it.ID && !m.cut.copy {
			m.setStatus("a folder can't go into itself", true)
			return nil
		}
		if n.Item.ID == it.ID {
			// A copy of the folder on its own row goes beside it.
			if len(n.Parents) > 0 {
				parent = n.Parents[len(n.Parents)-1]
				place.Folder = itemUID(dst, parent)
			}
			place.Position, place.Next, place.NextFolder = "after", itemUID(dst, n.Item), true
			if above {
				place.Position = "before"
			}
			break
		}
		parent = n.Item
		place.Folder = itemUID(dst, n.Item)
		if above {
			place.Position = "start"
		}
	case tree.Request:
		if len(n.Parents) > 0 {
			parent = n.Parents[len(n.Parents)-1]
			place.Folder = itemUID(dst, parent)
		}
		// Below the row (above with P); a copy on its own row goes right
		// below the original. A folder can't go between requests: Postman
		// keeps a folder's folders before its requests, and places a folder
		// only next to a folder. It goes after the last folder there.
		switch {
		case it.Folder:
			place.Position = "end"
		case n.Item.ID != it.ID || m.cut.copy:
			place.Position, place.Next = "after", itemUID(dst, n.Item)
			if above {
				place.Position = "before"
			}
		}
	}
	for _, p := range n.Parents {
		if p.ID == it.ID {
			m.setStatus("a folder can't go into itself", true)
			return nil
		}
	}
	cut := *m.cut
	m.cut = nil
	m.updateMarks()
	oldParent, oldNext := placeOf(src.Data, it.ID)
	u := &undoEntry{what: "move " + it.Name, colID: src.Summary.ID, kind: undoMove, item: &postman.Item{ID: it.ID, Name: it.Name, Folder: it.Folder}, parent: oldParent, next: oldNext, at: dst.Summary.ID}
	if cut.copy || cut.undoing {
		u = nil
	}
	// A copy is taken back by deleting it, once it is known what it is.
	var copied *undoEntry
	if cut.copy {
		copied = &undoEntry{what: "copy " + it.Name, colID: dst.Summary.ID, kind: undoCreate, item: &postman.Item{Name: it.Name + " Copy", Folder: it.Folder}}
	}
	if src.Path != "" || dst.Path != "" || m.st.API == nil {
		if src != dst {
			m.setStatus("moving between files isn't possible; copy the text instead", true)
			return nil
		}
		return m.saveFile(src, func() {
			moving := it
			if cut.copy {
				moving = it.Clone()
				moving.ID = store.NewID()
				moving.Name += " Copy"
				copied.item.ID = moving.ID
				u = copied
			} else {
				src.Data.Remove(it.ID)
			}
			index := -1
			list := *src.Data.Container(parent)
			switch place.Position {
			case "start":
				index = 0
			case "before", "after":
				for i, x := range list {
					if itemUID(src, x) == place.Next || x.ID == place.Next {
						index = i
						if place.Position == "after" {
							index++
						}
					}
				}
			}
			src.Data.Insert(parent, index, moving)
		}, func() tea.Msg {
			return changedMsg{colID: src.Summary.ID, selectID: it.ID, undo: u, note: "moved " + it.Name}
		})
	}
	api := m.api
	srcUID, dstUID := src.Summary.UID, dst.Summary.UID
	ids := []string{itemUID(src, it)}
	verb := "moved"
	if cut.copy {
		verb = "copied"
	}
	return m.run(strings.TrimSuffix(verb, "d")+"ing "+it.Name, func(ctx context.Context) tea.Msg {
		got, err := api.Transfer(ctx, it.Folder, ids, place, cut.copy)
		if err != nil {
			return changedMsg{err: fmt.Errorf("couldn't move %s: %w", it.Name, err)}
		}
		sel := it.ID
		if len(got) == 1 {
			// A uid is the owner, a dash and the id.
			_, sel, _ = strings.Cut(got[0], "-")
		}
		if u != nil {
			u.item.ID = sel // where it is now, to take it back from
		}
		if copied != nil && len(got) == 1 {
			copied.item.ID = sel
			u = copied
		}
		col, err := api.Collection(ctx, dstUID)
		msg := changedMsg{colID: dst.Summary.ID, col: col, err: err, selectID: sel, undo: u, note: verb + " " + it.Name}
		if srcUID != dstUID && err == nil {
			other, err2 := api.Collection(ctx, srcUID)
			if err2 == nil {
				return multiMsg{changedMsg{colID: src.Summary.ID, col: other}, msg}
			}
		}
		return msg
	})
}

// undoLast takes back the last change.
func (m *Model) undoLast() tea.Cmd {
	if len(m.undo) == 0 {
		m.setStatus("Already at oldest change", false)
		return nil
	}
	u := m.undo[len(m.undo)-1]
	m.undo = m.undo[:len(m.undo)-1]
	// When it's done, the status bar says what was taken back, whatever
	// the work of taking it back says.
	m.setStatus("undoing: "+u.what, false)
	m.undoNote = "took back: " + u.what
	if u.kind == undoRestore {
		return u.restore()
	}
	c := m.st.Col(u.colID)
	if c == nil || c.Data == nil {
		m.setStatus("that collection is gone", true)
		return nil
	}
	switch u.kind {
	case undoRename:
		var it *postman.Item
		if u.item.ID != "" {
			it, _ = c.Data.Find(u.item.ID)
			if it == nil {
				return nil
			}
		}
		return m.setName(c, it, u.oldName, false)
	case undoCreate:
		it, _ := c.Data.Find(u.item.ID)
		if it == nil {
			return nil
		}
		return m.removeQuiet(c, it, "took back: "+u.what)
	case undoSave:
		return m.restore(c, u.item, "took back: "+u.what)
	case undoDelete:
		return m.recreate(c, u)
	case undoMove:
		now := m.st.Col(u.at)
		if now == nil || now.Data == nil {
			now = c
		}
		it, _ := now.Data.Find(u.item.ID)
		if it == nil {
			m.setStatus("the moved item isn't there any more", true)
			return nil
		}
		m.cut = &cutState{colID: now.Summary.ID, ids: []string{it.ID}, undoing: true}
		var target *tree.Node
		switch {
		case u.next != "":
			next, parents := c.Data.Find(u.next)
			if next != nil {
				target = &tree.Node{Kind: tree.Request, Col: c, Item: next, Parents: parents}
				if next.Folder {
					// Before a folder: the start of its parent.
					return m.putBefore(c, now, it, u.parent, next)
				}
				return m.put(target, true)
			}
			fallthrough
		case u.parent != "":
			folder, parents := c.Data.Find(u.parent)
			if folder != nil {
				target = &tree.Node{Kind: tree.Folder, Col: c, Item: folder, Parents: parents}
			}
		}
		if target == nil {
			target = &tree.Node{Kind: tree.Collection, Col: c}
		}
		return m.put(target, false)
	}
	return nil
}

// putBefore moves an item before a folder.
func (m *Model) putBefore(c, from *store.Col, it *postman.Item, parent string, next *postman.Item) tea.Cmd {
	if c.Path != "" || from.Path != "" || m.st.API == nil {
		m.cut = nil
		if c != from {
			m.setStatus("moving between files isn't possible", true)
			return nil
		}
		return m.saveFile(c, func() {
			c.Data.Remove(it.ID)
			var p *postman.Item
			if parent != "" {
				p, _ = c.Data.Find(parent)
			}
			index := -1
			for i, x := range *c.Data.Container(p) {
				if x.ID == next.ID {
					index = i
				}
			}
			c.Data.Insert(p, index, it)
		}, func() tea.Msg {
			return changedMsg{colID: c.Summary.ID, selectID: it.ID, note: "moved " + it.Name + " back"}
		})
	}
	api := m.api
	place := postman.Place{Collection: c.Summary.UID, Position: "before", Next: itemUID(c, next), NextFolder: true}
	if parent != "" {
		if p, _ := c.Data.Find(parent); p != nil {
			place.Folder = itemUID(c, p)
		}
	}
	m.cut = nil
	ids := []string{itemUID(from, it)}
	colID, uid := c.Summary.ID, c.Summary.UID
	fromID, fromUID := from.Summary.ID, from.Summary.UID
	return m.run("moving "+it.Name+" back", func(ctx context.Context) tea.Msg {
		got, err := api.Transfer(ctx, it.Folder, ids, place, false)
		if err != nil {
			return changedMsg{err: err}
		}
		sel := it.ID
		if len(got) == 1 {
			_, sel, _ = strings.Cut(got[0], "-")
		}
		col, err := api.Collection(ctx, uid)
		msg := changedMsg{colID: colID, col: col, err: err, selectID: sel, note: "moved " + it.Name + " back"}
		if fromUID != uid && err == nil {
			if other, err := api.Collection(ctx, fromUID); err == nil {
				return multiMsg{changedMsg{colID: fromID, col: other}, msg}
			}
		}
		return msg
	})
}

// removeQuiet deletes an item without asking or recording an undo.
func (m *Model) removeQuiet(c *store.Col, it *postman.Item, note string) tea.Cmd {
	colID := c.Summary.ID
	if c.Path != "" || m.st.API == nil {
		return m.saveFile(c, func() { c.Data.Remove(it.ID) }, func() tea.Msg { return changedMsg{colID: colID, note: note} })
	}
	api := m.api
	uid, id, folder := c.Summary.UID, it.ID, it.Folder
	return m.run("taking it back", func(ctx context.Context) tea.Msg {
		var err error
		if folder {
			err = api.DeleteFolder(ctx, uid, id)
		} else {
			err = api.DeleteRequest(ctx, uid, id)
		}
		if err != nil {
			return changedMsg{err: err}
		}
		col, err := api.Collection(ctx, uid)
		return changedMsg{colID: colID, col: col, err: err, note: note}
	})
}

// restore saves an item as it was.
func (m *Model) restore(c *store.Col, it *postman.Item, note string) tea.Cmd {
	colID := c.Summary.ID
	if c.Path != "" || m.st.API == nil {
		return m.saveFile(c, func() {
			cur, _ := c.Data.Find(it.ID)
			if cur != nil && it.Folder {
				// A folder's own settings; what is in it now stays.
				cur.Name, cur.Description, cur.Auth, cur.Event = it.Name, it.Description, it.Auth, it.Event
				return
			}
			replaceItem(c, it)
		}, func() tea.Msg { return changedMsg{colID: colID, selectID: it.ID, note: note} })
	}
	api := m.api
	uid := c.Summary.UID
	return m.run("restoring "+it.Name, func(ctx context.Context) tea.Msg {
		var err error
		if it.Folder {
			err = api.UpdateFolder(ctx, uid, it)
		} else {
			_, err = api.UpdateRequest(ctx, uid, it)
		}
		if err != nil {
			return changedMsg{err: err}
		}
		col, err := api.Collection(ctx, uid)
		return changedMsg{colID: colID, col: col, err: err, selectID: it.ID, note: note}
	})
}

// recreate brings a deleted item back, with everything in it, where it
// was.
func (m *Model) recreate(c *store.Col, u undoEntry) tea.Cmd {
	colID := c.Summary.ID
	if c.Path != "" || m.st.API == nil {
		return m.saveFile(c, func() {
			var parent *postman.Item
			if u.parent != "" {
				parent, _ = c.Data.Find(u.parent)
			}
			index := -1
			for i, x := range *c.Data.Container(parent) {
				if x.ID == u.next {
					index = i
				}
			}
			c.Data.Insert(parent, index, u.item)
		}, func() tea.Msg {
			return changedMsg{colID: colID, selectID: u.item.ID, note: "brought back " + u.item.Name}
		})
	}
	api := m.api
	uid := c.Summary.UID
	owner := ownerOf(c)
	var nextUID string
	nextFolder := false
	if u.next != "" {
		if next, _ := c.Data.Find(u.next); next != nil {
			nextUID, nextFolder = itemUID(c, next), next.Folder
		}
	}
	parentUID := ""
	if u.parent != "" {
		if p, _ := c.Data.Find(u.parent); p != nil {
			parentUID = itemUID(c, p)
		}
	}
	return m.run("bringing back "+u.item.Name, func(ctx context.Context) tea.Msg {
		id, err := recreateItem(ctx, api, uid, u.parent, u.item)
		if err != nil {
			return changedMsg{err: fmt.Errorf("couldn't bring %s back: %w", u.item.Name, err)}
		}
		if nextUID != "" && u.item.Folder == nextFolder {
			_, _ = api.Transfer(ctx, u.item.Folder, []string{owner + "-" + id}, postman.Place{Collection: uid, Folder: parentUID, Position: "before", Next: nextUID, NextFolder: nextFolder}, false)
		}
		col, err := api.Collection(ctx, uid)
		return changedMsg{colID: colID, col: col, err: err, selectID: id, note: "brought back " + u.item.Name}
	})
}

// recreateItem makes an item again with everything in it, and its saved
// examples, in the collection uid.
func recreateItem(ctx context.Context, api *postman.Client, uid, parent string, it *postman.Item) (string, error) {
	if it.Folder {
		id, err := api.CreateFolder(ctx, uid, parent, it)
		if err != nil {
			return "", err
		}
		for _, child := range it.Item {
			if _, err := recreateItem(ctx, api, uid, id, child); err != nil {
				return id, err
			}
		}
		return id, nil
	}
	id, err := api.CreateRequest(ctx, uid, parent, it)
	if err != nil {
		return "", err
	}
	for _, r := range it.Response {
		if _, err := api.CreateResponse(ctx, uid, id, r); err != nil {
			return id, err
		}
	}
	return id, nil
}

// itemIDs are the ids of an item and everything in it.
func itemIDs(it *postman.Item) []string {
	out := []string{it.ID}
	for _, child := range it.Item {
		out = append(out, itemIDs(child)...)
	}
	return out
}
