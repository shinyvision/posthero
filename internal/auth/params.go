// Package auth signs requests with every kind of auth Postman has, and
// keeps OAuth 2.0 tokens fresh: it refreshes them before they run out and
// logs in again by itself when it has to, through the identity provider's
// session, its login form, or the browser as a last resort.
package auth

import (
	"encoding/json"
	"strconv"
	"strings"

	"github.com/shinyvision/posthero/internal/postman"
)

// Params are an auth's settings with variables filled in.
type Params struct {
	Type string
	raw  map[string]json.RawMessage
	res  func(string) string
}

// Resolve reads an auth's settings, filling in variables with res.
func Resolve(a *postman.Auth, res func(string) string) Params {
	p := Params{raw: map[string]json.RawMessage{}, res: res}
	if a == nil {
		return p
	}
	p.Type = a.Type
	for _, kv := range a.Params[a.Type] {
		p.raw[kv.Key] = kv.Value
	}
	if p.res == nil {
		p.res = func(s string) string { return s }
	}
	return p
}

// Str returns a setting as text, variables filled in.
func (p Params) Str(key string) string {
	raw, ok := p.raw[key]
	if !ok {
		return ""
	}
	s := strings.TrimSpace(string(raw))
	if s == "" || s == "null" {
		return ""
	}
	if s[0] == '"' {
		var str string
		if json.Unmarshal(raw, &str) == nil {
			return p.res(str)
		}
	}
	return p.res(s)
}

// Or returns a setting, or def when it is empty.
func (p Params) Or(key, def string) string {
	if v := p.Str(key); v != "" {
		return v
	}
	return def
}

// Bool returns a setting as a boolean.
func (p Params) Bool(key string) bool {
	switch strings.ToLower(p.Str(key)) {
	case "true", "1", "yes", "on":
		return true
	}
	return false
}

// Int returns a setting as a number, or def.
func (p Params) Int(key string, def int) int {
	if n, err := strconv.Atoi(p.Str(key)); err == nil {
		return n
	}
	return def
}

// Has reports whether a setting is present and not empty.
func (p Params) Has(key string) bool { return p.Str(key) != "" }

// Extra is an extra parameter of an OAuth 2.0 request (tokenRequestParams
// and friends): where it goes and what it says.
type Extra struct {
	Key, Value string
	// SendAs is request_body, request_header or request_url.
	SendAs string
}

// Extras returns a list of extra parameters, the enabled ones.
func (p Params) Extras(key string) []Extra {
	raw, ok := p.raw[key]
	if !ok {
		return nil
	}
	var list []struct {
		Key     string          `json:"key"`
		Value   postman.Str     `json:"value"`
		Enabled *bool           `json:"enabled"`
		SendAs  string          `json:"send_as"`
		Extra   json.RawMessage `json:"-"`
	}
	if json.Unmarshal(raw, &list) != nil {
		return nil
	}
	var out []Extra
	for _, e := range list {
		if e.Enabled != nil && !*e.Enabled || e.Key == "" {
			continue
		}
		sendAs := e.SendAs
		if sendAs == "" {
			sendAs = "request_body"
		}
		out = append(out, Extra{Key: p.res(e.Key), Value: p.res(string(e.Value)), SendAs: sendAs})
	}
	return out
}

// Names of the auth types, for people.
var Names = map[string]string{
	"noauth": "no auth", "bearer": "Bearer token", "basic": "Basic auth", "apikey": "API key", "oauth2": "OAuth 2.0",
	"oauth1": "OAuth 1.0", "digest": "Digest auth", "awsv4": "AWS Signature", "hawk": "Hawk", "ntlm": "NTLM",
	"edgegrid": "Akamai EdgeGrid", "jwt": "JWT Bearer", "asap": "ASAP (Atlassian)",
}
