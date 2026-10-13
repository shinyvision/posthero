package pmtest

import (
	"encoding/json"
	"strings"

	"github.com/shinyvision/posthero/internal/postman"
)

// The item endpoints change only the fields they are sent, like the real
// API.

type itemParam struct {
	Key         string          `json:"key"`
	Value       *string         `json:"value"`
	Enabled     *bool           `json:"enabled"`
	Description string          `json:"description"`
	Type        string          `json:"type"`
	Src         json.RawMessage `json:"src"`
	ContentType string          `json:"contentType"`
}

func toParams(list []itemParam) postman.Params {
	out := postman.Params{}
	for _, p := range list {
		q := postman.Param{Key: p.Key, Type: p.Type, ContentType: p.ContentType}
		if p.Value != nil {
			q.Value = postman.Str(*p.Value)
		} else if p.Type != "file" {
			q.NoValue = true
		}
		if p.Enabled != nil && !*p.Enabled {
			q.Disabled = true
		}
		if p.Description != "" {
			q.Description = &postman.Description{Content: p.Description}
		}
		if len(p.Src) > 0 && string(p.Src) != "null" {
			q.Src = p.Src
		}
		out = append(out, q)
	}
	return out
}

func applyRequest(it *postman.Item, body []byte) error {
	var in map[string]json.RawMessage
	if err := json.Unmarshal(body, &in); err != nil {
		return err
	}
	if it.Request == nil {
		it.Request = &postman.Request{Method: "GET", URL: &postman.URL{}}
	}
	req := it.Request
	str := func(k string) (string, bool) {
		raw, ok := in[k]
		if !ok {
			return "", false
		}
		var s string
		_ = json.Unmarshal(raw, &s)
		return s, true
	}
	if v, ok := str("name"); ok {
		it.Name = v
	}
	if v, ok := str("method"); ok {
		req.Method = v
	}
	if v, ok := str("url"); ok {
		old := req.URL
		req.URL = postman.ParseURL(v)
		if old != nil && len(old.Variable) > 0 {
			req.URL.Variable = old.Variable
		}
	}
	if raw, ok := in["queryParams"]; ok {
		var list []itemParam
		_ = json.Unmarshal(raw, &list)
		req.URL.Query = toParams(list)
		if len(req.URL.Query) == 0 {
			req.URL.Query = nil
		}
	}
	if raw, ok := in["pathVariableData"]; ok {
		var list []itemParam
		_ = json.Unmarshal(raw, &list)
		req.URL.Variable = nil
		for _, p := range toParams(list) {
			v := postman.Variable{Key: p.Key, Value: p.Value, Description: p.Description}
			req.URL.Variable = append(req.URL.Variable, v)
		}
	}
	if raw, ok := in["headerData"]; ok {
		var list []itemParam
		_ = json.Unmarshal(raw, &list)
		req.Header = toParams(list)
	}
	if raw, ok := in["dataMode"]; ok {
		var mode *string
		_ = json.Unmarshal(raw, &mode)
		if mode == nil || *mode == "" {
			req.Body = nil
		} else {
			b := &postman.Body{}
			var rawData *string
			_ = json.Unmarshal(in["rawModeData"], &rawData)
			var data []itemParam
			_ = json.Unmarshal(in["data"], &data)
			switch *mode {
			case "raw":
				b.Mode = "raw"
				if rawData != nil {
					b.Raw = *rawData
				}
			case "urlencoded":
				b.Mode, b.URLEncoded = "urlencoded", toParams(data)
			case "params":
				b.Mode, b.FormData = "formdata", toParams(data)
			case "binary":
				b.Mode, b.File = "file", &postman.BodyFile{}
				if rawData != nil {
					b.File.Src = postman.Str(*rawData)
				}
			case "graphql":
				var g struct {
					Query     string `json:"query"`
					Variables string `json:"variables"`
				}
				_ = json.Unmarshal(in["graphqlModeData"], &g)
				b.Mode, b.GraphQL = "graphql", &postman.GraphQL{Query: postman.Str(g.Query), Variables: postman.Str(g.Variables)}
			}
			if opts, ok := in["dataOptions"]; ok && string(opts) != "null" {
				b.Options = opts
			}
			var disabled bool
			_ = json.Unmarshal(in["dataDisabled"], &disabled)
			b.Disabled = disabled
			req.Body = b
		}
	}
	if raw, ok := in["auth"]; ok {
		req.Auth = nil
		if string(raw) != "null" {
			var a postman.Auth
			_ = json.Unmarshal(raw, &a)
			req.Auth = &a
		}
	}
	if raw, ok := in["events"]; ok {
		it.Event = nil
		_ = json.Unmarshal(raw, &it.Event)
	}
	if raw, ok := in["description"]; ok {
		req.Description = nil
		var d *string
		_ = json.Unmarshal(raw, &d)
		if d != nil && *d != "" {
			req.Description = &postman.Description{Content: *d}
		}
	}
	if raw, ok := in["protocolProfileBehavior"]; ok && string(raw) != "null" && strings.TrimSpace(string(raw)) != "{}" {
		it.ProtocolProfileBehavior = raw
	}
	return nil
}

func applyFolder(it *postman.Item, body []byte) error {
	var in map[string]json.RawMessage
	if err := json.Unmarshal(body, &in); err != nil {
		return err
	}
	if raw, ok := in["name"]; ok {
		_ = json.Unmarshal(raw, &it.Name)
	}
	if raw, ok := in["description"]; ok {
		it.Description = nil
		var d *string
		_ = json.Unmarshal(raw, &d)
		if d != nil && *d != "" {
			it.Description = &postman.Description{Content: *d}
		}
	}
	if raw, ok := in["auth"]; ok {
		it.Auth = nil
		if string(raw) != "null" {
			var a postman.Auth
			_ = json.Unmarshal(raw, &a)
			it.Auth = &a
		}
	}
	if raw, ok := in["events"]; ok {
		it.Event = nil
		_ = json.Unmarshal(raw, &it.Event)
	}
	return nil
}

func applyResponse(r *postman.Response, body []byte) {
	var in map[string]json.RawMessage
	if json.Unmarshal(body, &in) != nil {
		return
	}
	if raw, ok := in["name"]; ok {
		_ = json.Unmarshal(raw, &r.Name)
	}
	if raw, ok := in["responseCode"]; ok {
		var rc struct {
			Code int    `json:"code"`
			Name string `json:"name"`
		}
		_ = json.Unmarshal(raw, &rc)
		r.Code, r.Status = rc.Code, rc.Name
	}
	if raw, ok := in["headers"]; ok {
		var list []itemParam
		_ = json.Unmarshal(raw, &list)
		r.Header = toParams(list)
	}
	if raw, ok := in["text"]; ok {
		_ = json.Unmarshal(raw, &r.Body)
	}
	if raw, ok := in["language"]; ok {
		_ = json.Unmarshal(raw, &r.PreviewLanguage)
	}
	if raw, ok := in["cookies"]; ok {
		r.Cookie = raw
	}
	if raw, ok := in["requestObject"]; ok {
		it := &postman.Item{}
		if applyRequest(it, raw) == nil {
			r.OriginalRequest = it.Request
		}
	}
}
