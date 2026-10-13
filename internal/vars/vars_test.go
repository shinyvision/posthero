package vars

import (
	"regexp"
	"strings"
	"testing"
)

func TestResolve(t *testing.T) {
	env := &Scope{Kind: Environment, Name: "Local", Vars: []Var{
		{Key: "base", Value: "http://localhost:{{port}}"},
		{Key: "port", Value: "8080"},
		{Key: "Keycloak-URL", Value: "https://auth.example.com"},
		{Key: "Messaging bearer token", Value: "tok"},
		{Key: "who", Value: "env"},
	}}
	col := &Scope{Kind: Collection, Name: "Shop", Vars: []Var{{Key: "who", Value: "collection"}, {Key: "id", Value: "42"}}}
	local := &Scope{Kind: Local, Vars: []Var{{Key: "id", Value: "7"}}}
	s := New(local, nil, env, col)
	s.Vault = func(name string) (string, bool) { return "v-" + name, name == "api-key" }

	cases := map[string]string{
		"{{base}}/users/{{id}}":              "http://localhost:8080/users/7",
		"{{who}}":                            "env",
		"{{Keycloak-URL}}/realms":            "https://auth.example.com/realms",
		"Bearer {{Messaging bearer token}}":  "Bearer tok",
		"{{missing}} stays":                  "{{missing}} stays",
		"{{vault:api-key}} {{vault:nope}}":   "v-api-key {{vault:nope}}",
		"no variables":                       "no variables",
		"{{ {{port}} }}":                     "{{ 8080 }}",
		"{base} and {{port}}{{port}}":        "{base} and 80808080",
		"{{$nonsense}}":                      "{{$nonsense}}",
		"before {{base}} after {{missing2}}": "before http://localhost:8080 after {{missing2}}",
	}
	for in, want := range cases {
		if got := s.Resolve(in); got != want {
			t.Errorf("Resolve(%q) = %q, want %q", in, got, want)
		}
	}
	_, missing := s.ResolveReport("{{a}} {{base}} {{b}} {{a}}")
	if strings.Join(missing, ",") != "a,b" {
		t.Errorf("missing: %v", missing)
	}

	// A variable that refers to itself stops instead of looping forever.
	loop := New(&Scope{Kind: Environment, Vars: []Var{{Key: "a", Value: "x{{a}}"}}})
	if got := loop.Resolve("{{a}}"); !strings.HasPrefix(got, "xxxx") {
		t.Errorf("loop: %q", got)
	}
}

func TestDynamic(t *testing.T) {
	s := New()
	guid := regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	a, b := s.Resolve("{{$guid}}"), s.Resolve("{{$guid}}")
	if !guid.MatchString(a) || a == b {
		t.Errorf("guids: %q %q", a, b)
	}
	if got := s.Resolve("{{$timestamp}}"); !regexp.MustCompile(`^\d{10}$`).MatchString(got) {
		t.Errorf("timestamp: %q", got)
	}
	if got := s.Resolve("{{$randomEmail}}"); !strings.Contains(got, "@") || strings.Contains(got, " ") {
		t.Errorf("email: %q", got)
	}
	for _, name := range DynamicNames() {
		v, ok := Dynamic(name)
		if !ok || v == "" {
			t.Errorf("%s gave %q", name, v)
		}
	}
	if len(DynamicNames()) < 100 {
		t.Errorf("only %d dynamic variables", len(DynamicNames()))
	}
}

func TestAt(t *testing.T) {
	line := `Authorization: Bearer {{token}} and {{other var}}`
	if name, s, e, ok := At(line, 25); !ok || name != "token" || line[s:e] != "{{token}}" {
		t.Errorf("At: %q %d %d %v", name, s, e, ok)
	}
	if name, _, _, ok := At(line, 40); !ok || name != "other var" {
		t.Errorf("At: %q %v", name, ok)
	}
	if _, _, _, ok := At(line, 3); ok {
		t.Error("At outside a variable")
	}
}

// A name may be built from other variables, as Postman rescans the whole
// text after each pass.
func TestNestedNames(t *testing.T) {
	s := New(&Scope{Kind: Environment, Vars: []Var{
		{Key: "env", Value: "dev"},
		{Key: "host_dev", Value: "dev.example.com"},
		{Key: "which", Value: "{{env}}"},
		{Key: "url", Value: "https://{{host_{{which}}}}/"},
	}})
	cases := map[string]string{
		"{{host_{{env}}}}/x":      "dev.example.com/x",
		"{{url}}":                 "https://dev.example.com/",
		"{{host_{{missing}}}}":    "{{host_{{missing}}}}",
		"{{host_{{which}}}}{{a}}": "dev.example.com{{a}}",
	}
	for in, want := range cases {
		if got := s.Resolve(in); got != want {
			t.Errorf("Resolve(%q) = %q, want %q", in, got, want)
		}
	}
	if _, missing := s.ResolveReport("{{host_{{env}}}} {{host_{{nope}}}}"); strings.Join(missing, ",") != "nope" {
		t.Errorf("missing: %v", missing)
	}
}

// $randomPassword is 15 letters, digits and underscores, like Postman's
// (faker's internet.password), and never panics.
func TestRandomPassword(t *testing.T) {
	want := regexp.MustCompile(`^[A-Za-z0-9_]{15}$`)
	for range 50000 {
		if p, _ := Dynamic("$randomPassword"); !want.MatchString(p) {
			t.Fatalf("password %q", p)
		}
	}
}
