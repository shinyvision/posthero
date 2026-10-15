package app

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/shinyvision/posthero/internal/history"
	"github.com/shinyvision/posthero/internal/postman"
	"github.com/shinyvision/posthero/internal/reqdoc"
	"github.com/shinyvision/posthero/internal/send"
	"github.com/shinyvision/posthero/internal/store"
	"github.com/shinyvision/posthero/internal/textdiff"
	"github.com/shinyvision/posthero/internal/ui/modal"
	"github.com/shinyvision/posthero/internal/ui/picker"
	"github.com/shinyvision/posthero/internal/ui/theme"
	"github.com/shinyvision/posthero/internal/ui/tree"
)

// Saved examples are responses kept with their request, as Postman keeps
// them. One opens in the editor like a request does (its request, then
// ### response and what came back), :w saves it and ⏎ sends its request.
// In the tree they fold under their request, where r, D, yy, dd, p and
// :dup work on them as on requests, and u takes any of it back.

// openExample opens a saved example in the editor, and in the response
// pane what it says comes back.
func (m *Model) openExample(ref docRef, focus bool) tea.Cmd {
	c, it, _, ex := m.findDoc(ref)
	if ex == nil {
		m.setStatus("that example isn't there any more", true)
		return nil
	}
	ref = docRef{colID: c.Summary.ID, itemID: it.ID, exID: ex.ID}
	doc := reqdoc.RenderExample(it, ex)
	m.editor.open(ref, doc, versionOf(doc))
	m.state.AddRecent(ref.String())
	m.tree.Select(ex.ID)
	m.showRun = false
	m.resp.Show(exampleRecord(ref.colID, it, ex))
	m.resp.Busy = ""
	if _, busy := m.sending[ex.ID]; busy {
		m.resp.Busy = "sending " + ex.Name + "…"
	}
	if focus {
		m.focusPane(paneEditor)
	}
	m.updateMarks()
	m.refreshHints()
	return tea.Batch(m.editor.ensure(), m.ensureSchema(ref.colID, it, false))
}

// exampleRecord shows a saved example like a response.
func exampleRecord(colID string, it *postman.Item, r *postman.Response) *history.Record {
	rec := &history.Record{ID: "example-" + r.ID, CollectionID: colID, ItemID: r.ID, Name: it.Name + " · " + r.Name,
		Status: r.Code, StatusText: r.Status, Body: []byte(r.Body), Example: true, At: time.Now()}
	for _, h := range r.Header {
		if !h.Disabled {
			rec.Headers = append(rec.Headers, send.Header{Name: h.Key, Value: string(h.Value)})
		}
	}
	if rec.Status == 0 {
		rec.Status = 200
	}
	req := r.OriginalRequest
	if req == nil {
		req = it.Request
	}
	if req != nil {
		rec.Method = strings.ToUpper(orName(req.Method, "GET"))
		if req.URL != nil {
			rec.URL = req.URL.String()
		}
		for _, h := range req.Header {
			if !h.Disabled && h.Key != "" {
				rec.RequestHeaders = append(rec.RequestHeaders, send.Header{Name: h.Key, Value: string(h.Value)})
			}
		}
		if b := req.Body; b != nil && !b.Disabled {
			switch {
			case b.Mode == "raw":
				rec.RequestBody = b.Raw
			case b.Mode == "graphql" && b.GraphQL != nil:
				rec.RequestBody = string(b.GraphQL.Query)
			}
		}
	}
	return rec
}

// showingExample reports whether the response pane shows an example as it
// is saved, not a response.
func (m *Model) showingExample() bool {
	r := m.resp.Record()
	return r != nil && r.Example
}

// sendExample sends the request an example's text says.
func (m *Model) sendExample(ref docRef, text string) tea.Cmd {
	c, it, parents, ex := m.findDoc(ref)
	if ex == nil {
		m.setStatus("this example isn't there any more", true)
		return nil
	}
	got, err := reqdoc.ApplyExample(it, ex, text)
	if err != nil {
		return m.docError(err)
	}
	return m.sendItem(c, exampleItem(it, got), parents)
}

// exampleItem is what sending an example sends: its request, with the
// request's scripts, settings and (unless it has its own) auth. It goes by
// the example's id, so its response is the example's to keep.
func exampleItem(it *postman.Item, ex *postman.Response) *postman.Item {
	out := it.Clone()
	out.ID, out.UID = ex.ID, ""
	out.Name = it.Name + " · " + ex.Name
	out.Response = nil
	if ex.OriginalRequest != nil {
		req := (&postman.Item{Request: ex.OriginalRequest}).Clone().Request
		if req.Auth == nil && it.Request != nil {
			req.Auth = it.Request.Auth
		}
		out.Request = req
	}
	return out
}

// savedDoc is a document saved from its text: once the save is done, its
// draft goes (unless it changed since), and a :wq can quit.
type savedDoc struct {
	ref  docRef
	text string
}

// saveExampleText saves an example as its text says, asking first when a
// teammate changed it since the text started from it.
func (m *Model) saveExampleText(ref docRef, text string) tea.Cmd {
	return m.saveExampleAs(ref, text, false)
}

func (m *Model) saveExampleAs(ref docRef, text string, force bool) tea.Cmd {
	c, it, _, ex := m.findDoc(ref)
	if ex == nil {
		m.quitAfterSave = false
		m.setStatus("this example was deleted in Postman; your text is still in the editor", true)
		return nil
	}
	now := reqdoc.RenderExample(it, ex)
	got, err := reqdoc.ApplyExample(it, ex, text)
	if err != nil {
		m.quitAfterSave = false // :wq stays: the text needs fixing first
		m.drafts.set(ref.id(), text, m.editor.baseOf(ref.id(), versionOf(now)))
		m.updateMarks()
		return m.docError(err)
	}
	if reqdoc.RenderExample(it, got) == now {
		m.drafts.drop(ref.id())
		m.updateMarks()
		m.setStatus("no changes to save", false)
		return nil
	}
	known := m.editor.baseOf(ref.id(), "")
	if b := m.drafts.base(ref.id()); b != "" {
		known = b
	}
	if !force && known != "" && known != versionOf(now) {
		m.quitAfterSave = false
		m.drafts.set(ref.id(), text, known)
		m.updateMarks()
		return modal.Show(m.exampleConflict(ref, ex.Name, now, text))
	}
	before := ex.Clone()
	return m.changeExample(c.Summary.ID, it.ID, opSave, got, -1, exampleWork{
		busy:  "saving " + got.Name,
		note:  "saved " + got.Name + " " + m.saveTarget() + " · u in the tree undoes it",
		undo:  m.exampleRestore("save "+got.Name, c.Summary.ID, it.ID, before),
		saved: &savedDoc{ref: ref, text: text},
	})
}

// exampleConflict asks what to do when a teammate changed an example in
// Postman (theirs) after the text being saved (yours) started from it.
func (m *Model) exampleConflict(ref docRef, name, theirs, yours string) *modal.Choice {
	return &modal.Choice{
		Title: "Changed in Postman since you opened it",
		Body:  "Someone changed the example " + name + " in Postman after you started changing it.",
		Options: []modal.Option{
			{Key: "d", Label: "Show the difference", Run: func() tea.Msg {
				var lines []string
				for _, l := range textdiff.Lines(theirs, yours) {
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
				return multiMsg{modal.ShowMsg{Modal: m.exampleConflict(ref, name, theirs, yours)}, modal.ShowMsg{Modal: pager}}
			}},
			{Key: "o", Label: "Overwrite theirs", Run: func() tea.Msg {
				return cmdMsg{func() tea.Cmd { return m.saveExampleAs(ref, yours, true) }}
			}},
			{Key: "t", Label: "Take theirs", Run: func() tea.Msg {
				return cmdMsg{func() tea.Cmd {
					m.regs.Yank(yours)
					m.drafts.drop(ref.id())
					m.docChanged(ref)
					m.updateMarks()
					m.setStatus("took theirs · your version is in the \" register", false)
					return nil
				}}
			}},
			{Key: "c", Label: "Cancel", Run: nil},
		},
	}
}

// exampleOp is what changeExample does.
type exampleOp int

const (
	opMake exampleOp = iota
	opSave
	opDelete
)

// exampleWork is what goes with a change to an example: what the status
// bar says while it runs (busy) and once it is done (note), how u takes it
// back, where the tree's cursor goes, and what happens next.
type exampleWork struct {
	busy, note string
	undo       *undoEntry
	nameOnly   bool
	base       *postman.Response // version acted on, when a dialog preceded the command
	// undoMade is the undo of an example made, which needs its new id.
	undoMade func(id string) *undoEntry
	// selectID puts the tree's cursor on an item (or example); selectMade
	// puts it on the example made, and openMade opens that.
	selectID             string
	selectMade, openMade bool
	saved                *savedDoc
	dropDrafts           []string
}

// changeExample changes one of a request's examples, in Postman or in the
// collection's file: makes ex (in a file at index at of the request's
// examples, -1 for the end, where Postman always puts it), saves it over
// the one with its id, or deletes that one. The collection is fetched
// again after, so what posthero shows is what Postman has.
func (m *Model) changeExample(colID, itemID string, op exampleOp, ex *postman.Response, at int, w exampleWork) tea.Cmd {
	c, it, _ := m.findItem(colID, itemID)
	if it == nil {
		m.quitAfterSave = false
		m.setStatus("that request isn't there any more", true)
		return nil
	}
	if w.saved != nil {
		m.savesInFlight++ // until changed() hears how it went
	}
	result := func(made string) changedMsg {
		out := changedMsg{colID: colID, note: w.note, undo: w.undo, selectID: w.selectID, saved: w.saved, dropDrafts: w.dropDrafts}
		if made != "" {
			if w.undoMade != nil {
				out.undo = w.undoMade(made)
			}
			if w.selectMade {
				out.selectID = made
			}
			out.open = w.openMade
		}
		return out
	}
	ex = ex.Clone()
	if c.Path != "" || m.st.API == nil {
		made := ""
		return m.saveFile(c, func() {
			switch op {
			case opMake:
				ex.ID, ex.UID = store.NewID(), ""
				made = ex.ID
				if at < 0 || at > len(it.Response) {
					at = len(it.Response)
				}
				it.Response = append(it.Response[:at], append([]*postman.Response{ex}, it.Response[at:]...)...)
			case opSave:
				for i, r := range it.Response {
					if r.ID == ex.ID {
						it.Response[i] = ex
					}
				}
			case opDelete:
				var keep []*postman.Response
				for _, r := range it.Response {
					if r.ID != ex.ID {
						keep = append(keep, r)
					}
				}
				it.Response = keep
			}
		}, func() tea.Msg { return result(made) })
	}
	// Postman names the request and the example by the uids it gave them,
	// which have whoever made them: not always the collection's owner.
	api, uid, req := m.api, c.Summary.UID, postman.Ref(it.ID, it.UID)
	if cur := it.Example(ex.ID); cur != nil && op != opMake {
		ex.UID = cur.UID
	}
	base := w.base
	if base == nil {
		base = it.Example(ex.ID)
	}
	if base != nil {
		base = base.Clone()
	}
	return m.run(w.busy, func(ctx context.Context) tea.Msg {
		if op != opMake {
			now, err := latestCollection(ctx, api, uid)
			if err == nil {
				parent, current, _ := now.FindExample(ex.ID)
				if base == nil || current == nil || parent.ID != itemID {
					err = mutationConflict()
				} else if w.nameOnly {
					err = checkName(base.Name, ex.Name, current.Name)
				} else if !sameJSON(base, current) {
					err = mutationConflict()
				}
			}
			if err != nil {
				out := result("")
				out.err, out.undo = err, nil
				return out
			}
		}
		var made postman.Made
		var err error
		switch op {
		case opMake:
			made, err = api.CreateResponse(ctx, uid, req, ex)
		case opSave:
			if w.nameOnly {
				err = api.RenameResponse(ctx, uid, postman.Ref(ex.ID, ex.UID), ex.Name)
			} else {
				err = api.UpdateResponse(ctx, uid, ex)
			}
		case opDelete:
			err = api.DeleteResponse(ctx, uid, postman.Ref(ex.ID, ex.UID))
		}
		if err != nil {
			out := result("")
			out.err, out.undo = err, nil
			return out
		}
		out := result(made.ID)
		out.col, out.err = api.Collection(ctx, uid)
		return out
	})
}

// exampleRestore is the undo of a change to an example: it saves the
// example as it was.
func (m *Model) exampleRestore(what, colID, itemID string, was *postman.Response) *undoEntry {
	return &undoEntry{what: what, kind: undoRestore, restore: func() tea.Cmd {
		if _, it, _ := m.findItem(colID, itemID); it == nil || it.Example(was.ID) == nil {
			m.setStatus("that example isn't there any more", true)
			return nil
		}
		return m.changeExample(colID, itemID, opSave, was, -1, exampleWork{busy: "restoring " + was.Name, note: "took back: " + what, selectID: was.ID})
	}}
}

// exampleTakeBack is the undo of making an example: it deletes it again.
func (m *Model) exampleTakeBack(what, colID, itemID, id string) *undoEntry {
	return &undoEntry{what: what, kind: undoRestore, restore: func() tea.Cmd {
		_, it, _ := m.findItem(colID, itemID)
		if it == nil || it.Example(id) == nil {
			m.setStatus("that example isn't there any more", true)
			return nil
		}
		return m.removeExample(docRef{colID: colID, itemID: itemID, exID: id}, false)
	}}
}

// renameExample gives an example another name; record says u can take it
// back.
func (m *Model) renameExample(ref docRef, name string, record bool) tea.Cmd {
	return m.renameExampleExpected(ref, name, record, nil)
}

func (m *Model) renameExampleExpected(ref docRef, name string, record bool, base *postman.Response) tea.Cmd {
	_, _, _, ex := m.findDoc(ref)
	if ex == nil {
		return nil
	}
	renamed := ex.Clone()
	renamed.Name = name
	w := exampleWork{base: base, busy: "renaming", note: "renamed to " + name, selectID: ex.ID, nameOnly: true}
	if old := ex.Name; record {
		w.undo = &undoEntry{what: "rename " + old, kind: undoRestore, restore: func() tea.Cmd {
			return m.renameExample(ref, old, false)
		}}
	}
	return m.changeExample(ref.colID, ref.itemID, opSave, renamed, -1, w)
}

// deleteExample asks, then deletes an example.
func (m *Model) deleteExample(ref docRef) tea.Cmd {
	_, _, _, ex := m.findDoc(ref)
	if ex == nil {
		return nil
	}
	return modal.Show(&modal.Confirm{
		Title: "Delete the example " + ex.Name + "?",
		Body:  "u brings it back.",
		Yes: func() tea.Msg {
			return cmdMsg{func() tea.Cmd { return m.removeExampleExpected(ref, true, ex.Clone()) }}
		},
	})
}

// removeExample deletes an example; record says u can bring it back.
func (m *Model) removeExample(ref docRef, record bool) tea.Cmd {
	return m.removeExampleExpected(ref, record, nil)
}

func (m *Model) removeExampleExpected(ref docRef, record bool, base *postman.Response) tea.Cmd {
	_, it, _, ex := m.findDoc(ref)
	if ex == nil {
		return nil
	}
	at := 0
	for i, r := range it.Response {
		if r.ID == ex.ID {
			at = i
		}
	}
	w := exampleWork{base: base, busy: "deleting " + ex.Name, note: "took back: make " + ex.Name, selectID: it.ID, dropDrafts: []string{ex.ID}}
	if record {
		was := ex.Clone()
		w.note = "deleted " + ex.Name + " · u brings it back"
		w.undo = &undoEntry{what: "delete " + ex.Name, kind: undoRestore, restore: func() tea.Cmd {
			return m.changeExample(ref.colID, ref.itemID, opMake, was, at, exampleWork{busy: "bringing back " + was.Name, note: "brought back " + was.Name, selectMade: true})
		}}
	}
	if m.open != nil && m.open.exID == ex.ID {
		// The editor goes to its request.
		cmd := m.openItem(ref.colID, ref.itemID, false)
		return tea.Batch(cmd, m.changeExample(ref.colID, ref.itemID, opDelete, ex, -1, w))
	}
	return m.changeExample(ref.colID, ref.itemID, opDelete, ex, -1, w)
}

// copyExample puts a copy of an example in a request's examples: as "NAME
// Copy" next to it, under its own name under another request. u takes the
// copy back.
func (m *Model) copyExample(from docRef, toCol, toItem string) tea.Cmd {
	_, _, _, ex := m.findDoc(from)
	_, to, _ := m.findItem(toCol, toItem)
	if ex == nil || to == nil {
		m.setStatus("that example isn't there any more", true)
		return nil
	}
	cp := ex.Clone()
	if from.colID == toCol && from.itemID == toItem {
		cp.Name += " Copy"
	}
	return m.changeExample(toCol, toItem, opMake, cp, -1, exampleWork{
		busy: "copying " + ex.Name,
		note: "copied " + ex.Name + " to " + to.Name,
		undoMade: func(id string) *undoEntry {
			return m.exampleTakeBack("copy "+ex.Name, toCol, toItem, id)
		},
		selectMade: true,
	})
}

// moveExample moves an example to another request's examples (at the
// end): a copy there, then the example here goes. u moves it back.
func (m *Model) moveExample(from docRef, toCol, toItem string, record bool) tea.Cmd {
	return m.moveExampleExpected(from, toCol, toItem, record, nil)
}

func (m *Model) moveExampleExpected(from docRef, toCol, toItem string, record bool, base *postman.Response) tea.Cmd {
	src, _, _, ex := m.findDoc(from)
	dst, to, _ := m.findItem(toCol, toItem)
	if ex == nil || to == nil {
		m.setStatus("that example isn't there any more", true)
		return nil
	}
	if from.colID == toCol && from.itemID == toItem {
		m.setStatus(ex.Name+" is one of "+to.Name+"'s examples already", false)
		return nil
	}
	if src.Path != "" || dst.Path != "" || m.st.API == nil {
		if src != dst {
			m.setStatus("moving between files isn't possible; copy the text instead", true)
			return nil
		}
	}
	moved := ex.Clone()
	_, back, _ := m.findItem(from.colID, from.itemID)
	note := "moved " + ex.Name + " to " + to.Name
	if !record {
		note = "moved " + ex.Name + " back"
	}
	undoMade := func(id string) *undoEntry {
		if !record {
			return nil
		}
		return &undoEntry{what: "move " + ex.Name, kind: undoRestore, restore: func() tea.Cmd {
			return m.moveExample(docRef{colID: toCol, itemID: toItem, exID: id}, from.colID, back.ID, false)
		}}
	}
	if src.Path != "" || m.st.API == nil {
		// One file: both changes in it at once.
		made := ""
		return m.saveFile(src, func() {
			cp := moved.Clone()
			cp.ID = store.NewID()
			made = cp.ID
			to.Response = append(to.Response, cp)
			var keep []*postman.Response
			for _, r := range back.Response {
				if r.ID != ex.ID {
					keep = append(keep, r)
				}
			}
			back.Response = keep
		}, func() tea.Msg {
			return changedMsg{colID: toCol, selectID: made, undo: undoMade(made), note: note, dropDrafts: []string{ex.ID}}
		})
	}
	api := m.api
	srcUID, dstUID := src.Summary.UID, dst.Summary.UID
	toRef, fromRef := postman.Ref(to.ID, to.UID), postman.Ref(ex.ID, ex.UID)
	before, destination := ex.Clone(), mutationSnapshot(dst)
	if base != nil {
		before = base.Clone()
	}
	return m.run("moving "+ex.Name, func(ctx context.Context) tea.Msg {
		checkSource := func() error {
			now, err := latestCollection(ctx, api, srcUID)
			if err != nil {
				return err
			}
			parent, current, _ := now.FindExample(before.ID)
			if current == nil || parent.ID != from.itemID || !sameJSON(before, current) {
				return mutationConflict()
			}
			return nil
		}
		if err := checkSource(); err != nil {
			return changedMsg{err: err}
		}
		now, err := latestCollection(ctx, api, dstUID)
		if err != nil {
			return changedMsg{err: err}
		}
		if err := checkItem(destination.Data, now, to.ID); err != nil {
			return changedMsg{err: err}
		}
		made, err := api.CreateResponse(ctx, dstUID, toRef, moved)
		if err != nil {
			return changedMsg{err: fmt.Errorf("couldn't move %s: %w", moved.Name, err)}
		}
		id := made.ID
		// Moving an example is two writes. Check again before deleting the
		// source, so an edit during the copy survives there.
		if err := checkSource(); err != nil {
			return changedMsg{err: fmt.Errorf("example copied, but the original was kept: %w", err)}
		}
		if err := api.DeleteResponse(ctx, srcUID, fromRef); err != nil {
			// It is in both places now: a copy, then.
			col, _ := api.Collection(ctx, dstUID)
			return changedMsg{colID: toCol, col: col, err: fmt.Errorf("copied %s, but couldn't take it away from where it was: %w", moved.Name, err)}
		}
		col, err := api.Collection(ctx, dstUID)
		msg := changedMsg{colID: toCol, col: col, err: err, selectID: id, undo: undoMade(id), note: note, dropDrafts: []string{from.exID}}
		if srcUID != dstUID && err == nil {
			if other, err := api.Collection(ctx, srcUID); err == nil {
				return multiMsg{changedMsg{colID: from.colID, col: other}, msg}
			}
		}
		return msg
	})
}

// putExample puts the example cut (dd) or copied (yy) under the request
// on the node, or the request whose example the node is.
func (m *Model) putExample(n *tree.Node) tea.Cmd {
	to := requestOf(n)
	if to == nil {
		m.setStatus("an example goes under a request: p on a request (or one of its examples) puts it there", true)
		return nil
	}
	cut := *m.cut
	from := docRef{colID: cut.colID, itemID: cut.example, exID: cut.ids[0]}
	if _, _, _, ex := m.findDoc(from); ex == nil {
		m.cut = nil
		m.updateMarks()
		m.setStatus("that example isn't there any more", true)
		return nil
	}
	toCol := n.Col.Summary.ID
	if !cut.copy && from.colID == toCol && from.itemID == to.ID {
		m.setStatus("it's one of "+to.Name+"'s examples already; p on another request moves it there", false)
		return nil
	}
	m.cut = nil
	m.updateMarks()
	if cut.copy {
		return m.copyExample(from, toCol, to.ID)
	}
	var base *postman.Response
	if cut.base != nil {
		_, base, _ = cut.base.Data.FindExample(from.exID)
	}
	return m.moveExampleExpected(from, toCol, to.ID, true, base)
}

// lookupExample finds an example by its id, a Postman link to it, or its
// name (alone, or after its request's: "Get product / A product"); loose
// also takes the first whose name has s in it.
func (m *Model) lookupExample(s string, loose bool) (docRef, bool) {
	s = strings.TrimSpace(s)
	if i := strings.LastIndex(s, "/example/"); i >= 0 {
		s = s[i+len("/example/"):]
	}
	if m.st == nil || s == "" {
		return docRef{}, false
	}
	low := strings.ToLower(s)
	var named, partly *docRef
	for _, c := range m.st.Collections() {
		if c.Data == nil {
			continue
		}
		for _, id := range []string{s, s[strings.Index(s, "-")+1:]} {
			if req, ex, _ := c.Data.FindExample(id); ex != nil {
				return docRef{colID: c.Summary.ID, itemID: req.ID, exID: ex.ID}, true
			}
		}
		for _, loc := range c.Data.Requests() {
			for _, r := range loc.Item.Response {
				ref := docRef{colID: c.Summary.ID, itemID: loc.Item.ID, exID: r.ID}
				name := strings.ToLower(r.Name)
				switch {
				case named == nil && (name == low || strings.ToLower(loc.Item.Name+" / "+r.Name) == low || strings.ToLower(loc.Path()+" / "+r.Name) == low):
					named = &ref
				case partly == nil && loose && strings.Contains(name, low):
					partly = &ref
				}
			}
		}
	}
	switch {
	case named != nil:
		return *named, true
	case partly != nil:
		return *partly, true
	}
	return docRef{}, false
}

// requestOf is the request a tree node is or is in, for things that go
// under a request: an example's, or the request itself.
func requestOf(n *tree.Node) *postman.Item {
	if n != nil && n.Leaf() {
		return n.Item
	}
	return nil
}

// exampleTarget is the request :example works on: the one the response
// shown came from, else the one under the cursor (or open).
func (m *Model) exampleTarget() (docRef, *history.Record, bool) {
	if r := m.resp.Record(); r != nil && !r.Example && r.Status > 0 && r.ItemID != "" {
		if ref, ok := m.refOf(r.CollectionID, r.ItemID); ok {
			return ref, r, true
		}
	}
	n := m.treeNode()
	if it := requestOf(n); it != nil {
		ref := docRef{colID: n.Col.Summary.ID, itemID: it.ID}
		if n.Example != nil {
			ref.exID = n.Example.ID
		}
		return ref, nil, true
	}
	return docRef{}, nil, false
}

// saveExample saves the response shown as an example of the request it
// came from (or of the example's request it came from): the request as it
// was sent, and what came back. With no response shown, the example is a
// blank one of the request, to fill in: it opens in the editor.
func (m *Model) saveExample(name string) tea.Cmd {
	ref, rec, ok := m.exampleTarget()
	if !ok {
		m.setStatus("pick a request first; :example saves its response as an example", true)
		return nil
	}
	_, it, _, ex := m.findDoc(ref)
	if it == nil {
		m.setStatus("the request isn't there any more", true)
		return nil
	}
	// The request as it was sent: what the document says now.
	req := it.Request
	if text, ok := m.docText(ref); ok {
		if ex != nil {
			if got, err := reqdoc.ApplyExample(it, ex, text); err == nil {
				req = got.OriginalRequest
				if req == nil {
					req = it.Request
				}
			}
		} else if got, err := reqdoc.Apply(it, text); err == nil {
			req = got.Request
		}
	}
	made := &postman.Response{Name: name, OriginalRequest: (&postman.Item{Request: req}).Clone().Request}
	if rec != nil {
		fillResponse(made, rec)
		if made.Name == "" {
			made.Name = fmt.Sprintf("%d %s", rec.Status, rec.StatusText)
		}
	} else {
		made.Code, made.Status = 200, "OK"
		if made.Name == "" {
			made.Name = "New example"
		}
	}
	what := "save the example " + made.Name
	note := "saved " + made.Name + " as an example of " + it.Name + " " + m.saveTarget()
	if rec == nil {
		what = "make the example " + made.Name
		note = "made " + made.Name + ", an example of " + it.Name + " · fill in what comes back, then :w"
	}
	return m.changeExample(ref.colID, it.ID, opMake, made, -1, exampleWork{
		busy: "saving the example",
		note: note,
		undoMade: func(id string) *undoEntry {
			return m.exampleTakeBack(what, ref.colID, it.ID, id)
		},
		selectMade: true,
		openMade:   rec == nil,
	})
}

// fillResponse makes an example's response what came back in a record.
func fillResponse(ex *postman.Response, r *history.Record) {
	ex.Code, ex.Status, ex.Body = r.Status, r.StatusText, string(r.Body)
	ex.Header = nil
	for _, h := range r.Headers {
		ex.Header = append(ex.Header, postman.Param{Key: h.Name, Value: postman.Str(h.Value)})
	}
	ex.PreviewLanguage = ""
	switch ct := strings.ToLower(r.ContentType()); {
	case strings.Contains(ct, "json"):
		ex.PreviewLanguage = "json"
	case strings.Contains(ct, "html"):
		ex.PreviewLanguage = "html"
	case strings.Contains(ct, "xml"):
		ex.PreviewLanguage = "xml"
	case ct != "":
		ex.PreviewLanguage = "text"
	}
}

// updateExample (:example!) keeps the response shown in the open example:
// what came back when it (or its request) was sent replaces what it said
// comes back. Its request stays as its text says.
func (m *Model) updateExample() tea.Cmd {
	n := m.treeNode()
	if n == nil || n.Example == nil {
		m.setStatus("open an example first; :example! keeps the response shown in it", true)
		return nil
	}
	ref := docRef{colID: n.Col.Summary.ID, itemID: n.Item.ID, exID: n.Example.ID}
	r := m.resp.Record()
	if r == nil || r.Example || r.Status == 0 || r.ItemID != ref.exID && r.ItemID != ref.itemID {
		m.setStatus("send the example first (⏎); :example! keeps what came back in it", true)
		return nil
	}
	c, it, _, ex := m.findDoc(ref)
	if ex == nil {
		return nil
	}
	got := ex.Clone()
	if text, ok := m.docText(ref); ok {
		if applied, err := reqdoc.ApplyExample(it, ex, text); err == nil {
			got = applied
		} else {
			return m.docError(err)
		}
	}
	fillResponse(got, r)
	text := strings.TrimSuffix(reqdoc.RenderExample(it, got), "\n")
	before := ex.Clone()
	return m.changeExample(c.Summary.ID, it.ID, opSave, got, -1, exampleWork{
		busy:  "saving " + got.Name,
		note:  fmt.Sprintf("%s now says %d %s, as it came back · saved %s · u in the tree undoes it", got.Name, got.Code, got.Status, m.saveTarget()),
		undo:  m.exampleRestore("keep the response in "+got.Name, c.Summary.ID, it.ID, before),
		saved: &savedDoc{ref: ref, text: text},
	})
}

// examples lists the examples of the request under the cursor (or open),
// to open one.
func (m *Model) examples() tea.Cmd {
	n := m.treeNode()
	it := requestOf(n)
	if it == nil {
		m.setStatus("pick a request first", true)
		return nil
	}
	if len(it.Response) == 0 {
		m.setStatus("no examples saved for "+it.Name+" · :example NAME saves the response shown as one", false)
		return nil
	}
	colID := n.Col.Summary.ID
	var items []picker.Item
	dim := theme.Dim
	for _, r := range it.Response {
		items = append(items, picker.Item{
			Label: tree.ExampleBadge + " " + r.Name, Detail: strings.TrimSpace(fmt.Sprintf("%d %s", r.Code, r.Status)), Value: r.ID, Style: &dim,
			Current: m.open != nil && m.open.exID == r.ID,
		})
	}
	p := &picker.Picker{Title: "Examples of " + it.Name, Items: items, Placeholder: "example", Pick: func(pi picker.Item) tea.Cmd {
		ref := docRef{colID: colID, itemID: it.ID, exID: pi.Value.(string)}
		return func() tea.Msg { return cmdMsg{func() tea.Cmd { return m.openExample(ref, true) }} }
	}}
	return p.Open()
}

// peekExample says what an example holds.
func (m *Model) peekExample(n *tree.Node) tea.Cmd {
	ex := n.Example
	var b strings.Builder
	fmt.Fprintf(&b, "an example of %s\n", n.Item.Name)
	req := ex.OriginalRequest
	if req == nil {
		req = n.Item.Request
	}
	if req != nil {
		u := ""
		if req.URL != nil {
			u = req.URL.String()
		}
		fmt.Fprintf(&b, "\n%s %s\n", strings.ToUpper(orName(req.Method, "GET")), u)
	}
	fmt.Fprintf(&b, "\nsays %s\n", orName(strings.TrimSpace(fmt.Sprintf("%d %s", ex.Code, ex.Status)), "nothing about its status"))
	if ct, ok := ex.Header.Get("Content-Type"); ok {
		b.WriteString("content type " + ct + "\n")
	}
	if ex.Body != "" {
		b.WriteString(send.Size(int64(len(ex.Body))) + " of body\n")
	}
	if r := m.lastRecord(ex.ID); r != nil {
		b.WriteString("\nlast sent " + r.At.Local().Format("2 Jan 15:04") + ": " + summary(r) + "\n")
	}
	return showText(n.Name(), strings.TrimRight(b.String(), "\n"))
}
