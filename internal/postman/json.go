package postman

import (
	"bytes"
	"encoding/json"
	"errors"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/shinyvision/posthero/internal/jsonx"
)

// Extra holds the JSON fields of an object that posthero doesn't model, so
// writing a collection back keeps them.
type Extra map[string]json.RawMessage

var knownKeys sync.Map // reflect.Type -> map[string]bool

// keysOf returns the JSON names of a struct's fields.
func keysOf(t reflect.Type) map[string]bool {
	if k, ok := knownKeys.Load(t); ok {
		return k.(map[string]bool)
	}
	keys := map[string]bool{}
	for i := range t.NumField() {
		tag := t.Field(i).Tag.Get("json")
		name, _, _ := strings.Cut(tag, ",")
		if name != "" && name != "-" {
			keys[name] = true
		}
	}
	knownKeys.Store(t, keys)
	return keys
}

// unmarshalKeeping decodes data into v (a pointer to a plain struct type,
// without UnmarshalJSON) and returns the fields v has no place for. A value
// of the wrong type (a number where a string goes) leaves its field empty
// instead of failing the whole collection.
func unmarshalKeeping(data []byte, v any) (Extra, error) {
	if err := json.Unmarshal(data, v); err != nil {
		var typeErr *json.UnmarshalTypeError
		if !errors.As(err, &typeErr) {
			return nil, err
		}
	}
	var all map[string]json.RawMessage
	if err := json.Unmarshal(data, &all); err != nil {
		return nil, nil // not an object; v decoded it some other way
	}
	known := keysOf(reflect.TypeOf(v).Elem())
	var ex Extra
	for k, raw := range all {
		if !known[k] {
			if ex == nil {
				ex = Extra{}
			}
			ex[k] = raw
		}
	}
	return ex, nil
}

// marshalKeeping encodes v (a plain struct type, without MarshalJSON) and
// adds the extra fields, in a stable order after the known ones.
func marshalKeeping(v any, ex Extra) ([]byte, error) {
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
	b.Write(data[:len(data)-1]) // without the closing brace
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

// Str is a JSON value read as a string. Postman mostly writes strings, but
// numbers, booleans and null turn up in variables and parameters; objects
// and arrays become their JSON text.
type Str string

// UnmarshalJSON implements json.Unmarshaler.
func (s *Str) UnmarshalJSON(data []byte) error {
	*s = Str(jsonText(data))
	return nil
}

// jsonText is a JSON value as text: a string's contents, null as "", and
// anything else as written.
func jsonText(data []byte) string {
	data = bytes.TrimSpace(data)
	switch {
	case len(data) == 0, string(data) == "null":
		return ""
	case data[0] == '"':
		var str string
		if json.Unmarshal(data, &str) == nil {
			return str
		}
	}
	return string(data)
}

// Lines is a script or text that Postman writes either as one string or as
// an array of lines.
type Lines []string

// UnmarshalJSON implements json.Unmarshaler.
func (l *Lines) UnmarshalJSON(data []byte) error {
	data = bytes.TrimSpace(data)
	switch {
	case len(data) == 0 || string(data) == "null":
		*l = nil
	case data[0] == '"':
		var s string
		if err := json.Unmarshal(data, &s); err != nil {
			return err
		}
		*l = strings.Split(s, "\n")
	default:
		var raw []json.RawMessage
		if err := json.Unmarshal(data, &raw); err != nil {
			return err
		}
		out := make([]string, 0, len(raw))
		for _, r := range raw {
			out = append(out, jsonText(r))
		}
		*l = out
	}
	return nil
}

// Text joins the lines.
func (l Lines) Text() string { return strings.Join(l, "\n") }

// SplitLines turns text into Lines, nil when it is empty.
func SplitLines(text string) Lines {
	if text == "" {
		return nil
	}
	return strings.Split(text, "\n")
}

// Description is documentation in Markdown. Postman writes it as a string
// or as {"content": …, "type": "text/markdown"}.
type Description struct {
	Content string
	Type    string
	// Object says it was written as an object, so it is written back as one.
	Object bool
}

// UnmarshalJSON implements json.Unmarshaler.
func (d *Description) UnmarshalJSON(data []byte) error {
	data = bytes.TrimSpace(data)
	*d = Description{}
	if len(data) > 0 && data[0] == '{' {
		var o struct {
			Content Str `json:"content"`
			Type    Str `json:"type"`
		}
		if err := json.Unmarshal(data, &o); err != nil {
			return err
		}
		*d = Description{Content: string(o.Content), Type: string(o.Type), Object: true}
		return nil
	}
	d.Content = jsonText(data)
	return nil
}

// MarshalJSON implements json.Marshaler.
func (d Description) MarshalJSON() ([]byte, error) {
	if d.Object {
		return jsonx.Marshal(map[string]string{"content": d.Content, "type": d.Type})
	}
	return jsonx.Marshal(d.Content)
}

// IsZero lets omitempty-like checks skip an empty description.
func (d Description) IsZero() bool { return d.Content == "" }

// rawField returns one field of a JSON object.
func rawField(data []byte, name string) (json.RawMessage, bool) {
	var all map[string]json.RawMessage
	if json.Unmarshal(data, &all) != nil {
		return nil, false
	}
	raw, ok := all[name]
	return raw, ok
}

// boolish reads true, "true" and 1 as true, the way Postman's exports mix
// them.
func boolish(data []byte) bool {
	switch strings.Trim(strings.TrimSpace(string(data)), `"`) {
	case "true", "1":
		return true
	}
	return false
}

// Bool is a boolean that may be written as a string.
type Bool bool

// UnmarshalJSON implements json.Unmarshaler.
func (b *Bool) UnmarshalJSON(data []byte) error {
	*b = Bool(boolish(data))
	return nil
}

// formatNumber writes a float the way JavaScript prints numbers.
func formatNumber(f float64) string {
	return strconv.FormatFloat(f, 'f', -1, 64)
}
