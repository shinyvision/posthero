package run

import "github.com/shinyvision/posthero/internal/postman"

// Snapshot isolates one execution from edits made while it is running. Only
// the request and its ancestors are needed by Execute; siblings and saved
// responses can be arbitrarily large and are never read by a single send.
// Parents are supplied by the caller so drafts need not exist in the tree.
func Snapshot(collection *postman.Collection, item *postman.Item, parents []*postman.Item) Target {
	t := Target{Parents: make([]*postman.Item, len(parents))}
	if collection != nil {
		shallow := *collection
		shallow.Item = nil
		t.Collection = shallow.Clone()
	}
	for i, p := range parents {
		shallow := *p
		shallow.Item, shallow.Response = nil, nil
		t.Parents[i] = shallow.Clone()
	}
	shallow := *item
	shallow.Item, shallow.Response = nil, nil
	t.Item = shallow.Clone()
	// Retain a coherent tree for code that inspects the execution's context.
	child := t.Item
	for i := len(t.Parents) - 1; i >= 0; i-- {
		t.Parents[i].Item = []*postman.Item{child}
		child = t.Parents[i]
	}
	if t.Collection != nil {
		t.Collection.Item = []*postman.Item{child}
	}
	return t
}
