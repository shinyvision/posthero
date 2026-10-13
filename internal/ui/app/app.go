// Package app is posthero's root Bubble Tea model. It owns the tree of
// collections, the request editor (your own Neovim) and the response pane,
// routes keys through one keymap, sends requests, keeps Postman in sync and
// shows dialogs.
package app

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/shinyvision/posthero/internal/auth"
	"github.com/shinyvision/posthero/internal/clipboard"
	"github.com/shinyvision/posthero/internal/config"
	"github.com/shinyvision/posthero/internal/history"
	"github.com/shinyvision/posthero/internal/jsonx"
	"github.com/shinyvision/posthero/internal/local"
	"github.com/shinyvision/posthero/internal/postman"
	"github.com/shinyvision/posthero/internal/run"
	"github.com/shinyvision/posthero/internal/script"
	"github.com/shinyvision/posthero/internal/send"
	"github.com/shinyvision/posthero/internal/store"
	"github.com/shinyvision/posthero/internal/ui/editmodal"
	"github.com/shinyvision/posthero/internal/ui/form"
	"github.com/shinyvision/posthero/internal/ui/lineinput"
	"github.com/shinyvision/posthero/internal/ui/modal"
	"github.com/shinyvision/posthero/internal/ui/picker"
	"github.com/shinyvision/posthero/internal/ui/respview"
	"github.com/shinyvision/posthero/internal/ui/runview"
	"github.com/shinyvision/posthero/internal/ui/tree"
	"github.com/shinyvision/posthero/internal/vim"
)

// pane is where the keyboard is.
type pane int

const (
	paneTree pane = iota
	paneEditor
	paneResp
)

// Options are what main hands the app.
type Options struct {
	Login config.Login
	// HasLogin is false when there is no API key yet: the app asks for one
	// (unless Files are open).
	HasLogin bool
	// Files are collection and environment exports to open instead of a
	// workspace.
	Files []string
	// APIBase is the Postman API to use; empty is the real one.
	APIBase string
	RC      config.RC
	State   config.State
	// SaveState writes the state on quit; nil skips it.
	SaveState func(config.State) error
	// Notice is shown in the status bar at startup.
	Notice string
	// Open is a request to open at startup: a name, id or link.
	Open string
	// Dir is where posthero was started: relative files start there.
	Dir string
	// StateDir and CacheDir keep posthero's files; empty keeps them in
	// memory (tests).
	StateDir, CacheDir string
	// Demo says posthero runs against the built-in demo.
	Demo bool
	// Version goes in the help.
	Version string
}

// Model is the root model.
type Model struct {
	opts     Options
	settings config.Settings
	maps     []config.Mapping
	state    config.State
	regs     *vim.Registers
	// send delivers a message from work in the background.
	send func(tea.Msg)

	// api is what posthero reads and changes: Postman (remote), or the
	// workspace's local copy in local mode (local).
	api        *postman.Client
	remote     *postman.Client
	local      *local.Copy
	st         *store.Store
	workspaces []postman.Workspace
	me         postman.User
	loaded     bool // the first refresh came back

	engine *script.Engine
	authm  *auth.Manager
	// authSend are the request settings logging in uses too (read by the
	// token goroutines).
	authSend atomic.Pointer[send.Settings]
	runner   *run.Runner
	client   *send.Client
	jar      *send.Jar
	hist     *history.History
	vault    *vault

	tree   *tree.Model
	editor *editorPane
	// compl is what the editors' completion offers.
	compl completions
	// schemas are the GraphQL servers' schemas, by address.
	schemas map[string]*schemaEntry
	resp    *respview.Model
	runv    *runview.Model
	// showRun says the response pane shows the run's list.
	showRun bool
	// runStep is the step of the run shown in the response pane.
	runStep int
	running context.CancelFunc

	focus     pane
	prevFocus pane
	w, h      int
	zoomed    bool

	// open is the request in the editor.
	open *openRequest
	// drafts are requests' texts not saved to Postman, by item id.
	drafts *drafts
	// last is the last response of each request shown, by item id.
	last map[string]*history.Record
	// sending is the request on its way, by item id, and how to stop it.
	sending map[string]inflight
	sendSeq int

	// localChanges counts what the local copy has that Postman doesn't, and
	// localMarks marks it in the tree; localDirty and localChecking count
	// them again after a change, one count at a time.
	localChanges              int
	localMarks                map[string]bool
	localDirty, localChecking bool
	// conflicts are those a push left to look at, by key.
	conflicts map[string]*local.Conflict

	modals []modal.Modal
	form   *form.Form
	// formClosed runs when the form closes, either way.
	formClosed func()
	edit       *editmodal.Model
	// afterEdit runs when the editor dialog closes.
	afterEdit func() tea.Cmd
	cmdline   *lineinput.Model
	cmds      cmdlineState

	ms       mouseState
	keys     keyState
	keyTimer int
	status   string
	isError  bool
	log      []logEntry
	busy     map[int]string
	busySeq  int
	spin     int

	undo []undoEntry
	// undoNote is what the status bar says once an undo is done.
	undoNote string
	cut      *cutState
	quit     bool
	// quitAfterSave quits once the saves under way are done (:wq);
	// savesInFlight counts them.
	quitAfterSave, quitAfterSaveBang bool
	savesInFlight                    int
	// lastRefresh is when Postman was last asked for changes.
	lastRefresh time.Time
	// lastInput is when a key or the mouse was last used, and blurred
	// says the terminal is in the background: refreshing pauses while
	// nobody looks, to spare the team's Postman API calls.
	lastInput time.Time
	blurred   bool
	// savedState is the state as last written.
	savedState string
	refreshing bool
	refreshSeq int
	exitNote   string
	ctx        context.Context
	stop       context.CancelFunc
}

// New builds the app.
func New(o Options) *Model {
	ctx, stop := context.WithCancel(context.Background())
	if o.Dir == "" {
		o.Dir, _ = os.Getwd()
	}
	m := &Model{
		opts:      o,
		settings:  o.RC.Settings,
		maps:      o.RC.Maps,
		state:     o.State,
		regs:      vim.NewRegisters(),
		busy:      map[int]string{},
		last:      map[string]*history.Record{},
		sending:   map[string]inflight{},
		schemas:   map[string]*schemaEntry{},
		ctx:       ctx,
		stop:      stop,
		lastInput: time.Now(),
	}
	if m.state.Env == nil {
		m.state.Env = map[string]string{}
	}
	m.tree = tree.New(m.state.Expanded)
	m.resp = respview.New()
	m.resp.SetWrap(m.settings.Wrap)
	m.drafts = loadDrafts(m.stateFile("drafts.json"))
	m.engine = script.NewEngine()
	m.authm = auth.NewManager(m.stateFile("auth"))
	m.authm.KeepAlive = time.Duration(m.settings.KeepAlive) * time.Hour
	m.authm.Insecure = !m.settings.Verify
	m.jar = send.NewJar(m.stateFile("cookies.json"))
	m.client = send.NewClient(m.jar)
	// Logging in goes the way requests do: the same proxy, CA file and
	// client certificates (:set changes them; see applySettings).
	client := m.client
	m.authm.Transport = func() http.RoundTripper {
		s := m.authSend.Load()
		if s == nil {
			return nil
		}
		t, err := client.Transport(*s)
		if err != nil {
			return nil // an unreadable CA file: the request says so; logging in uses Go's defaults
		}
		return t
	}
	m.hist = history.Open(m.stateFile("history"), m.settings.History)
	m.vault = newVault(m.stateFile("vault.json"))
	m.runner = &run.Runner{Engine: m.engine, Client: m.client, Auth: m.authm, Dir: m.workdir(), Vault: m.vault}
	m.applySettings()
	m.editor = newEditorPane(m)
	switch {
	case len(o.Files) > 0:
		st, err := store.OpenFiles(o.Files, m.stateFile("values"))
		if st == nil {
			m.exitNote = err.Error()
			m.quit = true
			return m
		}
		m.st = st
		if err != nil {
			m.setStatus(err.Error(), true)
		}
		m.loaded = true
		m.tree.SetData(m.st.Collections())
	case o.HasLogin:
		m.remote = postman.NewClient(o.Login.APIKey, o.APIBase)
		m.api = m.remote
		m.openWorkspace(m.startWorkspace())
	default:
		m.form = m.loginForm("")
	}
	if o.Notice != "" {
		m.setStatus(o.Notice, false)
	}
	return m
}

// SetProgram gives the app its program, so work in the background (a
// login that needs a password) can reach it.
func (m *Model) SetProgram(p *tea.Program) { m.send = p.Send }

// stateFile is a file or directory in the state directory, empty when
// nothing is kept on disk.
func (m *Model) stateFile(name string) string {
	if m.opts.StateDir == "" {
		return ""
	}
	return filepath.Join(m.opts.StateDir, name)
}

// workdir is where relative file paths in requests start.
func (m *Model) workdir() string {
	if m.settings.Workdir != "" {
		return m.settings.Workdir
	}
	return m.opts.Dir
}

// applySettings hands the options to what uses them.
func (m *Model) applySettings() {
	s := m.settings.Send()
	s.Certs = m.opts.RC.Certs
	m.runner.Settings = s
	m.authSend.Store(&s)
	m.runner.Dir = m.workdir()
	m.authm.Configure(time.Duration(m.settings.KeepAlive)*time.Hour, !m.settings.Verify)
	m.hist.SetMax(m.settings.History)
	if m.resp != nil {
		m.resp.SetWrap(m.settings.Wrap)
	}
}

// startWorkspace is the workspace to open first: the one set, else the
// last one.
func (m *Model) startWorkspace() postman.Workspace {
	ws := m.state.Workspace
	if s := m.settings.Workspace; s != "" && s != ws && !strings.EqualFold(s, m.state.WorkspaceName) {
		ws = s // checked against the list once it is in
	}
	return postman.Workspace{ID: ws}
}

// openWorkspace shows a workspace (from the cache at once, if it was seen
// before) and asks Postman what changed. A workspace without an id is
// picked once the list is in.
func (m *Model) openWorkspace(ws postman.Workspace) {
	if ws.ID == "" {
		m.st = nil
		return
	}
	m.openLocal(ws)
	cache := ""
	if m.opts.CacheDir != "" && m.local == nil {
		cache = filepath.Join(m.opts.CacheDir, "workspaces") // the local copy is its own cache
	}
	m.st = store.Open(m.api, ws, cache, m.stateFile("values"))
	m.state.Workspace = ws.ID
	if ws.Name != "" {
		m.state.WorkspaceName = ws.Name
	}
	m.saveState()
	m.loaded = m.st.Cached()
	m.tree.SetData(m.st.Collections())
}

// Init implements tea.Model.
func (m *Model) Init() tea.Cmd {
	cmds := []tea.Cmd{tick(), tea.RequestBackgroundColor}
	if m.quit {
		return tea.Quit
	}
	if m.api != nil {
		cmds = append(cmds, m.start())
	}
	if m.st != nil && m.st.API == nil {
		cmds = append(cmds, m.afterLoad())
	}
	return tea.Batch(cmds...)
}

// Close saves what posthero keeps and shuts down the editor.
func (m *Model) Close() {
	m.stop()
	if m.edit != nil {
		m.edit.Close()
	}
	m.editor.close()
	m.drafts.save()
	_ = m.jar.Save()
	_ = m.authm.Tokens.Save()
	m.saveState()
}

// saveState writes where posthero is, when that changed.
func (m *Model) saveState() {
	// Files opened instead of a workspace leave the workspace's state be.
	if m.opts.SaveState == nil || len(m.opts.Files) > 0 {
		return
	}
	m.state.Expanded = m.tree.Expanded()
	if n := m.tree.Current(); n != nil {
		m.state.Cursor = n.ID()
	}
	if m.open != nil {
		m.state.Open = m.open.String()
	}
	data, _ := jsonx.Marshal(m.state)
	if string(data) == m.savedState {
		return
	}
	if m.opts.SaveState(m.state) == nil {
		m.savedState = string(data)
	}
}

// ExitNote is what to tell the user after posthero quits, if anything.
func (m *Model) ExitNote() string { return m.exitNote }

// Update implements tea.Model.
func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	cmd := m.update(msg)
	if m.quitAfterSave && m.savesInFlight == 0 {
		m.quitAfterSave = false
		cmd = tea.Batch(cmd, m.requestQuit(m.quitAfterSaveBang))
	}
	m.fixFocus()
	if text, ok := m.regs.TakeClipboard(); ok {
		cmd = tea.Batch(cmd, tea.SetClipboard(text), copyCmd(text))
	}
	if m.quit && len(m.busy) == 0 {
		return m, tea.Quit
	}
	return m, cmd
}

func (m *Model) update(msg tea.Msg) tea.Cmd {
	switch msg.(type) {
	case tea.KeyPressMsg, tea.MouseClickMsg, tea.MouseWheelMsg, tea.PasteMsg:
		away := !m.active()
		m.lastInput = time.Now()
		if away && m.stale() {
			// Back after a while: catch up with the team.
			return tea.Batch(m.update1(msg), m.refresh(false))
		}
	}
	return m.update1(msg)
}

// active reports whether someone is using posthero: the terminal has the
// focus and a key was pressed lately.
func (m *Model) active() bool {
	return !m.blurred && time.Since(m.lastInput) < idleAfter
}

// idleAfter is how long without a key or click counts as away.
const idleAfter = 10 * time.Minute

// stale reports whether it's time to ask Postman what changed.
func (m *Model) stale() bool {
	return m.settings.Refresh > 0 && time.Since(m.lastRefresh) > time.Duration(m.settings.Refresh)*time.Second
}

func (m *Model) update1(msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.w, m.h = msg.Width, msg.Height
		return m.editor.ensure()
	case tea.KeyPressMsg:
		return m.key(msg)
	case tea.MouseMsg:
		if !m.settings.Mouse {
			return nil
		}
		return m.mouse(msg)
	case tea.PasteMsg:
		return m.paste(msg.Content)
	case tea.FocusMsg:
		m.blurred = false
		if time.Since(m.lastRefresh) > 30*time.Second {
			return m.refresh(false)
		}
		return nil
	case tea.BlurMsg:
		m.blurred = true
		return nil
	case tickMsg:
		return m.onTick()
	case doneMsg:
		return m.done(msg)
	case statusMsg:
		m.setStatus(msg.text, msg.isError)
		return nil
	case modal.ShowMsg:
		m.modals = append(m.modals, msg.Modal)
		return nil
	case picker.OpenMsg:
		m.modals = append(m.modals, msg.Picker)
		return nil
	case picker.SourceMsg:
		if msg.Picker != nil {
			return msg.Picker.Update(msg)
		}
		return nil
	case editmodal.OpenMsg:
		if m.edit != nil {
			m.setStatus("finish the open editor first", true)
			return nil
		}
		var cmd tea.Cmd
		req := msg.Request
		if req.Runtime == "" {
			req.Runtime = m.editor.runtime
		}
		m.edit, cmd = editmodal.New(req, m.regs, m.w, m.h)
		return cmd
	case form.OpenMsg:
		m.form = msg.Form
		return nil
	case form.ResultMsg:
		if m.form == nil {
			return nil
		}
		cmd, closed := m.form.Update(msg)
		if closed {
			m.form = nil
		}
		return cmd
	case editorEvent:
		return m.editor.event(msg)
	case startedMsg:
		return m.started(msg)
	case loggedInMsg:
		return m.loggedIn(msg)
	case fetchedMsg:
		return m.fetched(msg)
	case progressMsg:
		return m.progress(msg)
	case schemaMsg:
		return m.gotSchema(msg)
	case sentMsg:
		return m.sent(msg)
	case savedMsg:
		return m.saved(msg)
	case changedMsg:
		return m.changed(msg)
	case runStepMsg:
		return m.runStepDone(msg)
	case runDoneMsg:
		return m.runDone(msg)
	case promptMsg:
		return m.showPrompt(msg)
	case keyTimeoutMsg:
		return m.keyTimeout(msg)
	case errorMsg:
		m.setStatus(msg.err.Error(), true)
		return modal.Error(msg.title, msg.err.Error())
	case cmdMsg:
		return msg.cmd()
	case multiMsg:
		var cmds []tea.Cmd
		for _, sub := range msg {
			cmds = append(cmds, m.update1(sub))
		}
		return tea.Batch(cmds...)
	case clipboardFailedMsg:
		m.setStatus("copied through the terminal (OSC 52); no clipboard tool here (wl-copy, xclip or xsel)", false)
		return nil
	case keptFreshMsg:
		return m.keptFresh(msg)
	case browserDoneMsg:
		return m.browserDone(msg)
	}
	if cmd, ok := m.localUpdate(msg); ok {
		return cmd
	}
	if r, ok := msg.(editmodal.ResultMsg); ok && (m.edit == nil || !r.FromEditor()) {
		// A save no editor waits for (:share saves this way too).
		var cmds []tea.Cmd
		if r.Err != nil {
			m.setStatus("not saved: "+r.Err.Error(), true)
			cmds = append(cmds, modal.Error("Not saved", r.Err.Error()))
		}
		if r.Then != nil {
			cmds = append(cmds, m.update1(r.Then))
		}
		return tea.Batch(cmds...)
	}
	if m.edit != nil {
		cmd, closed := m.edit.Update(msg)
		if closed {
			m.edit = nil
			if f := m.afterEdit; f != nil {
				m.afterEdit = nil
				cmd = tea.Batch(cmd, f())
			}
		}
		return cmd
	}
	return nil
}

// run starts work in the background with a spinner and a note in the
// status bar; f's message is delivered when it is done. The work stops
// when posthero quits.
func (m *Model) run(note string, f func(ctx context.Context) tea.Msg) tea.Cmd {
	m.busySeq++
	id := m.busySeq
	m.busy[id] = note
	ctx := m.ctx
	return func() tea.Msg {
		c, cancel := context.WithTimeout(ctx, 2*time.Minute)
		defer cancel()
		return doneMsg{id: id, msg: f(c)}
	}
}

// doneMsg ends a run and delivers its result.
type doneMsg struct {
	id  int
	msg tea.Msg
}

// Inner and WithInner let an editor dialog find its save's result inside.
func (d doneMsg) Inner() tea.Msg                { return d.msg }
func (d doneMsg) WithInner(msg tea.Msg) tea.Msg { d.msg = msg; return d }

func (m *Model) done(msg doneMsg) tea.Cmd {
	delete(m.busy, msg.id)
	if msg.msg == nil {
		return nil
	}
	return m.update1(msg.msg)
}

// statusMsg shows a message in the status bar.
type statusMsg struct {
	text    string
	isError bool
}

func status(text string) tea.Cmd { return func() tea.Msg { return statusMsg{text: text} } }

// errorMsg shows an error dialog.
type errorMsg struct {
	title string
	err   error
}

// cmdMsg runs a command when delivered.
type cmdMsg struct{ cmd func() tea.Cmd }

// multiMsg delivers several messages.
type multiMsg []tea.Msg

// tickMsg drives the spinner, automatic refreshes and keeping logins
// fresh.
type tickMsg time.Time

func tick() tea.Cmd { return tickEvery(120 * time.Millisecond) }

func tickEvery(d time.Duration) tea.Cmd {
	return tea.Tick(d, func(t time.Time) tea.Msg { return tickMsg(t) })
}

func (m *Model) busyNow() bool { return len(m.busy) > 0 || len(m.sending) > 0 || m.running != nil }

func (m *Model) onTick() tea.Cmd {
	m.spin++
	next := tickEvery(time.Second)
	if m.busyNow() {
		next = tick()
	}
	cmds := []tea.Cmd{next}
	// Postman: what the team changed.
	if m.st != nil && m.st.API != nil && !m.refreshing && m.idle() && m.active() && m.stale() {
		cmds = append(cmds, m.refresh(false))
	}
	// Logins stay fresh while posthero is open.
	if m.spin%15 == 0 {
		cmds = append(cmds, m.keepFresh())
	}
	if m.spin%10 == 0 {
		m.drafts.save()
		m.saveState()
	}
	return tea.Batch(cmds...)
}

// idle reports whether nothing is in the way of quiet work: no dialog, no
// half-typed keys.
func (m *Model) idle() bool {
	return len(m.modals) == 0 && m.edit == nil && m.cmdline == nil && m.form == nil && m.keys.pending == ""
}

type clipboardFailedMsg struct{ err error }

func copyCmd(text string) tea.Cmd {
	return func() tea.Msg {
		if err := clipboard.Write(text); err != nil {
			return clipboardFailedMsg{err}
		}
		return nil
	}
}

// copyText puts text in the clipboard (and posthero's registers).
func (m *Model) copyText(text, what string) {
	m.regs.CopyRegister(vim.Register{Text: text})
	m.setStatus("copied "+what, false)
}

// fixFocus keeps the keyboard in a pane that is on screen.
func (m *Model) fixFocus() {
	if m.w == 0 {
		return
	}
	l := m.layout()
	if !l.rect(m.focus).visible() {
		for _, p := range []pane{paneEditor, paneResp, paneTree} {
			if l.rect(p).visible() {
				m.focus = p
				return
			}
		}
	}
}
