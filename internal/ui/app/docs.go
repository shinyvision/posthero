package app

import (
	"fmt"
	"hash/fnv"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/shinyvision/posthero/internal/postman"
	"github.com/shinyvision/posthero/internal/reqdoc"
	"github.com/shinyvision/posthero/internal/store"
	"github.com/shinyvision/posthero/internal/ui/tree"
)

// A document is what the editor holds: a request, or one of its saved
// examples. These find, write and change either.

// findDoc finds a document: its collection, its request and the folders
// that is in, and for an example the example. The request is nil when it
// is gone, and so is the example.
func (m *Model) findDoc(ref docRef) (*store.Col, *postman.Item, []*postman.Item, *postman.Response) {
	c, it, parents := m.findItem(ref.colID, ref.itemID)
	if it == nil || ref.exID == "" {
		return c, it, parents, nil
	}
	return c, it, parents, it.Example(ref.exID)
}

// refOf names what an id is in a collection: a request, or an example (of
// its request). ok is false when it is neither.
func (m *Model) refOf(colID, id string) (docRef, bool) {
	c, it, _ := m.findItem(colID, id)
	switch {
	case it != nil:
		return docRef{colID: colID, itemID: it.ID}, true
	case c == nil || c.Data == nil:
		return docRef{}, false
	}
	if req, ex, _ := c.Data.FindExample(id); ex != nil {
		return docRef{colID: colID, itemID: req.ID, exID: ex.ID}, true
	}
	return docRef{}, false
}

// renderDoc writes a document as Postman has it now, and its version: a
// request's updatedAt, an example's content (Postman doesn't say when one
// changed); ok is false when the document is gone.
func (m *Model) renderDoc(ref docRef) (doc, base string, ok bool) {
	_, it, _, ex := m.findDoc(ref)
	switch {
	case it == nil:
		return "", "", false
	case ref.exID == "":
		return reqdoc.Render(it), it.UpdatedAt, true
	case ex == nil:
		return "", "", false
	}
	doc = reqdoc.RenderExample(it, ex)
	return doc, versionOf(doc), true
}

// versionOf is a version for a text: a hash of it.
func versionOf(text string) string {
	h := fnv.New64a()
	h.Write([]byte(text))
	return fmt.Sprintf("v%x", h.Sum64())
}

// docText is a document's text now: its draft, what its buffer has, or what
// Postman has. ok is false when the document is gone.
func (m *Model) docText(ref docRef) (string, bool) {
	doc, _, ok := m.renderDoc(ref)
	if !ok {
		return "", false
	}
	return m.editor.text(ref, doc), true
}

// nodeRef is the document a tree node (a request or an example) is.
func nodeRef(n *tree.Node) docRef {
	ref := docRef{colID: n.Col.Summary.ID, itemID: n.Item.ID}
	if n.Example != nil {
		ref.exID = n.Example.ID
	}
	return ref
}

// docItem is the request a document sends, as its text says now, and the
// folders it is in.
func (m *Model) docItem(ref docRef) (*postman.Item, []*postman.Item, error) {
	_, it, parents, ex := m.findDoc(ref)
	text, ok := m.docText(ref)
	if !ok {
		return nil, nil, fmt.Errorf("that request isn't there any more")
	}
	if ex == nil {
		item, err := reqdoc.Apply(it, text)
		return item, parents, err
	}
	got, err := reqdoc.ApplyExample(it, ex, text)
	if err != nil {
		return nil, nil, err
	}
	return exampleItem(it, got), parents, nil
}

// openDoc opens a document in the editor; focus moves the keyboard there.
func (m *Model) openDoc(ref docRef, focus bool) tea.Cmd {
	if ref.exID == "" {
		return m.openItem(ref.colID, ref.itemID, focus)
	}
	return m.openExample(ref, focus)
}

// saveDoc saves a document's text to Postman.
func (m *Model) saveDoc(ref docRef, text string) tea.Cmd {
	if ref.exID == "" {
		return m.saveRequest(ref.colID, ref.itemID, text)
	}
	return m.saveExampleText(ref, text)
}

// sendDoc sends what a document's text says.
func (m *Model) sendDoc(ref docRef, text string) tea.Cmd {
	if ref.exID == "" {
		return m.sendText(ref.colID, ref.itemID, text)
	}
	return m.sendExample(ref, text)
}

// docChanged shows a document as Postman has it now in its buffer, unless
// the buffer has changes of its own.
func (m *Model) docChanged(ref docRef) {
	if doc, base, ok := m.renderDoc(ref); ok {
		m.editor.changedOnServer(ref, doc, base)
	}
}

// docName is a document's name: the request's, or the request's and the
// example's.
func (m *Model) docName(ref docRef) string {
	_, it, _, ex := m.findDoc(ref)
	switch {
	case it == nil:
		return ""
	case ex != nil:
		return it.Name + " · " + ex.Name
	}
	return it.Name
}

// trackDraft keeps text as a document's draft when it differs from what
// Postman has, and drops the draft when it is the same again.
func (m *Model) trackDraft(ref docRef, text string) {
	doc, base, ok := m.renderDoc(ref)
	if !ok {
		return
	}
	if strings.TrimRight(text, "\n") == strings.TrimSuffix(doc, "\n") {
		m.drafts.drop(ref.id())
	} else {
		m.drafts.set(ref.id(), text, m.editor.baseOf(ref.id(), base))
	}
	m.updateMarks()
}
