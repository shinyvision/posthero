package app

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/shinyvision/posthero/internal/codegen"
	"github.com/shinyvision/posthero/internal/postman"
	"github.com/shinyvision/posthero/internal/reqdoc"
	"github.com/shinyvision/posthero/internal/store"
	"github.com/shinyvision/posthero/internal/ui/modal"
)

// itemFromLine makes a request from "METHOD URL".
func itemFromLine(line string) (*postman.Item, error) {
	it, err := reqdoc.Apply(&postman.Item{}, line)
	if err != nil {
		return nil, err
	}
	it.Name = it.Request.Method + " " + it.Request.URL.Base()
	if i := strings.Index(it.Name, "://"); i >= 0 {
		rest := it.Name[i+3:]
		if j := strings.IndexByte(rest, '/'); j >= 0 {
			it.Name = it.Request.Method + " " + rest[j:]
		}
	}
	return it, nil
}

// paste handles pasted text: into what has the keyboard, and a curl command
// pasted in the tree or response pane becomes a request.
func (m *Model) paste(s string) tea.Cmd {
	switch {
	case len(m.modals) > 0:
		top := m.modals[len(m.modals)-1]
		switch t := top.(type) {
		case interface{ Paste(string) tea.Cmd }:
			return t.Paste(s)
		case interface{ Paste(string) }:
			t.Paste(s)
		}
		return nil
	case m.form != nil:
		m.form.Paste(s)
		return nil
	case m.edit != nil:
		m.edit.Paste(s)
		return nil
	case m.cmdline != nil:
		m.cmdline.Paste(s)
		return nil
	case m.focus == paneEditor:
		m.editor.paste(s)
		return nil
	case m.focus == paneTree && m.tree.Filtering():
		m.tree.Paste(s)
		return nil
	}
	text := strings.TrimSpace(s)
	if strings.HasPrefix(text, "curl ") {
		return m.importCurl(text)
	}
	if c, it := m.pastedLink(text); it != "" {
		return m.openItem(c, it, true)
	}
	return nil
}

// pastedLink opens a request from a pasted Postman link.
func (m *Model) pastedLink(text string) (string, string) {
	if m.st == nil || !strings.Contains(text, "postman.co") {
		return "", ""
	}
	if c, it := m.lookup(text); it != nil {
		return c.Summary.ID, it.ID
	}
	return "", ""
}

// importCurl asks what to do with a curl command: add it as a request, or
// send it once.
func (m *Model) importCurl(cmd string) tea.Cmd {
	it, err := codegen.ParseCurl(cmd)
	if err != nil {
		m.setStatus(err.Error(), true)
		return nil
	}
	n := m.treeNode()
	where := "the collection"
	if n != nil {
		parent, _ := container(n)
		if parent != nil {
			where = parent.Name
		} else {
			where = n.Col.Name()
		}
	}
	opts := []modal.Option{
		{Key: "a", Label: "Add it to " + where, Run: func() tea.Msg {
			return cmdMsg{func() tea.Cmd {
				if n == nil {
					m.setStatus("pick a collection in the tree first", true)
					return nil
				}
				parent, after := container(n)
				return m.create(n.Col, parent, after, it)
			}}
		}},
		{Key: "c", Label: "Cancel", Run: nil},
	}
	return modal.Show(&modal.Choice{Title: "A curl command", Body: it.Request.Method + " " + it.Request.URL.Raw, Options: opts})
}

// cmdImport imports a collection or environment export into the workspace,
// or a curl command.
func cmdImport(m *Model, arg string, _ bool) tea.Cmd {
	if arg == "" {
		m.setStatus(":import FILE (a collection or environment export) or :import curl …", true)
		return nil
	}
	if strings.HasPrefix(arg, "curl ") {
		return m.importCurl(arg)
	}
	path := expandPath(arg, m.opts.Dir)
	data, err := os.ReadFile(path)
	if err != nil {
		m.setStatus(err.Error(), true)
		return nil
	}
	if strings.HasPrefix(strings.TrimSpace(string(data)), "curl ") {
		return m.importCurl(string(data))
	}
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(data, &probe); err != nil {
		m.setStatus(path+" isn't JSON", true)
		return nil
	}
	if raw, ok := probe["collection"]; ok {
		data = raw
		_ = json.Unmarshal(raw, &probe)
	}
	switch {
	case probe["info"] != nil && probe["item"] != nil:
		var c postman.Collection
		if err := json.Unmarshal(data, &c); err != nil {
			m.setStatus(err.Error(), true)
			return nil
		}
		return m.importCollection(&c, true)
	case probe["values"] != nil:
		var e postman.Environment
		if err := json.Unmarshal(data, &e); err != nil {
			m.setStatus(err.Error(), true)
			return nil
		}
		return m.newEnvFrom(&e)
	}
	m.setStatus(path+" isn't a Postman collection or environment", true)
	return nil
}

// importCollection makes a collection in the workspace from an export;
// record makes it a change u takes back.
func (m *Model) importCollection(c *postman.Collection, record bool) tea.Cmd {
	if m.st.API == nil {
		m.setStatus("importing into a workspace needs Postman; open the file with posthero FILE instead", true)
		return nil
	}
	c.Info.PostmanID = ""
	c.Walk(func(it *postman.Item, _ []*postman.Item) bool {
		it.ID, it.UID = "", ""
		return true
	})
	api := m.api
	ws := m.st.Workspace.ID
	return m.run("importing "+c.Info.Name, func(ctx context.Context) tea.Msg {
		sum, err := api.CreateCollection(ctx, ws, c)
		if err != nil {
			return errorMsg{"Not imported", err}
		}
		full, err := api.Collection(ctx, sum.UID)
		if err != nil {
			return errorMsg{"Imported, but not fetched", err}
		}
		sum.UpdatedAt = full.Info.UpdatedAt
		return cmdMsg{func() tea.Cmd {
			made := &store.Col{Summary: sum, Data: full}
			m.st.PutCol(made)
			m.tree.SetData(m.st.Collections())
			m.tree.Select(sum.ID)
			if record {
				m.pushUndo(undoEntry{what: "make " + full.Info.Name, kind: undoRestore, restore: func() tea.Cmd {
					return m.dropCollection(made, nil, "took back: make "+full.Info.Name)
				}})
			}
			verb := "imported "
			if len(full.Item) == 0 {
				verb = "made "
			}
			m.setStatus(verb+full.Info.Name, false)
			return nil
		}}
	})
}

// newEnvFrom makes an environment from an export.
func (m *Model) newEnvFrom(e *postman.Environment) tea.Cmd {
	base := &store.Env{Data: e}
	return m.newEnv(e.Name, base, true)
}

// cmdCollection makes a collection: :collection new NAME.
func cmdCollection(m *Model, arg string, _ bool) tea.Cmd {
	verb, name, _ := strings.Cut(arg, " ")
	name = strings.TrimSpace(name)
	if verb != "new" || name == "" {
		m.setStatus(":collection new NAME", true)
		return nil
	}
	return m.importCollection(&postman.Collection{Info: postman.Info{Name: name}}, true)
}

// cmdExport writes the collection (or the environment, with "env") to a
// file.
func cmdExport(m *Model, arg string, _ bool) tea.Cmd {
	if arg == "" {
		m.setStatus(":export FILE writes the collection; :export env FILE the environment", true)
		return nil
	}
	if f := strings.Fields(arg); len(f) == 2 && f[0] == "env" {
		e := m.st.Env(m.envFor(m.currentCol()))
		if e == nil || e.Data == nil {
			m.setStatus("no environment picked", true)
			return nil
		}
		data, err := store.EnvFileData(e.Data)
		if err != nil {
			m.setStatus(err.Error(), true)
			return nil
		}
		return m.writeExport(expandPath(f[1], m.opts.Dir), data)
	}
	n := m.treeNode()
	if n == nil || n.Col.Data == nil {
		return nil
	}
	data, err := store.FileData(n.Col.Data)
	if err != nil {
		m.setStatus(err.Error(), true)
		return nil
	}
	return m.writeExport(expandPath(arg, m.opts.Dir), data)
}

func (m *Model) writeExport(path string, data []byte) tea.Cmd {
	if err := os.WriteFile(path, data, 0o644); err != nil {
		m.setStatus(err.Error(), true)
		return nil
	}
	m.setStatus(fmt.Sprintf("wrote %s (%d KB)", path, len(data)/1024+1), false)
	return nil
}
