package local

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/shinyvision/posthero/internal/postman"
)

// remote is the team's version: what Postman has now. A collection or
// environment that didn't change since the base is the base's.
type remote struct {
	cols    map[string]Remote
	envs    map[string]RemoteEnv
	globals []postman.EnvValue
}

// fetch gets the team's version: every collection, and the environments
// that changed since the base (by their time) and, whatever their time
// says, those that changed here: Postman's list may not show a change made
// a moment ago, and a push must not take the base for the team's version
// of what it is about to send. A collection's time says nothing: Postman
// moves it when the collection itself changes, not what is in it.
func (c *Copy) fetch(ctx context.Context, api postman.API) (*remote, error) {
	fresh := map[string]bool{}
	for _, ch := range c.Changes() {
		fresh[ch.Col], fresh[ch.Env] = true, true
	}
	sums, err := api.Collections(ctx, c.ws.ID)
	if err != nil {
		return nil, err
	}
	envSums, err := api.Environments(ctx, c.ws.ID)
	if err != nil {
		return nil, err
	}
	// An environment that didn't change since the base is the base's; the
	// rest is fetched.
	r := &remote{cols: map[string]Remote{}, envs: map[string]RemoteEnv{}}
	getCols := sums
	var getEnvs []postman.Summary
	c.mu.Lock()
	for _, sum := range envSums {
		if b := c.base.Envs[sum.ID]; b != nil && b.UpdatedAt == sum.UpdatedAt && !fresh[sum.ID] {
			r.envs[sum.ID] = RemoteEnv{UID: sum.UID, UpdatedAt: sum.UpdatedAt, Data: b.Env.Clone()}
		} else {
			getEnvs = append(getEnvs, sum)
		}
	}
	c.mu.Unlock()
	var mu sync.Mutex
	var wg sync.WaitGroup
	var errs []error
	sem := make(chan struct{}, 6)
	get := func(f func() error) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			if err := f(); err != nil {
				mu.Lock()
				errs = append(errs, err)
				mu.Unlock()
			}
		}()
	}
	for _, sum := range getCols {
		get(func() error {
			col, err := api.Collection(ctx, sum.UID)
			if err != nil {
				return fmt.Errorf("%s: %w", sum.Name, err)
			}
			mu.Lock()
			r.cols[sum.ID] = Remote{UID: sum.UID, UpdatedAt: sum.UpdatedAt, Data: col}
			mu.Unlock()
			return nil
		})
	}
	for _, sum := range getEnvs {
		get(func() error {
			e, err := api.Environment(ctx, sum.UID)
			if err != nil {
				return fmt.Errorf("environment %s: %w", sum.Name, err)
			}
			mu.Lock()
			r.envs[sum.ID] = RemoteEnv{UID: sum.UID, UpdatedAt: sum.UpdatedAt, Data: e}
			mu.Unlock()
			return nil
		})
	}
	get(func() error {
		g, err := api.Globals(ctx, c.ws.ID)
		mu.Lock()
		r.globals = g
		mu.Unlock()
		return err
	})
	wg.Wait()
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	return r, ctx.Err()
}

// tree makes the collection a base holds.
func (b *ColBase) tree() *postman.Collection {
	c := b.Props.Clone()
	c.Item = buildTree(b.Items)
	return c
}

// buildTree puts flat items back in their folders, in order.
func buildTree(items map[string]*ItemBase) []*postman.Item {
	kids := map[string][]string{}
	for id, it := range items {
		parent := it.Parent
		if parent != "" && items[parent] == nil {
			parent = ""
		}
		kids[parent] = append(kids[parent], id)
	}
	var build func(parent string) []*postman.Item
	build = func(parent string) []*postman.Item {
		ids := kids[parent]
		sort.SliceStable(ids, func(a, b int) bool {
			if items[ids[a]].Order != items[ids[b]].Order {
				return items[ids[a]].Order < items[ids[b]].Order
			}
			return ids[a] < ids[b]
		})
		out := make([]*postman.Item, 0, len(ids))
		for _, id := range ids {
			it := items[id].Item.Clone()
			it.Folder = items[id].Folder
			if it.Folder {
				it.Item = build(id)
			}
			out = append(out, it)
		}
		return out
	}
	return build("")
}

// Pulled says what a pull did.
type Pulled struct {
	// Took counts what came in from the team; Kept what changed here and
	// stays (to push).
	Took, Kept int
}

// Pull takes in what the team changed since the base: everything that
// didn't change here becomes the team's version. What changed here stays,
// to be pushed (and, if the team changed it too, to be asked about then).
func (c *Copy) Pull(ctx context.Context, api postman.API) (Pulled, error) {
	r, err := c.fetch(ctx, api)
	if err != nil {
		return Pulled{}, err
	}
	return c.merge(r), nil
}

// merge takes a fetched team's version into the copy and the base.
func (c *Copy) merge(r *remote) Pulled {
	c.mu.Lock()
	defer c.mu.Unlock()
	var p Pulled
	cols, envs, globals := c.srv.Contents(c.ws.ID)
	local := map[string]*postman.Collection{}
	for _, l := range cols {
		local[l.Info.PostmanID] = l
	}
	ids := map[string]bool{}
	for id := range local {
		ids[id] = true
	}
	for id := range c.base.Cols {
		ids[id] = true
	}
	for id := range r.cols {
		ids[id] = true
	}
	for id := range ids {
		l, b := local[id], c.base.Cols[id]
		s, inS := r.cols[id]
		switch {
		case l != nil && b != nil && inS:
			m, nb, took, kept := mergeCol(l, b, s)
			p.Took += took
			p.Kept += kept
			if took > 0 {
				c.srv.PutCollection(c.ws.ID, m)
			}
			c.base.Cols[id] = nb
		case l != nil && b != nil && !inS:
			// Gone from Postman: gone here too, unless it changed here.
			if len(colChanges(l, b)) == 0 {
				c.srv.RemoveCollection(id)
				delete(c.base.Cols, id)
				p.Took++
			} else {
				p.Kept++
			}
		case l != nil && b == nil && inS:
			// Pushed: what Postman has is the base now.
			c.base.setCol(s)
		case l == nil && b != nil && !inS:
			delete(c.base.Cols, id) // deleted both here and in Postman
		case l == nil && b == nil && inS:
			c.srv.PutCollection(c.ws.ID, s.Data)
			c.base.setCol(s)
			p.Took++
		case l == nil && b != nil && inS:
			p.Kept++ // deleted here: to push
		default:
			if l != nil {
				p.Kept++ // made here: to push
			}
		}
	}

	lenv := map[string]*postman.Environment{}
	for _, e := range envs {
		lenv[e.ID] = e
	}
	eids := map[string]bool{}
	for id := range lenv {
		eids[id] = true
	}
	for id := range c.base.Envs {
		eids[id] = true
	}
	for id := range r.envs {
		eids[id] = true
	}
	for id := range eids {
		l, b := lenv[id], c.base.Envs[id]
		s, inS := r.envs[id]
		switch {
		case l != nil && b != nil && inS:
			switch {
			case sameEnv(l, b.Env):
				if !sameEnv(s.Data, b.Env) {
					c.srv.PutEnvironment(c.ws.ID, s.Data)
					p.Took++
				}
				c.base.Envs[id] = &EnvBase{UID: s.UID, UpdatedAt: s.UpdatedAt, Env: s.Data.Clone()}
			case sameEnv(l, s.Data):
				c.base.Envs[id] = &EnvBase{UID: s.UID, UpdatedAt: s.UpdatedAt, Env: s.Data.Clone()}
			default:
				p.Kept++
			}
		case l != nil && b != nil && !inS:
			if sameEnv(l, b.Env) {
				c.srv.RemoveEnvironment(id)
				delete(c.base.Envs, id)
				p.Took++
			} else {
				p.Kept++
			}
		case l != nil && b == nil && inS:
			c.base.Envs[id] = &EnvBase{UID: s.UID, UpdatedAt: s.UpdatedAt, Env: s.Data.Clone()}
		case l == nil && b != nil && !inS:
			delete(c.base.Envs, id)
		case l == nil && b == nil && inS:
			c.srv.PutEnvironment(c.ws.ID, s.Data)
			c.base.Envs[id] = &EnvBase{UID: s.UID, UpdatedAt: s.UpdatedAt, Env: s.Data.Clone()}
			p.Took++
		case l == nil && b != nil && inS:
			p.Kept++
		default:
			if l != nil {
				p.Kept++
			}
		}
	}

	switch {
	case sameValues(globals, c.base.Globals):
		if !sameValues(r.globals, c.base.Globals) {
			c.srv.PutGlobals(c.ws.ID, r.globals)
			p.Took++
		}
		c.base.Globals = r.globals
	case sameValues(globals, r.globals):
		c.base.Globals = r.globals
	default:
		p.Kept++
	}
	_ = c.saveBase()
	return p
}

// mergeCol takes the team's version of a collection into the local one:
// m is the result, nb the new base. took counts what came in, kept what
// changed here and stays.
func mergeCol(l *postman.Collection, b *ColBase, s Remote) (m *postman.Collection, nb *ColBase, took, kept int) {
	m = l.Clone()
	nb = &ColBase{UID: s.UID, UpdatedAt: s.UpdatedAt, Props: b.Props, Items: map[string]*ItemBase{}}
	sp := props(s.Data)
	switch {
	case sameProps(l, b.Props):
		if !sameProps(sp, b.Props) {
			setProps(m, sp)
			took++
		}
		nb.Props = sp
	case sameProps(l, sp):
		nb.Props = sp
	default:
		kept++
	}
	li, si := flat(l), flat(s.Data)
	lb := func(x string) bool { return li[x] != nil && b.Items[x] != nil }
	sb := func(x string) bool { return si[x] != nil && b.Items[x] != nil }
	// What changed here, by item, to keep: its content, or its place.
	changedHere := map[string]bool{}
	movedHere := map[string]bool{}
	for id, it := range li {
		bi := b.Items[id]
		if bi == nil || !sameItem(it.Item, bi.Item) {
			changedHere[id] = true
		}
		if bi != nil && place(li, id, lb) != place(b.Items, id, lb) {
			movedHere[id] = true
		}
	}

	// Items in both the base and the team's version.
	for id, sit := range si {
		lit, bi := li[id], b.Items[id]
		switch {
		case bi != nil && lit != nil:
			if changedHere[id] {
				if sameItem(lit.Item, sit.Item) {
					nb.Items[id] = sit
				} else {
					nb.Items[id] = bi
					kept++
				}
			} else {
				if !sameItem(sit.Item, bi.Item) {
					replaceContent(m, id, sit.Item)
					took++
				}
				nb.Items[id] = sit
			}
			if movedHere[id] {
				if place(li, id, func(x string) bool { return li[x] != nil && si[x] != nil }) == place(si, id, func(x string) bool { return li[x] != nil && si[x] != nil }) {
					nb.Items[id] = withPlace(nb.Items[id], sit)
				} else {
					nb.Items[id] = withPlace(nb.Items[id], bi)
					kept++
				}
			} else if place(si, id, sb) != place(b.Items, id, sb) {
				moveTo(m, id, si, sit)
				took++
			}
		case bi == nil && lit != nil:
			nb.Items[id] = sit // pushed from here
		case bi == nil && lit == nil:
			// New in Postman: in place after what is before it there.
			insertLike(m, id, si, sit)
			nb.Items[id] = sit
			took++
		case bi != nil && lit == nil:
			nb.Items[id] = bi // deleted here: to push (or ask about)
			kept++
		}
	}
	// Items the team deleted.
	for id, bi := range b.Items {
		if si[id] != nil {
			continue
		}
		if li[id] == nil {
			continue // deleted both here and there
		}
		if changedHere[id] || movedHere[id] || changedInside(id, li, changedHere) {
			nb.Items[id] = bi // changed here: to push (or ask about)
			kept++
			continue
		}
		m.Remove(id)
		took++
	}
	if !colSame(nb.tree(), s.Data) {
		// The base holds older versions of what changed on both sides: the
		// next fetch must get the team's version, not take the base for it.
		nb.UpdatedAt = ""
	}
	return m, nb, took, kept
}

// setProps gives a collection another's own settings.
func setProps(c, p *postman.Collection) {
	items := c.Item
	id := c.Info.PostmanID
	*c = *p.Clone()
	c.Item = items
	c.Info.PostmanID = id
}

// replaceContent gives an item another version's content, keeping what is
// in it.
func replaceContent(c *postman.Collection, id string, content *postman.Item) {
	it, _ := c.Find(id)
	if it == nil {
		return
	}
	kids := it.Item
	*it = *content.Clone()
	it.Folder = content.Folder
	it.Item = kids
}

// withPlace is an item of the base with another's place.
func withPlace(it, at *ItemBase) *ItemBase {
	out := *it
	out.Parent, out.Order = at.Parent, at.Order
	return &out
}

// changedInside says something in a folder changed here.
func changedInside(id string, items map[string]*ItemBase, changed map[string]bool) bool {
	for cid, it := range items {
		for p := it.Parent; p != ""; p = items[p].Parent {
			if p == id && changed[cid] {
				return true
			}
			if items[p] == nil {
				break
			}
		}
	}
	return false
}

// moveTo moves an item of c to where it is in the team's version: into the
// same folder (if c has it), after the same item (if c has it there).
func moveTo(c *postman.Collection, id string, si map[string]*ItemBase, sit *ItemBase) {
	it := c.Remove(id)
	if it == nil {
		return
	}
	putLike(c, it, si, sit)
}

// insertLike puts an item new in the team's version into c, where it is
// there.
func insertLike(c *postman.Collection, id string, si map[string]*ItemBase, sit *ItemBase) {
	if found, _ := c.Find(id); found != nil {
		return
	}
	it := sit.Item.Clone()
	it.Folder = sit.Folder
	putLike(c, it, si, sit)
}

func putLike(c *postman.Collection, it *postman.Item, si map[string]*ItemBase, sit *ItemBase) {
	var parent *postman.Item
	if sit.Parent != "" {
		parent, _ = c.Find(sit.Parent)
		if parent != nil && !parent.Folder {
			parent = nil
		}
	}
	list := *c.Container(parent)
	// After the nearest item before it there that c has in that folder.
	at := 0
	best := -1
	for i, x := range list {
		if o := si[x.ID]; o != nil && o.Parent == sit.Parent && o.Order < sit.Order && o.Order > best {
			best, at = o.Order, i+1
		}
	}
	c.Insert(parent, at, it)
}

// Conflict is something both changed here and the team changed in
// Postman, differently. Mine and Theirs are nil for a side that deleted
// it.
type Conflict struct {
	// Key names it, the same in every plan.
	Key string
	// Kind is "request", "folder", "collection", "environment" or
	// "globals".
	Kind string
	// What says how: "changed here and in Postman", "changed here, deleted
	// in Postman" or "deleted here, changed in Postman".
	What string
	// Col and Item, or Env, name what it is; Name is where (for showing).
	Col, Item, Env string
	Name           string
	// The three versions of a request or folder (Base, Mine, Theirs), of a
	// collection's own settings (BaseCol, …), or of an environment or the
	// globals (BaseEnv, …; the globals as an environment).
	Base, Mine, Theirs          *postman.Item
	BaseCol, MineCol, TheirsCol *postman.Collection
	BaseEnv, MineEnv, TheirsEnv *postman.Environment

	op       op  // what pushing it anyway does
	move     *op // an accompanying move needs the same approval
	reviewed *remote
}

// Plan is what a push would do: changes that can go as they are, and
// conflicts to answer first.
type Plan struct {
	ops        []op
	Conflicts  []*Conflict
	r          *remote
	localState string
}

// Changes counts what the plan pushes without asking.
func (p *Plan) Changes() int { return len(p.ops) }

// op is one change to make in Postman.
type op struct {
	kind  string // "createCol", "deleteCol", "patchCol", "create", "update", "delete", "move", "createEnv", "putEnv", "deleteEnv", "putGlobals"
	key   string // what it changes, as Conflict.Key
	col   string // local collection id
	uid   string // the collection's (or environment's) uid in Postman
	item  *postman.Item
	env   *postman.Environment
	full  *postman.Collection // createCol: the whole collection; patchCol: its settings
	vals  []postman.EnvValue
	order int // for sorting: creates parents first, deletes last

	// For an item: its folder and the item before it here (ids; empty at
	// the top or start), whether that is a folder, whether it is the last
	// in its folder, and the team's version (its examples, for updates).
	parent, prev string
	prevFolder   bool
	last         bool
	theirs       *postman.Item
	theirsCol    *postman.Collection // patchCol: the team's settings
}

// Plan works out what a push would do, fetching the team's version.
func (c *Copy) Plan(ctx context.Context, api postman.API) (*Plan, error) {
	r, err := c.fetch(ctx, api)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	cols, envs, globals := c.srv.Contents(c.ws.ID)
	p := &Plan{r: r, localState: localStateOf(cols, envs, globals)}
	local := map[string]*postman.Collection{}
	for _, l := range cols {
		local[l.Info.PostmanID] = l
	}
	ids := map[string]bool{}
	for id := range local {
		ids[id] = true
	}
	for id := range c.base.Cols {
		ids[id] = true
	}
	for _, id := range sortedKeys(ids) {
		l, b := local[id], c.base.Cols[id]
		s, inS := r.cols[id]
		switch {
		case l != nil && b == nil:
			if !inS {
				p.ops = append(p.ops, op{kind: "createCol", key: "collection:" + id, col: id, full: l})
			}
		case l == nil && b != nil:
			if !inS {
				continue
			}
			o := op{kind: "deleteCol", key: "collection:" + id, col: id, uid: s.UID, order: 3}
			// (Not by its time: what is in it changes without moving that.)
			if colSame(s.Data, b.tree()) {
				p.ops = append(p.ops, o)
			} else {
				p.Conflicts = append(p.Conflicts, &Conflict{Key: "collection:" + id, Kind: "collection", What: "deleted here, changed in Postman", Col: id, Name: b.Props.Info.Name, BaseCol: b.Props, TheirsCol: props(s.Data), op: o})
			}
		case l != nil && b != nil && !inS:
			if len(colChanges(l, b)) > 0 {
				p.Conflicts = append(p.Conflicts, &Conflict{Key: "collection:" + id, Kind: "collection", What: "changed here, deleted in Postman", Col: id, Name: l.Info.Name, BaseCol: b.Props, MineCol: props(l), op: op{kind: "createCol", key: "collection:" + id, col: id, full: l}})
			}
		case l != nil && b != nil:
			c.planCol(p, l, b, s)
		}
	}

	lenv := map[string]*postman.Environment{}
	for _, e := range envs {
		lenv[e.ID] = e
	}
	eids := map[string]bool{}
	for id := range lenv {
		eids[id] = true
	}
	for id := range c.base.Envs {
		eids[id] = true
	}
	for _, id := range sortedKeys(eids) {
		l, b := lenv[id], c.base.Envs[id]
		s, inS := r.envs[id]
		key := "environment:" + id
		switch {
		case l != nil && b == nil:
			if !inS {
				p.ops = append(p.ops, op{kind: "createEnv", key: key, env: l, order: 4})
			}
		case l == nil && b != nil:
			if !inS {
				continue
			}
			o := op{kind: "deleteEnv", key: key, uid: s.UID, env: b.Env, order: 4}
			if sameEnv(s.Data, b.Env) {
				p.ops = append(p.ops, o)
			} else {
				p.Conflicts = append(p.Conflicts, &Conflict{Key: key, Kind: "environment", What: "deleted here, changed in Postman", Env: id, Name: b.Env.Name, BaseEnv: b.Env, TheirsEnv: s.Data, op: o})
			}
		case l != nil && b != nil && !inS:
			if !sameEnv(l, b.Env) {
				p.Conflicts = append(p.Conflicts, &Conflict{Key: key, Kind: "environment", What: "changed here, deleted in Postman", Env: id, Name: l.Name, BaseEnv: b.Env, MineEnv: l, op: op{kind: "createEnv", key: key, env: l, order: 4}})
			}
		case l != nil && b != nil:
			if sameEnv(l, b.Env) || sameEnv(l, s.Data) {
				continue
			}
			put := op{kind: "putEnv", key: key, uid: s.UID, env: l, order: 4}
			if sameEnv(s.Data, b.Env) {
				p.ops = append(p.ops, put)
				continue
			}
			// Both changed: by variable, unless they changed the same one.
			vals, ok := MergeValues(b.Env.Values, l.Values, s.Data.Values)
			if ok && (l.Name == s.Data.Name || l.Name == b.Env.Name || s.Data.Name == b.Env.Name) {
				merged := l.Clone()
				merged.Values = vals
				if l.Name == b.Env.Name {
					merged.Name = s.Data.Name
				}
				put.env = merged
				p.ops = append(p.ops, put)
				continue
			}
			p.Conflicts = append(p.Conflicts, &Conflict{Key: key, Kind: "environment", What: "changed here and in Postman", Env: id, Name: l.Name, BaseEnv: b.Env, MineEnv: l, TheirsEnv: s.Data, op: put})
		}
	}

	if !sameValues(globals, c.base.Globals) && !sameValues(globals, r.globals) {
		put := op{kind: "putGlobals", key: "globals", vals: globals, order: 4}
		switch vals, ok := MergeValues(c.base.Globals, globals, r.globals); {
		case sameValues(r.globals, c.base.Globals):
			p.ops = append(p.ops, put)
		case ok:
			put.vals = vals
			p.ops = append(p.ops, put)
		default:
			asEnv := func(v []postman.EnvValue) *postman.Environment {
				return &postman.Environment{Name: "Globals", Values: v}
			}
			p.Conflicts = append(p.Conflicts, &Conflict{Key: "globals", Kind: "globals", What: "changed here and in Postman", Name: "Globals", BaseEnv: asEnv(c.base.Globals), MineEnv: asEnv(globals), TheirsEnv: asEnv(r.globals), op: put})
		}
	}
	sort.SliceStable(p.ops, func(a, b int) bool { return p.ops[a].order < p.ops[b].order })
	for _, cf := range p.Conflicts {
		cf.reviewed = r
	}
	return p, nil
}

// colSame says two versions of a collection are the same.
func colSame(a, b *postman.Collection) bool {
	if !sameProps(a, b) {
		return false
	}
	fa, fb := flat(a), flat(b)
	if len(fa) != len(fb) {
		return false
	}
	for id, x := range fa {
		y := fb[id]
		if y == nil || !sameItem(x.Item, y.Item) || x.Parent != y.Parent || x.Order != y.Order {
			return false
		}
	}
	return true
}

// planCol works out what to push of a collection both here and in Postman.
func (c *Copy) planCol(p *Plan, l *postman.Collection, b *ColBase, s Remote) {
	id := l.Info.PostmanID
	sp := props(s.Data)
	if !sameProps(l, b.Props) && !sameProps(l, sp) {
		o := op{kind: "patchCol", key: "collection:" + id, col: id, uid: s.UID, full: props(l), theirsCol: sp}
		if sameProps(sp, b.Props) {
			p.ops = append(p.ops, o)
		} else {
			p.Conflicts = append(p.Conflicts, &Conflict{Key: "collection:" + id, Kind: "collection", What: "changed here and in Postman", Col: id, Name: l.Info.Name, BaseCol: b.Props, MineCol: props(l), TheirsCol: sp, op: o})
		}
	}
	li, si := flat(l), flat(s.Data)
	lb := func(x string) bool { return li[x] != nil && b.Items[x] != nil }
	kind := func(f bool) string {
		if f {
			return "folder"
		}
		return "request"
	}
	// In the order of the local tree, so folders are made before what is
	// in them.
	l.Walk(func(it *postman.Item, parents []*postman.Item) bool {
		iid := it.ID
		lit, bi := li[iid], b.Items[iid]
		sit := si[iid]
		name := pathName(l.Info.Name, parents, it.Name)
		key := kind(it.Folder) + ":" + id + ":" + iid
		mk := func(k string, order int) op {
			o := op{kind: k, key: key, col: id, uid: s.UID, item: it, order: order, parent: lit.Parent}
			o.prev, o.prevFolder, o.last = neighbours(li, iid)
			if sit != nil {
				o.theirs = sit.Item
			}
			return o
		}
		switch {
		case bi == nil:
			if sit == nil {
				p.ops = append(p.ops, mk("create", 1))
			}
		case !sameItem(lit.Item, bi.Item):
			switch {
			case sit == nil:
				p.Conflicts = append(p.Conflicts, &Conflict{Key: key, Kind: kind(it.Folder), What: "changed here, deleted in Postman", Col: id, Item: iid, Name: name, Base: bi.Item, Mine: lit.Item, op: mk("create", 1)})
			case sameItem(sit.Item, bi.Item):
				p.ops = append(p.ops, mk("update", 2))
			case sameItem(sit.Item, lit.Item):
			default:
				p.Conflicts = append(p.Conflicts, &Conflict{Key: key, Kind: kind(it.Folder), What: "changed here and in Postman", Col: id, Item: iid, Name: name, Base: bi.Item, Mine: lit.Item, Theirs: sit.Item, op: mk("update", 2)})
			}
		}
		if bi != nil && sit != nil && place(li, iid, lb) != place(b.Items, iid, lb) {
			moving := mk("move", 2)
			both := func(x string) bool { return li[x] != nil && b.Items[x] != nil && si[x] != nil }
			before, mine, theirs := place(b.Items, iid, both), place(li, iid, both), place(si, iid, both)
			var conflict *Conflict
			for _, cf := range p.Conflicts {
				if cf.Key == key {
					conflict = cf
					break
				}
			}
			switch {
			case mine == theirs:
				// The team already put it where we want it.
			case conflict != nil:
				conflict.move = &moving
			case theirs != before:
				cf := &Conflict{Key: key, Kind: kind(it.Folder), What: "changed here and in Postman", Col: id, Item: iid, Name: name, Base: bi.Item, Mine: lit.Item, Theirs: sit.Item, op: moving}
				// A content edit and a conflicting move of the same item are
				// reviewed together, and neither runs when that review is skipped.
				for i, o := range p.ops {
					if o.key == key && o.kind == "update" {
						cf.op, cf.move = o, &moving
						p.ops = append(p.ops[:i], p.ops[i+1:]...)
						break
					}
				}
				p.Conflicts = append(p.Conflicts, cf)
			default:
				p.ops = append(p.ops, moving)
			}
		}
		return true
	})
	// Deleted here.
	for iid, bi := range b.Items {
		if li[iid] != nil || si[iid] == nil {
			continue
		}
		if bi.Parent != "" && li[bi.Parent] == nil && b.Items[bi.Parent] != nil {
			continue // its folder is deleted, and it with it
		}
		key := kind(bi.Folder) + ":" + id + ":" + iid
		o := op{kind: "delete", key: key, col: id, uid: s.UID, item: withFolder(bi), order: 3}
		if sameItem(si[iid].Item, bi.Item) && !changedInside(iid, si, changedSince(si, b.Items)) {
			p.ops = append(p.ops, o)
			continue
		}
		p.Conflicts = append(p.Conflicts, &Conflict{Key: key, Kind: kind(bi.Folder), What: "deleted here, changed in Postman", Col: id, Item: iid, Name: basePath(l.Info.Name, b.Items, iid), Base: bi.Item, Theirs: si[iid].Item, op: o})
	}
}

// neighbours are the item before an item in its folder and whether it is
// the last there, counting only its own kind: Postman orders a folder's
// folders and its requests apart (folders first), and places a request
// only relative to a request, a folder to a folder.
func neighbours(items map[string]*ItemBase, id string) (prev string, prevFolder, last bool) {
	it := items[id]
	last = true
	at := -1
	for oid, o := range items {
		if o.Parent != it.Parent || oid == id || o.Folder != it.Folder {
			continue
		}
		if o.Order < it.Order && o.Order > at {
			prev, prevFolder, at = oid, o.Folder, o.Order
		}
		if o.Order > it.Order {
			last = false
		}
	}
	return prev, prevFolder, last
}

// basePath is where an item of the base is, by name.
func basePath(col string, items map[string]*ItemBase, id string) string {
	var names []string
	for x := id; x != ""; {
		it := items[x]
		if it == nil {
			break
		}
		names = append([]string{it.Item.Name}, names...)
		x = it.Parent
	}
	return strings.Join(append([]string{col}, names...), " / ")
}

func withFolder(b *ItemBase) *postman.Item {
	it := b.Item.Clone()
	it.Folder = b.Folder
	return it
}

// changedSince marks the items of a version that differ from the base.
func changedSince(items, base map[string]*ItemBase) map[string]bool {
	out := map[string]bool{}
	for id, it := range items {
		if b := base[id]; b == nil || !sameItem(it.Item, b.Item) {
			out[id] = true
		}
	}
	return out
}

func pathName(col string, parents []*postman.Item, name string) string {
	parts := []string{col}
	for _, p := range parents {
		parts = append(parts, p.Name)
	}
	return strings.Join(append(parts, name), " / ")
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// MergeValues merges variables changed on two sides (mine and theirs) since
// base, by key: what only one side changed is taken; ok is false when both
// changed the same variable differently.
func MergeValues(base, mine, theirs []postman.EnvValue) ([]postman.EnvValue, bool) {
	index := func(vs []postman.EnvValue) map[string]postman.EnvValue {
		m := map[string]postman.EnvValue{}
		for _, v := range vs {
			m[v.Key] = v
		}
		return m
	}
	b, m, t := index(base), index(mine), index(theirs)
	same := func(x, y postman.EnvValue, okx, oky bool) bool {
		return okx == oky && (!okx || fingerprint(x) == fingerprint(y))
	}
	var out []postman.EnvValue
	seen := map[string]bool{}
	pick := func(key string) (postman.EnvValue, bool, bool) {
		bv, inB := b[key]
		mv, inM := m[key]
		tv, inT := t[key]
		switch {
		case same(mv, bv, inM, inB):
			return tv, inT, true
		case same(tv, bv, inT, inB), same(mv, tv, inM, inT):
			return mv, inM, true
		}
		return mv, inM, false
	}
	ok := true
	for _, list := range [][]postman.EnvValue{theirs, mine} {
		for _, v := range list {
			if seen[v.Key] {
				continue
			}
			seen[v.Key] = true
			x, keep, fine := pick(v.Key)
			ok = ok && fine
			if keep {
				out = append(out, x)
			}
		}
	}
	return out, ok
}
