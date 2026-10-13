package postman

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/shinyvision/posthero/internal/jsonx"
)

// The Postman API's item endpoints (one request, folder or example at a
// time) speak an older format than collections do. These turn posthero's
// items into it. Every field posthero edits is always sent, so a value that
// was removed is removed on the server too.

// requestBody is a request item in the item endpoints' format.
func requestBody(it *Item) map[string]any {
	req := it.Request
	if req == nil {
		req = &Request{Method: http.MethodGet, URL: &URL{}}
	}
	out := map[string]any{
		"name":   it.Name,
		"method": strings.ToUpper(orDefault(req.Method, http.MethodGet)),
	}
	u := req.URL
	if u == nil {
		u = &URL{}
	}
	out["url"] = u.String()
	query := []map[string]any{}
	for _, q := range u.Query {
		query = append(query, paramItem(q))
	}
	out["queryParams"] = query
	paths := []map[string]any{}
	for _, v := range u.Variable {
		m := map[string]any{"key": v.Key, "value": string(v.Value)}
		if v.Description != nil && v.Description.Content != "" {
			m["description"] = v.Description.Content
		}
		paths = append(paths, m)
	}
	out["pathVariableData"] = paths
	headers := []map[string]any{}
	for _, h := range req.Header {
		headers = append(headers, paramItem(h))
	}
	out["headerData"] = headers

	out["dataMode"], out["rawModeData"], out["data"], out["graphqlModeData"], out["dataOptions"] = nil, nil, nil, nil, nil
	out["dataDisabled"] = false
	if b := req.Body; b != nil && b.Mode != "" {
		out["dataDisabled"] = b.Disabled
		if len(b.Options) > 0 {
			out["dataOptions"] = b.Options
		}
		switch b.Mode {
		case "raw":
			out["dataMode"], out["rawModeData"] = "raw", b.Raw
		case "urlencoded":
			out["dataMode"], out["data"] = "urlencoded", paramItems(b.URLEncoded)
		case "formdata":
			out["dataMode"], out["data"] = "params", paramItems(b.FormData)
		case "file":
			src := ""
			if b.File != nil {
				src = string(b.File.Src)
			}
			out["dataMode"], out["rawModeData"] = "binary", src
		case "graphql":
			g := b.GraphQL
			if g == nil {
				g = &GraphQL{}
			}
			out["dataMode"] = "graphql"
			out["graphqlModeData"] = map[string]any{"query": string(g.Query), "variables": string(g.Variables)}
		}
	}
	out["auth"] = authItem(req.Auth)
	out["events"] = eventItems(it.Event)
	out["description"] = descriptionItem(req.Description)
	if it.Description != nil && req.Description == nil {
		out["description"] = descriptionItem(it.Description)
	}
	out["protocolProfileBehavior"] = settingsItem(it.ProtocolProfileBehavior)
	return out
}

// folderBody is a folder in the item endpoints' format.
func folderBody(it *Item) map[string]any {
	return map[string]any{
		"name":        it.Name,
		"description": descriptionItem(it.Description),
		"auth":        authItem(it.Auth),
		"events":      eventItems(it.Event),
	}
}

// responseBody is an example in the item endpoints' format.
func responseBody(r *Response) map[string]any {
	out := map[string]any{
		"name":         r.Name,
		"responseCode": map[string]any{"code": r.Code, "name": r.Status},
		"headers":      paramItems(r.Header),
		"text":         r.Body,
	}
	if r.PreviewLanguage != "" {
		out["language"] = r.PreviewLanguage
	}
	if len(r.Cookie) > 0 {
		out["cookies"] = r.Cookie
	}
	if r.OriginalRequest != nil {
		out["requestObject"] = requestBody(&Item{Name: r.Name, Request: r.OriginalRequest})
	}
	return out
}

func paramItem(p Param) map[string]any {
	m := map[string]any{"key": p.Key, "value": string(p.Value), "enabled": !p.Disabled}
	if p.Description != nil && p.Description.Content != "" {
		m["description"] = p.Description.Content
	}
	if p.Type != "" {
		m["type"] = p.Type
	}
	if len(p.Src) > 0 {
		m["src"] = p.Src
		if p.Type == "file" {
			m["value"] = nil
		}
	}
	if p.ContentType != "" {
		m["contentType"] = p.ContentType
	}
	return m
}

func paramItems(ps Params) []map[string]any {
	out := []map[string]any{}
	for _, p := range ps {
		out = append(out, paramItem(p))
	}
	return out
}

// authItem is nil for "inherit from the parent".
func authItem(a *Auth) any {
	if a == nil || a.Type == "" || a.Type == "inherit" {
		return nil
	}
	return a
}

func eventItems(events []Event) any {
	if len(events) == 0 {
		return nil
	}
	return events
}

func descriptionItem(d *Description) any {
	if d == nil || d.Content == "" {
		return nil
	}
	return d.Content
}

// settingDefaults are the values of request settings when nothing is set;
// writing them back is how a setting is removed (the API keeps an object
// it is sent nothing for).
var settingDefaults = map[string]any{
	"followRedirects":               true,
	"followOriginalHttpMethod":      false,
	"followAuthorizationHeader":     false,
	"removeRefererHeaderOnRedirect": false,
	"maxRedirects":                  10,
	"strictSSL":                     true,
	"disableBodyPruning":            false,
	"disableUrlEncoding":            false,
	"disableCookies":                false,
	"insecureHTTPParser":            false,
	"tlsPreferServerCiphers":        false,
}

func settingsItem(raw json.RawMessage) any {
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	if len(m) == 0 {
		return map[string]any{"followRedirects": true}
	}
	return m
}

// RemovedSettings fills in the defaults for settings that were set before
// and are gone now, so saving removes them.
func RemovedSettings(before, after json.RawMessage) json.RawMessage {
	var old, cur map[string]any
	_ = json.Unmarshal(before, &old)
	_ = json.Unmarshal(after, &cur)
	if cur == nil {
		cur = map[string]any{}
	}
	changed := false
	for k := range old {
		if _, ok := cur[k]; ok {
			continue
		}
		if d, ok := settingDefaults[k]; ok {
			cur[k] = d
			changed = true
		}
	}
	if !changed {
		return after
	}
	data, _ := jsonx.Marshal(cur)
	return data
}

func orDefault(s, d string) string {
	if s == "" {
		return d
	}
	return s
}
