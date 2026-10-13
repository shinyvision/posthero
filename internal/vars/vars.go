// Package vars resolves {{variables}} the way Postman does: through the
// scopes in order (local, iteration data, environment, collection,
// globals), recursively, with Postman's dynamic variables ({{$guid}},
// {{$randomEmail}}, …) and vault secrets ({{vault:name}}).
package vars

import (
	"regexp"
	"sort"
	"strings"
)

// Scope kinds, from the one that wins to the one that loses.
const (
	Local       = "local"
	Data        = "data"
	Environment = "environment"
	Collection  = "collection"
	Global      = "global"
)

// Var is one variable as it applies now: its current value.
type Var struct {
	Key    string
	Value  string
	Secret bool
	// Typed is what a script set when it wasn't text (a number, a list):
	// scripts get it back as it was, while {{key}} gives Value.
	Typed any
}

// Scope is the variables of one kind, in order.
type Scope struct {
	Kind string
	// Name is what the scope is called: the environment's or collection's
	// name.
	Name string
	Vars []Var
}

// Get returns a variable of the scope.
func (s *Scope) Get(key string) (Var, bool) {
	if s == nil {
		return Var{}, false
	}
	for _, v := range s.Vars {
		if v.Key == key {
			return v, true
		}
	}
	return Var{}, false
}

// Clone returns a copy of the scope that changes on its own.
func (s *Scope) Clone() *Scope {
	if s == nil {
		return nil
	}
	c := *s
	c.Vars = append([]Var(nil), s.Vars...)
	return &c
}

// Set changes or adds a variable.
func (s *Scope) Set(key, value string) { s.SetTyped(key, value, nil) }

// SetTyped changes or adds a variable with the value a script gave it.
func (s *Scope) SetTyped(key, value string, typed any) {
	for i := range s.Vars {
		if s.Vars[i].Key == key {
			s.Vars[i].Value, s.Vars[i].Typed = value, typed
			return
		}
	}
	s.Vars = append(s.Vars, Var{Key: key, Value: value, Typed: typed})
}

// Unset removes a variable.
func (s *Scope) Unset(key string) {
	for i := range s.Vars {
		if s.Vars[i].Key == key {
			s.Vars = append(s.Vars[:i], s.Vars[i+1:]...)
			return
		}
	}
}

// Set is every scope a request sees.
type Set struct {
	// Scopes in order of precedence: the first that has a variable wins.
	Scopes []*Scope
	// Vault looks up {{vault:name}} secrets; nil has none.
	Vault func(name string) (string, bool)
	// Dynamic makes {{$name}} values; nil uses Postman's.
	Dynamic func(name string) (string, bool)
}

// New makes a set from scopes, skipping nil ones.
func New(scopes ...*Scope) *Set {
	s := &Set{}
	for _, sc := range scopes {
		if sc != nil {
			s.Scopes = append(s.Scopes, sc)
		}
	}
	return s
}

// Clone returns a copy of the set whose scopes change on their own.
func (s *Set) Clone() *Set {
	c := *s
	c.Scopes = make([]*Scope, len(s.Scopes))
	for i, sc := range s.Scopes {
		c.Scopes[i] = sc.Clone()
	}
	return &c
}

// Scope returns the scope of a kind, nil when the set has none.
func (s *Set) Scope(kind string) *Scope {
	for _, sc := range s.Scopes {
		if sc.Kind == kind {
			return sc
		}
	}
	return nil
}

// Lookup finds a variable in the first scope that has it.
func (s *Set) Lookup(key string) (Var, *Scope, bool) {
	for _, sc := range s.Scopes {
		if v, ok := sc.Get(key); ok {
			return v, sc, true
		}
	}
	return Var{}, nil, false
}

// pattern matches {{name}}; names are anything but braces, spaces
// included, as in Postman.
var pattern = regexp.MustCompile(`\{\{([^{}]+?)\}\}`)

// maxPasses is how many times Postman scans text for variables. Each pass
// scans everything the one before made, so values may refer to variables
// and names may be built from them ({{host_{{env}}}}).
const maxPasses = 19

// Resolve replaces every {{variable}} it knows in text. Variables that
// aren't set stay as they are, as Postman leaves them.
func (s *Set) Resolve(text string) string {
	return s.resolve(text, nil)
}

// ResolveReport resolves text and returns the variables that weren't
// found.
func (s *Set) ResolveReport(text string) (string, []string) {
	missing := map[string]bool{}
	out := s.resolve(text, missing)
	return out, sortedKeys(missing)
}

// resolve replaces variables pass after pass, while a pass changes
// something.
func (s *Set) resolve(text string, missing map[string]bool) string {
	for range maxPasses {
		if !strings.Contains(text, "{{") {
			break
		}
		out, changed := s.pass(text, missing)
		text = out
		if !changed {
			break
		}
	}
	return text
}

// pass replaces the {{variables}} text has once.
func (s *Set) pass(text string, missing map[string]bool) (string, bool) {
	changed := false
	out := pattern.ReplaceAllStringFunc(text, func(m string) string {
		name := m[2 : len(m)-2]
		if v, ok := s.value(name); ok {
			changed = true
			return v
		}
		if missing != nil {
			missing[name] = true
		}
		return m
	})
	return out, changed
}

// value finds what one {{name}} stands for.
func (s *Set) value(name string) (string, bool) {
	switch {
	case strings.HasPrefix(name, "$"):
		if s.Dynamic != nil {
			return s.Dynamic(name)
		}
		return Dynamic(name)
	case strings.HasPrefix(name, "vault:"):
		if s.Vault == nil {
			return "", false
		}
		return s.Vault(strings.TrimPrefix(name, "vault:"))
	}
	if v, _, ok := s.Lookup(name); ok {
		return v.Value, true
	}
	return "", false
}

// Names returns the names of the {{variables}} in text, once each, in
// order.
func Names(text string) []string {
	var out []string
	seen := map[string]bool{}
	for _, m := range pattern.FindAllStringSubmatch(text, -1) {
		if !seen[m[1]] {
			seen[m[1]] = true
			out = append(out, m[1])
		}
	}
	return out
}

// At returns the {{variable}} the byte offset col of line is in, and where
// it starts and ends.
func At(line string, col int) (name string, start, end int, ok bool) {
	for _, loc := range pattern.FindAllStringSubmatchIndex(line, -1) {
		if col >= loc[0] && col < loc[1] {
			return line[loc[2]:loc[3]], loc[0], loc[1], true
		}
	}
	return "", 0, 0, false
}

// Keys returns every variable name the set knows, sorted.
func (s *Set) Keys() []string {
	seen := map[string]bool{}
	for _, sc := range s.Scopes {
		for _, v := range sc.Vars {
			seen[v.Key] = true
		}
	}
	return sortedKeys(seen)
}

// Secrets returns the values of secret variables, for masking them where
// they would show.
func (s *Set) Secrets() []string {
	var out []string
	for _, sc := range s.Scopes {
		for _, v := range sc.Vars {
			if v.Secret && len(v.Value) >= 4 {
				out = append(out, v.Value)
			}
		}
	}
	return out
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
