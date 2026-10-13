// Package history keeps what was sent and what came back, so posthero
// shows a request's last response when it opens and :history can bring any
// of them back.
package history

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/shinyvision/posthero/internal/auth"
	"github.com/shinyvision/posthero/internal/jsonx"
	"github.com/shinyvision/posthero/internal/postman"
	"github.com/shinyvision/posthero/internal/run"
	"github.com/shinyvision/posthero/internal/script"
	"github.com/shinyvision/posthero/internal/send"
)

// Record is one request sent and its response, everything the response
// pane shows.
type Record struct {
	ID           string    `json:"id"`
	At           time.Time `json:"at"`
	CollectionID string    `json:"collection,omitempty"`
	ItemID       string    `json:"item,omitempty"`
	Name         string    `json:"name"`
	// Location is the collection, folders and request names.
	Location []string `json:"location,omitempty"`
	Env      string   `json:"env,omitempty"`

	Method         string        `json:"method"`
	URL            string        `json:"url"`
	RequestHeaders []send.Header `json:"requestHeaders,omitempty"`
	RequestBody    string        `json:"requestBody,omitempty"`
	Auth           string        `json:"auth,omitempty"`

	Status     int             `json:"status,omitempty"`
	StatusText string          `json:"statusText,omitempty"`
	Proto      string          `json:"proto,omitempty"`
	Headers    []send.Header   `json:"headers,omitempty"`
	Body       []byte          `json:"body,omitempty"`
	Truncated  bool            `json:"truncated,omitempty"`
	BodySize   int64           `json:"bodySize,omitempty"`
	WireSize   int64           `json:"wireSize,omitempty"`
	HeaderSize int64           `json:"headerSize,omitempty"`
	Duration   time.Duration   `json:"duration,omitempty"`
	Timings    send.Timings    `json:"timings"`
	Redirects  []send.Redirect `json:"redirects,omitempty"`
	TLS        *send.TLSInfo   `json:"tls,omitempty"`
	Remote     string          `json:"remote,omitempty"`
	Cookies    []Cookie        `json:"cookies,omitempty"`
	Tests      []script.Test   `json:"tests,omitempty"`
	Logs       []script.Log    `json:"logs,omitempty"`
	ScriptErrs []string        `json:"scriptErrors,omitempty"`
	Visual     *script.Visual  `json:"visual,omitempty"`
	Error      string          `json:"error,omitempty"`
	Skipped    bool            `json:"skipped,omitempty"`
	Missing    []string        `json:"missing,omitempty"`
	// Example says the record is a saved example, not a response.
	Example bool `json:"example,omitempty"`

	// Secrets are values that are never written to disk: the values of
	// secret variables (vars.Set.Secrets). Add masks them in what it keeps
	// (see Redact); the record itself keeps them, for the screen.
	Secrets []string `json:"-"`
	// authQuery are the query parameters the auth added to the URL, and
	// authSecrets the tokens it sent: Redact masks them too.
	authQuery   []string
	authSecrets []string
}

// Cookie is a cookie a response set.
type Cookie struct {
	Name, Value, Domain, Path string
	Expires                   time.Time `json:",omitzero"`
	Secure, HTTPOnly          bool
}

// Get returns a response header's value, ignoring case.
func (r *Record) Get(name string) string {
	for _, h := range r.Headers {
		if strings.EqualFold(h.Name, name) {
			return h.Value
		}
	}
	return ""
}

// ContentType is the response's media type without parameters.
func (r *Record) ContentType() string {
	ct, _, _ := strings.Cut(r.Get("Content-Type"), ";")
	return strings.ToLower(strings.TrimSpace(ct))
}

// Passed and Failed count the tests.
func (r *Record) Passed() int {
	n := 0
	for _, t := range r.Tests {
		if t.Passed {
			n++
		}
	}
	return n
}

// Failed counts the tests that failed.
func (r *Record) Failed() int {
	n := 0
	for _, t := range r.Tests {
		if !t.Passed && !t.Skipped {
			n++
		}
	}
	return n
}

// NewID makes a record id.
func NewID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return time.Now().UTC().Format("20060102T150405") + "-" + hex.EncodeToString(b)
}

// FromExecution makes a record of a request that was sent.
func FromExecution(ex *run.Execution, colID, env string) *Record {
	r := &Record{ID: NewID(), At: ex.Start, CollectionID: colID, Env: env}
	if ex.Target.Item != nil {
		r.ItemID, r.Name = ex.Target.Item.ID, ex.Target.Item.Name
		r.Location = ex.Target.Location()
	}
	if p := ex.Prepared; p != nil {
		r.Method, r.URL = p.Method, p.URL
		r.RequestHeaders = p.Header
		switch {
		case p.BodyText != "":
			r.RequestBody = p.BodyText
		case len(p.Body) > 0:
			r.RequestBody = string(p.Body[:min(len(p.Body), 256<<10)])
		}
	} else if ex.Request != nil {
		r.Method, r.URL = ex.Request.Method, ex.Request.URL.String()
	}
	if u := ex.Auth; u != nil {
		r.Auth = describeAuth(u)
		if t := u.Token; t != nil {
			r.authSecrets = []string{t.AccessToken, t.RefreshToken, t.IDToken}
		}
	}
	if req := ex.Request; req != nil && signs(req.Auth) && ex.Prepared != nil {
		r.authQuery = addedQuery(req.URL, ex.Prepared.URL)
	}
	if resp := ex.Response; resp != nil {
		r.Status, r.StatusText, r.Proto = resp.Status, resp.StatusText, resp.Proto
		r.Headers, r.Body, r.Truncated = resp.Header, resp.Body, resp.Truncated
		r.BodySize, r.WireSize, r.HeaderSize = int64(len(resp.Body)), resp.WireSize, resp.HeaderSize
		r.Duration, r.Timings, r.Redirects, r.TLS, r.Remote = resp.Time, resp.Timings, resp.Redirects, resp.TLS, resp.Remote
		for _, c := range resp.Cookies {
			r.Cookies = append(r.Cookies, Cookie{Name: c.Name, Value: c.Value, Domain: c.Domain, Path: c.Path, Expires: c.Expires, Secure: c.Secure, HTTPOnly: c.HttpOnly})
		}
	}
	r.Tests, r.Logs, r.Visual = ex.Scripts.Tests, ex.Scripts.Logs, ex.Scripts.Visual
	for _, e := range ex.Scripts.Errors {
		r.ScriptErrs = append(r.ScriptErrs, e.Error())
	}
	if ex.Err != nil {
		r.Error = ex.Err.Error()
	}
	r.Skipped, r.Missing = ex.Skipped, ex.Missing
	return r
}

// signs reports whether an auth signs requests (noauth and inherit don't).
func signs(a *postman.Auth) bool {
	return a != nil && a.Type != "" && a.Type != "noauth" && a.Type != "inherit"
}

// describeAuth says how a request was signed: "OAuth 2.0 from the
// collection · Shop Token · rachel (refreshed)".
func describeAuth(u *auth.Used) string {
	parts := []string{auth.Names[u.Type]}
	if parts[0] == "" {
		parts[0] = u.Type
	}
	if u.From != "" {
		parts[0] += " from the " + u.From
		if strings.HasPrefix(u.From, "folder ") {
			parts[0] = auth.Names[u.Type] + " from " + u.From
		}
	}
	if t := u.Token; t != nil {
		if t.Name != "" {
			parts = append(parts, t.Name)
		}
		who := t.Subject
		switch u.Fetch {
		case auth.Refreshed:
			who += " (refreshed)"
		case auth.LoggedIn:
			who += " (just logged in)"
		}
		if strings.TrimSpace(who) != "" {
			parts = append(parts, strings.TrimSpace(who))
		}
	}
	return strings.Join(parts, " · ")
}

// Summary is a record as the history list shows it.
type Summary struct {
	ID           string        `json:"id"`
	At           time.Time     `json:"at"`
	CollectionID string        `json:"collection,omitempty"`
	ItemID       string        `json:"item,omitempty"`
	Name         string        `json:"name"`
	Env          string        `json:"env,omitempty"`
	Method       string        `json:"method"`
	URL          string        `json:"url"`
	Status       int           `json:"status,omitempty"`
	Duration     time.Duration `json:"duration,omitempty"`
	Passed       int           `json:"passed,omitempty"`
	Failed       int           `json:"failed,omitempty"`
	Error        string        `json:"error,omitempty"`
}

// Summary returns the record's summary.
func (r *Record) Summary() Summary {
	return Summary{ID: r.ID, At: r.At, CollectionID: r.CollectionID, ItemID: r.ItemID, Name: r.Name, Env: r.Env, Method: r.Method, URL: r.URL,
		Status: r.Status, Duration: r.Duration, Passed: r.Passed(), Failed: r.Failed(), Error: r.Error}
}

// History keeps records in a directory: an index of summaries and one
// file per record. Other posthero instances may keep records in the same
// directory: changes to it are made under a lock, on the index as it is on
// disk then.
type History struct {
	mu    sync.Mutex
	dir   string
	max   int
	index []Summary // oldest first
	// bodyLimit is how much of a response body is kept.
	bodyLimit int
}

// Open reads the history in dir (empty keeps it in memory), keeping at
// most max records; 0 keeps none.
func Open(dir string, max int) *History {
	h := &History{dir: dir, max: max, bodyLimit: 5 << 20}
	if dir != "" {
		h.index = h.readIndex()
	}
	return h
}

func (h *History) indexPath() string { return filepath.Join(h.dir, "index.jsonl") }

// readIndex reads the index as it is on disk.
func (h *History) readIndex() []Summary {
	f, err := os.Open(h.indexPath())
	if err != nil {
		return nil
	}
	defer f.Close()
	var index []Summary
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 4<<20)
	for sc.Scan() {
		var s Summary
		if json.Unmarshal(sc.Bytes(), &s) == nil && s.ID != "" {
			index = append(index, s)
		}
	}
	return index
}

// SetMax changes how many records are kept, dropping the oldest ones past
// it at once.
func (h *History) SetMax(n int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.max = n
	if len(h.index) <= n {
		return
	}
	if h.dir == "" {
		h.pruneMemory()
		return
	}
	unlock := lockFile(h.indexPath())
	defer unlock()
	h.index = h.readIndex()
	_ = h.prune()
}

// Add keeps a record. What goes to disk has its secrets masked (see
// Redact); with a maximum of 0 nothing is kept.
func (h *History) Add(r *Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.dir == "" {
		h.index = append(h.index, r.Summary())
		h.memory(r)
		h.pruneMemory()
		return nil
	}
	keep := *r
	Redact(&keep, r.Secrets)
	if len(keep.Body) > h.bodyLimit {
		keep.Body, keep.Truncated = keep.Body[:h.bodyLimit], true
	}
	unlock := lockFile(h.indexPath())
	defer unlock()
	h.index = h.readIndex()
	if h.max <= 0 {
		return h.prune()
	}
	if err := h.writeRecord(&keep); err != nil {
		return err
	}
	h.index = append(h.index, keep.Summary())
	if len(h.index) > h.max {
		return h.prune()
	}
	line, _ := jsonx.Marshal(keep.Summary())
	f, err := os.OpenFile(h.indexPath(), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, err = f.Write(append(line, '\n'))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

func (h *History) writeRecord(r *Record) error {
	data, err := jsonx.Marshal(r)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(h.dir, "records"), 0o700); err != nil {
		return err
	}
	return os.WriteFile(h.recordPath(r.ID), data, 0o600)
}

var mem = struct {
	sync.Mutex
	m map[string]*Record
}{m: map[string]*Record{}}

func (h *History) memory(r *Record) {
	mem.Lock()
	mem.m[r.ID] = r
	mem.Unlock()
}

func (h *History) recordPath(id string) string {
	return filepath.Join(h.dir, "records", id+".json")
}

// kept is what is left of the index after dropping the oldest records past
// the maximum.
func (h *History) kept() []Summary {
	if len(h.index) <= h.max {
		return h.index
	}
	return append([]Summary(nil), h.index[len(h.index)-max(h.max, 0):]...)
}

// pruneMemory drops the oldest records past the maximum, in memory.
func (h *History) pruneMemory() {
	kept := h.kept()
	drop := h.index[:len(h.index)-len(kept)]
	h.index = kept
	mem.Lock()
	for _, s := range drop {
		delete(mem.m, s.ID)
	}
	mem.Unlock()
}

// prune drops the oldest records past the maximum, and any record file the
// index doesn't list (another instance's, which an older posthero lost
// from the index), and writes the index. It runs under the lock.
func (h *History) prune() error {
	h.index = h.kept()
	listed := map[string]bool{}
	for _, s := range h.index {
		listed[s.ID+".json"] = true
	}
	entries, _ := os.ReadDir(filepath.Join(h.dir, "records"))
	for _, e := range entries {
		if !listed[e.Name()] {
			_ = os.Remove(filepath.Join(h.dir, "records", e.Name()))
		}
	}
	return h.writeIndex()
}

func (h *History) writeIndex() error {
	var b strings.Builder
	for _, s := range h.index {
		line, _ := jsonx.Marshal(s)
		b.Write(line)
		b.WriteByte('\n')
	}
	if err := os.MkdirAll(h.dir, 0o700); err != nil {
		return err
	}
	return writeAtomic(h.indexPath(), []byte(b.String()))
}

// writeAtomic writes a file through a temporary file of its own, so a
// reader never sees half of it.
func writeAtomic(path string, data []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	_, err = f.Write(data)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(f.Name(), path)
	}
	if err != nil {
		os.Remove(f.Name())
	}
	return err
}

// lockFile takes a lock next to path for a moment, so two posthero
// instances don't change the history at the same time. A lock left behind
// by one that died is taken over after a few seconds. (The same as
// internal/auth's.)
func lockFile(path string) (unlock func()) {
	lock := path + ".lock"
	_ = os.MkdirAll(filepath.Dir(lock), 0o700)
	for i := 0; i < 200; i++ {
		f, err := os.OpenFile(lock, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			f.Close()
			return func() { os.Remove(lock) }
		}
		if fi, err := os.Stat(lock); err == nil && time.Since(fi.ModTime()) > 5*time.Second {
			os.Remove(lock)
			continue
		}
		time.Sleep(10 * time.Millisecond)
	}
	return func() {}
}

// List returns the summaries, newest first.
func (h *History) List() []Summary {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]Summary, len(h.index))
	for i, s := range h.index {
		out[len(h.index)-1-i] = s
	}
	return out
}

// Load reads a record.
func (h *History) Load(id string) (*Record, error) {
	if h.dir == "" {
		mem.Lock()
		defer mem.Unlock()
		if r, ok := mem.m[id]; ok {
			return r, nil
		}
		return nil, fmt.Errorf("no record %s", id)
	}
	data, err := os.ReadFile(h.recordPath(id))
	if err != nil {
		return nil, err
	}
	var r Record
	if err := json.Unmarshal(data, &r); err != nil {
		return nil, err
	}
	return &r, nil
}

// Of returns the summaries of one request, newest first.
func (h *History) Of(itemID string) []Summary {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []Summary
	for i := len(h.index) - 1; i >= 0; i-- {
		if h.index[i].ItemID == itemID {
			out = append(out, h.index[i])
		}
	}
	return out
}

// Clear forgets everything.
func (h *History) Clear() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.index = nil
	if h.dir == "" {
		return nil
	}
	unlock := lockFile(h.indexPath())
	defer unlock()
	_ = os.RemoveAll(filepath.Join(h.dir, "records"))
	return h.writeIndex()
}
