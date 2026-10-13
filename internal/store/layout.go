package store

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"

	"github.com/shinyvision/posthero/internal/jsonx"
)

// keepLayout makes next, what a file should hold now as posthero writes
// it, look like old, what the file holds, wherever the two mean the same,
// so saving a file changes only what was changed:
//   - keys stay in the order the file has them; new ones go after the key
//     they follow in next;
//   - what posthero leaves out when it is empty ("header": [], "auth":
//     null) stays;
//   - a value posthero reads as text stays as the file writes it (25 for
//     "25", a URL as just its text, a script as one string), and so does a
//     string escaped another way;
//   - the indentation, a trailing newline, Windows line ends and the
//     wrapper of an API response stay; a file on one line stays on one.
//
// When old isn't JSON, next is all there is.
func keepLayout(old, next []byte) []byte {
	if !json.Valid(old) || !json.Valid(next) {
		return next
	}
	// An API response wraps the collection or environment.
	if w := wrapper(old); w != "" && !hasKey(next, w) {
		key, _ := jsonx.Marshal(w)
		next = []byte("{" + string(key) + ":" + string(next) + "}")
	}
	merged := merge(old, next)
	var b bytes.Buffer
	indent, oneLine := indentOf(old)
	var err error
	if oneLine {
		err = json.Compact(&b, merged)
	} else {
		err = json.Indent(&b, merged, "", indent)
	}
	if err != nil {
		return next
	}
	out := b.Bytes()
	if bytes.HasSuffix(old, []byte("\n")) {
		out = append(out, '\n')
	}
	if bytes.Contains(old, []byte("\r\n")) {
		out = bytes.ReplaceAll(out, []byte("\n"), []byte("\r\n"))
	}
	return out
}

// merge is next, written the way old has it where they mean the same.
func merge(old, next json.RawMessage) []byte {
	switch {
	case kindOf(old) == '{' && kindOf(next) == '{':
		return mergeObjects(old, next)
	case kindOf(old) == '[' && kindOf(next) == '[':
		return mergeArrays(old, next)
	case sameValue(old, next):
		return old
	}
	return next
}

// field is a key of an object and its value.
type field struct {
	key string
	val json.RawMessage
}

// fields lists an object's fields in the order they are written.
func fields(raw []byte) ([]field, bool) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	if t, err := dec.Token(); err != nil || t != json.Delim('{') {
		return nil, false
	}
	var out []field
	for dec.More() {
		t, err := dec.Token()
		if err != nil {
			return nil, false
		}
		key, _ := t.(string)
		var val json.RawMessage
		if err := dec.Decode(&val); err != nil {
			return nil, false
		}
		out = append(out, field{key, val})
	}
	return out, true
}

// mergeObjects keeps old's keys in their order, the values merged with
// next's; keys next has no more go, unless they meant nothing (see
// unwritten), and keys only next has go after the key they follow there.
func mergeObjects(old, next []byte) []byte {
	of, ok1 := fields(old)
	nf, ok2 := fields(next)
	if !ok1 || !ok2 {
		return next
	}
	inOld := map[string]bool{}
	for _, f := range of {
		inOld[f.key] = true
	}
	now := map[string]json.RawMessage{}
	after := map[string][]field{} // new fields by the old key they follow ("" for none)
	anchor := ""
	for _, f := range nf {
		now[f.key] = f.val
		if inOld[f.key] {
			anchor = f.key
			continue
		}
		after[anchor] = append(after[anchor], f)
	}
	var b bytes.Buffer
	b.WriteByte('{')
	write := func(key string, val []byte) {
		if b.Len() > 1 {
			b.WriteByte(',')
		}
		k, _ := jsonx.Marshal(key)
		b.Write(k)
		b.WriteByte(':')
		b.Write(val)
	}
	for _, f := range after[""] {
		write(f.key, f.val)
	}
	done := map[string]bool{}
	for _, f := range of {
		if done[f.key] {
			continue // a key written twice: the first one counts
		}
		done[f.key] = true
		if val, ok := now[f.key]; ok {
			write(f.key, merge(f.val, val))
		} else if unwritten(f.key, f.val) {
			write(f.key, f.val)
		}
		for _, nf := range after[f.key] {
			write(nf.key, nf.val)
		}
	}
	b.WriteByte('}')
	return b.Bytes()
}

// unwritten reports whether a key next doesn't have meant nothing in old:
// an empty list or object, null, "", false or 0, which posthero leaves out.
// "enabled": false means off, so it goes when next has no "enabled".
func unwritten(key string, val []byte) bool {
	switch string(bytes.Join(bytes.Fields(val), nil)) {
	case "[]", "{}", "null", `""`, "0":
		return true
	case "false":
		return key != "enabled"
	}
	return false
}

// mergeArrays merges each element of next with the one of old it was: the
// one with the same id, key, listen or name, or else the one in its place.
func mergeArrays(old, next []byte) []byte {
	var oe, ne []json.RawMessage
	if json.Unmarshal(old, &oe) != nil || json.Unmarshal(next, &ne) != nil {
		return next
	}
	byID := map[string][]int{}
	for j, o := range oe {
		if id := identity(o); id != "" {
			byID[id] = append(byID[id], j)
		}
	}
	used := make([]bool, len(oe))
	was := make([]int, len(ne))
	for i, n := range ne {
		was[i] = -1
		for _, j := range byID[identity(n)] {
			if !used[j] {
				was[i], used[j] = j, true
				break
			}
		}
	}
	for i, n := range ne {
		if was[i] < 0 && i < len(oe) && !used[i] && kindOf(oe[i]) == kindOf(n) {
			was[i], used[i] = i, true
		}
	}
	var b bytes.Buffer
	b.WriteByte('[')
	for i, n := range ne {
		if i > 0 {
			b.WriteByte(',')
		}
		if was[i] < 0 {
			b.Write(n)
		} else {
			b.Write(merge(oe[was[i]], n))
		}
	}
	b.WriteByte(']')
	return b.Bytes()
}

// identity is what tells an element of a list apart: its id, key (a
// header, parameter or variable), listen (a script) or name (a request or
// folder). Empty when it has none.
func identity(raw []byte) string {
	if kindOf(raw) != '{' {
		return ""
	}
	var o struct{ ID, Key, Listen, Name json.RawMessage }
	if json.Unmarshal(raw, &o) != nil {
		return ""
	}
	for _, f := range []struct {
		name string
		val  json.RawMessage
	}{{"id", o.ID}, {"key", o.Key}, {"listen", o.Listen}, {"name", o.Name}} {
		if s := string(bytes.TrimSpace(f.val)); s != "" && s != "null" && s != `""` {
			return f.name + ":" + jsonText(f.val)
		}
	}
	return ""
}

// sameValue reports whether posthero reads old as next: the same JSON
// written another way; a number, boolean, null, object or list it keeps as
// text (25 for "25"); "true" or 1 for true; a script or host written as one
// string instead of a list; a URL written as just its text.
func sameValue(old, next []byte) bool {
	a, ok1 := decode(old)
	b, ok2 := decode(next)
	if !ok1 || !ok2 {
		return false
	}
	if reflect.DeepEqual(a, b) {
		return true
	}
	text, isText := a.(string)
	switch b := b.(type) {
	case string:
		return !isText && jsonText(old) == b
	case bool:
		_, isNumber := a.(json.Number)
		return (isText || isNumber) && boolish(old) == b
	case []any:
		return isText && joinLines(b) == text
	case map[string]any:
		return isText && len(b) == 1 && b["raw"] == text
	}
	return false
}

func decode(raw []byte) (any, bool) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber() // 1.0 isn't written as 1
	var v any
	return v, dec.Decode(&v) == nil
}

// jsonText is a value as posthero reads it as text: a string's contents,
// null as "", anything else as written.
func jsonText(raw []byte) string {
	raw = bytes.TrimSpace(raw)
	switch {
	case len(raw) == 0, string(raw) == "null":
		return ""
	case raw[0] == '"':
		var s string
		if json.Unmarshal(raw, &s) == nil {
			return s
		}
	}
	return string(raw)
}

// boolish reads true, "true" and 1 as true, as posthero does.
func boolish(raw []byte) bool {
	switch strings.Trim(strings.TrimSpace(string(raw)), `"`) {
	case "true", "1":
		return true
	}
	return false
}

// joinLines joins a list of strings with newlines; "\x00" when it holds
// anything else, which no string in a file equals.
func joinLines(list []any) string {
	lines := make([]string, len(list))
	for i, l := range list {
		s, ok := l.(string)
		if !ok {
			return "\x00"
		}
		lines[i] = s
	}
	return strings.Join(lines, "\n")
}

func kindOf(raw []byte) byte {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return 0
	}
	return raw[0]
}

// wrapper is the key an API response wraps a collection or environment
// in, when old is one.
func wrapper(old []byte) string {
	f, ok := fields(old)
	if !ok || len(f) != 1 || kindOf(f[0].val) != '{' {
		return ""
	}
	if k := f[0].key; k == "collection" || k == "environment" {
		return k
	}
	return ""
}

func hasKey(raw []byte, key string) bool {
	f, _ := fields(raw)
	for _, x := range f {
		if x.key == key {
			return true
		}
	}
	return false
}

// indentOf is how a file is indented: as its second line starts. A file on
// one line is written on one line.
func indentOf(old []byte) (indent string, oneLine bool) {
	text := bytes.TrimSpace(old)
	i := bytes.IndexByte(text, '\n')
	if i < 0 {
		return "", true
	}
	rest := text[i+1:]
	n := 0
	for n < len(rest) && (rest[n] == ' ' || rest[n] == '\t') {
		n++
	}
	return string(rest[:n]), false
}
