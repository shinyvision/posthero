// Package tree is the left pane: the workspace's collections, their
// folders and requests, and the requests' saved examples, with vim keys to
// move, fold and filter.
package tree

import (
	"slices"
	"strconv"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/shinyvision/posthero/internal/fuzzy"
	"github.com/shinyvision/posthero/internal/graphql"
	"github.com/shinyvision/posthero/internal/postman"
	"github.com/shinyvision/posthero/internal/store"
	"github.com/shinyvision/posthero/internal/ui/lineinput"
	"github.com/shinyvision/posthero/internal/ui/theme"
)

// Kind is what a node is.
type Kind int

// Node kinds.
const (
	Collection Kind = iota
	Folder
	Request
	// Example is a request's saved example, under the request.
	Example
)

// Node is a line of the tree.
type Node struct {
	Kind Kind
	// Col is the node's collection; Item is the folder or request (nil for
	// a collection; for an example, its request), Parents the folders it
	// is in.
	Col     *store.Col
	Item    *postman.Item
	Parents []*postman.Item
	// Example is the example of an Example node.
	Example *postman.Response
	Depth   int
	// match marks the runes of the name a filter matched.
	match []int
}

// ID is what identifies the node: the collection's id, the item's or the
// example's.
func (n *Node) ID() string {
	switch {
	case n.Example != nil:
		return n.Example.ID
	case n.Item != nil:
		return n.Item.ID
	}
	return n.Col.Summary.ID
}

// Name is the node's name.
func (n *Node) Name() string {
	switch {
	case n.Example != nil:
		return n.Example.Name
	case n.Item != nil:
		return n.Item.Name
	}
	return n.Col.Name()
}

// Container reports whether the node holds others (a collection or folder).
func (n *Node) Container() bool { return n.Kind == Collection || n.Kind == Folder }

// Foldable reports whether the node opens and closes: a collection, a
// folder, or a request with examples.
func (n *Node) Foldable() bool {
	return n.Container() || n.Kind == Request && len(n.Item.Response) > 0
}

// Leaf reports whether the node is what opens in the editor: a request or
// an example.
func (n *Node) Leaf() bool { return n.Kind == Request || n.Kind == Example }

// Path is the names from the collection to the node.
func (n *Node) Path() string {
	parts := []string{n.Col.Name()}
	for _, p := range n.Parents {
		parts = append(parts, p.Name)
	}
	if n.Item != nil {
		parts = append(parts, n.Item.Name)
	}
	if n.Example != nil {
		parts = append(parts, n.Example.Name)
	}
	return strings.Join(parts, " / ")
}

// Marks are the little signs on nodes.
type Marks struct {
	// Open is the request in the editor.
	Open string
	// Drafts are requests with changes not saved to Postman.
	Drafts map[string]bool
	// Cut are items waiting for p.
	Cut map[string]bool
	// Status is the status code of each request's last response.
	Status map[string]int
	// Local are collections and items changed in local mode, not pushed.
	Local map[string]bool
}

// Model is the tree.
type Model struct {
	cols     []*store.Col
	expanded map[string]bool
	rows     []*Node
	// showing are the requests whose examples are on rows under them.
	showing map[string]bool
	cursor  int
	top     int
	h       int
	marks   Marks
	filter  string
	prompt  *lineinput.Model // the filter being typed
	pending string           // z or g
	count   int              // typed before a command
	scroll  int              // rows <C-d> and <C-u> move, set by a count; 0 is half the pane
}

// New returns an empty tree; expanded lists open collections and folders.
func New(expanded []string) *Model {
	m := &Model{expanded: map[string]bool{}}
	for _, id := range expanded {
		m.expanded[id] = true
	}
	return m
}

// SetData shows these collections, keeping the cursor on the same node
// when it is still there.
func (m *Model) SetData(cols []*store.Col) {
	cur := ""
	if n := m.Current(); n != nil {
		cur = n.ID()
	}
	m.cols = cols
	m.rebuild()
	if cur != "" {
		m.Select(cur)
	}
}

// SetMarks changes the signs.
func (m *Model) SetMarks(k Marks) { m.marks = k }

// Expanded lists the open collections and folders.
func (m *Model) Expanded() []string {
	out := make([]string, 0, len(m.expanded))
	for id, on := range m.expanded {
		if on {
			out = append(out, id)
		}
	}
	return out
}

// rebuild makes the visible lines: everything under open nodes, or with a
// filter, the requests that match and the folders they are in.
func (m *Model) rebuild() {
	m.rows = m.rows[:0]
	m.showing = map[string]bool{}
	if m.filter != "" {
		m.rebuildFiltered()
		return
	}
	for _, c := range m.cols {
		n := &Node{Kind: Collection, Col: c}
		m.rows = append(m.rows, n)
		if m.expanded[c.Summary.ID] && c.Data != nil {
			m.addItems(c, c.Data.Item, nil, 1)
		}
	}
	m.clamp()
}

func (m *Model) addItems(c *store.Col, items []*postman.Item, parents []*postman.Item, depth int) {
	for _, it := range items {
		kind := Request
		if it.Folder {
			kind = Folder
		}
		m.rows = append(m.rows, &Node{Kind: kind, Col: c, Item: it, Parents: parents, Depth: depth})
		switch {
		case it.Folder && m.expanded[it.ID]:
			m.addItems(c, it.Item, append(parents[:len(parents):len(parents)], it), depth+1)
		case !it.Folder && m.expanded[it.ID] && len(it.Response) > 0:
			m.showing[it.ID] = true
			for _, r := range it.Response {
				m.rows = append(m.rows, &Node{Kind: Example, Col: c, Item: it, Example: r, Parents: parents, Depth: depth + 1})
			}
		}
	}
}

// filterText is what a filter matches a request against.
func filterText(c *store.Col, it *postman.Item, parents []*postman.Item) string {
	var b strings.Builder
	b.WriteString(it.Name)
	b.WriteString(filterContext(c, it, parents))
	return b.String()
}

// exampleText is what a filter matches an example against: its name, then
// what its request is matched by.
func exampleText(c *store.Col, it *postman.Item, r *postman.Response, parents []*postman.Item) string {
	return r.Name + " e.g. " + it.Name + filterContext(c, it, parents)
}

// filterContext is where a request is and what it sends, for a filter.
func filterContext(c *store.Col, it *postman.Item, parents []*postman.Item) string {
	var b strings.Builder
	b.WriteByte(' ')
	b.WriteString(c.Name())
	for _, p := range parents {
		b.WriteByte(' ')
		b.WriteString(p.Name)
	}
	if it.Request != nil {
		b.WriteByte(' ')
		b.WriteString(Badge(it))
		if it.Request.URL != nil {
			b.WriteByte(' ')
			b.WriteString(it.Request.URL.Raw)
		}
	}
	return b.String()
}

func (m *Model) rebuildFiltered() {
	// The requests that match, per collection. When some contain every
	// word as typed, only those show: scattered letters are a fallback.
	type hit struct {
		node  *Node
		exact bool
	}
	perCol := make([][]hit, len(m.cols))
	anyExact := false
	for ci, c := range m.cols {
		if c.Data == nil {
			continue
		}
		c.Data.Walk(func(it *postman.Item, parents []*postman.Item) bool {
			if it.Folder {
				return true
			}
			parents = append([]*postman.Item(nil), parents...)
			try := func(n *Node, name, text string) {
				fm, ok := fuzzy.Score(m.filter, text)
				if !ok {
					return
				}
				runes := len([]rune(name))
				for _, r := range fm.Runes {
					if r < runes {
						n.match = append(n.match, r)
					}
				}
				exact := fuzzy.Contains(m.filter, text)
				anyExact = anyExact || exact
				perCol[ci] = append(perCol[ci], hit{n, exact})
			}
			try(&Node{Kind: Request, Col: c, Item: it, Parents: parents}, it.Name, filterText(c, it, parents))
			for _, r := range it.Response {
				try(&Node{Kind: Example, Col: c, Item: it, Example: r, Parents: parents}, r.Name, exampleText(c, it, r, parents))
			}
			return true
		})
	}
	for ci, c := range m.cols {
		var hits []*Node
		for _, h := range perCol[ci] {
			if h.exact || !anyExact {
				hits = append(hits, h.node)
			}
		}
		if len(hits) == 0 {
			continue
		}
		m.rows = append(m.rows, &Node{Kind: Collection, Col: c})
		var last []*postman.Item
		var request *postman.Item // whose row is the last one
		for _, h := range hits {
			// The folders a hit is in, where they differ from the last hit's.
			same := 0
			for same < len(last) && same < len(h.Parents) && last[same] == h.Parents[same] {
				same++
			}
			for i := same; i < len(h.Parents); i++ {
				m.rows = append(m.rows, &Node{Kind: Folder, Col: c, Item: h.Parents[i], Parents: h.Parents[:i], Depth: i + 1})
				request = nil
			}
			h.Depth = len(h.Parents) + 1
			if h.Kind == Example {
				// An example shows under its request.
				if request != h.Item {
					m.rows = append(m.rows, &Node{Kind: Request, Col: c, Item: h.Item, Parents: h.Parents, Depth: h.Depth})
				}
				m.showing[h.Item.ID] = true
				h.Depth++
			}
			request = h.Item
			m.rows = append(m.rows, h)
			last = h.Parents
		}
	}
	m.clamp()
}

func (m *Model) clamp() {
	if m.cursor >= len(m.rows) {
		m.cursor = len(m.rows) - 1
	}
	if m.cursor < 0 {
		m.cursor = 0
	}
}

// Current is the node under the cursor.
func (m *Model) Current() *Node {
	if m.cursor < 0 || m.cursor >= len(m.rows) {
		return nil
	}
	return m.rows[m.cursor]
}

// Select moves the cursor to a node, opening the folders it is in.
func (m *Model) Select(id string) bool {
	for i, n := range m.rows {
		if n.ID() == id {
			m.cursor = i
			return true
		}
	}
	// Open its collection and folders (and for an example, its request),
	// then look again.
	for _, c := range m.cols {
		if c.Data == nil {
			continue
		}
		it, parents := c.Data.Find(id)
		if it == nil {
			if req, _, ps := c.Data.FindExample(id); req != nil {
				it, parents = req, append(ps[:len(ps):len(ps)], req)
			}
		}
		if it != nil {
			m.expanded[c.Summary.ID] = true
			for _, p := range parents {
				m.expanded[p.ID] = true
			}
			if m.filter != "" {
				m.filter = ""
			}
			m.rebuild()
			for i, n := range m.rows {
				if n.ID() == id {
					m.cursor = i
					return true
				}
			}
		}
	}
	return false
}

// Filtering reports whether a filter is being typed.
func (m *Model) Filtering() bool { return m.prompt != nil }

// Filter is the active filter.
func (m *Model) Filter() string { return m.filter }

// Action is what a key asks the app to do.
type Action int

// Actions.
const (
	None Action = iota
	// Open asks to open the request under the cursor (⏎ or l on a request).
	Open
	// Moved says the cursor moved (the app previews the request).
	Moved
	// Pass says the key wasn't the tree's.
	Pass
)

// Key handles a key. Movement and folding happen here; everything else is
// passed to the app.
func (m *Model) Key(k string) Action {
	if m.prompt != nil {
		return m.filterKey(k)
	}
	before := m.cursor
	act := m.key(k)
	if act == None && m.cursor != before {
		return Moved
	}
	return act
}

func (m *Model) filterKey(k string) Action {
	switch k {
	case "<Esc>":
		m.prompt = nil
		if m.filter != "" {
			m.filter = ""
			m.rebuild()
		}
		return Moved
	case "<CR>":
		m.prompt = nil
		// Down to the first request (or example) found.
		for i, n := range m.rows {
			if n.Leaf() && (n.Kind == Example || n.match != nil || !m.showing[n.Item.ID]) {
				m.cursor = i
				break
			}
		}
		return Moved
	case "<Down>", "<C-n>", "<C-j>":
		m.move(1)
		return Moved
	case "<Up>", "<C-p>", "<C-k>":
		m.move(-1)
		return Moved
	}
	m.prompt.Key(k)
	m.setFilter(m.prompt.Value())
	return Moved
}

func (m *Model) setFilter(f string) {
	m.filter = strings.TrimSpace(f)
	m.cursor = 0
	m.rebuild()
	for i, n := range m.rows {
		if n.Leaf() && (n.Kind == Example || n.match != nil || !m.showing[n.Item.ID]) {
			m.cursor = i
			break
		}
	}
}

// StartFilter opens the filter prompt.
func (m *Model) StartFilter() {
	m.prompt = lineinput.New("/", m.filter, nil)
	m.prompt.Insert()
}

// ClearFilter drops the filter.
func (m *Model) ClearFilter() {
	m.prompt = nil
	if m.filter != "" {
		cur := ""
		if n := m.Current(); n != nil {
			cur = n.ID()
		}
		m.filter = ""
		m.rebuild()
		if cur != "" {
			m.Select(cur)
		}
	}
}

// Paste puts pasted text in the filter being typed.
func (m *Model) Paste(s string) {
	if m.prompt != nil {
		m.prompt.Paste(s)
		m.setFilter(m.prompt.Value())
	}
}

func (m *Model) key(k string) Action {
	if m.pending != "" {
		p := m.pending
		m.pending = ""
		typed := m.count
		m.count = 0
		switch p + k {
		case "gg":
			m.cursor = max(1, typed) - 1 // {n}gg goes to row n
			m.clamp()
			return None
		case "za", "zc", "zo", "zO", "zC", "zA":
			if n := m.Current(); n != nil {
				m.fold(n, k)
			}
			return None
		case "zM":
			n := m.Current()
			m.expanded = map[string]bool{}
			m.rebuild()
			m.keepOn(n)
			return None
		case "zR":
			n := m.Current()
			m.expandAll()
			m.keepOn(n)
			return None
		}
		if p == "g" || p == "z" {
			return Pass
		}
	}
	if k >= "1" && k <= "9" || k == "0" && m.count > 0 {
		m.count = m.count*10 + int(k[0]-'0')
		return None
	}
	if k == "g" || k == "z" {
		m.pending = k // the count waits for the second key
		return None
	}
	typed := m.count
	count := max(1, m.count)
	m.count = 0
	switch k {
	case "j", "<Down>", "<C-n>":
		m.move(count)
	case "k", "<Up>", "<C-p>":
		m.move(-count)
	case "G", "<End>":
		m.cursor = len(m.rows) - 1
		if typed > 0 && k == "G" {
			m.cursor = typed - 1 // {n}G goes to row n
			m.clamp()
		}
	case "<Home>":
		m.cursor = 0
	case "<C-d>", "<C-u>":
		if typed > 0 {
			m.scroll = typed
		}
		d := m.scroll
		if d == 0 {
			d = max(1, m.h/2)
		}
		if k == "<C-u>" {
			d = -d
		}
		// Like vim: the view and the cursor move together.
		m.top = max(0, min(m.top+d, len(m.rows)-max(1, m.h)))
		m.move(d)
	case "<C-e>":
		m.Scroll(count)
	case "<C-y>":
		m.Scroll(-count)
	case "<C-f>", "<PageDown>":
		m.move(max(1, m.h-1))
	case "<C-b>", "<PageUp>":
		m.move(-max(1, m.h-1))
	case "l", "<Right>":
		n := m.Current()
		if n == nil {
			return None
		}
		if n.Container() {
			if !m.expanded[n.ID()] {
				m.expanded[n.ID()] = true
				m.rebuild()
			} else {
				m.move(1)
			}
			return None
		}
		if n.Foldable() && !m.expanded[n.ID()] && m.filter == "" {
			// A request's examples open first; l again goes to the editor.
			m.expanded[n.ID()] = true
			m.rebuild()
			return None
		}
		return Open
	case "h", "<Left>":
		n := m.Current()
		if n == nil {
			return None
		}
		if n.Foldable() && m.expanded[n.ID()] && m.filter == "" {
			m.expanded[n.ID()] = false
			m.rebuild()
			return None
		}
		m.toParent(n)
	case "o", "<Tab>":
		n := m.Current()
		if n != nil && n.Foldable() {
			m.fold(n, "a")
			return None
		}
		if n != nil && k == "o" {
			return Open
		}
		if n != nil && n.Kind == Example {
			// Tab on an example folds its request's examples.
			m.fold(n, "c")
			return None
		}
		return Pass
	case "<CR>":
		n := m.Current()
		if n == nil {
			return None
		}
		if n.Container() {
			m.fold(n, "a")
			return None
		}
		return Open
	case "/":
		m.StartFilter()
	case "<Esc>":
		if m.filter != "" {
			m.ClearFilter()
			return Moved
		}
		return Pass
	default:
		return Pass
	}
	return None
}

// keepOn puts the cursor back on n after the rows changed, or on the
// nearest folder or collection holding it that still shows.
func (m *Model) keepOn(n *Node) {
	if n == nil {
		return
	}
	ids := []string{n.ID()}
	if n.Kind == Example {
		ids = append(ids, n.Item.ID)
	}
	for i := len(n.Parents) - 1; i >= 0; i-- {
		ids = append(ids, n.Parents[i].ID)
	}
	ids = append(ids, n.Col.Summary.ID)
	for _, id := range ids {
		for i, r := range m.rows {
			if r.Col == n.Col && r.ID() == id {
				m.cursor = i
				return
			}
		}
	}
}

func (m *Model) move(d int) {
	m.cursor = max(0, min(len(m.rows)-1, m.cursor+d))
}

// toParent moves to the folder or collection the node is in.
func (m *Model) toParent(n *Node) {
	if n.Kind == Collection {
		return
	}
	target := n.Col.Summary.ID
	switch {
	case n.Kind == Example:
		target = n.Item.ID
	case len(n.Parents) > 0:
		target = n.Parents[len(n.Parents)-1].ID
	}
	for i := m.cursor - 1; i >= 0; i-- {
		if m.rows[i].ID() == target {
			m.cursor = i
			return
		}
	}
}

// fold opens or closes a container: a toggles, o opens, c closes, and
// the capitals do it for everything inside too.
func (m *Model) fold(n *Node, how string) {
	if n.Kind == Example {
		// On an example, folding folds its request's examples.
		m.toParent(n)
		n = m.Current()
	}
	if n.Kind == Request && n.Foldable() {
		// A request with examples opens and closes like a folder; the
		// capitals do just that, it has nothing inside to open.
		how = strings.ToLower(how)
	}
	if !n.Foldable() {
		// On a request, closing folds the folder it is in.
		if how != "c" && how != "a" {
			return
		}
		m.toParent(n)
		n = m.Current()
	}
	id := n.ID()
	switch how {
	case "a":
		m.expanded[id] = !m.expanded[id]
	case "o":
		m.expanded[id] = true
	case "c":
		m.expanded[id] = false
	case "O", "C", "A":
		on := how == "O" || how == "A" && !m.expanded[id]
		m.expanded[id] = on
		var items []*postman.Item
		if n.Item != nil {
			items = n.Item.Item
		} else if n.Col.Data != nil {
			items = n.Col.Data.Item
		}
		var walk func([]*postman.Item)
		walk = func(list []*postman.Item) {
			for _, it := range list {
				if it.Folder || len(it.Response) > 0 {
					m.expanded[it.ID] = on
					walk(it.Item)
				}
			}
		}
		walk(items)
	}
	m.rebuild()
	m.Select(id)
}

func (m *Model) expandAll() {
	for _, c := range m.cols {
		m.expanded[c.Summary.ID] = true
		if c.Data == nil {
			continue
		}
		c.Data.Walk(func(it *postman.Item, _ []*postman.Item) bool {
			if it.Folder || len(it.Response) > 0 {
				m.expanded[it.ID] = true
			}
			return true
		})
	}
	m.rebuild()
}

// Expand opens a collection or folder.
func (m *Model) Expand(id string) {
	m.expanded[id] = true
	m.rebuild()
}

// Rows are the visible nodes, for the mouse.
func (m *Model) Rows() []*Node { return m.rows }

// Top is the first visible row.
func (m *Model) Top() int { return m.top }

// Scroll moves the view by d rows, keeping the cursor on screen.
func (m *Model) Scroll(d int) {
	h := max(1, m.h)
	m.top = max(0, min(max(0, len(m.rows)-h), m.top+d))
	m.cursor = max(m.top, min(m.top+h-1, m.cursor))
	m.cursor = max(0, min(len(m.rows)-1, m.cursor))
}

// Toggle opens or closes the folder or collection under the cursor.
func (m *Model) Toggle() {
	if n := m.Current(); n != nil {
		m.fold(n, "a")
	}
}

// SetCursor moves the cursor to a row.
func (m *Model) SetCursor(i int) {
	m.cursor = max(0, min(len(m.rows)-1, i))
}

// Badge is a request's method for the tree: QRY, MUT and SUB for GraphQL
// operations (whose HTTP method says little), the method otherwise.
func Badge(it *postman.Item) string {
	if it.Request == nil {
		return "GET"
	}
	if b := it.Request.Body; b != nil && b.Mode == "graphql" && b.GraphQL != nil {
		// A document with a mutation anywhere in it (after fragments, or
		// next to a query) is a mutation.
		ops := graphql.Operations(string(b.GraphQL.Query))
		switch {
		case slices.Contains(ops, "mutation"):
			return "MUT"
		case slices.Contains(ops, "subscription"):
			return "SUB"
		}
		return "QRY"
	}
	m := strings.ToUpper(it.Request.Method)
	switch m {
	case "":
		return "GET"
	case "DELETE":
		return "DEL"
	case "OPTIONS":
		return "OPT"
	case "PATCH":
		return "PTCH"
	}
	return m
}

// ExampleBadge marks an example in the tree, where a request has its
// method, as Postman marks them.
const ExampleBadge = "e.g."

// BadgeStyle colors a method badge: reads green, writes yellow, deletes
// red.
func BadgeStyle(badge string) lipgloss.Style {
	switch badge {
	case "GET", "QRY", "HEAD", "OPT":
		return theme.Success
	case "POST", "MUT":
		return theme.Warn
	case "PUT", "PTCH":
		return theme.AccentFg
	case "DEL":
		return theme.Error
	case "SUB":
		return theme.MagentaFg
	}
	return theme.Dim
}

// View renders the tree as h lines of width w.
func (m *Model) View(w, h int, focused bool) []string {
	h = max(0, h)
	total := h
	if m.prompt != nil || m.filter != "" {
		h = max(0, h-1) // the filter line
	}
	m.h = h
	if m.cursor < m.top {
		m.top = m.cursor
	}
	if m.cursor >= m.top+h {
		m.top = m.cursor - h + 1
	}
	m.top = max(0, min(m.top, max(0, len(m.rows)-h)))
	var out []string
	for i := m.top; i < len(m.rows) && len(out) < h; i++ {
		out = append(out, m.line(m.rows[i], i == m.cursor, focused, w))
	}
	if len(m.rows) == 0 {
		switch {
		case m.filter != "":
			out = append(out, theme.Dim.Render(" nothing matches"))
		case len(m.cols) == 0:
			out = append(out, theme.Dim.Render(" no collections yet"))
		}
	}
	for len(out) < h {
		out = append(out, "")
	}
	out = out[:h]
	if m.prompt != nil {
		line, _ := m.prompt.View(w)
		out = append(out, line)
	} else if m.filter != "" {
		out = append(out, theme.Dim.Render(theme.Truncate("/"+m.filter+"  (esc clears)", w)))
	}
	return out[:min(len(out), total)]
}

func (m *Model) line(n *Node, cur, focused bool, w int) string {
	indent := strings.Repeat("  ", n.Depth)
	var mark, label string
	name := n.Name()
	switch n.Kind {
	case Collection, Folder:
		mark = theme.Collapsed
		if m.expanded[n.ID()] || m.filter != "" {
			mark = theme.Expanded
		}
		switch {
		case n.Kind == Collection:
			label = theme.Bold.Render(name)
			if n.Col.Data == nil {
				label += theme.Dim.Render("  loading…")
			}
		default: // a folder, as ql shows them: cyan, with what's in it
			label = theme.CyanFg.Render(name) + theme.Dim.Render(" "+strconv.Itoa(len(n.Item.Item)))
		}
	case Request:
		badge := Badge(n.Item)
		label = BadgeStyle(badge).Render(pad(badge, 4)) + " " + highlightMatch(name, n.match)
		mark = "  "
		if k := len(n.Item.Response); k > 0 {
			// Its examples fold under it, like a folder's requests.
			mark = theme.Collapsed
			if m.showing[n.Item.ID] {
				mark = theme.Expanded
			}
			label += theme.Dim.Render(" " + strconv.Itoa(k))
		}
	case Example:
		label = theme.Dim.Render(ExampleBadge) + " " + highlightMatch(name, n.match)
		mark = "  "
	}
	mark = theme.Dim.Render(mark)
	right := ""
	if n.Item != nil {
		id := n.ID()
		if m.marks.Cut[id] {
			label = theme.Dim.Strikethrough(true).Render(name)
		}
		if m.marks.Drafts[id] {
			right += theme.Warn.Render(" +")
		}
		if m.marks.Local[id] {
			right += theme.AccentFg.Render(" ↑")
		}
		switch {
		case n.Kind == Example && n.Example.Code > 0:
			// What the example says comes back.
			right += " " + StatusStyle(n.Example.Code).Render(strconv.Itoa(n.Example.Code))
		case n.Kind == Request:
			if code, ok := m.marks.Status[id]; ok {
				right += " " + StatusStyle(code).Render(strconv.Itoa(code))
			}
		}
		if m.marks.Open == id && n.Leaf() {
			mark = theme.AccentFg.Render("● ")
		}
	}
	if n.Item == nil && n.Col != nil && m.marks.Local[n.Col.Summary.ID] {
		right += theme.AccentFg.Render(" ↑")
	}
	text := " " + indent + mark + label
	room := w - theme.Width(right)
	text = theme.Fit(theme.Truncate(text, room), room) + right
	if cur {
		if focused {
			return theme.Selected.Render(theme.Fit(stripForSelect(text), w))
		}
		return lipgloss.NewStyle().Underline(true).Render(theme.Fit(stripForSelect(text), w))
	}
	return text
}

// stripForSelect keeps the line's text for the reverse-video cursor line.
func stripForSelect(s string) string { return ansi.Strip(s) }

// StatusStyle colors a status code.
func StatusStyle(code int) lipgloss.Style {
	switch {
	case code >= 500:
		return theme.Error
	case code >= 400:
		return theme.Warn
	case code >= 300:
		return theme.AccentFg
	case code >= 200:
		return theme.Success
	}
	return theme.Dim
}

func highlightMatch(name string, match []int) string {
	if len(match) == 0 {
		return name
	}
	runes := []rune(name)
	set := map[int]bool{}
	for _, i := range match {
		set[i] = true
	}
	var b strings.Builder
	for i, r := range runes {
		if set[i] {
			b.WriteString(theme.AccentFg.Bold(true).Render(string(r)))
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func pad(s string, w int) string {
	if len(s) >= w {
		return s
	}
	return s + strings.Repeat(" ", w-len(s))
}
