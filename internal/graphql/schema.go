// Package graphql knows a GraphQL server's schema, from an introspection
// query, and what can be typed where in a query: the fields of the type a
// selection set is on, the arguments of a field. posthero uses it to
// complete GraphQL requests and to say what a field is.
package graphql

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// IntrospectionQuery asks a server for its schema: the types with their
// fields, arguments, input fields and enum values, with descriptions.
const IntrospectionQuery = `query IntrospectionQuery {
  __schema {
    queryType { name }
    mutationType { name }
    subscriptionType { name }
    types { ...FullType }
  }
}

fragment FullType on __Type {
  kind
  name
  description
  fields(includeDeprecated: true) {
    name
    description
    args { ...InputValue }
    type { ...TypeRef }
    isDeprecated
    deprecationReason
  }
  inputFields { ...InputValue }
  interfaces { ...TypeRef }
  enumValues(includeDeprecated: true) { name description isDeprecated }
  possibleTypes { ...TypeRef }
}

fragment InputValue on __InputValue {
  name
  description
  type { ...TypeRef }
  defaultValue
}

fragment TypeRef on __Type {
  kind
  name
  ofType { kind name ofType { kind name ofType { kind name ofType { kind name ofType { kind name ofType { kind name } } } } } }
}`

// Schema is what a server says it has.
type Schema struct {
	Query, Mutation, Subscription string
	Types                         map[string]*Type
}

// Type is an object, interface, union, enum, input object or scalar.
type Type struct {
	Kind          string // OBJECT, INTERFACE, UNION, ENUM, INPUT_OBJECT, SCALAR
	Name          string
	Description   string
	Fields        []Field
	InputFields   []InputValue
	EnumValues    []EnumValue
	PossibleTypes []TypeRef
}

// Field is a field of an object or interface.
type Field struct {
	Name, Description string
	Args              []InputValue
	Type              TypeRef
	IsDeprecated      bool
	DeprecationReason string
}

// InputValue is an argument or an input object's field.
type InputValue struct {
	Name, Description string
	Type              TypeRef
	DefaultValue      *string
}

// EnumValue is a value of an enum.
type EnumValue struct {
	Name, Description string
	IsDeprecated      bool
}

// TypeRef refers to a type, wrapped in lists and non-nulls.
type TypeRef struct {
	Kind   string
	Name   string
	OfType *TypeRef
}

// Named is the type a reference ends at, without lists and non-nulls.
func (r TypeRef) Named() string {
	for t := &r; t != nil; t = t.OfType {
		if t.Name != "" {
			return t.Name
		}
	}
	return ""
}

// String writes the reference as GraphQL does: [Location!]!.
func (r TypeRef) String() string {
	switch r.Kind {
	case "NON_NULL":
		if r.OfType != nil {
			return r.OfType.String() + "!"
		}
	case "LIST":
		if r.OfType != nil {
			return "[" + r.OfType.String() + "]"
		}
	}
	return r.Name
}

// Parse reads an introspection result: {"data": {"__schema": …}}, or the
// __schema object alone.
func Parse(data []byte) (*Schema, error) {
	var answer struct {
		Data struct {
			Schema *rawSchema `json:"__schema"`
		} `json:"data"`
		Schema *rawSchema `json:"__schema"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(data, &answer); err != nil {
		return nil, fmt.Errorf("the answer isn't JSON: %w", err)
	}
	raw := answer.Data.Schema
	if raw == nil {
		raw = answer.Schema
	}
	if raw == nil {
		if len(answer.Errors) > 0 {
			return nil, errors.New("the server said: " + answer.Errors[0].Message)
		}
		return nil, errors.New("the answer has no schema (is introspection switched off?)")
	}
	s := &Schema{Types: map[string]*Type{}}
	if raw.QueryType != nil {
		s.Query = raw.QueryType.Name
	}
	if raw.MutationType != nil {
		s.Mutation = raw.MutationType.Name
	}
	if raw.SubscriptionType != nil {
		s.Subscription = raw.SubscriptionType.Name
	}
	for _, t := range raw.Types {
		t := t
		s.Types[t.Name] = &t
	}
	return s, nil
}

type rawSchema struct {
	QueryType        *struct{ Name string } `json:"queryType"`
	MutationType     *struct{ Name string } `json:"mutationType"`
	SubscriptionType *struct{ Name string } `json:"subscriptionType"`
	Types            []Type                 `json:"types"`
}

// Field finds a field of a type; __typename is on every composite type.
func (s *Schema) Field(typeName, field string) (Field, bool) {
	t := s.Types[typeName]
	if t == nil {
		return Field{}, false
	}
	for _, f := range t.Fields {
		if f.Name == field {
			return f, true
		}
	}
	if field == "__typename" {
		return Field{Name: "__typename", Description: "the name of the object's type", Type: TypeRef{Kind: "NON_NULL", OfType: &TypeRef{Kind: "SCALAR", Name: "String"}}}, true
	}
	return Field{}, false
}

// root is the type an operation starts at.
func (s *Schema) root(op string) string {
	switch op {
	case "mutation":
		return s.Mutation
	case "subscription":
		return s.Subscription
	}
	return s.Query
}

// Describe writes a field for people: its type, arguments and description.
func Describe(f Field) string {
	var b strings.Builder
	b.WriteString(f.Name)
	if len(f.Args) > 0 {
		b.WriteString("(")
		for i, a := range f.Args {
			if i > 0 {
				b.WriteString(", ")
			}
			b.WriteString(a.Name + ": " + a.Type.String())
			if a.DefaultValue != nil {
				b.WriteString(" = " + *a.DefaultValue)
			}
		}
		b.WriteString(")")
	}
	b.WriteString(": " + f.Type.String())
	if f.IsDeprecated {
		b.WriteString("\ndeprecated")
		if f.DeprecationReason != "" {
			b.WriteString(": " + f.DeprecationReason)
		}
	}
	if d := strings.TrimSpace(f.Description); d != "" {
		b.WriteString("\n\n" + d)
	}
	return b.String()
}
