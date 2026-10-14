package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/shinyvision/posthero/internal/auth"
	"github.com/shinyvision/posthero/internal/codegen"
	"github.com/shinyvision/posthero/internal/history"
	"github.com/shinyvision/posthero/internal/postman"
	"github.com/shinyvision/posthero/internal/send"
	"github.com/shinyvision/posthero/internal/ui/editmodal"
	"github.com/shinyvision/posthero/internal/ui/modal"
	"github.com/shinyvision/posthero/internal/ui/picker"
	"github.com/shinyvision/posthero/internal/ui/respview"
	"github.com/shinyvision/posthero/internal/ui/theme"
	"github.com/shinyvision/posthero/internal/ui/tree"
)

// do runs an action of the keymap.
func (m *Model) do(action string, count int) tea.Cmd {
	n := m.treeNode()
	switch action {
	case "send":
		return m.sendCurrent()
	case "runall":
		if n == nil {
			return nil
		}
		if n.Leaf() && len(n.Parents) > 0 {
			p := n.Parents[len(n.Parents)-1]
			return m.runFolder(&tree.Node{Kind: tree.Folder, Col: n.Col, Item: p, Parents: n.Parents[:len(n.Parents)-1]})
		}
		if n.Leaf() {
			return m.runFolder(&tree.Node{Kind: tree.Collection, Col: n.Col})
		}
		return m.runFolder(n)
	case "cancel":
		if !m.cancelSend() {
			m.setStatus("nothing to stop", false)
		}
	case "find":
		return m.findRequest()
	case "menu":
		return m.menu()
	case "history":
		return m.historyPicker(true)
	case "examples":
		return m.examples()
	case "refresh":
		if m.st == nil || m.st.API == nil {
			m.setStatus("collections opened from files are read from disk; nothing to refresh", false)
			return nil
		}
		m.setStatus("asking Postman what changed…", false)
		return m.refresh(true)
	case "workspace":
		if len(m.workspaces) == 0 {
			m.setStatus("no workspaces here", false)
			return nil
		}
		return m.pickWorkspace("Workspace")
	case "edit":
		if n == nil {
			return nil
		}
		if n.Leaf() {
			return m.openDoc(nodeRef(n), true)
		}
		return m.editSettings(n)
	case "newrequest":
		return m.newRequest(n)
	case "newfolder":
		return m.newFolder(n)
	case "rename":
		return m.rename(n)
	case "delete":
		return m.deleteNode(n)
	case "cut":
		m.cutNode(n, false)
	case "yank":
		if n != nil && n.Item == nil && n.Kind == tree.Collection {
			return m.copyCollectionTo(n.Col)
		}
		m.cutNode(n, true)
	case "put":
		return m.put(n, false)
	case "putabove":
		return m.put(n, true)
	case "undo":
		return m.undoLast()
	case "peek":
		return m.peek(n)
	case "escape":
		if m.cut != nil {
			m.cut = nil
			m.updateMarks()
			m.setStatus("", false)
			return nil
		}
		m.tree.ClearFilter()
	case "openbody":
		return m.openBody()
	case "back":
		if m.runv != nil && !m.showRun {
			m.showRun = true
			return nil
		}
	case "visualize":
		return m.visualize()
	case "env":
		return m.pickEnv()
	case "vars":
		return m.editVars("", "")
	case "editenv":
		if m.st == nil {
			return nil
		}
		return m.editEnv(m.st.Env(m.envFor(m.currentCol())))
	case "token":
		return m.showToken()
	case "users":
		return m.showUsers()
	case "copycurl":
		return m.copyAs("curl")
	case "copyurl":
		p, err := m.prepared()
		if err != nil {
			m.setStatus(err.Error(), true)
			return nil
		}
		m.copyText(p.URL, "the URL")
	case "copyname":
		if n != nil {
			m.copyText(n.Name(), "the name")
		}
	case "browse":
		return m.browse(n)
	case "code":
		return m.codePicker()
	case "paneleft":
		m.moveFocus("h")
	case "paneright":
		m.moveFocus("l")
	case "panedown":
		m.moveFocus("j")
	case "paneup":
		m.moveFocus("k")
	case "borderleft":
		m.resize(-2, 0)
	case "borderright":
		m.resize(2, 0)
	case "borderup":
		m.resize(0, -1)
	case "borderdown":
		m.resize(0, 1)
	case "wincmd":
		m.keys.pending = "<C-w>"
		return nil
	case "cmdline":
		m.openCmdline("")
	case "help":
		return m.showHelp()
	case "quit":
		return m.requestQuit(false)
	}
	return nil
}

// menu shows every action that works here, each with its key.
func (m *Model) menu() tea.Cmd {
	ctx := m.context()
	var items []modal.Item
	seen := map[string]bool{}
	for _, b := range bindings {
		if b.where&ctx == 0 || b.help == "" || seen[b.action] {
			continue
		}
		switch b.action {
		case "menu", "paneleft", "paneright", "panedown", "paneup", "borderleft", "borderright", "borderup", "borderdown", "wincmd", "cmdline", "escape", "back":
			continue
		}
		seen[b.action] = true
		action := b.action
		label := b.help
		if i := strings.IndexAny(label, "(:"); i > 20 {
			label = strings.TrimSpace(label[:i])
		}
		items = append(items, modal.Item{Label: label, Key: b.keys, Run: func() tea.Cmd { return m.do(action, 0) }})
	}
	title := "Actions"
	if n := m.treeNode(); n != nil {
		title = n.Name()
	}
	return modal.Show(&modal.Menu{Title: title, Items: items, X: -1, Y: -1})
}

// findRequest finds any request of the workspace by name, folder, method
// or URL; recent ones first.
func (m *Model) findRequest() tea.Cmd {
	if m.st == nil {
		return nil
	}
	recent := map[string]int{}
	for i, r := range m.state.Recent {
		recent[r] = len(m.state.Recent) - i
	}
	type entry struct {
		item  picker.Item
		score int
	}
	var all []entry
	for _, c := range m.st.Collections() {
		if c.Data == nil {
			continue
		}
		for _, loc := range c.Data.Requests() {
			it := loc.Item
			path := c.Name()
			for _, p := range loc.Parents {
				path += " / " + p.Name
			}
			url := ""
			if it.Request != nil && it.Request.URL != nil {
				url = it.Request.URL.Raw
			}
			badge := tree.Badge(it)
			st := tree.BadgeStyle(badge)
			ref := docRef{colID: c.Summary.ID, itemID: it.ID}
			all = append(all, entry{picker.Item{
				Label: fmt.Sprintf("%-4s %s", badge, it.Name), Detail: path, Search: url, Style: &st,
				Value:   ref,
				Current: m.open != nil && m.open.docRef == ref,
			}, recent[ref.String()]})
			// Its examples, under it.
			for _, r := range it.Response {
				dim := theme.Dim
				ref := docRef{colID: c.Summary.ID, itemID: it.ID, exID: r.ID}
				all = append(all, entry{picker.Item{
					Label: tree.ExampleBadge + " " + r.Name, Detail: path + " / " + it.Name, Search: url, Style: &dim,
					Value:   ref,
					Current: m.open != nil && m.open.docRef == ref,
				}, recent[ref.String()]})
			}
		}
	}
	sort.SliceStable(all, func(a, b int) bool { return all[a].score > all[b].score })
	items := make([]picker.Item, len(all))
	for i, e := range all {
		items[i] = e.item
	}
	p := &picker.Picker{Title: "Find a request", Items: items, Placeholder: "name, folder, method or URL", Pick: func(it picker.Item) tea.Cmd {
		ref := it.Value.(docRef)
		return func() tea.Msg { return cmdMsg{func() tea.Cmd { return m.openDoc(ref, true) }} }
	}}
	return p.Open()
}

// peek says what the request under the cursor sends.
func (m *Model) peek(n *tree.Node) tea.Cmd {
	if n == nil {
		return nil
	}
	if n.Kind == tree.Example {
		return m.peekExample(n)
	}
	if n.Kind != tree.Request {
		return m.describeContainer(n)
	}
	colID := n.Col.Summary.ID
	p, err := m.preparedFor(nodeRef(n))
	var b strings.Builder
	if err != nil {
		b.WriteString(err.Error() + "\n")
	} else {
		fmt.Fprintf(&b, "%s %s\n", p.Method, mask(p.URL, m.scopes(colID)))
		if len(p.Missing) > 0 {
			fmt.Fprintf(&b, "not set: %s\n", braces(p.Missing))
		}
	}
	if a, from := send.EffectiveAuth(n.Item, n.Parents, n.Col.Data); a != nil {
		b.WriteString("\n" + authSummary(a, from, m.scopes(colID)) + "\n")
	}
	if env := m.envName(colID); env != "" {
		b.WriteString("\nenvironment " + env + "\n")
	}
	if r := m.lastRecord(n.Item.ID); r != nil {
		b.WriteString("\nlast sent " + r.At.Local().Format("2 Jan 15:04") + ": " + summary(r) + "\n")
	}
	if d := n.Item.Request.Description; d != nil && d.Content != "" {
		b.WriteString("\n" + d.Content + "\n")
	}
	return showText(n.Item.Name, strings.TrimRight(b.String(), "\n"))
}

// describeContainer shows a folder's or collection's description and what
// it holds.
func (m *Model) describeContainer(n *tree.Node) tea.Cmd {
	var desc *postman.Description
	var items []*postman.Item
	if n.Item != nil {
		desc, items = n.Item.Description, n.Item.Item
	} else if n.Col.Data != nil {
		desc, items = n.Col.Data.Info.Description, n.Col.Data.Item
	}
	requests, folders := 0, 0
	for _, it := range items {
		if it.Folder {
			folders++
		} else {
			requests++
		}
	}
	text := fmt.Sprintf("%d %s, %d %s", requests, plural(requests, "request"), folders, plural(folders, "folder"))
	if n.Kind == tree.Collection {
		text += "\nenvironment " + orName(m.envName(n.Col.Summary.ID), "(none)")
	}
	if desc != nil && desc.Content != "" {
		text += "\n\n" + desc.Content
	}
	return showText(n.Name(), text)
}

// prepared is the current request (or example) as it would be sent now
// (without its pre-request scripts and auth).
func (m *Model) prepared() (*send.Prepared, error) {
	n := m.treeNode()
	if n == nil || !n.Leaf() {
		return nil, fmt.Errorf("pick a request first")
	}
	return m.preparedFor(nodeRef(n))
}

func (m *Model) preparedFor(ref docRef) (*send.Prepared, error) {
	item, parents, err := m.docItem(ref)
	if err != nil {
		return nil, err
	}
	colID := ref.colID
	c := m.st.Col(colID)
	a, from := send.EffectiveAuth(item, parents, c.Data)
	r := send.FromItem(item, a, m.runner.Settings)
	set := m.scopes(colID)
	p, err := r.Prepare(set, send.Options{Dir: m.workdir(), Token: "posthero"})
	if err != nil {
		return nil, err
	}
	m.signForCode(p, a, from, set.Resolve)
	return p, nil
}

// signForCode adds the auth a snippet needs: the kept token for OAuth 2.0,
// the signature for the others.
func (m *Model) signForCode(p *send.Prepared, a *postman.Auth, from string, res func(string) string) {
	if a == nil {
		return
	}
	if a.Type == "oauth2" {
		cfg := auth.ConfigFrom(auth.Resolve(a, res))
		if t := m.authm.Tokens.Get(cfg.Key()); t != nil {
			prefix := cfg.HeaderPrefix
			if prefix == "" {
				prefix = "Bearer"
			}
			p.Set("Authorization", prefix+" "+t.AccessToken, true)
		}
		return
	}
	_ = from
	_, _, _ = m.authm.Do(context.Background(), noSend{}, p, a, from, res, nil)
}

// noSend signs without sending: auth.Do with it only adds the auth.
type noSend struct{}

func (noSend) Do(context.Context, *send.Prepared) (*send.Response, error) {
	return &send.Response{Status: 200}, nil
}

// copyAs copies the request as code.
func (m *Model) copyAs(lang string) tea.Cmd {
	p, err := m.prepared()
	if err != nil {
		m.setStatus(err.Error(), true)
		return nil
	}
	out, err := codegen.Snippet(lang, p)
	if err != nil {
		m.setStatus(err.Error(), true)
		return nil
	}
	m.copyText(out, "the request as "+lang)
	return nil
}

// codePicker picks a language and shows the request as code in it.
func (m *Model) codePicker() tea.Cmd {
	var items []picker.Item
	for _, l := range codegen.Languages {
		items = append(items, picker.Item{Label: l, Value: l})
	}
	p := &picker.Picker{Title: "The request as code", Items: items, Pick: func(it picker.Item) tea.Cmd {
		lang := it.Value.(string)
		return func() tea.Msg { return cmdMsg{func() tea.Cmd { return m.showCode(lang) }} }
	}}
	return p.Open()
}

func (m *Model) showCode(lang string) tea.Cmd {
	p, err := m.prepared()
	if err != nil {
		m.setStatus(err.Error(), true)
		return nil
	}
	out, err := codegen.Snippet(lang, p)
	if err != nil {
		m.setStatus(err.Error(), true)
		return nil
	}
	return modal.Show(&modal.Choice{Title: "As " + lang, Body: out, Options: []modal.Option{
		{Key: "y", Label: "Copy it", Run: func() tea.Msg { return cmdMsg{func() tea.Cmd { m.copyText(out, "the request as "+lang); return nil }} }},
		{Key: "q", Label: "Close", Run: nil},
	}})
}

// browse opens the request (or collection) in Postman.
func (m *Model) browse(n *tree.Node) tea.Cmd {
	if n == nil || m.st == nil || m.st.API == nil {
		return nil
	}
	u := "https://go.postman.co/collection/" + n.Col.Summary.UID
	switch {
	case n.Example != nil:
		u = "https://go.postman.co/example/" + exampleUID(n.Col, n.Example)
	case n.Item != nil:
		kind := "request"
		if n.Item.Folder {
			kind = "folder"
		}
		u = "https://go.postman.co/" + kind + "/" + itemUID(n.Col, n.Item)
	}
	return m.openURL(u)
}

func (m *Model) openURL(u string) tea.Cmd {
	if err := auth.OpenURL(u); err != nil {
		m.setStatus("couldn't open the browser: "+err.Error(), true)
		return nil
	}
	m.setStatus("opened "+u, false)
	return nil
}

// openBody shows the response body in the editor, read only.
func (m *Model) openBody() tea.Cmd {
	r := m.resp.Record()
	if r == nil || len(r.Body) == 0 {
		m.setStatus("no body to open", true)
		return nil
	}
	ext := ".txt"
	text := string(r.Body)
	switch ct := r.ContentType(); {
	case strings.Contains(ct, "json"):
		ext = ".json"
		if pretty, ok := prettyJSON(text); ok {
			text = pretty
		}
	case strings.Contains(ct, "html"):
		ext = ".html"
	case strings.Contains(ct, "xml"):
		ext = ".xml"
	}
	if jq := m.resp.JQ(); jq != "" {
		ext = ".json"
	}
	return editmodal.Open(editmodal.Request{
		Title: "Response · " + r.Name, Hint: ":w FILE saves a copy · :q closes", Text: text, Ext: ext, Name: "response", Tall: true,
		Submit: func(string) tea.Cmd { return func() tea.Msg { return editmodal.ResultMsg{} } },
	})
}

// visualize shows a pm.visualizer page in the browser.
func (m *Model) visualize() tea.Cmd {
	r := m.resp.Record()
	if r == nil || r.Visual == nil {
		m.setStatus("this response has no visualization", true)
		return nil
	}
	page := visualPage(r)
	dir, err := os.MkdirTemp("", "posthero-visual-")
	if err != nil {
		m.setStatus(err.Error(), true)
		return nil
	}
	path := filepath.Join(dir, "visualization.html")
	if err := os.WriteFile(path, []byte(page), 0o600); err != nil {
		m.setStatus(err.Error(), true)
		return nil
	}
	return m.openURL("file://" + path)
}

// visualPage is an HTML page that renders a visualizer template with
// Handlebars, as Postman does.
func visualPage(r *history.Record) string {
	return `<!doctype html><meta charset="utf-8"><title>` + htmlEscape(r.Name) + ` · posthero</title>
<script src="https://cdnjs.cloudflare.com/ajax/libs/handlebars.js/4.7.8/handlebars.min.js"></script>
<div id="out"></div>
<script id="tpl" type="text/x-handlebars-template">` + r.Visual.Template + `</script>
<script>
var data = ` + r.Visual.Data + `;
document.getElementById('out').innerHTML = Handlebars.compile(document.getElementById('tpl').innerHTML)(data);
var pm = { getData: function (cb) { cb(null, data); } };
</script>`
}

func htmlEscape(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(s)
}

// historyPicker lists earlier responses: of the open request, or of
// everything.
func (m *Model) historyPicker(this bool) tea.Cmd {
	list := m.hist.List()
	title := "History"
	if this && m.open != nil {
		list = m.hist.Of(m.open.id())
		if name := m.docName(m.open.docRef); name != "" {
			title = "History of " + name
		}
	}
	if len(list) == 0 {
		m.setStatus("nothing sent yet", false)
		return nil
	}
	var items []picker.Item
	for _, s := range list {
		st := ""
		switch {
		case s.Error != "":
			st = "error"
		case s.Status > 0:
			st = fmt.Sprint(s.Status)
		}
		detail := s.At.Local().Format("2 Jan 15:04:05") + " · " + st + " · " + respview.Duration(s.Duration)
		if s.Env != "" {
			detail += " · " + s.Env
		}
		if s.Failed > 0 {
			detail += fmt.Sprintf(" · ✗ %d", s.Failed)
		}
		items = append(items, picker.Item{Label: s.Method + " " + s.Name, Detail: detail, Search: s.URL, Value: s.ID})
	}
	p := &picker.Picker{Title: title, Items: items, Pick: func(it picker.Item) tea.Cmd {
		id := it.Value.(string)
		return func() tea.Msg {
			return cmdMsg{func() tea.Cmd {
				r, err := m.hist.Load(id)
				if err != nil {
					m.setStatus(err.Error(), true)
					return nil
				}
				m.showRun = false
				m.resp.Show(r)
				m.focusPane(paneResp)
				m.setStatus("from "+r.At.Local().Format("2 Jan 15:04")+": "+summary(r), false)
				return nil
			}}
		}
	}}
	return p.Open()
}

// requestQuit quits, asking first when requests have changes not saved to
// Postman (bang skips that).
func (m *Model) requestQuit(bang bool) tea.Cmd {
	if !bang {
		if n := len(m.drafts.ids()); n > 0 {
			return modal.Show(&modal.Choice{
				Title: "Changes not saved to Postman",
				Body:  fmt.Sprintf("%d %s %s changes Postman doesn't have. They stay here as drafts either way.", n, plural(n, "request"), map[bool]string{true: "has", false: "have"}[n == 1]),
				Options: []modal.Option{
					{Key: "q", Label: "Quit, keep the drafts", Run: func() tea.Msg { return cmdMsg{func() tea.Cmd { return m.requestQuit(true) }} }},
					{Key: "c", Label: "Cancel", Run: nil},
				},
			})
		}
	}
	m.quit = true
	if len(m.busy) > 0 {
		m.setStatus("quitting once the work in progress is done…", false)
	}
	return nil
}

// prettyJSON indents a JSON text.
func prettyJSON(s string) (string, bool) {
	var b bytes.Buffer
	if err := json.Indent(&b, []byte(strings.TrimSpace(s)), "", "  "); err != nil {
		return s, false
	}
	return b.String(), true
}
