package app

import (
	"os"
	"path/filepath"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/shinyvision/posthero/internal/editor"
	"github.com/shinyvision/posthero/internal/editor/launch"
	"github.com/shinyvision/posthero/internal/editor/runtime"
	"github.com/shinyvision/posthero/internal/mouse"
	"github.com/shinyvision/posthero/internal/reqdoc"
	"github.com/shinyvision/posthero/internal/ui/theme"
)

// docRef names a document the editor holds: a request, or one of its
// saved examples.
type docRef struct {
	colID, itemID string
	// exID is the example's id; "" is the request itself.
	exID string
}

// id is what the document is kept by: drafts, marks, sends and the
// history go by the request's id, or the example's.
func (r docRef) id() string {
	if r.exID != "" {
		return r.exID
	}
	return r.itemID
}

// String is how the state file remembers the document.
func (r docRef) String() string {
	s := r.colID + "/" + r.itemID
	if r.exID != "" {
		s += "/" + r.exID
	}
	return s
}

// parseRef reads a docRef's String.
func parseRef(s string) docRef {
	col, rest, _ := strings.Cut(s, "/")
	item, ex, _ := strings.Cut(rest, "/")
	return docRef{colID: col, itemID: item, exID: ex}
}

// openRequest is the document in the editor: a request or an example.
type openRequest struct {
	docRef
	file string
}

// editorPane is the request pane: the user's editor, one buffer per
// request (and per example).
type editorPane struct {
	m        *Model
	backend  editor.Backend
	fallback string
	dir      string
	runtime  string
	w, h     int
	// files maps buffer files to their documents.
	files map[string]docRef
	// loaded are the files the editor has opened.
	loaded map[string]bool
	// texts are the buffers' texts as last reported.
	texts map[string]string
	// bases are the versions (updatedAt) of the requests in Postman the
	// buffers were loaded from, by document id.
	bases map[string]string
	// started says the backend was started (or tried).
	started bool
	failed  string
	// skip are editors that stopped while starting: not tried again.
	skip []string
}

func newEditorPane(m *Model) *editorPane {
	return &editorPane{m: m, files: map[string]docRef{}, loaded: map[string]bool{}, texts: map[string]string{}, bases: map[string]string{}}
}

// editorEvent is something the editor did.
type editorEvent struct {
	b  editor.Backend
	ev editor.Event
}

// ensure starts the editor once the screen has a size.
func (p *editorPane) ensure() tea.Cmd {
	if p.started || p.m.w == 0 {
		return nil
	}
	r := p.m.layout().editor
	if !r.visible() {
		r = rect{w: max(40, p.m.w/2), h: max(10, p.m.h/2)}
	}
	return p.start(max(10, r.w-2), max(3, r.h-2))
}

func (p *editorPane) start(w, h int) tea.Cmd {
	p.started = true
	if p.dir == "" {
		dir, err := os.MkdirTemp("", "posthero-")
		if err != nil {
			p.failed = err.Error()
			return nil
		}
		p.dir = dir
		if rt, err := runtime.Write(filepath.Join(dir, "runtime")); err == nil {
			p.runtime = rt
		}
	}
	first := filepath.Join(p.dir, "posthero.posthero")
	if p.m.open != nil {
		first = p.m.open.file
	} else if _, err := os.Stat(first); err != nil {
		_ = os.WriteFile(first, []byte(welcome), 0o600)
	}
	p.w, p.h = w, h
	b, note := launch.Start(editor.Options{File: first, Main: true, Runtime: p.runtime, Complete: p.m.complete}, p.m.regs, w, h, p.skip...)
	if b == nil {
		p.failed = "no editor could start"
		return nil
	}
	p.backend, p.fallback = b, note
	p.loaded[first] = true
	if p.m.open != nil {
		// Reopened after a restart: back to the request, with its draft.
		if d, ok := p.m.drafts.get(p.m.open.id()); ok {
			b.Open(p.m.open.file, &d)
		}
		p.m.refreshHints()
	}
	return p.wait()
}

// welcome is what the editor shows before a request is open.
const welcome = `# posthero
#
# Pick a request in the tree on the left (⏎ opens it), or find one with
# ctrl+p. ⏎ here in normal mode sends the request; :w saves it to Postman.
# ? shows every key.
`

func (p *editorPane) wait() tea.Cmd {
	b := p.backend
	ch := b.Events()
	return func() tea.Msg { return editorEvent{b: b, ev: <-ch} }
}

func (p *editorPane) close() {
	if p.backend != nil {
		p.backend.Close()
	}
	if p.dir != "" {
		_ = os.RemoveAll(p.dir)
	}
}

// fileFor is the buffer file of a document.
func (p *editorPane) fileFor(ref docRef) string {
	name := strings.Map(func(r rune) rune {
		if r == '/' || r == '\\' || r == ':' || r == ' ' {
			return '_'
		}
		return r
	}, ref.id())
	f := filepath.Join(p.dir, name+reqdoc.Ext)
	p.files[f] = ref
	return f
}

// owner is the document of a buffer file.
func (p *editorPane) owner(file string) (docRef, bool) {
	ref, ok := p.files[file]
	if !ok {
		// Neovim may give the file another spelling (a resolved link).
		for f, r := range p.files {
			if sameFile(f, file) {
				ref, ok = r, true
				break
			}
		}
	}
	return ref, ok
}

// writeIfChanged writes a buffer's file, unless it already holds the text:
// an editor sees a rewritten file as changed on disk.
func writeIfChanged(file, text string) {
	if have, err := os.ReadFile(file); err == nil && string(have) == text {
		return
	}
	_ = os.WriteFile(file, []byte(text), 0o600)
}

func sameFile(a, b string) bool {
	fa, err1 := os.Stat(a)
	fb, err2 := os.Stat(b)
	return err1 == nil && err2 == nil && os.SameFile(fa, fb)
}

// open shows a document in the editor: what Postman has (doc, at version
// base), or its draft.
func (p *editorPane) open(ref docRef, doc, base string) {
	if p.dir == "" {
		dir, err := os.MkdirTemp("", "posthero-")
		if err == nil {
			p.dir = dir
		}
	}
	file := p.fileFor(ref)
	var draft *string
	if d, ok := p.m.drafts.get(ref.id()); ok && d != strings.TrimSuffix(doc, "\n") {
		draft = &d
	}
	// Without a draft, the buffer shows the request as Postman has it now,
	// also when it was loaded before and a teammate changed it since.
	stale := false
	if draft == nil {
		stale = p.loaded[file] && p.texts[file] != "" && p.texts[file] != strings.TrimSuffix(doc, "\n")
		writeIfChanged(file, doc)
		p.bases[ref.id()] = base
	}
	p.m.open = &openRequest{docRef: ref, file: file}
	if p.backend != nil {
		p.backend.Open(file, draft)
		if stale {
			p.backend.Reload(file)
			delete(p.texts, file)
		}
		p.loaded[file] = true
	}
}

// baseOf is the version of a document its buffer was loaded from, or
// else now (Postman's version now).
func (p *editorPane) baseOf(id, now string) string {
	if b := p.bases[id]; b != "" {
		return b
	}
	return now
}

// changedOnServer updates a document's buffer after Postman changed it (to
// doc, at version base): a buffer without changes reads the new text.
func (p *editorPane) changedOnServer(ref docRef, doc, base string) {
	file := p.fileFor(ref)
	if !p.loaded[file] {
		return
	}
	if _, draft := p.m.drafts.get(ref.id()); draft {
		return
	}
	writeIfChanged(file, doc)
	p.bases[ref.id()] = base
	delete(p.texts, file)
	if p.backend != nil {
		p.backend.Reload(file)
	}
}

// text is the current text of a document: its draft, its buffer's as last
// reported, or what Postman has (doc).
func (p *editorPane) text(ref docRef, doc string) string {
	if d, ok := p.m.drafts.get(ref.id()); ok {
		return d
	}
	if t, ok := p.texts[p.fileFor(ref)]; ok {
		return t
	}
	return strings.TrimSuffix(doc, "\n")
}

// event handles what the editor did.
func (p *editorPane) event(msg editorEvent) tea.Cmd {
	if msg.b != p.backend {
		return nil // from an editor that is gone
	}
	m := p.m
	switch ev := msg.ev.(type) {
	case editor.Exited:
		// :q in the request pane closes nothing; the editor comes back.
		// One that couldn't start gives way to the next.
		if ev.Early {
			p.skip = append(p.skip, p.backend.Name())
			p.fallback = strings.TrimPrefix(p.fallback+"; "+p.backend.Name()+": "+errText(ev.Err), "; ")
		}
		p.backend.Close()
		p.backend = nil
		p.started = false
		p.loaded = map[string]bool{}
		cmd := p.ensure()
		if m.open != nil && p.backend != nil {
			if d, ok := m.drafts.get(m.open.id()); ok {
				p.backend.Open(m.open.file, &d)
			} else {
				p.backend.Open(m.open.file, nil)
			}
			p.loaded[m.open.file] = true
		}
		if ev.Early {
			m.setStatus("the editor couldn't start: "+errText(ev.Err), true)
		} else {
			m.setStatus("the request pane stays open · quit posthero with :q from the tree or response pane", false)
		}
		return cmd
	case editor.Changed:
		cmd := p.wait()
		p.texts[ev.File] = ev.Text
		ref, ok := p.owner(ev.File)
		if !ok {
			return cmd
		}
		m.trackDraft(ref, ev.Text)
		m.refreshHints()
		return cmd
	case editor.Written:
		cmd := p.wait()
		p.texts[ev.File] = ev.Text
		ref, ok := p.owner(ev.File)
		if !ok {
			return cmd
		}
		return tea.Batch(cmd, m.saveDoc(ref, ev.Text))
	case editor.Send:
		cmd := p.wait()
		text := strings.Join(ev.Lines, "\n")
		p.texts[ev.File] = text
		ref, ok := p.owner(ev.File)
		if !ok {
			m.setStatus("open a request first (⏎ in the tree, or ctrl+p)", true)
			return cmd
		}
		m.trackDraft(ref, text)
		return tea.Batch(cmd, m.sendDoc(ref, text))
	case editor.Hover:
		cmd := p.wait()
		ref, ok := p.owner(ev.File)
		if !ok {
			return cmd
		}
		return tea.Batch(cmd, m.hover(ref.colID, ev))
	case editor.WinCmd:
		cmd := p.wait()
		return tea.Batch(cmd, m.wincmd(ev.Key))
	}
	return p.wait()
}

// Key forwards a key press to the editor.
func (p *editorPane) key(k tea.KeyPressMsg) {
	if p.backend != nil {
		p.backend.Key(k)
	}
}

func (p *editorPane) paste(s string) {
	if p.backend != nil {
		p.backend.Paste(s)
	}
}

func (p *editorPane) mouse(ev mouse.Event) {
	if p.backend == nil {
		return
	}
	if ev.Action == mouse.Press && !ev.In(p.w, p.h) {
		return
	}
	p.backend.Mouse(ev.Clamp(p.w, p.h))
}

// mode is the editor's mode for the status bar.
func (p *editorPane) mode() string {
	if p.backend == nil {
		return ""
	}
	return p.backend.Mode()
}

// cursor is the editor's cursor relative to its content.
func (p *editorPane) cursor() (int, int, tea.CursorShape, bool) {
	if p.backend == nil {
		return 0, 0, 0, false
	}
	return p.backend.Cursor()
}

// view renders the pane as a box of w×h cells.
func (p *editorPane) view(w, h int, focused bool, title, footer string) string {
	iw, ih := w-2, h-2
	if p.backend != nil && (iw != p.w || ih != p.h) && iw > 0 && ih > 0 {
		p.w, p.h = iw, ih
		p.backend.Resize(iw, ih)
	}
	var lines []string
	switch {
	case p.backend != nil:
		lines = p.backend.Render()
	case p.failed != "":
		lines = []string{theme.Error.Render(" the editor didn't start: " + p.failed)}
	default:
		lines = []string{theme.Dim.Render(" starting your editor…")}
	}
	if footer == "" && p.backend != nil {
		footer = p.backend.Name()
		if p.backend.Name() == "builtin" && p.fallback != "" {
			footer += " (" + p.fallback + ")"
		}
	}
	return theme.Box(title, footer, lines, w, h, focused)
}

// errText is an error's message, or nothing.
func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
