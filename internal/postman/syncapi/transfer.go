package syncapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"slices"

	"github.com/shinyvision/posthero/internal/postman"
)

// Moving and copying. The service moves a request or folder to another
// parent (a "transfer", which puts it last there) and keeps the order of a
// parent's requests (order) and folders (folders_order) on the parent;
// posthero puts the item in its place by changing that list. A copy is
// made the way the Postman API makes one: everything in it again, with new
// ids, " Copy" after the name of what was copied.

// Transfer moves (or with copy set, copies) requests or folders to a place
// and returns the uids of what is there now.
func (c *Client) Transfer(ctx context.Context, folders bool, uids []string, to postman.Place, copy bool) ([]string, error) {
	kind, list := "request", "order"
	if folders {
		kind, list = "folder", "folders_order"
	}
	if p := to.Position; (p == "before" || p == "after") && to.NextFolder != folders {
		// As the Postman API: a request goes before or after a request, a
		// folder before or after a folder.
		model := "request"
		if to.NextFolder {
			model = "folder"
		}
		return nil, &postman.Error{Status: http.StatusBadRequest, Name: "malformedRequestError", Message: "Incompatible location of type " + model + " provided"}
	}
	tOwner, tCol := postman.SplitUID(to.Collection)
	target := parentRef{kind: "collection", owner: tOwner, id: tCol}
	if to.Folder != "" {
		o, id := split(to.Folder, tOwner)
		target = parentRef{kind: "folder", owner: o, id: id}
	}
	var out []string
	for _, ref := range uids {
		var where struct {
			Collection string  `json:"collection"`
			Folder     *string `json:"folder"`
			Owner      string  `json:"owner"`
		}
		var got envelope[json.RawMessage]
		if err := c.call(ctx, "get", "/"+kind+"/"+url.PathEscape(ref), nil, &got); err != nil {
			if postman.IsNotFound(err) {
				return out, errNotFound("item")
			}
			return out, err
		}
		_ = json.Unmarshal(got.Data, &where)
		owner, id := split(ref, where.Owner)
		var placed string
		if copy {
			newID, newOwner, err := c.copyItem(ctx, kind, got.Data, target, to.Collection)
			if err != nil {
				return out, err
			}
			id, placed = newID, uid(newOwner, newID)
		} else {
			from := parentRef{kind: "collection", owner: owner, id: where.Collection}
			if where.Folder != nil && *where.Folder != "" {
				from = parentRef{kind: "folder", owner: owner, id: *where.Folder}
			}
			if from.kind != target.kind || from.id != target.id {
				body := map[string]any{
					"modelId": id, "owner": owner,
					"to":   map[string]any{"model": target.kind, "model_id": target.id, "owner": target.owner},
					"from": map[string]any{"model": from.kind, "model_id": from.id, "owner": from.owner},
				}
				if err := c.call(ctx, "put", path(kind+"/transfer", owner, id), body, nil); err != nil {
					return out, err
				}
			}
			placed = uid(owner, id)
		}
		if err := c.place(ctx, target, list, id, to); err != nil {
			return out, err
		}
		out = append(out, placed)
	}
	c.forget(to.Collection)
	return out, nil
}

// parentRef is a collection or folder that holds items.
type parentRef struct {
	kind, owner, id string
}

// place puts an item that is in parent at the place to says, among the
// parent's requests or folders (list).
func (c *Client) place(ctx context.Context, parent parentRef, list, id string, to postman.Place) error {
	var got envelope[map[string]json.RawMessage]
	if err := c.call(ctx, "get", "/"+parent.kind+"/"+url.PathEscape(uid(parent.owner, parent.id)), nil, &got); err != nil {
		return err
	}
	var order []string
	_ = json.Unmarshal(got.Data[list], &order)
	order = slices.DeleteFunc(order, func(s string) bool { return s == id })
	at := len(order)
	switch to.Position {
	case "start":
		at = 0
	case "before", "after":
		next := bareID(to.Next)
		if i := slices.Index(order, next); i >= 0 {
			at = i
			if to.Position == "after" {
				at++
			}
		}
	}
	want := slices.Insert(slices.Clone(order), at, id)
	var now []string
	_ = json.Unmarshal(got.Data[list], &now)
	if slices.Equal(now, want) {
		return nil
	}
	return c.call(ctx, "put", path(parent.kind, parent.owner, parent.id), map[string]any{list: want}, nil)
}

// itemFields are what a copy doesn't take from what it copies: whose it
// is, when, where and its id.
var itemFields = []string{"id", "owner", "lastUpdatedBy", "lastRevision", "createdAt", "updatedAt", "collection", "folder",
	"responses", "responses_order", "order", "folders_order", "folders", "requests", "request"}

// copyItem copies a request (with its examples) or a folder (with what is
// in it) into a parent and returns the copy's id and owner.
// collection is the uid of the collection the copy goes in.
func (c *Client) copyItem(ctx context.Context, kind string, data json.RawMessage, parent parentRef, collection string) (string, string, error) {
	var src map[string]json.RawMessage
	if err := json.Unmarshal(data, &src); err != nil {
		return "", "", err
	}
	var name string
	_ = json.Unmarshal(src["name"], &name)
	if kind == "request" {
		var withExamples envelope[map[string]json.RawMessage]
		var id string
		_ = json.Unmarshal(src["id"], &id)
		owner := text(src["owner"])
		if err := c.call(ctx, "get", "/request/"+url.PathEscape(uid(owner, id))+"?populate=true", nil, &withExamples); err != nil {
			return "", "", err
		}
		return c.copyRequest(ctx, withExamples.Data, name+" Copy", parent, collection)
	}
	// A folder: from its collection, which has what is in it.
	var colID, owner string
	_ = json.Unmarshal(src["collection"], &colID)
	owner = text(src["owner"])
	var col envelope[map[string]json.RawMessage]
	if err := c.call(ctx, "get", "/collection/"+url.PathEscape(uid(owner, colID))+"?populate=true&responses=true", nil, &col); err != nil {
		return "", "", err
	}
	t := rawTree(col.Data)
	var id string
	_ = json.Unmarshal(src["id"], &id)
	return c.copyFolder(ctx, t, id, name+" Copy", parent, collection)
}

// copyRequest makes a copy of a request (as the service has it) and its
// examples.
func (c *Client) copyRequest(ctx context.Context, src map[string]json.RawMessage, name string, parent parentRef, collection string) (string, string, error) {
	body := copyFields(src)
	body["name"] = raw(name)
	body["folder"] = raw(nil)
	if parent.kind == "folder" {
		body["folder"] = raw(parent.id)
	}
	m, err := c.create(ctx, "request", collection, mapOf(body))
	if err != nil {
		return "", "", err
	}
	owner, _ := postman.SplitUID(m.UID)
	var examples []map[string]json.RawMessage
	_ = json.Unmarshal(src["responses"], &examples)
	var order []string
	_ = json.Unmarshal(src["responses_order"], &order)
	examples = inOrder(examples, order)
	for _, x := range examples {
		e := copyFields(x)
		e["request"] = raw(m.ID)
		if _, err := c.create(ctx, "response", collection, mapOf(e)); err != nil {
			return "", "", err
		}
	}
	return m.ID, orDefault(owner, parent.owner), nil
}

// copyFolder makes a copy of a folder of a collection, and of what is in
// it, in order.
func (c *Client) copyFolder(ctx context.Context, t *rawCollection, id, name string, parent parentRef, collection string) (string, string, error) {
	src := t.folders[id]
	if src == nil {
		return "", "", errNotFound("folder")
	}
	body := copyFields(src)
	body["name"] = raw(name)
	body["folder"] = raw(nil)
	if parent.kind == "folder" {
		body["folder"] = raw(parent.id)
	}
	m, err := c.create(ctx, "folder", collection, mapOf(body))
	if err != nil {
		return "", "", err
	}
	owner, _ := postman.SplitUID(m.UID)
	owner = orDefault(owner, parent.owner)
	inside := parentRef{kind: "folder", owner: owner, id: m.ID}
	var folders, order []string
	_ = json.Unmarshal(src["folders_order"], &folders)
	_ = json.Unmarshal(src["order"], &order)
	for _, fid := range folders {
		if f := t.folders[fid]; f != nil {
			if _, _, err := c.copyFolder(ctx, t, fid, text(f["name"]), inside, collection); err != nil {
				return "", "", err
			}
		}
	}
	for _, rid := range order {
		if r := t.requests[rid]; r != nil {
			if _, _, err := c.copyRequest(ctx, r, text(r["name"]), inside, collection); err != nil {
				return "", "", err
			}
		}
	}
	return m.ID, owner, nil
}

// rawCollection is a populated collection's folders and requests by id, as
// the service sent them.
type rawCollection struct {
	folders, requests map[string]map[string]json.RawMessage
}

func rawTree(col map[string]json.RawMessage) *rawCollection {
	t := &rawCollection{folders: map[string]map[string]json.RawMessage{}, requests: map[string]map[string]json.RawMessage{}}
	var folders, requests []map[string]json.RawMessage
	_ = json.Unmarshal(col["folders"], &folders)
	_ = json.Unmarshal(col["requests"], &requests)
	for _, f := range folders {
		t.folders[text(f["id"])] = f
	}
	for _, r := range requests {
		t.requests[text(r["id"])] = r
	}
	return t
}

// copyFields are a request, folder or example without what makes it that
// one (see itemFields).
func copyFields(src map[string]json.RawMessage) map[string]json.RawMessage {
	out := map[string]json.RawMessage{}
	for k, v := range src {
		if !slices.Contains(itemFields, k) {
			out[k] = v
		}
	}
	return out
}

func mapOf(m map[string]json.RawMessage) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// inOrder sorts examples by the request's responses_order; the ones it
// doesn't name stay after, as they were.
func inOrder(list []map[string]json.RawMessage, order []string) []map[string]json.RawMessage {
	rank := map[string]int{}
	for i, id := range order {
		rank[id] = i
	}
	slices.SortStableFunc(list, func(a, b map[string]json.RawMessage) int {
		ra, aok := rank[text(a["id"])]
		rb, bok := rank[text(b["id"])]
		switch {
		case aok && bok:
			return ra - rb
		case aok:
			return -1
		case bok:
			return 1
		}
		return 0
	})
	return list
}
