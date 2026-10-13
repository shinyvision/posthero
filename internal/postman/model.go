// Package postman models Postman collections (format v2.1, reading v2.0
// too) and environments, and talks to the Postman API to read and change
// them.
package postman

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/shinyvision/posthero/internal/jsonx"
)

// SchemaV21 is the schema URL of collection format v2.1.
const SchemaV21 = "https://schema.getpostman.com/json/collection/v2.1.0/collection.json"

// Collection is a collection: folders and requests, with the auth,
// scripts and variables they share.
type Collection struct {
	Info                    Info            `json:"info"`
	Item                    []*Item         `json:"item"`
	Event                   []Event         `json:"event,omitempty"`
	Variable                []Variable      `json:"variable,omitempty"`
	Auth                    *Auth           `json:"auth,omitempty"`
	ProtocolProfileBehavior json.RawMessage `json:"protocolProfileBehavior,omitempty"`
	Extra                   Extra           `json:"-"`
}

// Info describes a collection.
type Info struct {
	PostmanID     string       `json:"_postman_id,omitempty"`
	Name          string       `json:"name"`
	Description   *Description `json:"description,omitempty"`
	Schema        string       `json:"schema,omitempty"`
	CreatedAt     string       `json:"createdAt,omitempty"`
	UpdatedAt     string       `json:"updatedAt,omitempty"`
	LastUpdatedBy string       `json:"lastUpdatedBy,omitempty"`
	UID           string       `json:"uid,omitempty"`
	Extra         Extra        `json:"-"`
}

// Item is a folder (Folder is set; Item holds what is in it) or a request.
type Item struct {
	ID                      string          `json:"id,omitempty"`
	UID                     string          `json:"uid,omitempty"`
	Name                    string          `json:"name"`
	Description             *Description    `json:"description,omitempty"`
	Item                    []*Item         `json:"item,omitempty"`
	Request                 *Request        `json:"request,omitempty"`
	Response                []*Response     `json:"response,omitempty"`
	Event                   []Event         `json:"event,omitempty"`
	Auth                    *Auth           `json:"auth,omitempty"`
	Variable                []Variable      `json:"variable,omitempty"`
	ProtocolProfileBehavior json.RawMessage `json:"protocolProfileBehavior,omitempty"`
	CreatedAt               string          `json:"createdAt,omitempty"`
	UpdatedAt               string          `json:"updatedAt,omitempty"`
	Extra                   Extra           `json:"-"`

	// Folder says this is a folder, which it is even when empty.
	Folder bool `json:"-"`
}

// Request is what a request item sends.
type Request struct {
	URL         *URL         `json:"url,omitempty"`
	Method      string       `json:"method,omitempty"`
	Header      Params       `json:"header,omitempty"`
	Body        *Body        `json:"body,omitempty"`
	Auth        *Auth        `json:"auth,omitempty"`
	Description *Description `json:"description,omitempty"`
	Extra       Extra        `json:"-"`
}

// URL is a request's address. Raw is how it is typed; the parts are what
// Postman keeps besides, including disabled query parameters and the
// values of path variables (:id).
type URL struct {
	Raw      string     `json:"raw"`
	Protocol string     `json:"protocol,omitempty"`
	Host     StrList    `json:"host,omitempty"`
	Port     string     `json:"port,omitempty"`
	Path     StrList    `json:"path,omitempty"`
	Query    Params     `json:"query,omitempty"`
	Hash     string     `json:"hash,omitempty"`
	Variable []Variable `json:"variable,omitempty"`
	Extra    Extra      `json:"-"`
}

// Param is a header, a query parameter, or a field of a form body.
type Param struct {
	Key         string       `json:"key"`
	Value       Str          `json:"value"`
	Disabled    bool         `json:"disabled,omitempty"`
	Description *Description `json:"description,omitempty"`
	// Type is "text" or "file" in form-data bodies.
	Type string `json:"type,omitempty"`
	// Src is the file (or files) of a form-data file field.
	Src         json.RawMessage `json:"src,omitempty"`
	ContentType string          `json:"contentType,omitempty"`
	Extra       Extra           `json:"-"`
	// NoValue is a query parameter written without =, like ?debug; its
	// value is null.
	NoValue bool `json:"-"`
}

// Params is a list of parameters. Headers may also be written as one
// "Name: value" string per line.
type Params []Param

// Body is what a request sends after its headers.
type Body struct {
	// Mode is raw, urlencoded, formdata, file or graphql.
	Mode       string          `json:"mode"`
	Raw        string          `json:"raw,omitempty"`
	URLEncoded Params          `json:"urlencoded,omitempty"`
	FormData   Params          `json:"formdata,omitempty"`
	File       *BodyFile       `json:"file,omitempty"`
	GraphQL    *GraphQL        `json:"graphql,omitempty"`
	Options    json.RawMessage `json:"options,omitempty"`
	Disabled   bool            `json:"disabled,omitempty"`
	Extra      Extra           `json:"-"`
}

// BodyFile is the file a binary body sends.
type BodyFile struct {
	Src     Str   `json:"src,omitempty"`
	Content Str   `json:"content,omitempty"`
	Extra   Extra `json:"-"`
}

// GraphQL is a GraphQL body: the query and its variables (JSON text).
type GraphQL struct {
	Query     Str   `json:"query"`
	Variables Str   `json:"variables,omitempty"`
	Extra     Extra `json:"-"`
}

// Event is a script that runs before a request (prerequest) or after its
// response (test).
type Event struct {
	ID       string `json:"id,omitempty"`
	Listen   string `json:"listen"`
	Script   Script `json:"script"`
	Disabled bool   `json:"disabled,omitempty"`
	Extra    Extra  `json:"-"`
}

// Script is JavaScript.
type Script struct {
	ID    string `json:"id,omitempty"`
	Type  string `json:"type,omitempty"`
	Exec  Lines  `json:"exec"`
	Name  string `json:"name,omitempty"`
	Extra Extra  `json:"-"`
}

// Variable is a collection variable, a path variable or a global.
type Variable struct {
	ID          string       `json:"id,omitempty"`
	Key         string       `json:"key"`
	Value       Str          `json:"value"`
	Type        string       `json:"type,omitempty"`
	Name        string       `json:"name,omitempty"`
	Description *Description `json:"description,omitempty"`
	System      bool         `json:"system,omitempty"`
	Disabled    bool         `json:"disabled,omitempty"`
	Extra       Extra        `json:"-"`
}

// Response is a saved example: a response kept with its request.
type Response struct {
	ID              string          `json:"id,omitempty"`
	UID             string          `json:"uid,omitempty"`
	Name            string          `json:"name"`
	OriginalRequest *Request        `json:"originalRequest,omitempty"`
	Status          string          `json:"status,omitempty"`
	Code            int             `json:"code,omitempty"`
	PreviewLanguage string          `json:"_postman_previewlanguage,omitempty"`
	Header          Params          `json:"header,omitempty"`
	Cookie          json.RawMessage `json:"cookie,omitempty"`
	Body            string          `json:"body,omitempty"`
	Extra           Extra           `json:"-"`
}

// Auth is how a request authenticates. Params holds the settings of each
// type present (usually only Type's).
type Auth struct {
	Type   string
	Params map[string][]AuthParam
}

// AuthParam is one setting of an auth type. Value is JSON: mostly a string,
// sometimes a boolean or a list of parameters.
type AuthParam struct {
	Key   string          `json:"key"`
	Value json.RawMessage `json:"value"`
	Type  string          `json:"type,omitempty"`
}

// StrList is a list of strings that may be written as one string (host
// "a.example.com", path "a/b") or as segment objects.
type StrList []string

// UnmarshalJSON implements json.Unmarshaler.
func (l *StrList) UnmarshalJSON(data []byte) error {
	data = bytes.TrimSpace(data)
	*l = nil
	switch {
	case len(data) == 0 || string(data) == "null":
	case data[0] == '"':
		var s string
		if err := json.Unmarshal(data, &s); err != nil {
			return err
		}
		*l = StrList{s}
	case data[0] == '[':
		var raw []json.RawMessage
		if err := json.Unmarshal(data, &raw); err != nil {
			return err
		}
		for _, r := range raw {
			if bytes.HasPrefix(bytes.TrimSpace(r), []byte("{")) {
				var seg struct {
					Value Str `json:"value"`
				}
				_ = json.Unmarshal(r, &seg)
				*l = append(*l, string(seg.Value))
				continue
			}
			*l = append(*l, jsonText(r))
		}
	}
	return nil
}

// UnmarshalJSON implements json.Unmarshaler.
func (c *Collection) UnmarshalJSON(data []byte) error {
	type plain Collection
	var p plain
	ex, err := unmarshalKeeping(data, &p)
	if err != nil {
		return err
	}
	*c = Collection(p)
	c.Extra = ex
	return nil
}

// MarshalJSON implements json.Marshaler.
func (c Collection) MarshalJSON() ([]byte, error) {
	type plain Collection
	if c.Item == nil {
		c.Item = []*Item{}
	}
	return marshalKeeping(plain(c), c.Extra)
}

// UnmarshalJSON implements json.Unmarshaler.
func (i *Info) UnmarshalJSON(data []byte) error {
	type plain Info
	var p plain
	ex, err := unmarshalKeeping(data, &p)
	if err != nil {
		return err
	}
	*i = Info(p)
	i.Extra = ex
	return nil
}

// MarshalJSON implements json.Marshaler.
func (i Info) MarshalJSON() ([]byte, error) {
	type plain Info
	return marshalKeeping(plain(i), i.Extra)
}

// UnmarshalJSON implements json.Unmarshaler.
func (it *Item) UnmarshalJSON(data []byte) error {
	type plain Item
	var p plain
	ex, err := unmarshalKeeping(data, &p)
	if err != nil {
		return err
	}
	*it = Item(p)
	it.Extra = ex
	var keys map[string]json.RawMessage
	_ = json.Unmarshal(data, &keys)
	_, hasItems := keys["item"]
	_, hasRequest := keys["request"]
	it.Folder = hasItems || !hasRequest
	if it.Folder {
		it.Request = nil
	}
	return nil
}

// MarshalJSON implements json.Marshaler. A folder always has its item list,
// even when empty, and a request item always has its request.
func (it Item) MarshalJSON() ([]byte, error) {
	type plain struct {
		ID                      string          `json:"id,omitempty"`
		UID                     string          `json:"uid,omitempty"`
		Name                    string          `json:"name"`
		Description             *Description    `json:"description,omitempty"`
		Item                    *[]*Item        `json:"item,omitempty"`
		Request                 *Request        `json:"request,omitempty"`
		Response                *[]*Response    `json:"response,omitempty"`
		Event                   []Event         `json:"event,omitempty"`
		Auth                    *Auth           `json:"auth,omitempty"`
		Variable                []Variable      `json:"variable,omitempty"`
		ProtocolProfileBehavior json.RawMessage `json:"protocolProfileBehavior,omitempty"`
		CreatedAt               string          `json:"createdAt,omitempty"`
		UpdatedAt               string          `json:"updatedAt,omitempty"`
	}
	p := plain{ID: it.ID, UID: it.UID, Name: it.Name, Description: it.Description, Event: it.Event, Auth: it.Auth,
		Variable: it.Variable, ProtocolProfileBehavior: it.ProtocolProfileBehavior, CreatedAt: it.CreatedAt, UpdatedAt: it.UpdatedAt}
	if it.Folder {
		items := it.Item
		if items == nil {
			items = []*Item{}
		}
		p.Item = &items
	} else {
		req := it.Request
		if req == nil {
			req = &Request{Method: "GET", URL: &URL{}}
		}
		p.Request = req
		resp := it.Response
		if resp == nil {
			resp = []*Response{}
		}
		p.Response = &resp
	}
	return marshalKeeping(p, it.Extra)
}

// UnmarshalJSON implements json.Unmarshaler. A request may be just its URL.
func (r *Request) UnmarshalJSON(data []byte) error {
	data = bytes.TrimSpace(data)
	if len(data) > 0 && data[0] == '"' {
		*r = Request{Method: "GET", URL: &URL{Raw: jsonText(data)}}
		return nil
	}
	type plain Request
	var p plain
	ex, err := unmarshalKeeping(data, &p)
	if err != nil {
		return err
	}
	*r = Request(p)
	r.Extra = ex
	return nil
}

// MarshalJSON implements json.Marshaler.
func (r Request) MarshalJSON() ([]byte, error) {
	type plain Request
	return marshalKeeping(plain(r), r.Extra)
}

// UnmarshalJSON implements json.Unmarshaler. A URL may be just a string.
func (u *URL) UnmarshalJSON(data []byte) error {
	data = bytes.TrimSpace(data)
	if len(data) > 0 && data[0] == '"' {
		*u = URL{Raw: jsonText(data)}
		return nil
	}
	type plain URL
	var p plain
	ex, err := unmarshalKeeping(data, &p)
	if err != nil {
		return err
	}
	*u = URL(p)
	u.Extra = ex
	return nil
}

// MarshalJSON implements json.Marshaler.
func (u URL) MarshalJSON() ([]byte, error) {
	type plain URL
	return marshalKeeping(plain(u), u.Extra)
}

// UnmarshalJSON implements json.Unmarshaler.
func (p *Param) UnmarshalJSON(data []byte) error {
	type plain Param
	var q plain
	ex, err := unmarshalKeeping(data, &q)
	if err != nil {
		return err
	}
	*p = Param(q)
	if raw, ok := ex["enabled"]; ok {
		// The API's own format says enabled instead of disabled.
		p.Disabled = !boolish(raw)
		delete(ex, "enabled")
	}
	if raw, ok := rawField(data, "disabled"); ok {
		p.Disabled = boolish(raw)
	}
	if raw, ok := rawField(data, "value"); ok && string(raw) == "null" && p.Type != "file" {
		p.NoValue = true
	}
	if len(ex) == 0 {
		ex = nil
	}
	p.Extra = ex
	return nil
}

// MarshalJSON implements json.Marshaler.
func (p Param) MarshalJSON() ([]byte, error) {
	type plain Param
	if p.NoValue && p.Value == "" || p.Type == "file" && p.Value == "" {
		type nullValue struct {
			plain
			Value *string `json:"value"`
		}
		return marshalKeeping(nullValue{plain: plain(p)}, p.Extra)
	}
	return marshalKeeping(plain(p), p.Extra)
}

// UnmarshalJSON implements json.Unmarshaler. Headers may be a string of
// "Name: value" lines; a line starting with // is a disabled header.
func (ps *Params) UnmarshalJSON(data []byte) error {
	data = bytes.TrimSpace(data)
	*ps = nil
	switch {
	case len(data) == 0 || string(data) == "null":
	case data[0] == '"':
		var s string
		if err := json.Unmarshal(data, &s); err != nil {
			return err
		}
		for _, line := range strings.Split(s, "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			disabled := strings.HasPrefix(line, "//")
			line = strings.TrimSpace(strings.TrimPrefix(line, "//"))
			k, v, _ := strings.Cut(line, ":")
			*ps = append(*ps, Param{Key: strings.TrimSpace(k), Value: Str(strings.TrimSpace(v)), Disabled: disabled})
		}
	default:
		var list []Param
		if err := json.Unmarshal(data, &list); err != nil {
			return err
		}
		*ps = list
	}
	return nil
}

// UnmarshalJSON implements json.Unmarshaler.
func (b *Body) UnmarshalJSON(data []byte) error {
	type plain Body
	var p plain
	ex, err := unmarshalKeeping(data, &p)
	if err != nil {
		return err
	}
	*b = Body(p)
	b.Extra = ex
	return nil
}

// MarshalJSON implements json.Marshaler.
func (b Body) MarshalJSON() ([]byte, error) {
	type plain Body
	return marshalKeeping(plain(b), b.Extra)
}

// UnmarshalJSON implements json.Unmarshaler. A file body's src may be null.
func (f *BodyFile) UnmarshalJSON(data []byte) error {
	type plain BodyFile
	var p plain
	ex, err := unmarshalKeeping(data, &p)
	if err != nil {
		return err
	}
	*f = BodyFile(p)
	f.Extra = ex
	return nil
}

// MarshalJSON implements json.Marshaler.
func (f BodyFile) MarshalJSON() ([]byte, error) {
	type plain BodyFile
	return marshalKeeping(plain(f), f.Extra)
}

// UnmarshalJSON implements json.Unmarshaler.
func (g *GraphQL) UnmarshalJSON(data []byte) error {
	type plain GraphQL
	var p plain
	ex, err := unmarshalKeeping(data, &p)
	if err != nil {
		return err
	}
	*g = GraphQL(p)
	g.Extra = ex
	return nil
}

// MarshalJSON implements json.Marshaler.
func (g GraphQL) MarshalJSON() ([]byte, error) {
	type plain GraphQL
	return marshalKeeping(plain(g), g.Extra)
}

// UnmarshalJSON implements json.Unmarshaler.
func (e *Event) UnmarshalJSON(data []byte) error {
	type plain Event
	var p plain
	ex, err := unmarshalKeeping(data, &p)
	if err != nil {
		return err
	}
	*e = Event(p)
	e.Extra = ex
	return nil
}

// MarshalJSON implements json.Marshaler.
func (e Event) MarshalJSON() ([]byte, error) {
	type plain Event
	return marshalKeeping(plain(e), e.Extra)
}

// UnmarshalJSON implements json.Unmarshaler.
func (s *Script) UnmarshalJSON(data []byte) error {
	type plain Script
	var p plain
	ex, err := unmarshalKeeping(data, &p)
	if err != nil {
		return err
	}
	*s = Script(p)
	s.Extra = ex
	return nil
}

// MarshalJSON implements json.Marshaler.
func (s Script) MarshalJSON() ([]byte, error) {
	type plain Script
	if s.Exec == nil {
		s.Exec = Lines{}
	}
	return marshalKeeping(plain(s), s.Extra)
}

// UnmarshalJSON implements json.Unmarshaler.
func (v *Variable) UnmarshalJSON(data []byte) error {
	type plain Variable
	var p plain
	ex, err := unmarshalKeeping(data, &p)
	if err != nil {
		return err
	}
	*v = Variable(p)
	if v.Key == "" && v.ID != "" {
		v.Key = v.ID // v2.0 names variables by id
	}
	if raw, ok := ex["enabled"]; ok {
		v.Disabled = !boolish(raw)
		delete(ex, "enabled")
	}
	if raw, ok := rawField(data, "disabled"); ok {
		v.Disabled = boolish(raw)
	}
	if len(ex) == 0 {
		ex = nil
	}
	v.Extra = ex
	return nil
}

// MarshalJSON implements json.Marshaler.
func (v Variable) MarshalJSON() ([]byte, error) {
	type plain Variable
	return marshalKeeping(plain(v), v.Extra)
}

// UnmarshalJSON implements json.Unmarshaler.
func (r *Response) UnmarshalJSON(data []byte) error {
	type plain Response
	var p plain
	ex, err := unmarshalKeeping(data, &p)
	if err != nil {
		return err
	}
	*r = Response(p)
	r.Extra = ex
	return nil
}

// MarshalJSON implements json.Marshaler.
func (r Response) MarshalJSON() ([]byte, error) {
	type plain Response
	return marshalKeeping(plain(r), r.Extra)
}

// UnmarshalJSON implements json.Unmarshaler. Settings are lists of
// {key, value, type} in v2.1 and plain objects in v2.0.
func (a *Auth) UnmarshalJSON(data []byte) error {
	var all map[string]json.RawMessage
	if err := json.Unmarshal(data, &all); err != nil {
		return err
	}
	*a = Auth{Type: jsonText(all["type"]), Params: map[string][]AuthParam{}}
	for k, raw := range all {
		if k == "type" {
			continue
		}
		raw = bytes.TrimSpace(raw)
		switch {
		case len(raw) > 0 && raw[0] == '[':
			var list []AuthParam
			if json.Unmarshal(raw, &list) == nil {
				a.Params[k] = list
			}
		case len(raw) > 0 && raw[0] == '{':
			var obj map[string]json.RawMessage
			if json.Unmarshal(raw, &obj) == nil {
				names := make([]string, 0, len(obj))
				for n := range obj {
					names = append(names, n)
				}
				sort.Strings(names)
				var list []AuthParam
				for _, n := range names {
					list = append(list, AuthParam{Key: n, Value: obj[n], Type: "string"})
				}
				a.Params[k] = list
			}
		}
	}
	return nil
}

// MarshalJSON implements json.Marshaler.
func (a Auth) MarshalJSON() ([]byte, error) {
	var b bytes.Buffer
	t, _ := jsonx.Marshal(a.Type)
	b.WriteString(`{"type":`)
	b.Write(t)
	names := make([]string, 0, len(a.Params))
	for k := range a.Params {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, k := range names {
		list, err := jsonx.Marshal(a.Params[k])
		if err != nil {
			return nil, err
		}
		key, _ := jsonx.Marshal(k)
		b.WriteByte(',')
		b.Write(key)
		b.WriteByte(':')
		b.Write(list)
	}
	b.WriteByte('}')
	return b.Bytes(), nil
}

// MarshalJSON implements json.Marshaler: a value without one is null.
func (p AuthParam) MarshalJSON() ([]byte, error) {
	type plain AuthParam
	if len(p.Value) == 0 {
		p.Value = json.RawMessage(`""`)
	}
	return jsonx.Marshal(plain(p))
}

// NewID returns a random UUID, the kind of id Postman gives scripts and
// items.
func NewID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = b[6]&0x0f | 0x40 // version 4
	b[8] = b[8]&0x3f | 0x80 // RFC 4122 variant
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}
