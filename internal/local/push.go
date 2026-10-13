package local

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/shinyvision/posthero/internal/postman"
)

// Pushed says what a push did.
type Pushed struct {
	// Pushed counts the changes made in Postman.
	Pushed int
	// IDs maps the ids of what was made here (collections, items, examples,
	// environments) to what Postman gave it: drafts and this machine's
	// values follow them.
	IDs map[string]string
	// Pulled is what came in from the team afterwards.
	Pulled Pulled
}

// Push makes the plan's changes in Postman, with the conflicts whose keys
// overwrite says to push anyway; the other conflicts stay here. With only
// set, only what has that key goes. Afterwards it takes the team's version
// in again, so the base is what Postman has now. A change that fails is
// reported; the others still go.
func (c *Copy) Push(ctx context.Context, api *postman.Client, p *Plan, overwrite map[string]bool, only string) (Pushed, error) {
	var ops []op
	for _, o := range p.ops {
		if only == "" || o.key == only {
			ops = append(ops, o)
		}
	}
	for _, cf := range p.Conflicts {
		if overwrite[cf.Key] && (only == "" || cf.Key == only) {
			ops = append(ops, cf.op)
		}
	}
	sort.SliceStable(ops, func(a, b int) bool { return ops[a].order < ops[b].order })
	res := Pushed{IDs: map[string]string{}}
	var errs []error
	for _, o := range ops {
		if err := c.exec(ctx, api, o, p.r, res.IDs); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", o.what(), err))
			continue
		}
		res.Pushed++
	}
	c.rename(res.IDs)
	r, err := c.fetch(ctx, api)
	if err != nil {
		return res, errors.Join(append(errs, fmt.Errorf("pushed, but couldn't fetch the result: %w", err))...)
	}
	res.Pulled = c.merge(r)
	return res, errors.Join(errs...)
}

func (o op) what() string {
	switch {
	case o.item != nil:
		return o.item.Name
	case o.env != nil:
		return o.env.Name
	case o.full != nil:
		return o.full.Info.Name
	case o.kind == "putGlobals":
		return "globals"
	}
	return o.kind
}

// exec makes one change in Postman. ids maps what was made here to its id
// in Postman, for what comes after it (an item in a new folder).
func (c *Copy) exec(ctx context.Context, api *postman.Client, o op, r *remote, ids map[string]string) error {
	id := func(x string) string {
		if to, ok := ids[x]; ok {
			return to
		}
		return x
	}
	owner, _ := postman.SplitUID(o.uid)
	uidOf := func(x string) string {
		if owner == "" || x == "" {
			return x
		}
		return owner + "-" + x
	}
	switch o.kind {
	case "createCol":
		col := o.full.Clone()
		col.Info.PostmanID = ""
		col.Walk(func(it *postman.Item, _ []*postman.Item) bool {
			it.ID, it.UID = "", ""
			for _, resp := range it.Response {
				resp.ID, resp.UID = "", ""
			}
			return true
		})
		sum, err := api.CreateCollection(ctx, c.ws.ID, col)
		if err != nil {
			return err
		}
		got, err := api.Collection(ctx, sum.UID)
		if err != nil {
			return err
		}
		ids[o.col] = sum.ID
		mapIDs(o.full.Item, got.Item, ids)
		// The copy here takes Postman's ids.
		c.srv.RemoveCollection(o.col)
		c.srv.PutCollection(c.ws.ID, got)
		return nil
	case "deleteCol":
		return api.DeleteCollection(ctx, o.uid)
	case "patchCol":
		// Only what differs from the team's version: the rest stays theirs.
		mine, theirs := o.full, o.theirsCol
		if theirs == nil {
			theirs = &postman.Collection{}
		}
		var patch postman.CollectionPatch
		if mine.Info.Name != theirs.Info.Name {
			patch.Name = &mine.Info.Name
		}
		if fingerprint(mine.Info.Description) != fingerprint(theirs.Info.Description) {
			desc := ""
			if mine.Info.Description != nil {
				desc = mine.Info.Description.Content
			}
			patch.Description = &desc
		}
		if fingerprint(mine.Auth) != fingerprint(theirs.Auth) {
			auth := mine.Auth
			patch.Auth = &auth
		}
		if fingerprint(mine.Event) != fingerprint(theirs.Event) {
			ev := mine.Event
			patch.Event = &ev
		}
		if fingerprint(mine.Variable) != fingerprint(theirs.Variable) {
			vars := mine.Variable
			patch.Variable = &vars
		}
		return api.PatchCollection(ctx, o.uid, patch)
	case "create":
		parent := ""
		if o.parent != "" {
			if to, ok := ids[o.parent]; ok {
				parent = to
			} else if r.hasItem(o.col, o.parent) {
				parent = o.parent
			}
		}
		var made string
		var err error
		if o.item.Folder {
			made, err = api.CreateFolder(ctx, o.uid, parent, o.item)
		} else {
			made, err = api.CreateRequest(ctx, o.uid, parent, o.item)
		}
		if err != nil {
			return err
		}
		ids[o.item.ID] = made
		for _, resp := range o.item.Response {
			rid, err := api.CreateResponse(ctx, o.uid, made, resp)
			if err != nil {
				return err
			}
			ids[resp.ID] = rid
		}
		if o.last {
			return nil // made at the end, where it is here
		}
		return c.placeItem(ctx, api, o, made, parent, id, uidOf)
	case "update":
		it := o.item.Clone()
		if it.Folder {
			return api.UpdateFolder(ctx, o.uid, it)
		}
		if _, err := api.UpdateRequest(ctx, o.uid, it); err != nil {
			return err
		}
		return syncExamples(ctx, api, o.uid, it, o.theirs, ids)
	case "move":
		parent := ""
		if o.parent != "" {
			parent = id(o.parent)
		}
		return c.placeItem(ctx, api, o, o.item.ID, parent, id, uidOf)
	case "delete":
		if o.item.Folder {
			return ignoreGone(api.DeleteFolder(ctx, o.uid, o.item.ID))
		}
		return ignoreGone(api.DeleteRequest(ctx, o.uid, o.item.ID))
	case "createEnv":
		e := o.env.Clone()
		old := e.ID
		e.ID, e.UID, e.Owner = "", "", ""
		sum, err := api.CreateEnvironment(ctx, c.ws.ID, e)
		if err != nil {
			return err
		}
		ids[old] = sum.ID
		e.ID = sum.ID
		c.srv.RemoveEnvironment(old)
		c.srv.PutEnvironment(c.ws.ID, e)
		return nil
	case "putEnv":
		if err := api.PutEnvironment(ctx, o.uid, o.env); err != nil {
			return err
		}
		// What went may be merged with the team's: the copy has it too.
		c.srv.PutEnvironment(c.ws.ID, o.env)
		return nil
	case "deleteEnv":
		return ignoreGone(api.DeleteEnvironment(ctx, o.uid))
	case "putGlobals":
		if err := api.PutGlobals(ctx, c.ws.ID, o.vals); err != nil {
			return err
		}
		c.srv.PutGlobals(c.ws.ID, o.vals)
		return nil
	}
	return fmt.Errorf("posthero can't push a %s", o.kind)
}

// placeItem puts an item in Postman where it is here: after the item before
// it, or at the start of its folder.
func (c *Copy) placeItem(ctx context.Context, api *postman.Client, o op, itemID, parent string, id, uidOf func(string) string) error {
	to := postman.Place{Collection: o.uid, Folder: uidOf(parent), Position: "start"}
	if o.prev != "" {
		to.Position, to.Next, to.NextFolder = "after", uidOf(id(o.prev)), o.prevFolder
	}
	_, err := api.Transfer(ctx, o.item.Folder, []string{uidOf(itemID)}, to, false)
	return err
}

// syncExamples makes a request's examples in Postman what they are here.
func syncExamples(ctx context.Context, api *postman.Client, uid string, mine, theirs *postman.Item, ids map[string]string) error {
	there := map[string]*postman.Response{}
	if theirs != nil {
		for _, r := range theirs.Response {
			there[r.ID] = r
		}
	}
	here := map[string]bool{}
	for _, r := range mine.Response {
		here[r.ID] = true
		t, ok := there[r.ID]
		switch {
		case !ok:
			rid, err := api.CreateResponse(ctx, uid, mine.ID, r)
			if err != nil {
				return err
			}
			ids[r.ID] = rid
		case fingerprint(t) != fingerprint(r):
			if err := api.UpdateResponse(ctx, uid, r); err != nil {
				return err
			}
		}
	}
	for rid := range there {
		if !here[rid] {
			if err := ignoreGone(api.DeleteResponse(ctx, uid, rid)); err != nil {
				return err
			}
		}
	}
	return nil
}

func ignoreGone(err error) error {
	if postman.IsNotFound(err) {
		return nil
	}
	return err
}

// mapIDs pairs up the items (and examples) of a collection as sent with
// the same items as Postman made them.
func mapIDs(sent, got []*postman.Item, ids map[string]string) {
	for i := 0; i < len(sent) && i < len(got); i++ {
		if sent[i].ID != "" && got[i].ID != "" {
			ids[sent[i].ID] = got[i].ID
		}
		for j := 0; j < len(sent[i].Response) && j < len(got[i].Response); j++ {
			ids[sent[i].Response[j].ID] = got[i].Response[j].ID
		}
		mapIDs(sent[i].Item, got[i].Item, ids)
	}
}

// hasItem says the team's version of a collection has an item.
func (r *remote) hasItem(col, id string) bool {
	s, ok := r.cols[col]
	if !ok {
		return false
	}
	it, _ := s.Data.Find(id)
	return it != nil
}

// rename gives what was made here the ids Postman gave it.
func (c *Copy) rename(ids map[string]string) {
	if len(ids) == 0 {
		return
	}
	cols, _, _ := c.srv.Contents(c.ws.ID)
	for _, col := range cols {
		changed := false
		col.Walk(func(it *postman.Item, _ []*postman.Item) bool {
			if to, ok := ids[it.ID]; ok {
				it.ID, it.UID, changed = to, "", true
			}
			for _, r := range it.Response {
				if to, ok := ids[r.ID]; ok {
					r.ID, r.UID, changed = to, "", true
				}
			}
			return true
		})
		if changed {
			c.srv.PutCollection(c.ws.ID, col)
		}
	}
}
