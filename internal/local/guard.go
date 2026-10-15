package local

import (
	"context"
	"fmt"

	"github.com/shinyvision/posthero/internal/jsonx"
	"github.com/shinyvision/posthero/internal/postman"
)

func stalePlan() error {
	return fmt.Errorf("changed since the push was reviewed; run :push again to review the latest changes")
}

// CheckReviewed ensures an open diff still describes the team's version
// in this new plan. Replanning must not silently renew overwrite consent.
func (p *Plan) CheckReviewed(cf *Conflict) error {
	if cf == nil || cf.reviewed == nil {
		return stalePlan()
	}
	before, now := cf.reviewed, p.r
	switch {
	case cf.Col != "":
		b, n := before.cols[cf.Col], now.cols[cf.Col]
		if (b.Data == nil) != (n.Data == nil) {
			return stalePlan()
		}
		if b.Data != nil {
			switch {
			case cf.Item != "":
				oldItem, _ := b.Data.Find(cf.Item)
				newItem, _ := n.Data.Find(cf.Item)
				if !sameItem(oldItem, newItem) {
					return stalePlan()
				}
				if oldItem != nil && newItem != nil {
					oldTree, newTree := flat(b.Data), flat(n.Data)
					both := func(id string) bool { return oldTree[id] != nil && newTree[id] != nil }
					if place(oldTree, cf.Item, both) != place(newTree, cf.Item, both) {
						return stalePlan()
					}
				}
			case cf.op.kind == "patchCol":
				if !sameProps(b.Data, n.Data) {
					return stalePlan()
				}
			default:
				if !colSame(b.Data, n.Data) {
					return stalePlan()
				}
			}
		}
	case cf.Env != "":
		if !sameEnv(before.envs[cf.Env].Data, now.envs[cf.Env].Data) {
			return stalePlan()
		}
	default:
		if !sameValues(before.globals, now.globals) {
			return stalePlan()
		}
	}
	return nil
}

func opResource(o op) string {
	if o.col != "" {
		return "collection:" + o.col
	}
	if o.env != nil {
		return "environment:" + o.env.ID
	}
	return "globals"
}

// checkPush compares against what was reviewed, including an explicitly
// approved overwrite. Approval for one version never approves a later one.
// Read errors stop the write; list timestamps are not sufficient here.
func (c *Copy) checkPush(ctx context.Context, api postman.API, o op, r *remote) error {
	if o.col != "" {
		before, exists := r.cols[o.col]
		if !exists {
			cols, err := api.Collections(ctx, c.ws.ID)
			if err != nil {
				return err
			}
			for _, col := range cols {
				if col.ID == o.col {
					return stalePlan()
				}
			}
			return nil
		}
		now, err := api.Collection(ctx, before.UID)
		if err != nil {
			return err
		}
		if !colSame(before.Data, now) {
			return stalePlan()
		}
		return nil
	}
	if o.env != nil {
		before, exists := r.envs[o.env.ID]
		if !exists {
			envs, err := api.Environments(ctx, c.ws.ID)
			if err != nil {
				return err
			}
			for _, env := range envs {
				if env.ID == o.env.ID {
					return stalePlan()
				}
			}
			return nil
		}
		now, err := api.Environment(ctx, before.UID)
		if err != nil {
			return err
		}
		if !sameEnv(before.Data, now) {
			return stalePlan()
		}
		return nil
	}
	now, err := api.Globals(ctx, c.ws.ID)
	if err != nil {
		return err
	}
	if !sameValues(r.globals, now) {
		return stalePlan()
	}
	return nil
}

// advancePush accepts the result of our write, but never silently accepts
// changes to another item as a new baseline for a later operation. If such
// a change arrived during the write, the rest of this collection stops.
func (c *Copy) advancePush(ctx context.Context, api postman.API, o op, r *remote, ids map[string]string) error {
	if o.col == "" || o.kind == "createCol" || o.kind == "deleteCol" {
		return nil
	}
	before := r.cols[o.col]
	now, err := api.Collection(ctx, before.UID)
	if err != nil {
		return err
	}
	a, b := before.Data.Clone(), now.Clone()
	switch o.kind {
	case "patchCol":
		// Collection settings were the write's target; its children weren't.
		a.Info, a.Auth, a.Event, a.Variable = b.Info, b.Auth, b.Event, b.Variable
	case "create":
		b.Remove(ids[o.item.ID])
	case "delete":
		a.Remove(o.item.ID)
	case "move":
		a.Remove(o.item.ID)
		b.Remove(o.item.ID)
	case "update":
		if o.item.Folder {
			// Only the folder's settings were written, not its children.
			for _, col := range []*postman.Collection{a, b} {
				it, _ := col.Find(o.item.ID)
				if it == nil {
					return stalePlan()
				}
				*it = postman.Item{ID: it.ID, Folder: true, Item: it.Item}
			}
		} else {
			a.Remove(o.item.ID)
			b.Remove(o.item.ID)
		}
	}
	if !colSame(a, b) {
		return stalePlan()
	}
	before.Data = now
	r.cols[o.col] = before
	return nil
}

// localState includes identities as map keys so replacing an object with
// identical content is still a change to the plan's input.
func (c *Copy) localState() string {
	cols, envs, globals := c.srv.Contents(c.ws.ID)
	return localStateOf(cols, envs, globals)
}

func localStateOf(cols []*postman.Collection, envs []*postman.Environment, globals []postman.EnvValue) string {
	state := map[string]any{"globals": globals}
	for _, col := range cols {
		state["collection:"+col.Info.PostmanID] = col
	}
	for _, env := range envs {
		state["environment:"+env.ID] = env
	}
	data, _ := jsonx.Marshal(state)
	return string(data)
}
