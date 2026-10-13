// Package jsonx encodes JSON the way people and Postman write it: without
// escaping &, < and > as &, < and >, which Go does by default
// for HTML's sake. posthero's JSON ends up in files, in Postman, on screen
// and in generated code, never in HTML.
package jsonx

import (
	"bytes"
	"encoding/json"
)

// Marshal is json.Marshal without HTML escaping.
func Marshal(v any) ([]byte, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(b.Bytes(), []byte("\n")), nil
}

// MarshalIndent is json.MarshalIndent without HTML escaping.
func MarshalIndent(v any, prefix, indent string) ([]byte, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	enc.SetIndent(prefix, indent)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(b.Bytes(), []byte("\n")), nil
}
