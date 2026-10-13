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
	"github.com/shinyvision/posthero/internal/postman"
	"github.com/shinyvision/posthero/internal/reqdoc"
	"github.com/shinyvision/posthero/internal/ui/theme"
)

// openRequest is the request in the editor.
type openRequest struct {
	colID, itemID string
	file          string
}

// editorPane is the request pane: the user's editor, one buffer per
// request.
type editorPane struct {
	m        *Model
	backend  editor.Backend
	fallback string
	dir      string
	runtime  string
	w, h     int
	// files maps buffer files to "collection/item".
	files map[string]string
	// loaded are the files the editor has opened.
	loaded map[string]bool
	// texts are the buffers' texts as last reported.
	texts map[string]string
	// bases are the versions (updatedAt) of the requests in Postman the
	// buffers were loaded from, by item id.
	bases map[string]string
	// started says the backend was started (or tried).
	started bool
	failed  string
	// skip are editors that stopped while starting: not tried again.
	skip []string
}

func newEditorPane(m *Model) *editorPane {
	return &editorPane{m: m, files: map[string]string{}, loaded: map[string]bool{}, texts: map[string]string{}, bases: map[string]string{}}
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
		if d, ok := p.m.drafts.get(p.m.open.itemID); ok {
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

// fileFor is the buffer file of a request.
func (p *editorPane) fileFor(colID, itemID string) string {
	name := strings.Map(func(r rune) rune {
		if r == '/' || r == '\\' || r == ':' || r == ' ' {
			return '_'
		}
		return r
	}, itemID)
	f := filepath.Join(p.dir, name+reqdoc.Ext)
	p.files[f] = colID + "/" + itemID
	return f
}

// owner is the collection and item of a buffer file.
func (p *editorPane) owner(file string) (colID, itemID string, ok bool) {
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
	if !ok {
		return "", "", false
	}
	colID, itemID, _ = strings.Cut(ref, "/")
	return colID, itemID, true
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

// open shows a request in the editor: what Postman has, or its draft.
func (p *editorPane) open(colID string, it *postman.Item) {
	if p.dir == "" {
		dir, err := os.MkdirTemp("", "posthero-")
		if err == nil {
			p.dir = dir
		}
	}
	file := p.fileFor(colID, it.ID)
	doc := reqdoc.Render(it)
	var draft *string
	if d, ok := p.m.drafts.get(it.ID); ok && d != strings.TrimSuffix(doc, "\n") {
		draft = &d
	}
	// Without a draft, the buffer shows the request as Postman has it now,
	// also when it was loaded before and a teammate changed it since.
	stale := false
	if draft == nil {
		stale = p.loaded[file] && p.texts[file] != "" && p.texts[file] != strings.TrimSuffix(doc, "\n")
		writeIfChanged(file, doc)
		p.bases[it.ID] = it.UpdatedAt
	}
	p.m.open = &openRequest{colID: colID, itemID: it.ID, file: file}
	if p.backend != nil {
		p.backend.Open(file, draft)
		if stale {
			p.backend.Reload(file)
			delete(p.texts, file)
		}
		p.loaded[file] = true
	}
}

// baseOf is the version of a request its buffer was loaded from.
func (p *editorPane) baseOf(it *postman.Item) string {
	if b := p.bases[it.ID]; b != "" {
		return b
	}
	return it.UpdatedAt
}

// changedOnServer updates a request's buffer after Postman changed it: a
// buffer without changes reads the new text.
func (p *editorPane) changedOnServer(colID string, it *postman.Item) {
	file := p.fileFor(colID, it.ID)
	if !p.loaded[file] {
		return
	}
	if _, draft := p.m.drafts.get(it.ID); draft {
		return
	}
	writeIfChanged(file, reqdoc.Render(it))
	p.bases[it.ID] = it.UpdatedAt
	delete(p.texts, file)
	if p.backend != nil {
		p.backend.Reload(file)
	}
}

// text is the current text of the open request: its buffer's as last
// reported, its draft, or what Postman has.
func (p *editorPane) text(colID, itemID string, it *postman.Item) string {
	if d, ok := p.m.drafts.get(itemID); ok {
		return d
	}
	if t, ok := p.texts[p.fileFor(colID, itemID)]; ok {
		return t
	}
	return strings.TrimSuffix(reqdoc.Render(it), "\n")
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
			if d, ok := m.drafts.get(m.open.itemID); ok {
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
		colID, itemID, ok := p.owner(ev.File)
		if !ok {
			return cmd
		}
		m.trackDraft(colID, itemID, ev.Text)
		m.refreshHints()
		return cmd
	case editor.Written:
		cmd := p.wait()
		p.texts[ev.File] = ev.Text
		colID, itemID, ok := p.owner(ev.File)
		if !ok {
			return cmd
		}
		return tea.Batch(cmd, m.saveRequest(colID, itemID, ev.Text))
	case editor.Send:
		cmd := p.wait()
		text := strings.Join(ev.Lines, "\n")
		p.texts[ev.File] = text
		colID, itemID, ok := p.owner(ev.File)
		if !ok {
			m.setStatus("open a request first (⏎ in the tree, or ctrl+p)", true)
			return cmd
		}
		m.trackDraft(colID, itemID, text)
		return tea.Batch(cmd, m.sendText(colID, itemID, text))
	case editor.Hover:
		cmd := p.wait()
		colID, _, ok := p.owner(ev.File)
		if !ok {
			return cmd
		}
		return tea.Batch(cmd, m.hover(colID, ev))
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
