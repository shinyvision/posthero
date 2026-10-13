package graphql

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestCorpus walks every GraphQL query of real collections, at every
// offset, when $POSTHERO_CORPUS points at a directory of exports (col/).
// Nothing may panic, whatever the schema.
func TestCorpus(t *testing.T) {
	dir := os.Getenv("POSTHERO_CORPUS")
	if dir == "" {
		t.Skip("POSTHERO_CORPUS is not set")
	}
	s := schema(t)
	files, _ := filepath.Glob(filepath.Join(dir, "col", "*.json"))
	queries := 0
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		var any map[string]json.RawMessage
		_ = json.Unmarshal(data, &any)
		var walk func(raw json.RawMessage)
		walk = func(raw json.RawMessage) {
			var items []struct {
				Item    json.RawMessage `json:"item"`
				Request struct {
					Body struct {
						Mode    string `json:"mode"`
						GraphQL struct {
							Query string `json:"query"`
						} `json:"graphql"`
					} `json:"body"`
				} `json:"request"`
			}
			_ = json.Unmarshal(raw, &items)
			for _, it := range items {
				if it.Item != nil {
					walk(it.Item)
				}
				if q := it.Request.Body.GraphQL.Query; it.Request.Body.Mode == "graphql" && q != "" {
					queries++
					for i := 0; i <= len(q); i++ {
						s.At(q, i)
						s.Complete(q, i)
						s.Info(q, i)
					}
				}
			}
		}
		col := any["item"]
		if c, ok := any["collection"]; ok {
			var inner map[string]json.RawMessage
			_ = json.Unmarshal(c, &inner)
			col = inner["item"]
		}
		walk(col)
	}
	t.Logf("%d queries", queries)
}
