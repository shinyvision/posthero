package syncapi

import (
	"bytes"
	"encoding/json"
	"sort"
	"strings"

	"github.com/shinyvision/posthero/internal/jsonx"
	"github.com/shinyvision/posthero/internal/postman"
)

// From the sync service's format to posthero's: what the Postman API's
// collection endpoint answers for the same collection, down to the fields
// it fills in (TestParity checks this against real collections).

// uid is a thing's uid: its owner's id, a dash and its id.
func uid(owner, id string) string {
	if owner == "" || id == "" {
		return id
	}
	return owner + "-" + id
}

// toCollection builds a collection from one fetched with its folders,
// requests and examples.
func toCollection(c *collectionDTO) *postman.Collection {
	col := &postman.Collection{
		Info: postman.Info{
			PostmanID:     c.ID,
			Name:          c.Name,
			Description:   description(c.Description),
			Schema:        postman.SchemaV21,
			CreatedAt:     c.CreatedAt,
			UpdatedAt:     c.UpdatedAt,
			LastUpdatedBy: c.LastUpdatedBy,
			UID:           uid(c.Owner, c.ID),
		},
		Event:    c.Events,
		Variable: c.Variables,
		Auth:     typedAuth(c.Auth),
	}
	t := &tree{folders: map[string]*folderDTO{}, requests: map[string]*requestDTO{}, used: map[string]bool{}}
	for _, f := range c.Folders {
		t.folders[f.ID] = f
	}
	for _, r := range c.Requests {
		t.requests[r.ID] = r
	}
	col.Item = t.items(c.FoldersOrder, c.Order)
	return col
}

// tree puts a collection's flat lists back together.
type tree struct {
	folders  map[string]*folderDTO
	requests map[string]*requestDTO
	used     map[string]bool
}

// items are what a collection or folder holds: its folders, then its
// requests, each in their order. An id that isn't there is skipped.
func (t *tree) items(foldersOrder, order []string) []*postman.Item {
	var out []*postman.Item
	for _, id := range foldersOrder {
		f := t.folders[id]
		if f == nil || t.used[id] {
			continue
		}
		t.used[id] = true
		out = append(out, t.folder(f))
	}
	for _, id := range order {
		r := t.requests[id]
		if r == nil || t.used[id] {
			continue
		}
		t.used[id] = true
		out = append(out, toItem(r))
	}
	return out
}

func (t *tree) folder(f *folderDTO) *postman.Item {
	it := &postman.Item{
		ID:          f.ID,
		UID:         uid(f.Owner, f.ID),
		Name:        f.Name,
		Description: description(f.Description),
		Event:       f.Events,
		Auth:        typedAuth(f.Auth),
		Variable:    f.Variables,
		CreatedAt:   f.CreatedAt,
		UpdatedAt:   f.UpdatedAt,
		Folder:      true,
	}
	it.Item = t.items(f.FoldersOrder, f.Order)
	return it
}

// toItem makes a request item.
func toItem(r *requestDTO) *postman.Item {
	it := &postman.Item{
		ID:        r.ID,
		UID:       uid(r.Owner, r.ID),
		Name:      r.Name,
		Request:   toRequest(r),
		Event:     r.Events,
		Variable:  r.Variables,
		CreatedAt: r.CreatedAt,
		UpdatedAt: r.UpdatedAt,
	}
	if settings(r.Settings) {
		it.ProtocolProfileBehavior = r.Settings
	}
	byID := map[string]*responseDTO{}
	for _, x := range r.Responses {
		byID[x.ID] = x
	}
	seen := map[string]bool{}
	for _, id := range r.ResponsesOrder {
		if x := byID[id]; x != nil && !seen[id] {
			seen[id] = true
			it.Response = append(it.Response, toResponse(x))
		}
	}
	for _, x := range r.Responses {
		if !seen[x.ID] {
			seen[x.ID] = true
			it.Response = append(it.Response, toResponse(x))
		}
	}
	return it
}

// toRequest is what a request sends.
func toRequest(r *requestDTO) *postman.Request {
	req := &postman.Request{
		Method:      strings.ToUpper(orDefault(r.Method, "GET")),
		Header:      toParams(r.HeaderData),
		Auth:        typedAuth(r.Auth),
		Description: description(r.Description),
		URL:         toURL(r),
		Body:        toBody(r),
	}
	return req
}

// toURL is a request's address: its string, with the query parameters
// (disabled ones too) and the values of its path variables. As in the
// Postman API, the string's query is the enabled parameters, in their
// order. A request without an address has none.
func toURL(r *requestDTO) *postman.URL {
	if r.URL == "" && len(r.QueryParams) == 0 {
		return nil
	}
	u := postman.ParseURL(r.URL)
	if r.QueryParams != nil {
		u.Query = toParams(r.QueryParams)
		for i, p := range r.QueryParams {
			if p.Equals != nil && !*p.Equals && u.Query[i].Value == "" {
				u.Query[i].NoValue = true
			}
		}
		base, _, hash := postman.SplitRaw(r.URL)
		raw := base
		if q := postman.QueryString(u.Query.Enabled()); q != "" {
			raw += "?" + q
		}
		if hash != "" {
			raw += "#" + hash
		}
		u.Raw = raw
	}
	u.Variable = pathVariables(r)
	return u
}

// pathVariables are a request's path variables: from pathVariableData, or
// in older requests from pathVariables (in the order the path names them),
// or without either the ones the path names, with no values.
func pathVariables(r *requestDTO) []postman.Variable {
	var out []postman.Variable
	for _, p := range r.PathVariableData {
		v := postman.Variable{Key: p.Key, Value: postman.Str(text(p.Value)), Description: description(p.Description)}
		if raw, ok := p.Extra["id"]; ok {
			v.ID = text(raw)
		}
		out = append(out, v)
	}
	if len(out) > 0 {
		return out
	}
	names := postman.PathVariables(r.URL)
	var values map[string]json.RawMessage
	if json.Unmarshal(r.PathVariables, &values) != nil || len(values) == 0 {
		// Nothing says their values: the variables the path names.
		for _, n := range names {
			out = append(out, postman.Variable{Key: n})
		}
		return out
	}
	rank := map[string]int{}
	for i, n := range names {
		rank[n] = i
	}
	keys := make([]string, 0, len(values))
	for k := range values {
		keys = append(keys, k)
	}
	sort.SliceStable(keys, func(i, j int) bool {
		ri, iok := rank[keys[i]]
		rj, jok := rank[keys[j]]
		switch {
		case iok && jok:
			return ri < rj
		case iok != jok:
			return iok
		}
		return keys[i] < keys[j]
	})
	for _, k := range keys {
		out = append(out, postman.Variable{Key: k, Value: postman.Str(text(values[k]))})
	}
	return out
}

// toBody is a request's body, nil when it has none. Older requests keep a
// raw body in data, and a GraphQL one there too.
func toBody(r *requestDTO) *postman.Body {
	if r.DataMode == nil || *r.DataMode == "" {
		return nil
	}
	b := &postman.Body{}
	switch *r.DataMode {
	case "raw":
		b.Mode = "raw"
		if r.RawModeData != nil && *r.RawModeData != "" {
			b.Raw = *r.RawModeData
		} else if s, ok := jsonString(r.Data); ok {
			b.Raw = s
		}
	case "urlencoded":
		b.Mode, b.URLEncoded = "urlencoded", toParams(paramList(r.Data))
	case "params", "formdata":
		b.Mode, b.FormData = "formdata", toParams(paramList(r.Data))
	case "binary", "file":
		b.Mode, b.File = "file", &postman.BodyFile{}
		if r.RawModeData != nil {
			b.File.Src = postman.Str(*r.RawModeData)
		}
	case "graphql":
		// A GraphQL body is graphqlModeData; the Postman API reads nothing
		// else, though older requests have one in data.
		b.Mode = "graphql"
		if g := r.GraphqlModeData; g != nil {
			b.GraphQL = &postman.GraphQL{Query: g.Query, Variables: g.Variables}
		}
	default:
		return nil
	}
	b.Options = bodyOptions(r.DataOptions)
	if r.DataDisabled != nil {
		b.Disabled = *r.DataDisabled
	}
	return b
}

// paramList reads a body's fields; data that isn't a list has none.
func paramList(raw json.RawMessage) []paramDTO {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || raw[0] != '[' {
		return nil
	}
	var list []paramDTO
	_ = decode(raw, &list)
	return list
}

// bodyOptions are a body's options without the empty ones, which the
// Postman API leaves out: {"graphql":{}} or {"raw":null} is no option.
func bodyOptions(raw json.RawMessage) json.RawMessage {
	var all map[string]json.RawMessage
	if json.Unmarshal(raw, &all) != nil {
		return nil
	}
	kept := map[string]json.RawMessage{}
	for k, v := range all {
		var obj map[string]json.RawMessage
		if json.Unmarshal(v, &obj) == nil && len(obj) > 0 {
			kept[k] = v
		}
	}
	if len(kept) == 0 {
		return nil
	}
	data, _ := jsonx.Marshal(kept)
	return data
}

// toParams turns a list of parameters into posthero's. A file field's file
// is its value in the sync service's format and its src in posthero's.
func toParams(list []paramDTO) postman.Params {
	if list == nil {
		return nil
	}
	out := postman.Params{}
	for _, p := range list {
		q := postman.Param{Key: p.Key, Type: p.Type, ContentType: p.ContentType, Description: description(p.Description)}
		switch {
		case p.Type == "file" && len(p.Value) > 0 && !isNull(p.Value):
			q.Src = p.Value
		case p.Type == "file":
			if len(p.Src) > 0 && !isNull(p.Src) {
				q.Src = p.Src
			}
		default:
			q.Value = postman.Str(text(p.Value))
			q.NoValue = isNull(p.Value)
			if len(p.Src) > 0 && !isNull(p.Src) {
				q.Src = p.Src
			}
		}
		if p.Enabled != nil && !*p.Enabled {
			q.Disabled = true
		}
		if len(p.Extra) > 0 {
			q.Extra = postman.Extra{}
			for k, v := range p.Extra {
				q.Extra[k] = v
			}
		}
		out = append(out, q)
	}
	return out
}

// typedAuth is an auth with each setting's type said, as the Postman API
// says it: string, boolean, number, or any (for lists and objects).
func typedAuth(a *postman.Auth) *postman.Auth {
	if a == nil {
		return nil
	}
	for k, list := range a.Params {
		for i, p := range list {
			if p.Type != "" {
				continue
			}
			v := bytes.TrimSpace(p.Value)
			switch {
			case len(v) == 0, v[0] == '"':
				list[i].Type = "string"
			case string(v) == "true" || string(v) == "false":
				list[i].Type = "boolean"
			case v[0] == '-' || v[0] >= '0' && v[0] <= '9':
				list[i].Type = "number"
			default:
				list[i].Type = "any"
			}
		}
		a.Params[k] = list
	}
	return a
}

// toResponse is a saved example.
func toResponse(x *responseDTO) *postman.Response {
	r := &postman.Response{
		ID:     x.ID,
		UID:    uid(x.Owner, x.ID),
		Name:   x.Name,
		Header: toParams(x.Headers),
		Cookie: cookies(x.Cookies),
	}
	if x.ResponseCode != nil {
		r.Code, r.Status = x.ResponseCode.Code, x.ResponseCode.Name
	}
	if x.Status != nil && *x.Status != "" {
		r.Status = *x.Status
	}
	if x.Language != nil {
		r.PreviewLanguage = *x.Language
	}
	if x.Text != nil {
		r.Body = *x.Text
	}
	if req := requestObject(x.RequestObject); req != nil {
		r.OriginalRequest = toRequest(req)
	}
	for k, v := range map[string]string{"createdAt": x.CreatedAt, "updatedAt": x.UpdatedAt} {
		if v != "" {
			if r.Extra == nil {
				r.Extra = postman.Extra{}
			}
			r.Extra[k], _ = jsonx.Marshal(v)
		}
	}
	return r
}

// cookies are an example's cookies: a list, without the ones that have no
// name (which the Postman API leaves out).
func cookies(raw json.RawMessage) json.RawMessage {
	var list []map[string]json.RawMessage
	_ = json.Unmarshal(raw, &list)
	kept := []map[string]json.RawMessage{}
	for _, c := range list {
		if text(c["name"]) != "" {
			kept = append(kept, c)
		}
	}
	data, _ := jsonx.Marshal(kept)
	return data
}

// requestObject reads an example's request: JSON text of a requestDTO (or,
// as some clients send it, the object itself).
func requestObject(raw json.RawMessage) *requestDTO {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || isNull(raw) {
		return nil
	}
	if raw[0] == '"' {
		var s string
		if json.Unmarshal(raw, &s) != nil || strings.TrimSpace(s) == "" {
			return nil
		}
		raw = []byte(s)
	}
	var r requestDTO
	if decode(raw, &r) != nil {
		return nil
	}
	return &r
}

// description is documentation, nil when there is none.
func description(d *postman.Description) *postman.Description {
	if d == nil || d.Content == "" {
		return nil
	}
	return d
}

// settings reports whether a request's protocolProfileBehavior says
// anything.
func settings(raw json.RawMessage) bool {
	var m map[string]json.RawMessage
	return json.Unmarshal(raw, &m) == nil && len(m) > 0
}

// text is a JSON value as text: a string's contents, null as "".
func text(raw json.RawMessage) string {
	var s postman.Str
	_ = s.UnmarshalJSON(raw)
	return string(s)
}

// jsonString is a JSON string's contents; ok is false for anything else.
func jsonString(raw json.RawMessage) (string, bool) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || raw[0] != '"' {
		return "", false
	}
	var s string
	return s, json.Unmarshal(raw, &s) == nil
}

func isNull(raw json.RawMessage) bool {
	return string(bytes.TrimSpace(raw)) == "null"
}

func orDefault(s, d string) string {
	if s == "" {
		return d
	}
	return s
}

// marshalKeeping encodes v (a plain struct type) with the extra fields
// after its own, in a stable order.
func marshalKeeping(v any, ex postman.Extra) ([]byte, error) {
	data, err := jsonx.Marshal(v)
	if err != nil || len(ex) == 0 {
		return data, err
	}
	names := make([]string, 0, len(ex))
	for k := range ex {
		names = append(names, k)
	}
	sort.Strings(names)
	var b bytes.Buffer
	b.Write(data[:len(data)-1])
	for _, k := range names {
		if b.Len() > 1 {
			b.WriteByte(',')
		}
		key, _ := jsonx.Marshal(k)
		b.Write(key)
		b.WriteByte(':')
		b.Write(ex[k])
	}
	b.WriteByte('}')
	return b.Bytes(), nil
}
