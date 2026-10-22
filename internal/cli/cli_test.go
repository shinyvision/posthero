package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/shinyvision/posthero/internal/postman"
	"github.com/shinyvision/posthero/internal/postman/pmtest"
)

const getOrder = "GET {{baseUrl}}/orders/1\n"

// shop starts a fake with a collection "Shop": a folder "Orders" with a
// request "Get order", and a folder "a/b" (a slash in its name).
func shop(t *testing.T) (*pmtest.Server, *Env, *bytes.Buffer) {
	t.Helper()
	srv := pmtest.New()
	t.Cleanup(srv.Close)
	api := postman.NewClient(pmtest.Key, srv.Start())
	get := &postman.Item{Name: "Get order", Request: &postman.Request{Method: "GET", URL: postman.ParseURL("{{baseUrl}}/orders/1")}}
	srv.AddCollection("ws1", &postman.Collection{
		Info: postman.Info{Name: "Shop"},
		Item: []*postman.Item{
			{Name: "Orders", Folder: true, Item: []*postman.Item{get}},
			{Name: "a/b", Folder: true},
		},
	})
	out := &bytes.Buffer{}
	return srv, &Env{API: api, Last: "ws1", Out: out}, out
}

func cmd(t *testing.T, e *Env, in string, args ...string) error {
	t.Helper()
	e.In = strings.NewReader(in)
	return Run(context.Background(), e, args)
}

func TestListAndCat(t *testing.T) {
	_, e, out := shop(t)
	if err := cmd(t, e, "", "ls"); err != nil || !strings.HasPrefix(out.String(), "Shop\t") {
		t.Fatalf("ls: %v, %q", err, out)
	}
	out.Reset()
	if err := cmd(t, e, "", "ls", "Shop"); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Orders/\t", "  GET Get order\t", "a/b/\t"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("ls Shop lacks %q:\n%s", want, out)
		}
	}
	out.Reset()
	if err := cmd(t, e, "", "cat", "shop/orders/get order"); err != nil || out.String() != getOrder {
		t.Fatalf("cat, ignoring case: %v, %q", err, out)
	}
	if err := cmd(t, e, "", "cat", "Shop/Orders"); err == nil {
		t.Error("cat of a folder worked")
	}
	if err := cmd(t, e, "", "cat", "Shop/Nope"); err == nil || !strings.Contains(err.Error(), `"Nope"`) {
		t.Errorf("cat of nothing: %v", err)
	}
}

func TestMakeAndSave(t *testing.T) {
	_, e, out := shop(t)
	if err := cmd(t, e, "", "mkdir", "Shop/Orders/Refunds"); err != nil {
		t.Fatal(err)
	}
	text := "POST {{baseUrl}}/refunds\nContent-Type: application/json\n\n{\"order\": 1}\n"
	if err := cmd(t, e, text, "add", "Shop/Orders/Refunds/Make refund", "-"); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := cmd(t, e, "", "cat", "Shop/Orders/Refunds/Make refund"); err != nil || out.String() != text {
		t.Fatalf("cat after add: %v\n%s", err, out)
	}
	if err := cmd(t, e, text, "add", "Shop/Orders/Refunds/Make refund"); err == nil || !strings.Contains(err.Error(), "already") {
		t.Errorf("a second request of the same name: %v", err)
	}
	if err := cmd(t, e, "", "mkdir", "Shop/Orders/Get order/x"); err == nil {
		t.Error("mkdir in a request worked")
	}

	changed := "DELETE {{baseUrl}}/orders/1\n"
	out.Reset()
	if err := cmd(t, e, changed, "save", "Shop/Orders/Get order"); err != nil || !strings.HasPrefix(out.String(), "saved Get order\t") {
		t.Fatalf("save: %v, %q", err, out)
	}
	out.Reset()
	if err := cmd(t, e, changed, "save", "Shop/Orders/Get order"); err != nil || out.String() != "no changes to save\n" {
		t.Errorf("saving the same text: %v, %q", err, out)
	}
	if err := cmd(t, e, "", "save", "Shop/Orders/Get order"); err == nil {
		t.Error("saving text that isn't a request worked")
	}
}

func TestSaveSince(t *testing.T) {
	_, e, out := shop(t)
	if err := cmd(t, e, "", "ls", "-l", "Shop/Orders/Get order"); err != nil {
		t.Fatal(err)
	}
	fields := strings.Split(strings.TrimSpace(out.String()), "\t")
	since := fields[len(fields)-1]
	if err := cmd(t, e, "POST {{baseUrl}}/orders/1\n", "save", "--since", since, "Shop/Orders/Get order"); err != nil {
		t.Fatalf("save since the version read: %v", err)
	}
	// The fake's clock moved on: what was read is older than Postman's now.
	err := cmd(t, e, "PUT {{baseUrl}}/orders/1\n", "save", "Shop/Orders/Get order", "--since", since)
	if err == nil || !strings.Contains(err.Error(), "changed in Postman") {
		t.Errorf("save over a newer version: %v", err)
	}
}

func TestPaths(t *testing.T) {
	_, e, out := shop(t)
	if err := cmd(t, e, "", "mkdir", `Shop/a\/b/c`); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := cmd(t, e, "", "ls", `Shop/a\/b`); err != nil || !strings.HasPrefix(out.String(), "c/\t") {
		t.Errorf(`ls Shop/a\/b: %v, %q`, err, out)
	}
	got := splitPath(`x / y\/z/ `)
	if len(got) != 2 || got[0] != "x" || got[1] != "y/z" {
		t.Errorf("splitPath: %q", got)
	}
	if joinPath(got) != `x/y\/z` {
		t.Errorf("joinPath: %q", joinPath(got))
	}
}

func TestWorkspaces(t *testing.T) {
	srv, e, out := shop(t)
	srv.AddWorkspace("ws2", "Other")
	e.Workspace = "other"
	if err := cmd(t, e, "", "ls"); err != nil || out.Len() != 0 {
		t.Errorf("ls in an empty workspace, by name: %v, %q", err, out)
	}
	if err := cmd(t, e, "", "ls", "-w", "nope"); err == nil {
		t.Error("a workspace that isn't there worked")
	}
	e.Workspace, e.Last = "", ""
	if err := cmd(t, e, "", "ls"); err == nil || !strings.Contains(err.Error(), "-w") {
		t.Errorf("no workspace at all: %v", err)
	}
	e.Last = "ws1"
	e.Local = func(id string) bool { return id == "ws1" }
	if err := cmd(t, e, "", "mkdir", "Shop/New"); err == nil || !strings.Contains(err.Error(), "local mode") {
		t.Errorf("a change to a workspace in local mode: %v", err)
	}
	if err := cmd(t, e, "", "ls", "Shop"); err != nil {
		t.Errorf("reading a workspace in local mode: %v", err)
	}
}

func TestEnvironments(t *testing.T) {
	srv, e, out := shop(t)
	id := srv.AddEnvironment("ws1", &postman.Environment{Name: "Shop - Local", Values: []postman.EnvValue{
		{Key: "base", Value: "http://localhost"},
		{Key: "token", Value: "s3cr3t", Type: "secret"},
	}})
	if err := cmd(t, e, "", "env"); err != nil || !strings.HasPrefix(out.String(), "Shop - Local\t") {
		t.Fatalf("env: %v, %q", err, out)
	}
	out.Reset()
	if err := cmd(t, e, "", "env", "cat", "shop - local"); err != nil || out.String() != "base = http://localhost\ntoken = ••••••\n" {
		t.Fatalf("env cat: %v, %q", err, out)
	}
	out.Reset()
	if err := cmd(t, e, "", "env", "set", "Shop - Local", "base", "http://localhost:8081"); err != nil || !strings.Contains(out.String(), "(was http://localhost)") {
		t.Fatalf("env set: %v, %q", err, out)
	}
	got := srv.Environment(id).Values
	if len(got) != 2 || got[0].Value != "http://localhost:8081" || got[1].Value != "s3cr3t" || got[1].Type != "secret" {
		t.Errorf("after env set, Postman has %+v", got)
	}
	if err := cmd(t, e, "", "env", "set", "Shop - Local", "port", "8081"); err != nil || len(srv.Environment(id).Values) != 3 {
		t.Errorf("adding a variable: %v, %+v", err, srv.Environment(id).Values)
	}
	if err := cmd(t, e, "", "env", "set", "--since", "2000-01-01T00:00:00Z", "Shop - Local", "base", "x"); err == nil || !strings.Contains(err.Error(), "changed in Postman") {
		t.Errorf("env set over a newer version: %v", err)
	}
	if err := cmd(t, e, "", "env", "set", "Shop - Local", "base"); err == nil {
		t.Error("env set without a value worked")
	}
}
