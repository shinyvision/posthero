package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/shinyvision/posthero/internal/jsonx"
	"github.com/shinyvision/posthero/internal/postman"
	"github.com/shinyvision/posthero/internal/vars"
)

// Override is a current value: what a variable is on this machine when it
// differs from the team's (initial) value.
type Override struct {
	Value string `json:"value"`
	// Typed is a script's non-text value.
	Typed any `json:"typed,omitempty"`
	// Removed says the variable was unset here.
	Removed bool `json:"removed,omitempty"`
}

// Overrides are the current values of one scope, by name.
type Overrides map[string]Override

// Values keeps current values on disk, one file per scope (an
// environment, a collection, a workspace's globals). More than one posthero
// may keep them at once: each reads a scope's file again when the other
// changed it, and writes only what it changed itself.
type Values struct {
	mu   sync.Mutex
	dir  string
	sets map[string]*known
}

// known is a scope's overrides as this posthero last read or wrote them,
// and when the file was changed then.
type known struct {
	o     Overrides
	stamp stamp
}

// stamp tells a file's versions apart: its modification time and size.
type stamp struct {
	mod  int64
	size int64
}

// NewValues keeps values in dir; empty keeps them in memory.
func NewValues(dir string) *Values { return &Values{dir: dir, sets: map[string]*known{}} }

// Get returns the overrides of a scope (by id), read again when another
// posthero changed them.
func (v *Values) Get(id string) Overrides {
	v.mu.Lock()
	defer v.mu.Unlock()
	st := v.stampOf(id)
	if k, ok := v.sets[id]; ok && (v.dir == "" || k.stamp == st) {
		return k.o
	}
	o := v.read(id)
	v.sets[id] = &known{o: o, stamp: st}
	return o
}

// Set replaces the overrides of a scope and saves them. Another posthero
// may have changed them since this one read them: what changed here (from
// what Get last returned to o) is put on what the file holds now, so
// neither loses what the other set.
func (v *Values) Set(id string, o Overrides) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.dir == "" {
		v.sets[id] = &known{o: o}
		return nil
	}
	unlock := lockFile(v.path(id))
	defer unlock()
	var base Overrides
	if k, ok := v.sets[id]; ok {
		base = k.o
	}
	merged := v.read(id)
	for key, ov := range o {
		if was, ok := base[key]; !ok || !sameOverride(was, ov) {
			merged[key] = ov
		}
	}
	for key := range base {
		if _, ok := o[key]; !ok {
			delete(merged, key)
		}
	}
	err := v.write(id, merged)
	v.sets[id] = &known{o: merged, stamp: v.stampOf(id)}
	return err
}

// read reads a scope's overrides from its file.
func (v *Values) read(id string) Overrides {
	o := Overrides{}
	if v.dir != "" {
		if data, err := os.ReadFile(v.path(id)); err == nil {
			_ = json.Unmarshal(data, &o)
		}
	}
	return o
}

// write saves a scope's overrides; none removes its file.
func (v *Values) write(id string, o Overrides) error {
	if len(o) == 0 {
		err := os.Remove(v.path(id))
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	data, err := jsonx.MarshalIndent(o, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(v.dir, 0o700); err != nil {
		return err
	}
	return writeAtomic(v.path(id), data, 0o600)
}

func (v *Values) stampOf(id string) stamp {
	if v.dir == "" {
		return stamp{}
	}
	fi, err := os.Stat(v.path(id))
	if err != nil {
		return stamp{}
	}
	return stamp{mod: fi.ModTime().UnixNano(), size: fi.Size()}
}

// sameOverride compares overrides by what they hold: a script's 5 is the
// same whether it is an int64 or, read back from disk, a float64.
func sameOverride(a, b Override) bool {
	da, _ := json.Marshal(a)
	db, _ := json.Marshal(b)
	return string(da) == string(db)
}

// lockFile takes a lock next to path for a moment, so two posthero
// instances don't write a file at the same time. A lock left behind by one
// that died is taken over after a few seconds. (The same as
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

func (v *Values) path(id string) string {
	safe := strings.Map(func(r rune) rune {
		if r == '/' || r == '\\' || r == ':' {
			return '_'
		}
		return r
	}, id)
	return filepath.Join(v.dir, safe+".json")
}

// Initial is a variable as the team has it.
type Initial struct {
	Key, Value string
	Secret     bool
	Off        bool
}

// Scope makes the variables of a scope as they apply here: the team's
// values, with this machine's on top.
func Scope(kind, name string, initial []Initial, o Overrides) *vars.Scope {
	s := &vars.Scope{Kind: kind, Name: name}
	seen := map[string]bool{}
	for _, in := range initial {
		if in.Off || seen[in.Key] {
			continue
		}
		seen[in.Key] = true
		v := vars.Var{Key: in.Key, Value: in.Value, Secret: in.Secret}
		if ov, ok := o[in.Key]; ok {
			if ov.Removed {
				continue
			}
			v.Value, v.Typed = ov.Value, ov.Typed
		}
		s.Vars = append(s.Vars, v)
	}
	// Variables only this machine has.
	keys := make([]string, 0, len(o))
	for k := range o {
		keys = append(keys, k)
	}
	sortStrings(keys)
	for _, k := range keys {
		if seen[k] || o[k].Removed {
			continue
		}
		s.Vars = append(s.Vars, vars.Var{Key: k, Value: o[k].Value, Typed: o[k].Typed})
	}
	return s
}

// Diff works out the overrides that make initial into what a scope holds
// now.
func Diff(initial []Initial, now *vars.Scope) Overrides {
	o := Overrides{}
	team := map[string]Initial{}
	for _, in := range initial {
		if !in.Off {
			if _, dup := team[in.Key]; !dup {
				team[in.Key] = in
			}
		}
	}
	have := map[string]bool{}
	for _, v := range now.Vars {
		have[v.Key] = true
		if in, ok := team[v.Key]; ok && in.Value == v.Value && v.Typed == nil {
			continue
		}
		o[v.Key] = Override{Value: v.Value, Typed: v.Typed}
	}
	for k := range team {
		if !have[k] {
			o[k] = Override{Removed: true}
		}
	}
	return o
}

// EnvInitial lists an environment's variables.
func EnvInitial(values []postman.EnvValue) []Initial {
	out := make([]Initial, 0, len(values))
	for _, v := range values {
		out = append(out, Initial{Key: v.Key, Value: string(v.Value), Secret: v.Secret(), Off: !v.On()})
	}
	return out
}

// CollectionInitial lists a collection's variables.
func CollectionInitial(vs []postman.Variable) []Initial {
	out := make([]Initial, 0, len(vs))
	for _, v := range vs {
		out = append(out, Initial{Key: v.Key, Value: string(v.Value), Secret: v.Type == "secret", Off: v.Disabled})
	}
	return out
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}
