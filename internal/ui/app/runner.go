package app

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/shinyvision/posthero/internal/history"
	"github.com/shinyvision/posthero/internal/run"
	"github.com/shinyvision/posthero/internal/store"
	"github.com/shinyvision/posthero/internal/ui/runview"
	"github.com/shinyvision/posthero/internal/ui/tree"
	"github.com/shinyvision/posthero/internal/vars"
)

// runStepMsg is a request of a run that finished.
type runStepMsg struct {
	view *runview.Model
	row  runview.Row
}

// runDoneMsg ends a run.
type runDoneMsg struct {
	view         *runview.Model
	sum          run.Summary
	colID, envID string
	// before and set are the run's scopes as it started and as its scripts
	// left them; ws is the workspace it ran in.
	before, set *vars.Set
	ws          string
}

// runFolder runs every request in a folder or collection, once.
func (m *Model) runFolder(n *tree.Node) tea.Cmd {
	if n == nil {
		return nil
	}
	o := run.RunOptions{}
	if n.Kind == tree.Folder {
		o.Folder = n.Item.ID
	}
	return m.startRun(n.Col, n.Name(), o)
}

// cmdRun runs the folder or collection with options: -n N, -d FILE,
// -delay MS, -bail, -noscripts.
func cmdRun(m *Model, arg string, _ bool) tea.Cmd {
	n := m.containerNode()
	if n == nil {
		return nil
	}
	o := run.RunOptions{}
	if n.Kind == tree.Folder {
		o.Folder = n.Item.ID
	}
	f := strings.Fields(arg)
	for i := 0; i < len(f); i++ {
		next := func() string {
			if i+1 < len(f) {
				i++
				return f[i]
			}
			return ""
		}
		switch f[i] {
		case "-n", "-iterations":
			o.Iterations, _ = strconv.Atoi(next())
		case "-d", "-data":
			rows, err := run.LoadData(expandPath(next(), m.opts.Dir))
			if err != nil {
				m.setStatus(err.Error(), true)
				return nil
			}
			o.Data = rows
		case "-delay":
			ms, _ := strconv.Atoi(next())
			o.Delay = time.Duration(ms) * time.Millisecond
		case "-bail":
			o.Bail = true
		case "-noscripts":
			o.NoScripts = true
		default:
			m.setStatus("unknown option "+f[i]+": :run [-n N] [-d FILE] [-delay MS] [-bail] [-noscripts]", true)
			return nil
		}
	}
	return m.startRun(n.Col, n.Name(), o)
}

// startRun runs requests in the background, showing each as it finishes.
func (m *Model) startRun(c *store.Col, title string, o run.RunOptions) tea.Cmd {
	if c.Data == nil {
		return nil
	}
	if m.running != nil {
		m.setStatus("a run is going already; ctrl+c stops it", true)
		return nil
	}
	order := run.Order(c.Data, o.Folder, o.Only)
	if len(order) == 0 {
		m.setStatus("no requests to run in "+title, true)
		return nil
	}
	colID := c.Summary.ID
	env := m.envName(colID)
	if m.settings.Confirm && isProd(env) {
		for _, t := range order {
			if changesData(t.Item) {
				m.setStatus("not running "+title+" on "+env+": it changes things on production. :set noconfirm allows it", true)
				return nil
			}
		}
	}
	iterations := o.Iterations
	if iterations <= 0 {
		iterations = max(1, len(o.Data))
	}
	view := runview.New(title, iterations, len(order))
	m.runv, m.showRun = view, true
	m.focusPane(paneResp)
	ctx, cancel := context.WithCancel(m.ctx)
	m.running = cancel
	envID := m.envFor(colID)
	// One set of scopes for the whole run, so what a script sets reaches
	// the requests after it, in every iteration; the data scope is each
	// iteration's own.
	base := m.st.Scopes(colID, envID, nil, nil)
	before := base.Clone()
	ws := m.st.Workspace.ID
	vlt := m.vault
	vault := vlt.Get
	scopes := func(data map[string]any) *vars.Set {
		set := vars.New(base.Scope(vars.Local), run.DataScope(data), base.Scope(vars.Environment), base.Scope(vars.Collection), base.Scope(vars.Global))
		set.Vault = vault
		return set
	}
	r := *m.runner
	o.Prompt = m.prompter()
	send := func(msg tea.Msg) {
		if m.send != nil {
			m.send(msg)
		}
	}
	// The run works on a copy: the collection can change here meanwhile.
	data := c.Data.Clone()
	m.setStatus("running "+title+"…", false)
	return func() tea.Msg {
		vaultVals := vaultSecrets(vlt)
		sum := r.RunCollection(ctx, data, o, scopes, func(s run.Step) {
			rec := history.FromExecution(s.Execution, colID, env)
			// Masked in what the history keeps.
			rec.Secrets = append(append(before.Secrets(), base.Secrets()...), vaultVals...)
			send(runStepMsg{view: view, row: runview.Row{Iteration: s.Iteration, Item: s.Target.Item, Record: rec}})
		})
		return runDoneMsg{view: view, sum: sum, colID: colID, envID: envID, before: before, set: base, ws: ws}
	}
}

func (m *Model) runStepDone(msg runStepMsg) tea.Cmd {
	msg.view.Add(msg.row)
	rec := msg.row.Record
	if !rec.Skipped {
		_ = m.hist.Add(rec)
		m.last[rec.ItemID] = rec
	}
	m.updateMarks()
	return nil
}

func (m *Model) runDone(msg runDoneMsg) tea.Cmd {
	msg.view.Finish(msg.sum)
	if m.runv == msg.view {
		m.running = nil
	}
	// Keep the variables the run's scripts changed.
	if m.st != nil && msg.set != nil && m.st.Workspace.ID == msg.ws {
		if err := m.st.CommitChanges(msg.before, msg.set, msg.colID, msg.envID); err != nil {
			m.setStatus("couldn't keep the variables the run set: "+err.Error(), true)
		}
	}
	m.refreshHints()
	s := msg.sum
	note := fmt.Sprintf("ran %d %s in %s", s.Requests, plural(s.Requests, "request"), shortDur(s.Duration))
	if s.TestsFailed > 0 {
		note += fmt.Sprintf(" · ✗ %d of %d tests failed", s.TestsFailed, s.TestsFailed+s.TestsPassed)
	} else if s.TestsPassed > 0 {
		note += fmt.Sprintf(" · ✓ %d %s", s.TestsPassed, plural(s.TestsPassed, "test"))
	}
	if s.Errors > 0 {
		note += fmt.Sprintf(" · %d %s", s.Errors, plural(s.Errors, "error"))
	}
	if s.Loop {
		note += " · stopped: setNextRequest went round in circles"
	} else if s.Stopped {
		note += " · stopped"
	}
	m.setStatus(note, s.TestsFailed > 0 || s.Errors > 0)
	return nil
}

func shortDur(d time.Duration) string {
	if d < time.Second {
		return fmt.Sprintf("%d ms", d.Milliseconds())
	}
	return fmt.Sprintf("%.1f s", d.Seconds())
}

// showRunStep shows the run's request under the cursor in the response
// pane; <BS> goes back to the list.
func (m *Model) showRunStep() tea.Cmd {
	row := m.runv.Current()
	if row == nil {
		return nil
	}
	m.showRun = false
	m.resp.Show(row.Record)
	return nil
}
