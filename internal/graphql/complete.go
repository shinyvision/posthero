package graphql

import (
	"sort"
	"strings"
)

// token is a lexical token of a GraphQL document.
type token struct {
	kind       byte // 'n' name, 'p' punctuator, 's' string, '0' number
	text       string
	start, end int
	// open says a string runs to the end without its closing quote.
	open bool
}

// lex splits a document into tokens, leaving out comments, commas and
// white space. An unfinished string runs to the end.
func lex(doc string) []token {
	var out []token
	i := 0
	for i < len(doc) {
		c := doc[i]
		switch {
		case c == '#':
			for i < len(doc) && doc[i] != '\n' {
				i++
			}
		case c == ',' || c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == 0xEF || c == 0xBB || c == 0xBF:
			i++
		case c == '"':
			start, open := i, true
			if strings.HasPrefix(doc[i:], `"""`) {
				i += 3
				for i < len(doc) {
					if strings.HasPrefix(doc[i:], `\"""`) {
						i += 4 // an escaped """ stays in the string
						continue
					}
					if strings.HasPrefix(doc[i:], `"""`) {
						i, open = i+3, false
						break
					}
					i++
				}
			} else {
				i++
				for i < len(doc) && doc[i] != '"' && doc[i] != '\n' {
					if doc[i] == '\\' {
						i++
					}
					i++
				}
				if i < len(doc) && doc[i] == '"' {
					i, open = i+1, false
				}
			}
			i = min(i, len(doc))
			out = append(out, token{kind: 's', text: doc[start:i], start: start, end: i, open: open})
		case c == '.' && strings.HasPrefix(doc[i:], "..."):
			out = append(out, token{kind: 'p', text: "...", start: i, end: i + 3})
			i += 3
		case isNameStart(c):
			start := i
			for i < len(doc) && isName(doc[i]) {
				i++
			}
			out = append(out, token{kind: 'n', text: doc[start:i], start: start, end: i})
		case c == '-' || c >= '0' && c <= '9':
			start := i
			i++
			for i < len(doc) && (isName(doc[i]) || doc[i] == '.' || doc[i] == '+' || doc[i] == '-') {
				i++
			}
			out = append(out, token{kind: '0', text: doc[start:i], start: start, end: i})
		default:
			out = append(out, token{kind: 'p', text: string(c), start: i, end: i + 1})
			i++
		}
	}
	return out
}

func isNameStart(c byte) bool { return c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' }
func isName(c byte) bool      { return isNameStart(c) || c >= '0' && c <= '9' }

// SpotKind says what can be typed at a place in a query.
type SpotKind int

// Kinds of spots.
const (
	// Nothing known can go here.
	Nothing SpotKind = iota
	// Top is between operations: query, mutation, subscription, fragment.
	Top
	// Fields of Type go here (a selection set).
	Fields
	// Arguments of Type.Field go here.
	Arguments
	// InputFields of the input object Type go here.
	InputFields
	// Value is a value of ValueType.
	Value
)

// Spot is what is at a place in a query.
type Spot struct {
	Kind SpotKind
	// Type is the type of the selection set, the type with the field whose
	// arguments these are, or the input object.
	Type string
	// Field is the field whose arguments go here.
	Field string
	// ValueType is the type a value must have.
	ValueType TypeRef
	// Word is the name before the cursor, which Start is the beginning of.
	Word  string
	Start int
}

// frame is a level of nesting while walking a query.
type frame struct {
	kind byte // 's' selection set, 'a' arguments, 'o' input object, 'l' list value, 'x' skipped
	typ  string
	// field is the selection's last field, or the field the arguments are of.
	field string
	// expectName is set where an argument or input field name goes, unset
	// after its colon, where the value goes.
	expectName bool
	// valueType is the type of the value after a colon.
	valueType TypeRef
	// elem is a list value's element type.
	elem TypeRef
	// fieldType is the type of field, for the selection set it may open.
	fieldType string
}

// walker follows a document's nesting, token by token.
type walker struct {
	s     *Schema
	stack []frame
	toks  []token
	i     int
	// op is the operation keyword before the first selection set.
	op string
	// pendingOn says "... on" or "fragment X on" was just read: the next
	// name is a type condition.
	pendingOn bool
	cond      string
	// skipParens counts parentheses being skipped (variable definitions,
	// directive arguments).
	skipParens int
}

func (w *walker) top() *frame {
	if len(w.stack) == 0 {
		return nil
	}
	return &w.stack[len(w.stack)-1]
}

// valueDone marks the value after a colon finished.
func (w *walker) valueDone() {
	if f := w.top(); f != nil && (f.kind == 'a' || f.kind == 'o') {
		f.expectName = true
	}
}

// step takes in one token.
func (w *walker) step(t token) {
	if w.skipParens > 0 {
		switch t.text {
		case "(":
			w.skipParens++
		case ")":
			w.skipParens--
		}
		return
	}
	f := w.top()
	if f == nil {
		w.topLevel(t)
		return
	}
	switch f.kind {
	case 's':
		w.selection(f, t)
	case 'a', 'o':
		w.arguments(f, t)
	case 'l':
		w.list(f, t)
	case 'x':
		switch t.text {
		case "{", "(", "[":
			w.stack = append(w.stack, frame{kind: 'x'})
		case "}", ")", "]":
			w.stack = w.stack[:len(w.stack)-1]
		}
	}
}

func (w *walker) topLevel(t token) {
	switch {
	case t.kind == 'n' && w.pendingOn:
		w.cond, w.pendingOn = t.text, false
	case t.kind == 'n' && (t.text == "query" || t.text == "mutation" || t.text == "subscription"):
		w.op, w.cond = t.text, ""
	case t.kind == 'n' && t.text == "fragment":
		w.op, w.cond = "fragment", ""
	case t.kind == 'n' && t.text == "on" && w.op == "fragment":
		w.pendingOn = true
	case t.text == "(":
		w.skipParens = 1 // variable definitions
	case t.text == "{":
		typ := w.s.root(w.op)
		if w.op == "fragment" {
			typ = w.cond
		}
		w.stack = append(w.stack, frame{kind: 's', typ: typ})
		w.op, w.cond = "", ""
	}
}

func (w *walker) selection(f *frame, t token) {
	switch {
	case w.pendingOn && t.kind == 'n':
		w.cond, w.pendingOn = t.text, false
	case t.text == "...":
		w.cond = f.typ // without a type condition, the type it is in
		switch {
		case w.i+1 < len(w.toks) && w.toks[w.i+1].text == "on":
			w.i++
			w.pendingOn = true
		case w.i+1 < len(w.toks) && w.toks[w.i+1].kind == 'n':
			w.i++ // a fragment spread: its name
			w.cond = ""
		}
	case t.text == "@":
		// A directive: its name, and arguments when there are any.
		if w.i+1 < len(w.toks) && w.toks[w.i+1].kind == 'n' {
			w.i++
			if w.i+1 < len(w.toks) && w.toks[w.i+1].text == "(" {
				w.i++
				w.skipParens = 1
			}
		}
	case t.kind == 'n':
		name := t.text
		if w.i+1 < len(w.toks) && w.toks[w.i+1].text == ":" && w.i+2 < len(w.toks) && w.toks[w.i+2].kind == 'n' {
			w.i += 2 // an alias
			name = w.toks[w.i].text
		}
		f.field, f.fieldType, w.cond = name, "", ""
		if fd, ok := w.s.Field(f.typ, name); ok {
			f.fieldType = fd.Type.Named()
		}
	case t.text == "(":
		w.stack = append(w.stack, frame{kind: 'a', typ: f.typ, field: f.field, expectName: true})
	case t.text == "{":
		typ := f.fieldType
		if w.cond != "" {
			typ = w.cond // an inline fragment
		}
		w.cond = ""
		w.stack = append(w.stack, frame{kind: 's', typ: typ})
	case t.text == "}":
		w.stack = w.stack[:len(w.stack)-1]
	}
}

// argType is the type of an argument or input field named name in f.
func (w *walker) argType(f *frame, name string) TypeRef {
	var list []InputValue
	if f.kind == 'a' {
		if fd, ok := w.s.Field(f.typ, f.field); ok {
			list = fd.Args
		}
	} else if t := w.s.Types[f.typ]; t != nil {
		list = t.InputFields
	}
	for _, a := range list {
		if a.Name == name {
			return a.Type
		}
	}
	return TypeRef{}
}

func (w *walker) arguments(f *frame, t token) {
	switch {
	case f.expectName && t.kind == 'n':
		f.valueType = w.argType(f, t.text)
	case f.expectName && t.text == ":":
		f.expectName = false
	case f.kind == 'a' && t.text == ")" || f.kind == 'o' && t.text == "}":
		w.stack = w.stack[:len(w.stack)-1]
		if f.kind == 'o' {
			w.valueDone()
		}
	case !f.expectName:
		w.value(f.valueType, t)
	}
}

func (w *walker) list(f *frame, t token) {
	if t.text == "]" {
		w.stack = w.stack[:len(w.stack)-1]
		w.valueDone()
		return
	}
	w.value(f.elem, t)
}

// value takes in a token where a value of type typ goes.
func (w *walker) value(typ TypeRef, t token) {
	switch t.text {
	case "{":
		w.stack = append(w.stack, frame{kind: 'o', typ: typ.Named(), expectName: true})
	case "[":
		elem := typ
		for elem.Kind == "NON_NULL" && elem.OfType != nil {
			elem = *elem.OfType
		}
		if elem.Kind == "LIST" && elem.OfType != nil {
			elem = *elem.OfType
		}
		w.stack = append(w.stack, frame{kind: 'l', elem: elem})
	case "$":
		// A variable: its name ends the value.
		if w.i+1 < len(w.toks) && w.toks[w.i+1].kind == 'n' && w.toks[w.i+1].start == t.end {
			w.i++
		}
		w.valueDone()
	default:
		w.valueDone()
	}
}

// At says what can be typed at offset, a byte offset in doc. Only what
// comes before offset counts.
func (s *Schema) At(doc string, offset int) Spot {
	offset = max(0, min(offset, len(doc)))
	toks := lex(doc[:offset])
	spot := Spot{Start: offset}
	if n := len(toks); n > 0 {
		last := toks[n-1]
		switch {
		case last.kind == 's' && last.open, last.kind == '0' && last.end == offset:
			// In a string, or typing a number: nothing to offer.
			return Spot{Kind: Nothing, Start: offset}
		case last.kind == 'n' && last.end == offset:
			// The name being typed.
			if n > 1 && toks[n-2].end == last.start && (toks[n-2].text == "$" || toks[n-2].text == "@") {
				return Spot{Kind: Nothing, Start: offset} // a variable or directive name
			}
			spot.Word, spot.Start = last.text, last.start
			toks = toks[:n-1]
		}
	}
	w := &walker{s: s, toks: toks}
	for w.i = 0; w.i < len(toks); w.i++ {
		w.step(toks[w.i])
	}
	if w.skipParens > 0 {
		spot.Kind = Nothing
		return spot
	}
	f := w.top()
	switch {
	case f == nil:
		spot.Kind = Top
	case w.pendingOn:
		spot.Kind = Nothing
	case f.kind == 's':
		spot.Kind, spot.Type = Fields, f.typ
	case f.kind == 'a' && f.expectName:
		spot.Kind, spot.Type, spot.Field = Arguments, f.typ, f.field
	case f.kind == 'o' && f.expectName:
		spot.Kind, spot.Type = InputFields, f.typ
	case f.kind == 'a' || f.kind == 'o':
		spot.Kind, spot.ValueType = Value, f.valueType
	case f.kind == 'l':
		spot.Kind, spot.ValueType = Value, f.elem
	}
	return spot
}

// Suggestion is something that can be typed.
type Suggestion struct {
	// Text is what is typed; Label what the menu shows when it differs.
	Text, Label string
	// Type is the field's or argument's type, or what the suggestion is.
	Type        string
	Description string
	Deprecated  bool
}

// Complete suggests what can be typed at offset: the start of the word
// being typed, and the suggestions, best first.
func (s *Schema) Complete(doc string, offset int) (int, []Suggestion) {
	spot := s.At(doc, offset)
	var out []Suggestion
	add := func(sg Suggestion) {
		if strings.HasPrefix(strings.ToLower(sg.Text), strings.ToLower(spot.Word)) {
			out = append(out, sg)
		}
	}
	switch spot.Kind {
	case Top:
		for _, k := range []string{"query", "mutation", "subscription", "fragment"} {
			add(Suggestion{Text: k, Type: "keyword"})
		}
	case Fields:
		t := s.Types[spot.Type]
		if t == nil {
			return spot.Start, nil
		}
		for _, f := range t.Fields {
			add(Suggestion{Text: f.Name, Type: f.Type.String(), Description: oneLine(f.Description), Deprecated: f.IsDeprecated})
		}
		for _, p := range t.PossibleTypes {
			add(Suggestion{Text: "... on " + p.Name, Label: "... on " + p.Name, Type: "fragment"})
		}
		add(Suggestion{Text: "__typename", Type: "String!"})
	case Arguments:
		if f, ok := s.Field(spot.Type, spot.Field); ok {
			for _, a := range f.Args {
				add(Suggestion{Text: a.Name + ": ", Label: a.Name, Type: a.Type.String(), Description: oneLine(a.Description)})
			}
		}
	case InputFields:
		if t := s.Types[spot.Type]; t != nil {
			for _, a := range t.InputFields {
				add(Suggestion{Text: a.Name + ": ", Label: a.Name, Type: a.Type.String(), Description: oneLine(a.Description)})
			}
		}
	case Value:
		name := spot.ValueType.Named()
		if t := s.Types[name]; t != nil && t.Kind == "ENUM" {
			for _, v := range t.EnumValues {
				add(Suggestion{Text: v.Name, Type: name, Description: oneLine(v.Description), Deprecated: v.IsDeprecated})
			}
		}
		if name == "Boolean" {
			add(Suggestion{Text: "true", Type: "Boolean"})
			add(Suggestion{Text: "false", Type: "Boolean"})
		}
	}
	// Deprecated ones last; otherwise as the schema has them.
	sort.SliceStable(out, func(a, b int) bool { return !out[a].Deprecated && out[b].Deprecated })
	return spot.Start, out
}

// Info describes the name at offset: a field, an argument, an input field
// or a type. ok is false when the schema doesn't know it.
func (s *Schema) Info(doc string, offset int) (string, bool) {
	toks := lex(doc)
	at := -1
	for i, t := range toks {
		if t.kind == 'n' && t.start <= offset && offset < t.end {
			at = i
			break
		}
	}
	if at < 0 {
		return "", false
	}
	name := toks[at].text
	// A type condition: "on Type".
	if at > 0 && toks[at-1].text == "on" {
		return s.typeInfo(name)
	}
	// What the name is: the spot just before it, and an alias's field.
	spot := s.At(doc, toks[at].start)
	if at+2 < len(toks) && toks[at+1].text == ":" && toks[at+2].kind == 'n' && spot.Kind == Fields {
		name = toks[at+2].text
	}
	switch spot.Kind {
	case Fields:
		if f, ok := s.Field(spot.Type, name); ok {
			return spot.Type + "." + Describe(f), true
		}
	case Arguments:
		if f, ok := s.Field(spot.Type, spot.Field); ok {
			for _, a := range f.Args {
				if a.Name == name {
					return describeInput(spot.Field+"("+a.Name, a) + ")", true
				}
			}
		}
	case InputFields:
		if t := s.Types[spot.Type]; t != nil {
			for _, a := range t.InputFields {
				if a.Name == name {
					return describeInput(spot.Type+"."+a.Name, a), true
				}
			}
		}
	}
	return s.typeInfo(name)
}

func describeInput(label string, a InputValue) string {
	out := label + ": " + a.Type.String()
	if a.DefaultValue != nil {
		out += " = " + *a.DefaultValue
	}
	if d := strings.TrimSpace(a.Description); d != "" {
		out += "\n\n" + d
	}
	return out
}

func (s *Schema) typeInfo(name string) (string, bool) {
	t := s.Types[name]
	if t == nil {
		return "", false
	}
	out := strings.ToLower(t.Kind) + " " + t.Name
	if d := strings.TrimSpace(t.Description); d != "" {
		out += "\n\n" + d
	}
	if t.Kind == "ENUM" {
		var names []string
		for _, v := range t.EnumValues {
			names = append(names, v.Name)
		}
		out += "\n\n" + strings.Join(names, " · ")
	}
	return out, true
}

func oneLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = strings.TrimSpace(s[:i]) + " …"
	}
	return s
}

// Operations lists the kinds of a document's operations, in order:
// "query", "mutation" or "subscription" (a bare selection set is a query).
// Fragments aren't operations.
func Operations(doc string) []string {
	var out []string
	depth := 0
	parens := 0
	for _, t := range lex(doc) {
		switch t.text {
		case "{":
			if depth == 0 && parens == 0 {
				if len(out) > 0 && out[len(out)-1] == "pending" {
					// The selection set of the operation or fragment just named.
					out = out[:len(out)-1]
				} else {
					// A selection set at the top that no keyword opened.
					out = append(out, "query")
				}
			}
			depth++
			continue
		case "}":
			depth--
			continue
		case "(":
			parens++
			continue
		case ")":
			parens--
			continue
		}
		if depth != 0 || parens != 0 || t.kind != 'n' {
			continue
		}
		switch t.text {
		case "query", "mutation", "subscription":
			out = append(out, t.text, "pending")
		case "fragment":
			out = append(out, "pending")
		}
	}
	var kinds []string
	for _, k := range out {
		if k != "pending" {
			kinds = append(kinds, k)
		}
	}
	return kinds
}
