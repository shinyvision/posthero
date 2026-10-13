package app

import (
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/shinyvision/posthero/internal/codegen"
	"github.com/shinyvision/posthero/internal/config"
	"github.com/shinyvision/posthero/internal/postman"
	"github.com/shinyvision/posthero/internal/ui/lineinput"
	"github.com/shinyvision/posthero/internal/ui/modal"
	"github.com/shinyvision/posthero/internal/ui/theme"
	"github.com/shinyvision/posthero/internal/ui/tree"
	"github.com/shinyvision/posthero/internal/vim"
)

// cmdlineState is the ":" line's history and completion.
type cmdlineState struct {
	history []string
	browse  int    // position while going through the history, -1 when not
	prefix  string // what was typed before browsing started
	wild    []string
	wildSel int
	base    string // the line before the word being completed
}

func (s *cmdlineState) remember(text string) {
	for i, h := range s.history {
		if h == text {
			s.history = append(s.history[:i], s.history[i+1:]...)
			break
		}
	}
	s.history = append(s.history, text)
	if len(s.history) > 200 {
		s.history = s.history[1:]
	}
}

func (m *Model) openCmdline(text string) {
	m.cmdline = lineinput.New(":", text, m.regs)
	m.cmds.browse, m.cmds.wild = -1, nil
}

// cmdlineKey edits the command line like vim's: esc cancels, ⏎ runs, tab
// completes (again for the next match), ↑/↓ go through history.
func (m *Model) cmdlineKey(k string) tea.Cmd {
	switch k {
	case "<Tab>", "<C-i>":
		m.complete2(1)
		return nil
	case "<S-Tab>":
		m.complete2(-1)
		return nil
	case "<Up>", "<C-p>":
		m.browseHistory(-1)
		return nil
	case "<Down>", "<C-n>":
		m.browseHistory(1)
		return nil
	case "<Esc>", "<C-c>":
		m.cmdline = nil
		return nil
	}
	m.cmds.wild, m.cmds.browse = nil, -1
	if k == "<BS>" && m.cmdline.Value() == "" {
		m.cmdline = nil
		return nil
	}
	switch m.cmdline.Key(k).Kind {
	case vim.Submit:
		text := strings.TrimSpace(m.cmdline.Value())
		m.cmdline = nil
		if text != "" {
			m.cmds.remember(text)
		}
		return m.command(text)
	case vim.Cancel:
		m.cmdline = nil
	}
	return nil
}

func (m *Model) browseHistory(d int) {
	s := &m.cmds
	if s.browse < 0 {
		s.prefix = m.cmdline.Value()
		s.browse = len(s.history)
	}
	for i := s.browse + d; i >= 0 && i <= len(s.history); i += d {
		if i == len(s.history) {
			s.browse = i
			m.cmdline.SetValue(s.prefix)
			return
		}
		if strings.HasPrefix(s.history[i], s.prefix) {
			s.browse = i
			m.cmdline.SetValue(s.history[i])
			return
		}
	}
}

// complete2 fills in the word before the cursor; repeating it cycles
// through the matches, which the wildmenu above the status bar lists.
func (m *Model) complete2(d int) {
	s := &m.cmds
	if s.wild == nil {
		s.base, s.wild = m.candidates(m.cmdline.Value())
		s.wildSel = -1
		if len(s.wild) == 0 {
			s.wild = nil
			return
		}
		if len(s.wild) == 1 {
			m.cmdline.SetValue(s.base + s.wild[0])
			s.wild = nil
			return
		}
	}
	s.wildSel = (s.wildSel + d + len(s.wild)) % len(s.wild)
	m.cmdline.SetValue(s.base + s.wild[s.wildSel])
}

// wildmenu lists the completions above the status bar.
func (m *Model) wildmenu(w int) string {
	s := m.cmds
	if len(s.wild) < 2 {
		return ""
	}
	var parts []string
	for i, c := range s.wild {
		c = strings.TrimSpace(c)
		if i == s.wildSel {
			parts = append(parts, theme.Selected.Render(c))
		} else {
			parts = append(parts, c)
		}
	}
	line := strings.Join(parts, "  ")
	return lipglossBar(theme.Truncate(line, w), w)
}

func lipglossBar(s string, w int) string { return "\x1b[7m" + theme.Fit(s, w) + "\x1b[0m" }

// command is a : command.
type command struct {
	name  string
	short string // the shortest abbreviation, as in vim (":tabc")
	args  string // what it takes, for the help
	help  string
	// complete names what the argument completes to.
	complete string
	run      func(m *Model, arg string, bang bool) tea.Cmd
}

var commands []command

func init() {
	commands = []command{
		{name: "quit", short: "q", help: "quit (asks when requests have changes not saved; ! doesn't)", run: func(m *Model, _ string, bang bool) tea.Cmd { return m.requestQuit(bang) }},
		{name: "qall", short: "qa", help: "quit", run: func(m *Model, _ string, bang bool) tea.Cmd { return m.requestQuit(bang) }},
		{name: "xit", short: "x", help: "quit", run: func(m *Model, _ string, bang bool) tea.Cmd { return m.requestQuit(bang) }},
		{name: "write", short: "w", args: "[FILE]", help: "save the request (or example) to Postman; with a file, save the response body to it", complete: "file", run: cmdWrite},
		{name: "wq", help: "save the request and quit", run: func(m *Model, _ string, bang bool) tea.Cmd {
			// Quit once the save is done (or right away when there's nothing
			// to save); a save that fails keeps posthero open.
			m.quitAfterSave, m.quitAfterSaveBang = true, bang
			return cmdWrite(m, "", false)
		}},
		{name: "edit", short: "e", args: "[NAME]", help: "open a request (or example) by name; :e! throws away the changes to the open one", complete: "request", run: cmdEdit},
		{name: "find", short: "f", args: "[TEXT]", help: "find a request (ctrl+p)", run: func(m *Model, _ string, _ bool) tea.Cmd { return m.findRequest() }},
		{name: "send", short: "s", help: "send the request", run: func(m *Model, _ string, _ bool) tea.Cmd { return m.sendCurrent() }},
		{name: "run", args: "[-n N] [-d FILE] [-delay MS] [-bail] [-noscripts]", help: "run the folder or collection: N times, once per row of a CSV or JSON file", complete: "file", run: cmdRun},
		{name: "cancel", help: "stop the request or run on its way", run: func(m *Model, _ string, _ bool) tea.Cmd { m.cancelSend(); return nil }},
		{name: "env", args: "[NAME|edit|new NAME|copy [NAME]|rename NAME|delete]", help: "pick the environment; edit, make, copy (to a workspace, without NAME), rename or delete one", complete: "env", run: cmdEnv},
		{name: "vars", short: "va", args: "[NAME]", help: "your values of the variables (on this machine)", run: func(m *Model, arg string, _ bool) tea.Cmd { return m.editVars("", arg) }},
		{name: "share", args: "[NAME]", help: "give your value of a variable (or all of them) to your team", complete: "var", run: func(m *Model, arg string, _ bool) tea.Cmd { return m.share(arg) }},
		{name: "reset", args: "[NAME]", help: "take your team's value of a variable (or all of them) again", complete: "var", run: func(m *Model, arg string, _ bool) tea.Cmd { return m.reset(arg) }},
		{name: "globals", short: "glo", help: "your team's global variables", run: func(m *Model, _ string, _ bool) tea.Cmd { return m.editGlobals() }},
		{name: "vault", help: "your vault: secrets on this machine only ({{vault:name}})", run: func(m *Model, _ string, _ bool) tea.Cmd { return m.editVault() }},
		{name: "cookies", short: "coo", help: "the cookies posthero keeps", run: func(m *Model, _ string, _ bool) tea.Cmd { return m.editCookies() }},
		{name: "settings", short: "sett", help: "the folder's or collection's auth, scripts, variables and description", run: func(m *Model, _ string, _ bool) tea.Cmd { return m.editSettings(m.containerNode()) }},
		{name: "token", short: "to", help: "the login: who, how long it lasts; log in again or out", run: func(m *Model, _ string, _ bool) tea.Cmd { return m.showToken() }},
		{name: "login", args: "[USER]", help: "log in again (as another user)", run: func(m *Model, arg string, _ bool) tea.Cmd { return m.forceLogin(arg) }},
		{name: "logout", args: "", help: "log out; :logout! forgets the saved password too", run: func(m *Model, _ string, bang bool) tea.Cmd { return m.logout(bang) }},
		{name: "workspace", short: "ws", args: "[NAME]", help: "switch workspace", complete: "workspace", run: cmdWorkspace},
		{name: "new", args: "[METHOD] [URL]", help: "new request here; with a URL, it is filled in", run: cmdNew},
		{name: "folder", args: "NAME", help: "new folder here", run: cmdFolder},
		{name: "collection", short: "col", args: "new NAME", help: "make a collection", run: cmdCollection},
		{name: "rename", short: "ren", args: "NAME", help: "rename what's under the cursor", run: cmdRename},
		{name: "delete", short: "del", help: "delete what's under the cursor (asks first)", run: func(m *Model, _ string, _ bool) tea.Cmd { return m.deleteNode(m.treeNode()) }},
		{name: "duplicate", short: "dup", help: "make a copy of the request (or collection) next to it", run: cmdDuplicate},
		{name: "undo", short: "u", help: "undo your last change", run: func(m *Model, _ string, _ bool) tea.Cmd { return m.undoLast() }},
		{name: "history", short: "his", args: "[clear]", help: "earlier responses (! for this request only); clear forgets them all", run: func(m *Model, arg string, bang bool) tea.Cmd {
			switch arg {
			case "":
				return m.historyPicker(bang)
			case "clear":
				return modal.Show(&modal.Confirm{Title: "Forget every earlier response?", Body: "The responses posthero remembers on this machine go. Postman isn't touched.", Yes: func() tea.Msg {
					return cmdMsg{func() tea.Cmd {
						if err := m.hist.Clear(); err != nil {
							m.setStatus("couldn't forget the responses: "+err.Error(), true)
							return nil
						}
						m.setStatus("forgot every earlier response", false)
						return nil
					}}
				}})
			}
			m.setStatus(":history takes nothing or clear", true)
			return nil
		}},
		{name: "examples", short: "exa", help: "the request's saved examples, to open one", run: func(m *Model, _ string, _ bool) tea.Cmd { return m.examples() }},
		{name: "example", args: "[NAME]", help: "save the response shown as an example of its request (with none, a blank one to fill in); :example! keeps it in the open example", run: func(m *Model, arg string, bang bool) tea.Cmd {
			if bang {
				return m.updateExample()
			}
			return m.saveExample(arg)
		}},
		{name: "schema", help: "fetch the GraphQL schema of the open request's server again (completion and K use it)", run: cmdSchema},
		{name: "jq", args: "[FILTER]", help: "filter the response body with jq; without a filter, show the body", run: cmdJQ},
		{name: "code", args: "[LANGUAGE]", help: "the request as code: " + strings.Join(codegen.Languages, ", "), complete: "language", run: cmdCode},
		{name: "curl", help: "copy the request as a curl command", run: func(m *Model, _ string, _ bool) tea.Cmd { return m.copyAs("curl") }},
		{name: "import", short: "imp", args: "FILE|curl …", help: "import a collection or environment export, or a curl command", complete: "file", run: cmdImport},
		{name: "export", args: "FILE", help: "write the collection (or environment) to a file", complete: "file", run: cmdExport},
		{name: "browse", short: "bro", help: "open in Postman, in the browser", run: func(m *Model, _ string, _ bool) tea.Cmd { return m.browse(m.treeNode()) }},
		{name: "docs", help: "the description of what's under the cursor", run: func(m *Model, _ string, _ bool) tea.Cmd { return m.peek(m.treeNode()) }},
		{name: "refresh", short: "ref", help: "get what your team changed in Postman now", run: func(m *Model, _ string, _ bool) tea.Cmd { return m.do("refresh", 0) }},
		{name: "pull", help: "local mode: take in what your team changed in Postman", run: func(m *Model, _ string, _ bool) tea.Cmd { return m.pull() }},
		{name: "push", help: "local mode: send your changes to Postman, asking about what your team changed too", run: func(m *Model, _ string, _ bool) tea.Cmd { return m.push() }},
		{name: "set", short: "se", args: "OPTION", help: "change an option (:set all lists them)", complete: "option", run: cmdSet},
		{name: "mkpostherorc", short: "mk", help: "save the options to postherorc (! overwrites one that exists)", run: cmdMkrc},
		{name: "messages", short: "mes", help: "earlier messages", run: func(m *Model, _ string, _ bool) tea.Cmd { return m.showMessages() }},
		{name: "help", short: "h", help: "help", run: func(m *Model, _ string, _ bool) tea.Cmd { return m.showHelp() }},
		{name: "apikey", help: "use another Postman API key", run: func(m *Model, _ string, _ bool) tea.Cmd { m.form = m.loginForm(""); return nil }},
	}
}

// lookup finds a command by name or abbreviation.
func lookup(name string) (command, bool) {
	if name == "" {
		return command{}, false
	}
	for _, c := range commands {
		if c.name == name || c.short != "" && c.short == name {
			return c, true
		}
	}
	for _, c := range commands {
		if c.short != "" && strings.HasPrefix(c.name, name) && strings.HasPrefix(name, c.short) {
			return c, true
		}
	}
	var hits []command
	for _, c := range commands {
		if strings.HasPrefix(c.name, name) {
			hits = append(hits, c)
		}
	}
	if len(hits) == 1 {
		return hits[0], true
	}
	return command{}, false
}

// command runs a command line.
func (m *Model) command(text string) tea.Cmd {
	text = strings.TrimSpace(strings.TrimPrefix(text, ":"))
	if text == "" {
		return nil
	}
	if n, err := strconv.Atoi(text); err == nil && m.focus == paneResp {
		m.resp.Key("g")
		m.resp.Key("g")
		for i := 1; i < n; i++ {
			m.resp.Key("j")
		}
		return nil
	}
	name, arg, _ := strings.Cut(text, " ")
	bang := strings.HasSuffix(name, "!")
	name = strings.TrimSuffix(name, "!")
	c, ok := lookup(name)
	if !ok {
		m.setStatus("E492: Not an editor command: "+text+" (:help lists the commands)", true)
		return nil
	}
	if m.st == nil && c.name != "quit" && c.name != "qall" && c.name != "help" && c.name != "set" && c.name != "apikey" && c.name != "workspace" {
		m.setStatus("not connected to Postman yet", true)
		return nil
	}
	return c.run(m, strings.TrimSpace(arg), bang)
}

// containerNode is the folder or collection under the cursor, or the
// collection of the open request.
func (m *Model) containerNode() *tree.Node {
	n := m.treeNode()
	if n == nil {
		return nil
	}
	if n.Leaf() {
		if len(n.Parents) > 0 {
			return &tree.Node{Kind: tree.Folder, Col: n.Col, Item: n.Parents[len(n.Parents)-1], Parents: n.Parents[:len(n.Parents)-1]}
		}
		return &tree.Node{Kind: tree.Collection, Col: n.Col}
	}
	return n
}

func cmdWrite(m *Model, arg string, _ bool) tea.Cmd {
	if arg != "" {
		r := m.resp.Record()
		if r == nil {
			m.setStatus("no response to write", true)
			return nil
		}
		path := expandPath(arg, m.opts.Dir)
		data := r.Body
		if jq := m.resp.JQ(); jq != "" {
			out, _ := m.respJQ()
			data = []byte(out)
		}
		if err := os.WriteFile(path, data, 0o644); err != nil {
			m.setStatus(err.Error(), true)
			return nil
		}
		m.setStatus("wrote the body to "+path, false)
		return nil
	}
	if m.open == nil {
		m.setStatus("no request open", true)
		return nil
	}
	if m.editor.backend != nil {
		m.editor.backend.Command("w")
		return nil
	}
	text, ok := m.docText(m.open.docRef)
	if !ok {
		return nil
	}
	return m.saveDoc(m.open.docRef, text)
}

func (m *Model) respJQ() (string, bool) {
	r := m.resp.Record()
	if r == nil {
		return "", false
	}
	return string(r.Body), true
}

func expandPath(p, dir string) string {
	if strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			p = filepath.Join(home, p[2:])
		}
	}
	if !filepath.IsAbs(p) {
		p = filepath.Join(dir, p)
	}
	return p
}

func cmdEdit(m *Model, arg string, bang bool) tea.Cmd {
	if arg == "" {
		if bang && m.open != nil {
			// Throw away the changes: Postman's version again.
			m.drafts.drop(m.open.id())
			if m.editor.backend != nil {
				m.editor.backend.Command("e!")
				m.docChanged(m.open.docRef)
			}
			m.updateMarks()
			m.setStatus("threw away the changes", false)
			return nil
		}
		return m.findRequest()
	}
	c, it := m.findRequest2(arg)
	if it == "" {
		if ref, ok := m.lookupExample(arg, true); ok {
			return m.openExample(ref, true)
		}
		m.setStatus("no request "+arg, true)
		return nil
	}
	return m.openItem(c, it, true)
}

// findRequest2 finds a request by name (or the start of it, ignoring case).
func (m *Model) findRequest2(s string) (string, string) {
	if c, it := m.lookup(s); it != nil {
		return c.Summary.ID, it.ID
	}
	low := strings.ToLower(s)
	for _, c := range m.st.Collections() {
		if c.Data == nil {
			continue
		}
		for _, loc := range c.Data.Requests() {
			if strings.Contains(strings.ToLower(loc.Item.Name), low) {
				return c.Summary.ID, loc.Item.ID
			}
		}
	}
	return "", ""
}

func cmdEnv(m *Model, arg string, _ bool) tea.Cmd {
	colID := m.currentCol()
	verb, rest, _ := strings.Cut(arg, " ")
	rest = strings.TrimSpace(rest)
	cur := m.st.Env(m.envFor(colID))
	switch verb {
	case "":
		return m.pickEnv()
	case "edit":
		return m.editEnv(cur)
	case "new":
		if rest == "" {
			m.setStatus(":env new NAME", true)
			return nil
		}
		return m.newEnv(rest, nil, true)
	case "copy":
		if cur == nil {
			m.setStatus("pick an environment first (E); :env copy copies it to a workspace, :env copy NAME next to it", true)
			return nil
		}
		if rest == "" {
			return m.copyEnvTo(cur)
		}
		return m.newEnv(rest, cur, true)
	case "rename":
		if cur == nil || rest == "" {
			m.setStatus(":env rename NAME renames the environment in use", true)
			return nil
		}
		return m.renameEnv(cur, rest, true)
	case "delete":
		if cur == nil {
			return nil
		}
		return m.deleteEnv(cur)
	case "none", "-":
		m.setEnv(colID, "")
		return nil
	}
	e := m.envByName(arg)
	if e == nil {
		m.setStatus("no environment "+arg, true)
		return nil
	}
	m.setEnv(colID, e.Summary.ID)
	return nil
}

func cmdWorkspace(m *Model, arg string, _ bool) tea.Cmd {
	if arg == "" {
		return m.do("workspace", 0)
	}
	ws, ok := m.findWorkspace(arg)
	if !ok {
		m.setStatus("no workspace "+arg, true)
		return nil
	}
	return m.switchWorkspace(ws)
}

func cmdNew(m *Model, arg string, _ bool) tea.Cmd {
	n := m.treeNode()
	if n == nil {
		m.setStatus("pick a collection in the tree first", true)
		return nil
	}
	if arg == "" {
		return m.newRequest(n)
	}
	text := arg
	f := strings.Fields(arg)
	switch {
	case len(f) == 1 && isMethod(f[0]):
		text = strings.ToUpper(f[0]) + " " + commonBase(n.Col.Data, nil)
	case len(f) == 1:
		text = "GET " + f[0]
	}
	it, err := itemFromLine(text)
	if err != nil {
		m.setStatus(err.Error(), true)
		return nil
	}
	parent, after := container(n)
	return m.create(n.Col, parent, after, it)
}

func cmdFolder(m *Model, arg string, _ bool) tea.Cmd {
	n := m.treeNode()
	if n == nil {
		return nil
	}
	if arg == "" {
		return m.newFolder(n)
	}
	parent, _ := container(n)
	return m.create(n.Col, parent, nil, &postman.Item{Name: arg, Folder: true})
}

func cmdRename(m *Model, arg string, _ bool) tea.Cmd {
	n := m.treeNode()
	if n == nil {
		return nil
	}
	if arg == "" {
		return m.rename(n)
	}
	return m.setName(n.Col, n.Item, arg, true)
}

func cmdDuplicate(m *Model, _ string, _ bool) tea.Cmd {
	n := m.treeNode()
	if n == nil {
		return nil
	}
	if n.Item == nil {
		return m.copyCollection(n.Col, m.st.Workspace, nil)
	}
	if n.Kind == tree.Example {
		return m.copyExample(nodeRef(n), n.Col.Summary.ID, n.Item.ID)
	}
	m.cutNode(n, true)
	return m.put(n, false)
}

func cmdJQ(m *Model, arg string, _ bool) tea.Cmd {
	if m.resp.Record() == nil {
		m.setStatus("no response to filter", true)
		return nil
	}
	m.showRun = false
	m.resp.SetJQ(arg)
	m.focusPane(paneResp)
	return nil
}

func cmdCode(m *Model, arg string, _ bool) tea.Cmd {
	if arg == "" {
		return m.codePicker()
	}
	return m.showCode(arg)
}

func cmdSet(m *Model, arg string, _ bool) tea.Cmd {
	// local belongs to the workspace, not to the options.
	var rest []string
	var cmd tea.Cmd
	for _, f := range strings.Fields(arg) {
		switch f {
		case "local", "nolocal", "invlocal", "local!":
			on := f == "local" || (f == "invlocal" || f == "local!") && !m.isLocal()
			cmd = m.setLocal(on)
		case "local?":
			if m.isLocal() {
				m.setStatus("  local", false)
			} else {
				m.setStatus("nolocal", false)
			}
		default:
			rest = append(rest, f)
		}
	}
	if len(rest) == 0 && arg != "" {
		return cmd
	}
	arg = strings.Join(rest, " ")
	msg, err := m.settings.Set(arg)
	if err != nil {
		m.setStatus(err.Error(), true)
		return nil
	}
	m.applySettings()
	m.refreshHints()
	if strings.Contains(msg, "\n") {
		if m.isLocal() {
			msg += "\n  local"
		} else {
			msg += "\n  nolocal"
		}
		return tea.Batch(cmd, showText("Options", msg))
	}
	if msg != "" {
		m.setStatus(strings.TrimSpace(msg), false)
	}
	return cmd
}

func cmdMkrc(m *Model, _ string, bang bool) tea.Cmd {
	path, err := config.RCPath()
	if err != nil {
		m.setStatus(err.Error(), true)
		return nil
	}
	if _, err := os.Stat(path); err == nil && !bang {
		m.setStatus("E189: "+path+" exists (add ! to overwrite)", true)
		return nil
	}
	rc := m.opts.RC
	rc.Settings, rc.Maps = m.settings, m.maps
	if _, err := config.SaveRC(rc); err != nil {
		m.setStatus(err.Error(), true)
		return nil
	}
	m.setStatus("wrote "+path, false)
	return nil
}

// candidates completes a command line: command names, then what each
// command's argument is.
func (m *Model) candidates(line string) (string, []string) {
	name, arg, hasArg := strings.Cut(line, " ")
	if !hasArg {
		var out []string
		for _, c := range commands {
			if strings.HasPrefix(c.name, name) {
				out = append(out, c.name)
			}
		}
		sort.Strings(out)
		return "", out
	}
	c, ok := lookup(strings.TrimSuffix(name, "!"))
	if !ok {
		return "", nil
	}
	base := name + " "
	var all []string
	switch c.complete {
	case "env":
		all = append(all, "edit", "new ", "copy ", "rename ", "delete", "none")
		if m.st != nil {
			for _, e := range m.st.Environments() {
				all = append(all, e.Name())
			}
		}
	case "workspace":
		for _, ws := range m.workspaces {
			all = append(all, ws.Name)
		}
	case "option":
		f := strings.Fields(arg)
		word := ""
		if len(f) > 0 && !strings.HasSuffix(arg, " ") {
			word = f[len(f)-1]
			base += strings.TrimSuffix(arg, word)
		} else {
			base += arg
		}
		for _, o := range append(m.settings.OptionNames(), "local", "nolocal") {
			if strings.HasPrefix(o, word) {
				all = append(all, o)
			}
		}
		return base, all
	case "language":
		all = codegen.Languages
	case "request":
		if m.st != nil {
			for _, col := range m.st.Collections() {
				if col.Data == nil {
					continue
				}
				for _, loc := range col.Data.Requests() {
					all = append(all, loc.Item.Name)
					for _, r := range loc.Item.Response {
						all = append(all, r.Name)
					}
				}
			}
		}
	case "var":
		if m.st != nil {
			set := m.scopes(m.currentCol())
			all = set.Keys()
		}
	case "file":
		return m.fileCandidates(base, arg)
	}
	var out []string
	low := strings.ToLower(arg)
	for _, a := range all {
		if strings.HasPrefix(strings.ToLower(a), low) {
			out = append(out, a)
		}
	}
	if len(out) == 0 {
		for _, a := range all {
			if strings.Contains(strings.ToLower(a), low) {
				out = append(out, a)
			}
		}
	}
	return base, dedupe(out)
}

func dedupe(list []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range list {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

// fileCandidates completes a path after base.
func (m *Model) fileCandidates(base, word string) (string, []string) {
	f := strings.Fields(word)
	last := ""
	if len(f) > 0 && !strings.HasSuffix(word, " ") {
		last = f[len(f)-1]
		base += strings.TrimSuffix(word, last)
	} else {
		base += word
	}
	dir, prefix := filepath.Split(last)
	look := expandPath(dir, m.opts.Dir)
	if dir == "" {
		look = m.opts.Dir
	}
	entries, err := os.ReadDir(look)
	if err != nil {
		return base, nil
	}
	var out []string
	for _, e := range entries {
		name := e.Name()
		if !strings.HasPrefix(name, prefix) || strings.HasPrefix(name, ".") && !strings.HasPrefix(prefix, ".") {
			continue
		}
		if e.IsDir() {
			name += "/"
		}
		out = append(out, dir+name)
	}
	sort.Strings(out)
	return base, out
}

// showMessages lists earlier status messages.
func (m *Model) showMessages() tea.Cmd {
	if len(m.log) == 0 {
		m.setStatus("no messages", false)
		return nil
	}
	var lines []string
	for _, e := range m.log {
		line := e.at.Format("15:04:05") + "  " + e.text
		if e.isError {
			line = theme.Error.Render(line)
		}
		lines = append(lines, line)
	}
	return modal.Show(&modal.Pager{Title: "Messages", Lines: lines})
}

// isMethod reports whether a word is an HTTP method.
func isMethod(w string) bool {
	switch strings.ToUpper(w) {
	case "GET", "POST", "PUT", "PATCH", "DELETE", "HEAD", "OPTIONS", "COPY", "LINK", "UNLINK", "PURGE", "LOCK", "UNLOCK", "PROPFIND", "VIEW":
		return true
	}
	return false
}
