package app

import (
	"fmt"
	"strings"
	"testing"

	"github.com/shinyvision/posthero/internal/postman"
	"github.com/shinyvision/posthero/internal/store"
)

func TestIsProd(t *testing.T) {
	cases := map[string]bool{
		"Shop - Prod": true, "Products - Prod": true, "Delivery Live": true, "PRODUCTION": true, "shop.prd": true,
		"Products - Local": false, "Liveblog - Dev": false, "Productivity": false, "Acceptance": false, "": false,
	}
	for name, want := range cases {
		if got := isProd(name); got != want {
			t.Errorf("isProd(%q) = %v", name, got)
		}
	}
}

func TestGuessEnv(t *testing.T) {
	env := func(name string) *store.Env { return &store.Env{Summary: postman.Summary{ID: name, Name: name}} }
	envs := []*store.Env{env("Shop · Production"), env("Shop · Local"), env("Facturen - Dev"), env("Other")}
	if e := guessEnv("Shop API", envs); e == nil || e.Name() != "Shop · Local" {
		t.Errorf("Shop API: %v", e)
	}
	if e := guessEnv("Unrelated", envs); e != nil {
		t.Errorf("Unrelated: %v", e.Name())
	}
}

// The environment editor's document reads back as exactly its variables:
// its help isn't one, a # switches one off.
func TestEnvDocument(t *testing.T) {
	off := false
	values := []postman.EnvValue{
		{Key: "baseUrl", Value: "http://localhost:8080", Type: "default"},
		{Key: "password", Value: "s3cret", Type: "secret"},
		{Key: "debug", Value: "1", Type: "default", Enabled: &off},
	}
	var b strings.Builder
	fmt.Fprintf(&b, envHeader, "Shop · Local")
	b.WriteByte('\n')
	writeEnvLines(&b, values)
	got, err := parseEnvLines(b.String(), values)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("%d variables from:\n%s\n%+v", len(got), b.String(), got)
	}
	for i, v := range got {
		if v.Key != values[i].Key || v.Value != values[i].Value || v.Type != values[i].Type || v.On() != values[i].On() {
			t.Errorf("variable %d: %+v, want %+v", i, v, values[i])
		}
	}
}

// Values with backslashes, line breaks and trailing spaces, and a switched
// off variable whose key has a space, come back as they were.
func TestEnvDocumentValues(t *testing.T) {
	off := false
	values := []postman.EnvValue{
		{Key: "path", Value: `C:\new\table`, Type: "default"},
		{Key: "pattern", Value: `^\d+\n$`, Type: "default"},
		{Key: "multi", Value: "line one\nline two", Type: "default"},
		{Key: "padded", Value: "x  ", Type: "default"},
		{Key: "api key", Value: "abc", Type: "default", Enabled: &off},
	}
	var b strings.Builder
	fmt.Fprintf(&b, envHeader, "Shop · Local")
	b.WriteByte('\n')
	writeEnvLines(&b, values)
	got, err := parseEnvLines(b.String(), values)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(values) {
		t.Fatalf("%d variables from:\n%s", len(got), b.String())
	}
	for i, v := range got {
		if v.Key != values[i].Key || v.Value != values[i].Value || v.On() != values[i].On() {
			t.Errorf("variable %d: %q=%q on=%v, want %q=%q on=%v", i, v.Key, v.Value, v.On(), values[i].Key, values[i].Value, values[i].On())
		}
	}
	// A value typed with single backslashes reads as typed.
	if got := fromLine(`^\d+$`); got != `^\d+$` {
		t.Errorf("typed: %q", got)
	}
}

// A save keeps what a teammate changed meanwhile: only this side's changes
// go on top of what Postman has now.
func TestMergeByKey(t *testing.T) {
	v := func(k, val string) postman.EnvValue { return postman.EnvValue{Key: k, Value: postman.Str(val)} }
	base := []postman.EnvValue{v("a", "1"), v("b", "2"), v("c", "3")}
	mine := []postman.EnvValue{v("a", "10"), v("c", "3"), v("mine", "new")}                // a changed, b removed
	theirs := []postman.EnvValue{v("a", "1"), v("b", "2"), v("c", "30"), v("theirs", "x")} // c changed, theirs added
	got := mergeByKey(base, mine, theirs, envKey, sameEnvValue)
	var parts []string
	for _, x := range got {
		parts = append(parts, x.Key+"="+string(x.Value))
	}
	if strings.Join(parts, " ") != "a=10 c=30 theirs=x mine=new" {
		t.Fatalf("merged: %v", parts)
	}
}

// Saving an environment keeps the variable a teammate added while it was
// open.
func TestEnvSaveKeepsTeammatesChange(t *testing.T) {
	d := newDriver(t)
	d.keys("gE")
	d.waitFor("Environment · Shop · Local")
	var envID string
	for _, e := range d.m.st.Environments() {
		if e.Name() == "Shop · Local" {
			envID = e.Summary.ID
		}
	}
	d.demo.Postman.EditEnvironment(envID, func(e *postman.Environment) {
		e.Values = append(e.Values, postman.EnvValue{Key: "teammate", Value: "added", Type: "default"})
	})
	d.typeText(":%s/realm: shop/realm: other/")
	d.keys("<CR>")
	d.writeQuit()
	d.waitFor("saved Shop · Local to Postman")
	got := map[string]string{}
	for _, v := range d.demo.Postman.Environment(envID).Values {
		got[v.Key] = string(v.Value)
	}
	if got["teammate"] != "added" || got["realm"] != "other" {
		t.Fatalf("after saving: %v", got)
	}
}

// A GraphQL mutation changes data wherever it is in the document: after a
// fragment, after a query.
func TestChangesData(t *testing.T) {
	gql := func(q string) *postman.Item {
		return &postman.Item{Request: &postman.Request{Method: "POST", Body: &postman.Body{Mode: "graphql", GraphQL: &postman.GraphQL{Query: postman.Str(q)}}}}
	}
	cases := []struct {
		it   *postman.Item
		want bool
	}{
		{gql("query Q { a }"), false},
		{gql("{ a }"), false},
		{gql("mutation M { b }"), true},
		{gql("fragment F on Order { id }\nmutation PlaceOrder { b { ...F } }"), true},
		{gql("# just a comment\nquery A { a }\nmutation B { b }"), true},
		{&postman.Item{Request: &postman.Request{Method: "GET"}}, false},
		{&postman.Item{Request: &postman.Request{Method: "delete"}}, true},
		{&postman.Item{Request: &postman.Request{Method: "POST"}}, true},
	}
	for i, c := range cases {
		if got := changesData(c.it); got != c.want {
			t.Errorf("%d: changesData = %v, want %v", i, got, c.want)
		}
	}
}
