package graphql

import (
	"strings"
	"testing"
)

// testSchema is a small introspection answer: locations with an address,
// a paginated list with filters, a union and an enum.
const testSchema = `{"data":{"__schema":{
 "queryType":{"name":"Query"},"mutationType":{"name":"Mutation"},"subscriptionType":null,
 "types":[
  {"kind":"OBJECT","name":"Query","fields":[
    {"name":"getLocations","description":"Locations, a page at a time.","args":[
      {"name":"pagination","type":{"kind":"NON_NULL","ofType":{"kind":"INPUT_OBJECT","name":"PaginationInput"}}},
      {"name":"filters","type":{"kind":"LIST","ofType":{"kind":"NON_NULL","ofType":{"kind":"INPUT_OBJECT","name":"LocationsFilterInput"}}}}],
     "type":{"kind":"NON_NULL","ofType":{"kind":"OBJECT","name":"LocationPage"}}},
    {"name":"location","args":[{"name":"id","type":{"kind":"NON_NULL","ofType":{"kind":"SCALAR","name":"ID"}}}],"type":{"kind":"OBJECT","name":"Location"}},
    {"name":"search","args":[{"name":"text","type":{"kind":"SCALAR","name":"String"}}],"type":{"kind":"LIST","ofType":{"kind":"UNION","name":"SearchResult"}}},
    {"name":"oldLocations","isDeprecated":true,"deprecationReason":"use getLocations","args":[],"type":{"kind":"LIST","ofType":{"kind":"OBJECT","name":"Location"}}}]},
  {"kind":"OBJECT","name":"Mutation","fields":[
    {"name":"createLocation","args":[{"name":"input","type":{"kind":"NON_NULL","ofType":{"kind":"INPUT_OBJECT","name":"LocationInput"}}}],"type":{"kind":"OBJECT","name":"Location"}}]},
  {"kind":"OBJECT","name":"LocationPage","fields":[
    {"name":"rows","args":[],"type":{"kind":"LIST","ofType":{"kind":"NON_NULL","ofType":{"kind":"OBJECT","name":"Location"}}}},
    {"name":"totalCount","args":[],"type":{"kind":"SCALAR","name":"Int"}}]},
  {"kind":"OBJECT","name":"Location","description":"A place.","fields":[
    {"name":"id","args":[],"type":{"kind":"NON_NULL","ofType":{"kind":"SCALAR","name":"ID"}}},
    {"name":"name","description":"What people call it.\nSecond line.","args":[],"type":{"kind":"SCALAR","name":"String"}},
    {"name":"kind","args":[],"type":{"kind":"ENUM","name":"LocationKind"}},
    {"name":"address","args":[],"type":{"kind":"OBJECT","name":"Address"}}]},
  {"kind":"OBJECT","name":"Address","fields":[
    {"name":"street","args":[],"type":{"kind":"SCALAR","name":"String"}},
    {"name":"city","args":[],"type":{"kind":"SCALAR","name":"String"}}]},
  {"kind":"OBJECT","name":"Person","fields":[{"name":"email","args":[],"type":{"kind":"SCALAR","name":"String"}}]},
  {"kind":"UNION","name":"SearchResult","possibleTypes":[{"kind":"OBJECT","name":"Location"},{"kind":"OBJECT","name":"Person"}]},
  {"kind":"ENUM","name":"LocationKind","enumValues":[{"name":"BUILDING"},{"name":"ROOM"},{"name":"OFFICE","isDeprecated":true}]},
  {"kind":"INPUT_OBJECT","name":"PaginationInput","inputFields":[
    {"name":"page","type":{"kind":"SCALAR","name":"Int"},"defaultValue":"1"},
    {"name":"pageSize","type":{"kind":"SCALAR","name":"Int"}}]},
  {"kind":"INPUT_OBJECT","name":"LocationsFilterInput","inputFields":[
    {"name":"kind","type":{"kind":"ENUM","name":"LocationKind"}},
    {"name":"onlyRoots","type":{"kind":"SCALAR","name":"Boolean"}}]},
  {"kind":"INPUT_OBJECT","name":"LocationInput","inputFields":[
    {"name":"name","type":{"kind":"NON_NULL","ofType":{"kind":"SCALAR","name":"String"}}},
    {"name":"kind","type":{"kind":"ENUM","name":"LocationKind"}}]},
  {"kind":"SCALAR","name":"ID"},{"kind":"SCALAR","name":"String"},{"kind":"SCALAR","name":"Int"},{"kind":"SCALAR","name":"Boolean"}
 ]}}}`

func schema(t *testing.T) *Schema {
	t.Helper()
	s, err := Parse([]byte(testSchema))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// complete completes at the | in doc.
func complete(t *testing.T, s *Schema, doc string) (string, []string) {
	t.Helper()
	at := strings.Index(doc, "|")
	doc = doc[:at] + doc[at+1:]
	start, sg := s.Complete(doc, at)
	var out []string
	for _, x := range sg {
		out = append(out, x.Text)
	}
	return doc[start:at], out
}

func TestComplete(t *testing.T) {
	s := schema(t)
	cases := []struct {
		doc, word string
		want      []string
	}{
		{"|", "", []string{"query", "mutation", "subscription", "fragment"}},
		{"query Q {\n  |\n}", "", []string{"getLocations", "location", "search", "__typename", "oldLocations"}},
		{"{ loc| }", "loc", []string{"location"}},
		{"query Q($p: PaginationInput!) {\n  getLocations(pagination: $p) {\n    rows {\n      add|", "add", []string{"address"}},
		{"query { getLocations(pagination: $p) { rows { address { | } } } }", "", []string{"street", "city", "__typename"}},
		// An alias is followed to its field.
		{"{ first: location(id: 1) { na| } }", "na", []string{"name"}},
		{"mutation { createLocation(| ) { id } }", "", []string{"input: "}},
		{"mutation { createLocation(input: { name: \"x\", | }) { id } }", "", []string{"name: ", "kind: "}},
		{"mutation { createLocation(input: { kind: | }) { id } }", "", []string{"BUILDING", "ROOM", "OFFICE"}},
		{"{ getLocations(pagination: {page: 1}, filters: [{onlyRoots: |}]) { totalCount } }", "", []string{"true", "false"}},
		{"{ getLocations(pagination: {page: 1} fil|", "fil", []string{"filters: "}},
		{"{ search(text: \"x\") { ... on Person { e| } } }", "e", []string{"email"}},
		{"{ search { | } }", "", []string{"... on Location", "... on Person", "__typename"}},
		{"fragment F on Location { address { c| } }", "c", []string{"city"}},
		// Directives and variable definitions are stepped over.
		{"query Q($a: Int = 1, $b: [String!]) { location(id: $a) @include(if: true) { | } }", "", []string{"id", "name", "kind", "address", "__typename"}},
		// Inside a string there is nothing to offer.
		{`{ search(text: "lo|c") { __typename } }`, "", nil},
		// Comments are no fields.
		{"{\n  # location\n  getLo|", "getLo", []string{"getLocations"}},
		// What comes after the cursor doesn't count: an alias being typed,
		// a variable, a directive.
		{"{ loc: locat| }", "locat", []string{"location"}},
		{"{ location(id: $i|) { id } }", "", nil},
		{"{ location(id: 1) @inc| { id } }", "", nil},
		// In a string that isn't closed yet.
		{"{ search(text: \"ab|", "", nil},
		// An inline fragment without a type condition is on the type it's in.
		{"{ location(id: 1) { id ... @include(if: true) { na| } } }", "na", []string{"name"}},
		{"{ location(id: 1) { id ... { na| } } }", "na", []string{"name"}},
		// A block string with an escaped \"\"\" in it ends where it ends.
		{"{ search(text: \"\"\"a \\\"\"\" b\"\"\") { ... on Person { em| } } }", "em", []string{"email"}},
	}
	for _, c := range cases {
		word, got := complete(t, s, c.doc)
		if word != c.word || strings.Join(got, ",") != strings.Join(c.want, ",") {
			t.Errorf("%q: word %q, got %q, want %q", c.doc, word, got, c.want)
		}
	}
}

func TestInfo(t *testing.T) {
	s := schema(t)
	doc := "query { getLocations(pagination: {page: 1}) { rows { name kind } } search { ... on Person { email } } }"
	info := func(word string) string {
		i := strings.Index(doc, word)
		got, _ := s.Info(doc, i+1)
		return got
	}
	if got := info("getLocations"); !strings.HasPrefix(got, "Query.getLocations(pagination: PaginationInput!, filters: [LocationsFilterInput!]): LocationPage!") || !strings.Contains(got, "a page at a time") {
		t.Errorf("field: %q", got)
	}
	if got := info("pagination"); !strings.HasPrefix(got, "getLocations(pagination: PaginationInput!)") {
		t.Errorf("argument: %q", got)
	}
	if got := info("page:"); !strings.HasPrefix(got, "PaginationInput.page: Int = 1") {
		t.Errorf("input field: %q", got)
	}
	if got := info("kind"); !strings.HasPrefix(got, "Location.kind: LocationKind") {
		t.Errorf("enum field: %q", got)
	}
	if got := info("Person"); !strings.HasPrefix(got, "object Person") {
		t.Errorf("type condition: %q", got)
	}
	if _, ok := s.Info(doc, strings.Index(doc, "{")); ok {
		t.Error("a brace isn't a name")
	}
}

func TestParseErrors(t *testing.T) {
	if _, err := Parse([]byte(`{"errors":[{"message":"introspection is disabled"}]}`)); err == nil || !strings.Contains(err.Error(), "introspection is disabled") {
		t.Errorf("error answer: %v", err)
	}
	if _, err := Parse([]byte(`<html>`)); err == nil {
		t.Error("not JSON")
	}
	r := TypeRef{Kind: "NON_NULL", OfType: &TypeRef{Kind: "LIST", OfType: &TypeRef{Kind: "NON_NULL", OfType: &TypeRef{Kind: "OBJECT", Name: "Location"}}}}
	if r.String() != "[Location!]!" || r.Named() != "Location" {
		t.Errorf("type ref: %s %s", r, r.Named())
	}
}

func TestOperations(t *testing.T) {
	cases := map[string]string{
		"{ me { id } }": "query",
		"query Q { a }": "query",
		"mutation M($x: Int) { a(x: $x) { id } }":                        "mutation",
		"fragment F on Order { id }\nmutation PlaceOrder { b { ...F } }": "mutation",
		"# mutation in a comment\nquery Q { a(note: \"mutation\") }":     "query",
		"query A { a }\nmutation B { b }":                                "query mutation",
		"subscription S { s }":                                           "subscription",
		"query Q($p: In = {a: 1}) @d(x: {b: 2}) { a }\nmutation { b }":   "query mutation",
		"": "",
	}
	for doc, want := range cases {
		if got := strings.Join(Operations(doc), " "); got != want {
			t.Errorf("%q: %q, want %q", doc, got, want)
		}
	}
}
