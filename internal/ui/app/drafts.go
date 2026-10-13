package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/shinyvision/posthero/internal/jsonx"
	"github.com/shinyvision/posthero/internal/postman"
	"github.com/shinyvision/posthero/internal/secret"
)

// drafts are requests' (and examples') texts that differ from what Postman
// has: changes not saved yet. They survive quitting, like unsaved tabs in
// Postman.
type drafts struct {
	mu    sync.Mutex
	path  string
	all   map[string]draft
	dirty bool
}

type draft struct {
	Text string    `json:"text"`
	At   time.Time `json:"at"`
	// Base is the version (updatedAt) of the request in Postman the draft
	// started from: saving it over a newer one asks first.
	Base string `json:"base,omitempty"`
}

func loadDrafts(path string) *drafts {
	d := &drafts{path: path, all: map[string]draft{}}
	if path != "" {
		if data, err := os.ReadFile(path); err == nil {
			_ = json.Unmarshal(data, &d.all)
		}
	}
	return d
}

func (d *drafts) get(id string) (string, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	dr, ok := d.all[id]
	return dr.Text, ok
}

// set keeps a request's draft; base is the version it starts from, when
// it is a new one.
func (d *drafts) set(id, text, base string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	old, ok := d.all[id]
	if ok && old.Text == text {
		return
	}
	if ok && old.Base != "" {
		base = old.Base
	}
	d.all[id] = draft{Text: text, At: time.Now(), Base: base}
	d.dirty = true
}

// base is the version a draft started from.
func (d *drafts) base(id string) string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.all[id].Base
}

// rebase makes a draft start from another version (the one just saved).
func (d *drafts) rebase(id, base string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if dr, ok := d.all[id]; ok {
		dr.Base = base
		d.all[id] = dr
		d.dirty = true
	}
}

func (d *drafts) drop(id string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if _, ok := d.all[id]; ok {
		delete(d.all, id)
		d.dirty = true
	}
}

// ids lists the requests with drafts.
func (d *drafts) ids() map[string]bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make(map[string]bool, len(d.all))
	for id := range d.all {
		out[id] = true
	}
	return out
}

func (d *drafts) save() {
	d.mu.Lock()
	if !d.dirty || d.path == "" {
		d.mu.Unlock()
		return
	}
	data, err := jsonx.MarshalIndent(d.all, "", "  ")
	d.dirty = false
	d.mu.Unlock()
	if err != nil {
		return
	}
	_ = os.MkdirAll(filepath.Dir(d.path), 0o700)
	tmp := d.path + ".tmp"
	if os.WriteFile(tmp, data, 0o600) == nil {
		_ = os.Rename(tmp, d.path)
	}
}

// vault keeps pm.vault and {{vault:name}} secrets on this machine: the
// names in a file, the values in the system keyring (or the file, readable
// only by the user, without one).
type vault struct {
	mu    sync.Mutex
	path  string
	names map[string]string // name → value when there is no keyring, "" otherwise
}

func newVault(path string) *vault {
	v := &vault{path: path, names: map[string]string{}}
	if path != "" {
		if data, err := os.ReadFile(path); err == nil {
			_ = json.Unmarshal(data, &v.names)
		}
	}
	return v
}

func vaultID(key string) string { return "vault " + key }

// Get implements script.Vault.
func (v *vault) Get(key string) (string, bool) {
	v.mu.Lock()
	inFile, known := v.names[key]
	v.mu.Unlock()
	if !known {
		return "", false
	}
	if inFile != "" {
		return inFile, true
	}
	val, err := secret.Get(vaultID(key))
	if err != nil {
		return "", false
	}
	return val, true
}

// Set implements script.Vault.
func (v *vault) Set(key, value string) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	if err := secret.Set(vaultID(key), value); err == nil {
		v.names[key] = ""
	} else {
		v.names[key] = value
	}
	return v.save()
}

// Unset implements script.Vault.
func (v *vault) Unset(key string) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	_ = secret.Delete(vaultID(key))
	delete(v.names, key)
	return v.save()
}

// Names lists the vault's secrets.
func (v *vault) Names() []string {
	v.mu.Lock()
	defer v.mu.Unlock()
	out := make([]string, 0, len(v.names))
	for k := range v.names {
		out = append(out, k)
	}
	return out
}

func (v *vault) save() error {
	if v.path == "" {
		return nil
	}
	data, err := jsonx.MarshalIndent(v.names, "", "  ")
	if err != nil {
		return err
	}
	_ = os.MkdirAll(filepath.Dir(v.path), 0o700)
	return os.WriteFile(v.path, data, 0o600)
}

// pruneDrafts drops the drafts of requests (and examples) that aren't in
// any collection any more (a teammate deleted them), once every collection
// is loaded.
func (m *Model) pruneDrafts() {
	if m.st == nil {
		return
	}
	have := map[string]bool{}
	for _, c := range m.st.Collections() {
		if c.Data == nil {
			return // not everything is here: nothing can be said to be gone
		}
		c.Data.Walk(func(it *postman.Item, _ []*postman.Item) bool {
			have[it.ID] = true
			for _, r := range it.Response {
				have[r.ID] = true
			}
			return true
		})
	}
	for id := range m.drafts.ids() {
		if !have[id] && (m.open == nil || m.open.id() != id) {
			m.drafts.drop(id)
		}
	}
	m.updateMarks()
}
