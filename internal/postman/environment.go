package postman

import (
	"encoding/json"

	"github.com/shinyvision/posthero/internal/jsonx"
)

// Environment is a named set of variables, like "Shop - Local".
type Environment struct {
	ID        string     `json:"id,omitempty"`
	UID       string     `json:"uid,omitempty"`
	Name      string     `json:"name"`
	Owner     string     `json:"owner,omitempty"`
	CreatedAt string     `json:"createdAt,omitempty"`
	UpdatedAt string     `json:"updatedAt,omitempty"`
	IsPublic  bool       `json:"isPublic,omitempty"`
	Values    []EnvValue `json:"values"`
	Extra     Extra      `json:"-"`
}

// EnvValue is an environment variable. Value is the shared (initial)
// value; everyone's current values stay on their own machine.
type EnvValue struct {
	Key   string `json:"key"`
	Value Str    `json:"value"`
	// Enabled is false for a variable switched off; missing means on.
	Enabled *bool `json:"enabled,omitempty"`
	// Type is "default" or "secret" (and "any" in older exports).
	Type  string `json:"type,omitempty"`
	Extra Extra  `json:"-"`
}

// On reports whether the variable is switched on.
func (v EnvValue) On() bool { return v.Enabled == nil || *v.Enabled }

// Secret reports whether the variable is marked secret, so its value is
// masked on screen.
func (v EnvValue) Secret() bool { return v.Type == "secret" }

// UnmarshalJSON implements json.Unmarshaler.
func (e *Environment) UnmarshalJSON(data []byte) error {
	type plain Environment
	var p plain
	ex, err := unmarshalKeeping(data, &p)
	if err != nil {
		return err
	}
	*e = Environment(p)
	e.Extra = ex
	return nil
}

// MarshalJSON implements json.Marshaler.
func (e Environment) MarshalJSON() ([]byte, error) {
	type plain Environment
	if e.Values == nil {
		e.Values = []EnvValue{}
	}
	return marshalKeeping(plain(e), e.Extra)
}

// UnmarshalJSON implements json.Unmarshaler.
func (v *EnvValue) UnmarshalJSON(data []byte) error {
	type plain EnvValue
	var p plain
	ex, err := unmarshalKeeping(data, &p)
	if err != nil {
		return err
	}
	*v = EnvValue(p)
	if raw, ok := rawField(data, "enabled"); ok {
		on := string(raw) == "null" || boolish(raw)
		v.Enabled = &on
	}
	v.Extra = ex
	return nil
}

// MarshalJSON implements json.Marshaler.
func (v EnvValue) MarshalJSON() ([]byte, error) {
	type plain EnvValue
	return marshalKeeping(plain(v), v.Extra)
}

// Get returns the value of a variable that is switched on.
func (e *Environment) Get(key string) (string, bool) {
	for _, v := range e.Values {
		if v.Key == key && v.On() {
			return string(v.Value), true
		}
	}
	return "", false
}

// Clone returns a deep copy.
func (e *Environment) Clone() *Environment {
	if e == nil {
		return nil
	}
	data, _ := jsonx.Marshal(e)
	var c Environment
	_ = json.Unmarshal(data, &c)
	return &c
}
