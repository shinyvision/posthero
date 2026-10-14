package syncapi

import (
	"encoding/json"

	"github.com/shinyvision/posthero/internal/postman"
)

// From posthero's format to the sync service's, for what posthero sends.
// A request, folder or example goes in the format of the Postman API's item
// endpoints (postman.RequestFields and its kin), which is the service's
// own; the same fields go, so the service changes the same as the API does.
// The one difference: a file field of a form names its file in value here,
// where the API's endpoints take src.

// requestPayload is a request's fields.
func requestPayload(it *postman.Item) map[string]any {
	body := postman.RequestFields(it)
	fileValues(body)
	return body
}

// folderPayload is a folder's own fields.
func folderPayload(it *postman.Item) map[string]any {
	return postman.FolderFields(it)
}

// responsePayload is an example's fields.
func responsePayload(r *postman.Response) map[string]any {
	body := postman.ResponseFields(r)
	if req, ok := body["requestObject"].(map[string]any); ok {
		fileValues(req)
	}
	return body
}

// fileValues moves the files of a request's form fields from src to value.
func fileValues(body map[string]any) {
	list, ok := body["data"].([]map[string]any)
	if !ok {
		return
	}
	for _, p := range list {
		if p["type"] != "file" {
			continue
		}
		if src, ok := p["src"]; ok {
			p["value"] = src
			delete(p, "src")
		}
	}
}

// collectionPayload is a whole collection for the service's import: its
// settings, its folders and requests in flat lists with their order, and
// the examples of each request. The service gives everything ids of its
// own; the ids here only tie the parts together.
func collectionPayload(col *postman.Collection) map[string]any {
	colID := orDefault(col.Info.PostmanID, postman.NewID())
	out := map[string]any{
		"id":          colID,
		"name":        col.Info.Name,
		"description": descriptionText(col.Info.Description),
		"auth":        authValue(col.Auth),
		"events":      eventsValue(col.Event),
		"variables":   col.Variable,
	}
	var folders, requests []map[string]any
	var add func(items []*postman.Item, parent any) (foldersOrder, order []string)
	add = func(items []*postman.Item, parent any) ([]string, []string) {
		foldersOrder, order := []string{}, []string{}
		for _, it := range items {
			id := orDefault(it.ID, postman.NewID())
			if it.Folder {
				f := folderPayload(it)
				f["id"], f["collection"], f["folder"] = id, colID, parent
				if len(it.Variable) > 0 {
					f["variables"] = it.Variable
				}
				f["folders_order"], f["order"] = add(it.Item, id)
				folders = append(folders, f)
				foldersOrder = append(foldersOrder, id)
				continue
			}
			r := requestPayload(it)
			r["id"], r["collection"], r["folder"] = id, colID, parent
			examples, examplesOrder := []map[string]any{}, []string{}
			for _, x := range it.Response {
				xid := orDefault(x.ID, postman.NewID())
				e := responsePayload(x)
				e["id"], e["request"] = xid, id
				examples = append(examples, e)
				examplesOrder = append(examplesOrder, xid)
			}
			r["responses"], r["responses_order"] = examples, examplesOrder
			requests = append(requests, r)
			order = append(order, id)
		}
		return foldersOrder, order
	}
	out["folders_order"], out["order"] = add(col.Item, nil)
	if folders == nil {
		folders = []map[string]any{}
	}
	if requests == nil {
		requests = []map[string]any{}
	}
	out["folders"], out["requests"] = folders, requests
	return out
}

func descriptionText(d *postman.Description) any {
	if d == nil || d.Content == "" {
		return nil
	}
	return d.Content
}

func authValue(a *postman.Auth) any {
	if a == nil || a.Type == "" || a.Type == "inherit" {
		return nil
	}
	return a
}

func eventsValue(ev []postman.Event) any {
	if len(ev) == 0 {
		return nil
	}
	return ev
}

// raw is v as JSON.
func raw(v any) json.RawMessage {
	data, _ := json.Marshal(v)
	return data
}
