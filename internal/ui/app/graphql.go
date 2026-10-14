package app

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/shinyvision/posthero/internal/auth"
	"github.com/shinyvision/posthero/internal/editor"
	"github.com/shinyvision/posthero/internal/graphql"
	"github.com/shinyvision/posthero/internal/postman"
	"github.com/shinyvision/posthero/internal/reqdoc"
	"github.com/shinyvision/posthero/internal/run"
	"github.com/shinyvision/posthero/internal/send"
)

// schemaEntry is what posthero knows of a GraphQL server's schema.
type schemaEntry struct {
	schema   *graphql.Schema
	err      error
	at       time.Time
	fetching bool
}

// schemaMsg delivers a fetched schema.
type schemaMsg struct {
	url    string
	schema *graphql.Schema
	err    error
	// show says the fetch was asked for (:schema): say how it went.
	show bool
}

// isGraphQL reports whether a request sends GraphQL.
func isGraphQL(it *postman.Item) bool {
	return it != nil && it.Request != nil && it.Request.Body != nil && it.Request.Body.Mode == "graphql"
}

// schemaURL is the address a GraphQL request goes to, which its schema is
// known by.
func (m *Model) schemaURL(colID string, it *postman.Item) string {
	if !isGraphQL(it) || it.Request.URL == nil {
		return ""
	}
	return send.ResolveURL(it.Request.URL, m.scopes(colID).Resolve)
}

// ensureSchema fetches the schema of a GraphQL request's server in the
// background, as Postman does, when it isn't known yet. It never asks to
// log in for it, and on production it waits until you sent a request
// there yourself (sent says that just happened).
func (m *Model) ensureSchema(colID string, it *postman.Item, sent bool) tea.Cmd {
	url := m.schemaURL(colID, it)
	if url == "" || strings.Contains(url, "{{") {
		return nil
	}
	if e := m.schemas[url]; e != nil {
		switch {
		case e.fetching, e.schema != nil:
			return nil
		case e.err != nil && !sent && time.Since(e.at) < 5*time.Minute:
			return nil // it failed lately; a send of your own tries again
		}
	}
	if !sent && (isProd(m.envName(colID)) || !m.loggedInFor(colID, it)) {
		return nil
	}
	return m.fetchSchema(colID, it, url, false)
}

// loggedInFor reports whether a request can be sent without logging in:
// it needs no OAuth 2.0 token, or the token it needs is good or can be
// refreshed. Looking around never logs anyone in.
func (m *Model) loggedInFor(colID string, it *postman.Item) bool {
	c, _, parents := m.findItem(colID, it.ID)
	if c == nil || c.Data == nil {
		return false
	}
	a, _ := send.EffectiveAuth(it, parents, c.Data)
	if a == nil || a.Type != "oauth2" {
		return true
	}
	cfg := auth.ConfigFrom(auth.Resolve(a, m.scopes(colID).Resolve))
	t := m.authm.Tokens.Get(cfg.Key())
	now := time.Now()
	return t != nil && (t.Valid(now) || t.CanRefresh(now))
}

// fetchSchema sends the introspection query with the request's address,
// auth and pre-request scripts.
func (m *Model) fetchSchema(colID string, it *postman.Item, url string, show bool) tea.Cmd {
	c, _, parents := m.findItem(colID, it.ID)
	if c == nil || c.Data == nil {
		return nil
	}
	e := &schemaEntry{fetching: true, at: time.Now()}
	if prev := m.schemas[url]; prev != nil {
		e.schema = prev.schema // still good while the new one comes
	}
	m.schemas[url] = e
	target := run.Snapshot(c.Data, it, parents)
	noTests(target.Collection)
	item := target.Item
	item.Request.Body = &postman.Body{Mode: "graphql", GraphQL: &postman.GraphQL{Query: postman.Str(graphql.IntrospectionQuery)}}
	item.Event = withoutTests(item.Event)
	set := m.scopes(colID)
	r := *m.runner
	r.Packages, _ = m.remote.(postman.PackageProvider)
	parent := m.ctx
	// Fetched by itself, a schema never logs anyone in; :schema may ask.
	prompt := auth.NoLogin
	if show {
		prompt = m.prompter()
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(parent, 30*time.Second)
		defer cancel()
		ex := r.Execute(ctx, target, set, run.Options{Prompt: prompt})
		switch {
		case ex.Err != nil:
			return schemaMsg{url: url, err: ex.Err, show: show}
		case ex.Response == nil:
			return schemaMsg{url: url, err: fmt.Errorf("no answer"), show: show}
		case ex.Response.Status >= 300:
			return schemaMsg{url: url, err: fmt.Errorf("the server answered %d %s", ex.Response.Status, ex.Response.StatusText), show: show}
		}
		s, err := graphql.Parse(ex.Response.Body)
		return schemaMsg{url: url, schema: s, err: err, show: show}
	}
}

func noTests(c *postman.Collection) {
	c.Event = withoutTests(c.Event)
	c.Walk(func(it *postman.Item, _ []*postman.Item) bool {
		it.Event = withoutTests(it.Event)
		return true
	})
}

func withoutTests(events []postman.Event) []postman.Event {
	var out []postman.Event
	for _, e := range events {
		if e.Listen != "test" {
			out = append(out, e)
		}
	}
	return out
}

func (m *Model) gotSchema(msg schemaMsg) tea.Cmd {
	e := &schemaEntry{schema: msg.schema, err: msg.err, at: time.Now()}
	if old := m.schemas[msg.url]; msg.err != nil && old != nil && old.schema != nil {
		e.schema = old.schema // a failed fetch doesn't take a working one away
	}
	m.schemas[msg.url] = e
	m.updateCompletions()
	if msg.show {
		if msg.err != nil {
			m.setStatus("couldn't fetch the schema of "+msg.url+": "+msg.err.Error(), true)
		} else {
			m.setStatus(fmt.Sprintf("the schema of %s: %d types · <C-x><C-o> completes fields, K says what one is", msg.url, len(msg.schema.Types)), false)
		}
	}
	return nil
}

// openSchema is the schema for the open request, nil when unknown.
func (m *Model) openSchema() *graphql.Schema {
	if m.open == nil {
		return nil
	}
	_, it, _ := m.findItem(m.open.colID, m.open.itemID)
	if e := m.schemas[m.schemaURL(m.open.colID, it)]; e != nil {
		return e.schema
	}
	return nil
}

// cmdSchema fetches the open GraphQL request's schema again.
func cmdSchema(m *Model, _ string, _ bool) tea.Cmd {
	if m.open == nil {
		m.setStatus("open a GraphQL request first", true)
		return nil
	}
	_, it, _ := m.findItem(m.open.colID, m.open.itemID)
	url := m.schemaURL(m.open.colID, it)
	if url == "" {
		m.setStatus("this request doesn't send GraphQL", true)
		return nil
	}
	m.setStatus("fetching the schema of "+url+"…", false)
	return m.fetchSchema(m.open.colID, it, url, true)
}

// queryAt finds the GraphQL query in a request's lines and the byte
// offset in it of a place (row, col): ok is false outside the query.
func queryAt(lines []string, row, col int) (query string, offset, first int, ok bool) {
	q, first, ok := reqdoc.GraphQLQuery(strings.Join(lines, "\n"))
	n := strings.Count(q, "\n") + 1
	if !ok || row < first || row >= first+n {
		return "", 0, 0, false
	}
	qlines := strings.Split(q, "\n")
	for i := 0; i < row-first; i++ {
		offset += len(qlines[i]) + 1
	}
	return q, offset + min(col, len(qlines[row-first])), first, true
}

// completeGraphQL offers what the schema has at the cursor.
func completeGraphQL(s *graphql.Schema, lines []string, row, col int) (int, []editor.Item, bool) {
	q, offset, _, ok := queryAt(lines, row, col)
	if !ok || s == nil {
		return -1, nil, false
	}
	start, sgs := s.Complete(q, offset)
	var items []editor.Item
	for _, sg := range sgs {
		info := sg.Description
		if sg.Deprecated {
			info = strings.TrimSpace("deprecated · " + info)
		}
		items = append(items, editor.Item{Word: sg.Text, Abbr: orName(sg.Label, sg.Text), Menu: sg.Type, Info: info})
	}
	return col - (offset - start), items, true
}

// graphqlInfo says what the name under the cursor is in the schema.
func graphqlInfo(s *graphql.Schema, lines []string, row, col int) (string, bool) {
	q, offset, _, ok := queryAt(lines, row, col)
	if !ok || s == nil {
		return "", false
	}
	return s.Info(q, offset)
}
