package app

import (
	"strings"
	"time"
	"unicode"

	tea "charm.land/bubbletea/v2"

	"github.com/shinyvision/posthero/internal/keys"
	"github.com/shinyvision/posthero/internal/ui/modal"
	"github.com/shinyvision/posthero/internal/ui/tree"
)

// where says in which pane a key binding works.
type where uint8

const (
	inTree where = 1 << iota
	inResp
	inEditor
	inPanes = inTree | inResp
	inAll   = inTree | inResp | inEditor
)

// binding maps a key sequence to an action. The same table runs the keys
// and writes the help, so the two never disagree. Keys the table doesn't
// have go to the pane (the editor gets everything but inEditor bindings).
type binding struct {
	keys    string
	where   where
	action  string
	help    string
	section string
}

// Help sections, in the order the help shows them.
const (
	secSend    = "Sending"
	secMove    = "Finding your way"
	secTree    = "The tree"
	secResp    = "The response"
	secVars    = "Environments and variables"
	secMore    = "Copying and more"
	secWindows = "Panes"
)

var bindings = []binding{
	// Sending.
	{"s", inPanes, "send", "send the request (in the tree: the one under the cursor; on a folder: run it all)", secSend},
	{"S", inPanes, "runall", "run every request in the folder or collection (the collection runner)", secSend},
	{"<C-c>", inAll, "cancel", "stop the request or run on its way", secSend},

	// Finding your way.
	{"<C-p>", inAll, "find", "find any request: type a few letters of its name, folder or URL", secMove},
	{" ", inPanes, "menu", "every action, in a menu", secMove},
	{"gh", inPanes, "history", "earlier responses of this request (:history for all)", secMove},
	{"ge", inPanes, "examples", "the request's saved examples, to open one", secMove},
	{"R", inPanes, "refresh", "get what your team changed in Postman now (it also happens by itself)", secMove},
	{"W", inPanes, "workspace", "switch workspace", secMove},

	// The tree.
	{"e", inTree, "edit", "edit: a request (or example) in the editor; a folder's or collection's auth, scripts and variables", secTree},
	{"a", inTree, "newrequest", "new request here", secTree},
	{"A", inTree, "newfolder", "new folder here", secTree},
	{"r", inTree, "rename", "rename", secTree},
	{"D", inTree, "delete", "delete (asks first; u brings it back)", secTree},
	{"dd", inTree, "cut", "cut, to move it with p", secTree},
	{"yy", inTree, "yank", "copy, to put a copy with p; a collection: copy it to a workspace", secTree},
	{"p", inTree, "put", "put what was cut or copied below the cursor (into a folder; an example under a request)", secTree},
	{"P", inTree, "putabove", "put it above", secTree},
	{"u", inTree, "undo", "undo your last change (a save, a move, a delete …)", secTree},
	{"K", inTree, "peek", "what the request sends: the URL as it goes out, its auth, its last response (an example: what it says)", secTree},
	{"<Esc>", inTree, "escape", "forget what was cut; clear the filter", secTree},

	// The response.
	{"o", inResp, "openbody", "the body in your editor (read only), for searching and folding your way", secResp},
	{"<BS>", inResp, "back", "back to the run's list (after ⏎ on one of its requests)", secResp},
	{"<C-o>", inResp, "back", "", secResp},
	{"gV", inResp, "visualize", "show the visualization (pm.visualizer) in the browser", secResp},

	// Environments and variables.
	{"E", inPanes, "env", "pick the environment for this collection", secVars},
	{"gv", inPanes, "vars", "your values of the variables (:vars)", secVars},
	{"gE", inPanes, "editenv", "your team's values of the environment (:env edit)", secVars},
	{"gl", inPanes, "token", "the login: who, how long it lasts; log in again or out", secVars},

	// Copying and more.
	{"yc", inPanes, "copycurl", "copy the request as a curl command", secMore},
	{"yu", inPanes, "copyurl", "copy the URL as it is sent", secMore},
	{"yn", inTree, "copyname", "copy the request's name", secMore},
	{"gx", inTree, "browse", "open the request in Postman, in the browser", secMore},
	{"gc", inPanes, "code", "the request as code: curl, HTTPie, fetch, Python, Go …", secMore},

	// Panes.
	{"<C-h>", inAll, "paneleft", "the pane to the left", secWindows},
	{"<C-l>", inAll, "paneright", "the pane to the right", secWindows},
	{"<C-j>", inAll, "panedown", "the pane below", secWindows},
	{"<C-k>", inAll, "paneup", "the pane above", secWindows},
	{"<C-S-h>", inAll, "borderleft", "move the tree's border left (ctrl+shift+l: right); so does dragging it", secWindows},
	{"<C-S-l>", inAll, "borderright", "", secWindows},
	{"<C-S-k>", inAll, "borderup", "move the line above the response up (ctrl+shift+j: down); so does dragging it", secWindows},
	{"<C-S-j>", inAll, "borderdown", "", secWindows},
	{"<C-w>", inPanes, "wincmd", "then h j k l w p o = < > + -: vim's window commands, on posthero's panes", secWindows},
	{":", inPanes, "cmdline", "command line", secWindows},
	{"?", inPanes, "help", "this help", secWindows},
	{"<F1>", inAll, "help", "", secWindows},
	{"ZZ", inPanes, "quit", "quit (so do :q and ctrl+q)", secWindows},
	{"<C-q>", inAll, "quit", "", secWindows},
}

// keyState is a half-typed key sequence and its count.
type keyState struct {
	pending string
	count   int
}

// context is where keys go now.
func (m *Model) context() where {
	switch m.focus {
	case paneEditor:
		return inEditor
	case paneResp:
		return inResp
	}
	return inTree
}

// key routes a key press: dialogs first, then the command line, the
// editor, and the keymap.
func (m *Model) key(msg tea.KeyPressMsg) tea.Cmd {
	k := keys.Vim(msg)
	if k == "" {
		return nil // a key with no name in vim (a media key): nothing to do
	}
	if k == "<M-Esc>" {
		esc := tea.KeyPressMsg{Code: tea.KeyEscape}
		return tea.Batch(m.key(esc), m.key(esc))
	}
	if n := len(m.modals); n > 0 {
		done, cmd := m.modals[n-1].Key(k)
		if done {
			m.modals = m.modals[:n-1]
		}
		return cmd
	}
	if m.form != nil {
		if k == "<C-c>" && m.api == nil && m.st == nil {
			return m.quitWithoutLogin()
		}
		cmd, closed := m.form.Key(k)
		if closed {
			m.form = nil
			if f := m.formClosed; f != nil {
				m.formClosed = nil
				f()
			}
			if m.api == nil && m.st == nil {
				return m.quitWithoutLogin()
			}
		}
		return cmd
	}
	if m.edit != nil {
		m.edit.Key(msg)
		return nil
	}
	if m.cmdline != nil {
		return m.cmdlineKey(k)
	}
	if m.focus == paneEditor {
		return m.editorKey(k, msg)
	}
	// A pane's own prompt (the tree's filter, the response's search) gets
	// everything.
	if m.focus == paneTree && m.tree.Filtering() {
		return m.treeKey(k)
	}
	if m.focus == paneResp && m.resp.Capturing() && !m.showRun {
		m.resp.Key(k)
		return m.respNotice()
	}
	if k == "<C-c>" {
		m.keys = keyState{}
		if !m.cancelSend() {
			m.setStatus("type :q to quit", false)
		}
		return nil
	}
	return m.press(k, true)
}

// editorKey sends keys to the editor, but for posthero's few: moving
// between panes, finding a request (in normal mode) and cancelling.
func (m *Model) editorKey(k string, msg tea.KeyPressMsg) tea.Cmd {
	normal := strings.HasPrefix(strings.ToUpper(m.editor.mode()), "NORMAL") || m.editor.mode() == ""
	switch k {
	case "<C-h>", "<C-j>", "<C-k>", "<C-l>":
		return m.do(map[string]string{"<C-h>": "paneleft", "<C-j>": "panedown", "<C-k>": "paneup", "<C-l>": "paneright"}[k], 0)
	case "<C-S-h>", "<C-S-j>", "<C-S-k>", "<C-S-l>":
		return m.do(map[string]string{"<C-S-h>": "borderleft", "<C-S-j>": "borderdown", "<C-S-k>": "borderup", "<C-S-l>": "borderright"}[k], 0)
	case "<C-q>", "<F1>":
		return m.do(map[string]string{"<C-q>": "quit", "<F1>": "help"}[k], 0)
	case "<C-p>":
		if normal {
			return m.do("find", 0)
		}
	case "<C-c>":
		// A request or run on its way stops; otherwise ctrl+c is the
		// editor's.
		busy := m.running != nil
		if m.open != nil {
			_, sending := m.sending[m.open.id()]
			busy = busy || sending
		}
		if busy && m.cancelSend() {
			return nil
		}
	}
	m.editor.key(msg)
	return nil
}

// press runs one key through the keymap; user maps apply when remap is
// set. Keys the keymap doesn't know go to the focused pane.
func (m *Model) press(k string, remap bool) tea.Cmd {
	ks := &m.keys
	if ks.pending == "<C-w>" {
		*ks = keyState{}
		return m.wincmd(k)
	}
	// Counts, as in vim: digits before a command; 0 alone is a motion.
	if ks.pending == "" && len(k) == 1 && k[0] >= '0' && k[0] <= '9' && (k != "0" || ks.count > 0) {
		ks.count = ks.count*10 + int(k[0]-'0')
		return nil
	}
	seq := ks.pending + k
	ctx := m.context()
	if remap {
		if rhs, exact, prefix := m.findMap(seq); exact {
			count := ks.count
			*ks = keyState{}
			return m.feed(rhs, count)
		} else if prefix {
			ks.pending = seq
			if m.binding(seq, ctx) != nil {
				m.keyTimer++
				id := m.keyTimer
				return tea.Tick(time.Second, func(time.Time) tea.Msg { return keyTimeoutMsg{id} })
			}
			return nil
		}
	}
	var hit *binding
	prefix := false
	for i := range bindings {
		b := &bindings[i]
		if b.where&ctx == 0 {
			continue
		}
		if b.keys == seq {
			hit = b
		} else if strings.HasPrefix(b.keys, seq) {
			prefix = true
		}
	}
	if hit != nil && !prefix {
		count := ks.count
		*ks = keyState{}
		return m.do(hit.action, count)
	}
	if prefix {
		ks.pending = seq
		if hit != nil {
			m.keyTimer++
			id := m.keyTimer
			return tea.Tick(time.Second, func(time.Time) tea.Msg { return keyTimeoutMsg{id} })
		}
		return nil
	}
	// Not the keymap's: the pane's, with the count and the keys typed so
	// far.
	count := ks.count
	*ks = keyState{}
	var cmds []tea.Cmd
	pending := splitKeys(seq)
	if count > 0 {
		for _, d := range []rune(itoa(count)) {
			cmds = append(cmds, m.paneKey(string(d)))
		}
	}
	for _, pk := range pending {
		cmds = append(cmds, m.paneKey(pk))
	}
	return tea.Batch(cmds...)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

// paneKey gives a key to the focused pane.
func (m *Model) paneKey(k string) tea.Cmd {
	switch m.focus {
	case paneTree:
		return m.treeKey(k)
	case paneResp:
		if m.showRun && m.runv != nil {
			open, _ := m.runv.Key(k)
			if open {
				return m.showRunStep()
			}
			return nil
		}
		e := m.resp.Key(k)
		switch {
		case e.Copy != "":
			m.copyText(e.Copy, e.What)
		case e.Open != "":
			return m.openURL(e.Open)
		case e.Show != "":
			return showText(e.Title, e.Show)
		case e.Pass:
			return nil
		}
		return m.respNotice()
	}
	return nil
}

func (m *Model) respNotice() tea.Cmd {
	if n := m.resp.Notice(); n != "" {
		m.setStatus(n, strings.HasPrefix(n, "E"))
	}
	return nil
}

// treeKey handles a key in the tree.
func (m *Model) treeKey(k string) tea.Cmd {
	before := m.tree.Current()
	switch m.tree.Key(k) {
	case tree.Open:
		return m.openDoc(nodeRef(m.tree.Current()), true)
	case tree.Moved:
		if n := m.tree.Current(); n != nil && n != before {
			return m.preview(n)
		}
	}
	return nil
}

// preview shows the request (or example) under the tree's cursor in the
// editor, and its last response (or what the example says comes back), as
// the cursor moves.
func (m *Model) preview(n *tree.Node) tea.Cmd {
	if !n.Leaf() {
		return nil
	}
	if m.open != nil && m.open.docRef == nodeRef(n) {
		return nil
	}
	return m.openDoc(nodeRef(n), false)
}

// keyTimeoutMsg ends a wait for more keys of a mapping.
type keyTimeoutMsg struct{ id int }

// keyTimeout does what the keys typed so far mean on their own, when no
// more keys came.
func (m *Model) keyTimeout(msg keyTimeoutMsg) tea.Cmd {
	if msg.id != m.keyTimer || m.keys.pending == "" {
		return nil
	}
	b := m.binding(m.keys.pending, m.context())
	count := m.keys.count
	m.keys = keyState{}
	if b == nil {
		return nil
	}
	return m.do(b.action, count)
}

// binding finds the binding for exactly these keys here.
func (m *Model) binding(seq string, ctx where) *binding {
	for i := range bindings {
		if b := &bindings[i]; b.where&ctx != 0 && b.keys == seq {
			return b
		}
	}
	return nil
}

// findMap looks seq up in the user's mappings.
func (m *Model) findMap(seq string) (rhs string, exact, prefix bool) {
	for _, mp := range m.maps {
		lhs := normalKeys(mp.LHS)
		if lhs == seq {
			return mp.RHS, true, false
		}
		if strings.HasPrefix(lhs, seq) {
			prefix = true
		}
	}
	return "", false, prefix
}

// normalKeys writes keys the way posthero sees them: <c-j> is <C-j>, <cr>
// is <CR>, <Space> and <leader> are a space.
func normalKeys(s string) string {
	var b strings.Builder
	for _, k := range splitKeys(s) {
		b.WriteString(k)
	}
	return b.String()
}

// feed plays a mapping's keys: ":cmd<CR>" runs a command, other keys go
// through the keymap without remapping (like noremap).
func (m *Model) feed(rhs string, count int) tea.Cmd {
	var cmds []tea.Cmd
	for _, k := range splitKeys(rhs) {
		switch {
		case m.cmdline != nil:
			cmds = append(cmds, m.cmdlineKey(k))
		case k == ":" && len(m.modals) == 0:
			m.openCmdline("")
		case len(m.modals) > 0:
			n := len(m.modals)
			done, cmd := m.modals[n-1].Key(k)
			if done {
				m.modals = m.modals[:n-1]
			}
			cmds = append(cmds, cmd)
		default:
			if count > 0 {
				m.keys.count = count
				count = 0
			}
			cmds = append(cmds, m.press(k, false))
		}
	}
	return tea.Batch(cmds...)
}

// splitKeys splits "dd<C-r>:q<CR>" into keys. A "<" that doesn't start a
// key name is just "<", as in vim.
func splitKeys(s string) []string {
	var out []string
	for len(s) > 0 {
		if s[0] == '<' {
			if end := strings.IndexByte(s, '>'); end > 1 {
				if k, ok := vimKey(s[:end+1]); ok {
					out = append(out, k)
					s = s[end+1:]
					continue
				}
			}
		}
		r := []rune(s)[0]
		out = append(out, string(r))
		s = s[len(string(r)):]
	}
	return out
}

// keyNames are the named keys by lowercase name, written as keys.Vim
// writes them.
var keyNames = map[string]string{
	"cr": "CR", "enter": "CR", "return": "CR", "esc": "Esc", "bs": "BS", "backspace": "BS",
	"tab": "Tab", "del": "Del", "delete": "Del", "insert": "Insert", "ins": "Insert",
	"up": "Up", "down": "Down", "left": "Left", "right": "Right", "home": "Home", "end": "End",
	"pageup": "PageUp", "pagedown": "PageDown", "bar": "|", "lt": "<", "bslash": "\\",
	"f1": "F1", "f2": "F2", "f3": "F3", "f4": "F4", "f5": "F5", "f6": "F6", "f7": "F7", "f8": "F8", "f9": "F9", "f10": "F10", "f11": "F11", "f12": "F12",
}

// vimKey reads a <…> key name; ok is false for text that isn't one.
func vimKey(s string) (string, bool) {
	name := strings.ToLower(s[1 : len(s)-1])
	switch name {
	case "space", "leader":
		return " ", true
	case "bar":
		return "|", true
	case "lt":
		return "<", true
	case "bslash":
		return "\\", true
	}
	var mods []string
	for len(name) > 2 && name[1] == '-' {
		switch name[0] {
		case 'c':
			mods = append(mods, "C-")
		case 'm', 'a':
			mods = append(mods, "M-")
		case 's':
			mods = append(mods, "S-")
		case 'd':
			mods = append(mods, "D-")
		default:
			return "", false
		}
		name = name[2:]
	}
	base, ok := keyNames[name]
	if !ok {
		if len([]rune(name)) != 1 {
			return "", false
		}
		base = name
		if len(mods) == 0 {
			return base, true
		}
		r := []rune(name)[0]
		if unicode.IsUpper(r) {
			base = string(unicode.ToLower(r))
			mods = append(mods, "S-")
		}
	}
	if len(mods) == 0 && len(base) > 1 {
		return "<" + base + ">", true
	}
	return "<" + strings.Join(mods, "") + base + ">", true
}

// quitWithoutLogin leaves when the login was cancelled before there was
// one, saying what to do instead.
func (m *Model) quitWithoutLogin() tea.Cmd {
	m.exitNote = "not logged in. Run posthero again to log in, posthero FILE.json to open an export, or posthero --demo to look around."
	return tea.Quit
}

// showText shows text in a scrollable dialog.
func showText(title, text string) tea.Cmd {
	return modal.Show(&modal.Pager{Title: title, Lines: strings.Split(text, "\n")})
}
