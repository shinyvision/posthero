package history

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shinyvision/posthero/internal/postman"
	"github.com/shinyvision/posthero/internal/run"
	"github.com/shinyvision/posthero/internal/send"
)

func TestHistory(t *testing.T) {
	dir := t.TempDir()
	h := Open(dir, 3)
	for i := 0; i < 5; i++ {
		r := &Record{ID: NewID() + string(rune('a'+i)), At: time.Now(), ItemID: []string{"x", "y"}[i%2], Name: "n", Status: 200 + i, Body: []byte("body")}
		if err := h.Add(r); err != nil {
			t.Fatal(err)
		}
	}
	list := h.List()
	if len(list) != 3 || list[0].Status != 204 || list[2].Status != 202 {
		t.Fatalf("list: %+v", list)
	}
	if of := h.Of("x"); len(of) != 2 || of[0].Status != 204 {
		t.Errorf("of x: %+v", of)
	}
	again := Open(dir, 3)
	if len(again.List()) != 3 {
		t.Errorf("reopened: %d", len(again.List()))
	}
	r, err := again.Load(list[0].ID)
	if err != nil || string(r.Body) != "body" {
		t.Errorf("load: %v %+v", err, r)
	}
	if _, err := again.Load("gone"); err == nil {
		t.Error("loaded a pruned record")
	}
}

// recordFiles counts the record files on disk.
func recordFiles(t *testing.T, dir string) int {
	t.Helper()
	entries, _ := os.ReadDir(filepath.Join(dir, "records"))
	return len(entries)
}

func record(i int) *Record {
	return &Record{ID: NewID() + string(rune('a'+i)), At: time.Now(), ItemID: "x", Name: "n", Status: 200 + i}
}

// Two posthero instances share the history: neither drops the other's
// records from the index, and what is pruned is gone from disk.
func TestHistoryTwoInstances(t *testing.T) {
	dir := t.TempDir()
	a, b := Open(dir, 3), Open(dir, 3)
	for i := 0; i < 3; i++ {
		if err := a.Add(record(i)); err != nil {
			t.Fatal(err)
		}
	}
	for i := 3; i < 5; i++ {
		if err := b.Add(record(i)); err != nil {
			t.Fatal(err)
		}
	}
	if err := a.Add(record(5)); err != nil {
		t.Fatal(err)
	}
	list := Open(dir, 3).List()
	if len(list) != 3 || list[0].Status != 205 || list[1].Status != 204 || list[2].Status != 203 {
		t.Errorf("index: %+v", list)
	}
	if n := recordFiles(t, dir); n != 3 {
		t.Errorf("%d record files for 3 entries", n)
	}
	for _, s := range list {
		if _, err := a.Load(s.ID); err != nil {
			t.Errorf("load %s: %v", s.ID, err)
		}
	}
	// a sees b's records too.
	if l := a.List(); len(l) != 3 || l[1].Status != 204 {
		t.Errorf("a's list: %+v", l)
	}
	// Record files the index lost earlier are cleared away.
	_ = os.WriteFile(filepath.Join(dir, "records", "orphan.json"), []byte("{}"), 0o600)
	if err := b.Add(record(6)); err != nil {
		t.Fatal(err)
	}
	if n := recordFiles(t, dir); n != 3 {
		t.Errorf("%d record files for 3 entries after an orphan", n)
	}
}

// history=0 keeps nothing, as its :set help says.
func TestHistoryZeroKeepsNone(t *testing.T) {
	dir := t.TempDir()
	h := Open(dir, 3)
	for i := 0; i < 2; i++ {
		_ = h.Add(record(i))
	}
	h.SetMax(0)
	if len(h.List()) != 0 || recordFiles(t, dir) != 0 {
		t.Errorf("after SetMax(0): %d listed, %d files", len(h.List()), recordFiles(t, dir))
	}
	r := record(9)
	if err := h.Add(r); err != nil {
		t.Fatal(err)
	}
	if len(h.List()) != 0 || recordFiles(t, dir) != 0 || len(Open(dir, 0).List()) != 0 {
		t.Errorf("kept with max 0: %d listed, %d files", len(h.List()), recordFiles(t, dir))
	}
	mem := Open("", 0)
	_ = mem.Add(record(1))
	if len(mem.List()) != 0 {
		t.Errorf("kept in memory with max 0: %d", len(mem.List()))
	}
}

// What history writes to disk has no secrets: the auth's headers and query
// parameters, and the values of secret variables, are masked. The record
// itself (shown on screen) keeps them.
func TestHistoryRedacts(t *testing.T) {
	dir := t.TempDir()
	h := Open(dir, 10)
	r := &Record{
		ID: NewID(), At: time.Now(), ItemID: "x", Name: "Login", Method: "POST",
		URL: "https://api.example.com/login?api_key=k3y-s3cret&user=rachel",
		RequestHeaders: []send.Header{
			{Name: "Authorization", Value: "Basic cmFjaGVsOmh1bnRlcjI=", Auth: true},
			{Name: "X-Api-Key", Value: "abcdef123456", Auth: true},
			{Name: "X-Trace", Value: "t-1"},
		},
		RequestBody: `{"user": "rachel", "password": "hun\"ter2!", "pin": "p@ss word"}`,
		Status:      200,
		Body:        []byte(`{"token": "tok-9876543210", "lab": 1}`),
		Logs:        nil,
		Secrets:     []string{"k3y-s3cret", `hun"ter2!`, "p@ss word", "tok-9876543210", "ab"},
	}
	r.Redirects = []send.Redirect{{Status: 302, From: "https://api.example.com/login?pin=p%40ss+word", To: "https://api.example.com/"}}
	if err := h.Add(r); err != nil {
		t.Fatal(err)
	}
	var disk strings.Builder
	_ = filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			data, _ := os.ReadFile(p)
			disk.Write(data)
		}
		return nil
	})
	for _, s := range []string{"k3y-s3cret", "cmFjaGVsOmh1bnRlcjI=", "abcdef123456", "ter2!", "p@ss word", "p%40ss+word", "tok-9876543210"} {
		if strings.Contains(disk.String(), s) {
			t.Errorf("%q is on disk", s)
		}
	}
	got, err := h.Load(r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.RequestHeaders[0].Value != "Basic ••••" || got.RequestHeaders[1].Value != "••••" || got.RequestHeaders[2].Value != "t-1" {
		t.Errorf("headers: %+v", got.RequestHeaders)
	}
	if got.URL != "https://api.example.com/login?api_key=••••&user=rachel" {
		t.Errorf("url: %s", got.URL)
	}
	if !json.Valid([]byte(got.RequestBody)) || !json.Valid(got.Body) {
		t.Errorf("bodies: %s %s", got.RequestBody, got.Body)
	}
	if !strings.Contains(string(got.Body), "ab") {
		t.Error("a secret too short to mask was masked")
	}
	if h.List()[0].URL != got.URL {
		t.Errorf("index: %s", h.List()[0].URL)
	}
	// The record on screen keeps what was sent.
	if r.RequestHeaders[0].Value != "Basic cmFjaGVsOmh1bnRlcjI=" || !strings.Contains(r.URL, "k3y-s3cret") {
		t.Errorf("the record itself changed: %+v", r)
	}
}

// A query parameter an auth added (an API key in the query) is masked even
// when its value is no secret variable.
func TestHistoryRedactsAuthQuery(t *testing.T) {
	apikey := &postman.Auth{Type: "apikey", Params: map[string][]postman.AuthParam{"apikey": {
		{Key: "key", Value: json.RawMessage(`"api_key"`)},
		{Key: "value", Value: json.RawMessage(`"{{key}}"`)},
		{Key: "in", Value: json.RawMessage(`"query"`)},
	}}}
	ex := &run.Execution{
		Target:   run.Target{Item: &postman.Item{ID: "x", Name: "List"}},
		Request:  &send.Request{Method: "GET", URL: &postman.URL{Raw: "{{base}}/items?page=2"}, Auth: apikey},
		Prepared: &send.Prepared{Method: "GET", URL: "https://api.example.com/items?page=2&api_key=plain-k3y"},
		Start:    time.Now(),
	}
	r := FromExecution(ex, "c", "Dev")
	h := Open(t.TempDir(), 10)
	if err := h.Add(r); err != nil {
		t.Fatal(err)
	}
	got, _ := h.Load(r.ID)
	if got.URL != "https://api.example.com/items?page=2&api_key=••••" || h.List()[0].URL != got.URL {
		t.Errorf("url: %s", got.URL)
	}
	if r.URL != ex.Prepared.URL {
		t.Errorf("the record itself changed: %s", r.URL)
	}
	// Redact does the same on a record of its own.
	cp := *r
	Redact(&cp, []string{"items"})
	if cp.URL != "https://api.example.com/••••?page=2&api_key=••••" {
		t.Errorf("redact: %s", cp.URL)
	}
}
