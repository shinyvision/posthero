package postman

import (
	"encoding/json"
	"strings"

	"github.com/shinyvision/posthero/internal/jsonx"
)

// Walk calls f for every item, depth first, with the folders it is in
// (outermost first). Returning false from f skips a folder's contents.
func (c *Collection) Walk(f func(it *Item, parents []*Item) bool) {
	var walk func(items []*Item, parents []*Item)
	walk = func(items []*Item, parents []*Item) {
		for _, it := range items {
			if f(it, parents) && it.Folder {
				walk(it.Item, append(parents[:len(parents):len(parents)], it))
			}
		}
	}
	walk(c.Item, nil)
}

// Find returns the item with an id and the folders it is in.
func (c *Collection) Find(id string) (*Item, []*Item) {
	var found *Item
	var path []*Item
	c.Walk(func(it *Item, parents []*Item) bool {
		if found != nil {
			return false
		}
		if it.ID == id || (it.UID != "" && it.UID == id) {
			found, path = it, parents
			return false
		}
		return true
	})
	return found, path
}

// FindExample returns the saved example with an id, the request it belongs
// to and the folders that request is in.
func (c *Collection) FindExample(id string) (*Item, *Response, []*Item) {
	if id == "" {
		return nil, nil, nil
	}
	var req *Item
	var found *Response
	var path []*Item
	c.Walk(func(it *Item, parents []*Item) bool {
		if found != nil {
			return false
		}
		if r := it.Example(id); r != nil {
			req, found, path = it, r, parents
			return false
		}
		return true
	})
	return req, found, path
}

// Example returns the request's saved example with an id, or nil.
func (it *Item) Example(id string) *Response {
	for _, r := range it.Response {
		if r.ID == id || (r.UID != "" && r.UID == id) {
			return r
		}
	}
	return nil
}

// Requests returns every request item with the folders it is in.
func (c *Collection) Requests() []Located {
	var out []Located
	c.Walk(func(it *Item, parents []*Item) bool {
		if !it.Folder {
			out = append(out, Located{Item: it, Parents: parents})
		}
		return true
	})
	return out
}

// Located is an item with the folders it is in.
type Located struct {
	Item    *Item
	Parents []*Item
}

// Path is the names of the folders and the item, joined with " / ".
func (l Located) Path() string {
	names := make([]string, 0, len(l.Parents)+1)
	for _, p := range l.Parents {
		names = append(names, p.Name)
	}
	return strings.Join(append(names, l.Item.Name), " / ")
}

// Container returns the item list a parent (nil: the collection) holds.
func (c *Collection) Container(parent *Item) *[]*Item {
	if parent == nil {
		return &c.Item
	}
	return &parent.Item
}

// ParentOf returns the folder an item is in (nil at the top), and whether
// the item was found.
func (c *Collection) ParentOf(id string) (*Item, bool) {
	it, parents := c.Find(id)
	if it == nil {
		return nil, false
	}
	if len(parents) == 0 {
		return nil, true
	}
	return parents[len(parents)-1], true
}

// Remove takes an item out of the collection and returns it.
func (c *Collection) Remove(id string) *Item {
	parent, ok := c.ParentOf(id)
	if !ok {
		return nil
	}
	list := c.Container(parent)
	for i, it := range *list {
		if it.ID == id || it.UID == id {
			*list = append((*list)[:i], (*list)[i+1:]...)
			return it
		}
	}
	return nil
}

// Insert puts an item in a folder (nil: the collection) at index i, or at
// the end when i is out of range.
func (c *Collection) Insert(parent *Item, i int, it *Item) {
	list := c.Container(parent)
	if i < 0 || i > len(*list) {
		i = len(*list)
	}
	*list = append(*list, nil)
	copy((*list)[i+1:], (*list)[i:])
	(*list)[i] = it
}

// Scripts returns the text of the item's scripts for an event (prerequest
// or test), skipping disabled ones.
func Scripts(events []Event, listen string) string {
	var parts []string
	for _, e := range events {
		if e.Listen == listen && !e.Disabled {
			if t := strings.TrimSpace(e.Script.Exec.Text()); t != "" {
				parts = append(parts, e.Script.Exec.Text())
			}
		}
	}
	return strings.Join(parts, "\n")
}

// SetScript replaces the script for an event, keeping the event's id;
// empty text removes it. Disabled scripts are not what Scripts shows, so
// they stay as they are.
func SetScript(events []Event, listen, text string) []Event {
	out := make([]Event, 0, len(events)+1)
	done := false
	for _, e := range events {
		if e.Listen != listen || e.Disabled {
			out = append(out, e)
			continue
		}
		if done || strings.TrimSpace(text) == "" {
			continue
		}
		e.Script.Exec = SplitLines(text)
		if e.Script.Type == "" {
			e.Script.Type = "text/javascript"
		}
		out = append(out, e)
		done = true
	}
	if !done && strings.TrimSpace(text) != "" {
		out = append(out, Event{Listen: listen, Script: Script{Type: "text/javascript", Exec: SplitLines(text)}})
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// Get returns the value of an auth setting as text.
func (a *Auth) Get(key string) string {
	if a == nil {
		return ""
	}
	for _, p := range a.Params[a.Type] {
		if p.Key == key {
			return jsonText(p.Value)
		}
	}
	return ""
}

// Has reports whether an auth setting is present.
func (a *Auth) Has(key string) bool {
	if a == nil {
		return false
	}
	for _, p := range a.Params[a.Type] {
		if p.Key == key {
			return true
		}
	}
	return false
}

// Raw returns an auth setting's JSON.
func (a *Auth) Raw(key string) json.RawMessage {
	if a == nil {
		return nil
	}
	for _, p := range a.Params[a.Type] {
		if p.Key == key {
			return p.Value
		}
	}
	return nil
}

// Set changes an auth setting of the current type, adding it when missing.
func (a *Auth) Set(key, value string) {
	raw, _ := jsonx.Marshal(value)
	a.SetRaw(key, raw)
}

// SetRaw changes an auth setting to a JSON value.
func (a *Auth) SetRaw(key string, value json.RawMessage) {
	if a.Params == nil {
		a.Params = map[string][]AuthParam{}
	}
	list := a.Params[a.Type]
	for i, p := range list {
		if p.Key == key {
			list[i].Value = value
			return
		}
	}
	typ := "string"
	if len(value) > 0 && (value[0] == 't' || value[0] == 'f') {
		typ = "boolean"
	} else if len(value) > 0 && (value[0] == '[' || value[0] == '{') {
		typ = "any"
	}
	a.Params[a.Type] = append(list, AuthParam{Key: key, Value: value, Type: typ})
}

// Keys returns the settings of the current type in order.
func (a *Auth) Keys() []string {
	if a == nil {
		return nil
	}
	var out []string
	for _, p := range a.Params[a.Type] {
		out = append(out, p.Key)
	}
	return out
}

// Language is the language of a raw body (json, text, javascript, html,
// xml), from its options.
func (b *Body) Language() string {
	if b == nil || len(b.Options) == 0 {
		return ""
	}
	var o struct {
		Raw struct {
			Language string `json:"language"`
		} `json:"raw"`
	}
	_ = json.Unmarshal(b.Options, &o)
	return o.Raw.Language
}

// SetLanguage records the language of a raw body in its options, keeping
// the other options.
func (b *Body) SetLanguage(lang string) {
	var o map[string]any
	_ = json.Unmarshal(b.Options, &o)
	if o == nil {
		o = map[string]any{}
	}
	raw, _ := o["raw"].(map[string]any)
	if raw == nil {
		raw = map[string]any{}
	}
	if lang == "" {
		delete(raw, "language")
	} else {
		raw["language"] = lang
	}
	if len(raw) == 0 {
		delete(o, "raw")
	} else {
		o["raw"] = raw
	}
	if len(o) == 0 {
		b.Options = nil
		return
	}
	b.Options, _ = jsonx.Marshal(o)
}

// Files returns the file paths of a form-data file field.
func (p Param) Files() []string {
	if len(p.Src) == 0 {
		return nil
	}
	var one string
	if json.Unmarshal(p.Src, &one) == nil {
		if one == "" {
			return nil
		}
		return []string{one}
	}
	var many []string
	_ = json.Unmarshal(p.Src, &many)
	return many
}

// Enabled returns the parameters that are switched on.
func (ps Params) Enabled() Params {
	var out Params
	for _, p := range ps {
		if !p.Disabled {
			out = append(out, p)
		}
	}
	return out
}

// Get returns the value of the first enabled parameter named key, ignoring
// case.
func (ps Params) Get(key string) (string, bool) {
	for _, p := range ps {
		if !p.Disabled && strings.EqualFold(p.Key, key) {
			return string(p.Value), true
		}
	}
	return "", false
}
