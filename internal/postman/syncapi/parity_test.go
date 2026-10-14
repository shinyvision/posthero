package syncapi

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/shinyvision/posthero/internal/jsonx"
	"github.com/shinyvision/posthero/internal/postman"
)

// TestParity compares collections fetched from the sync service, turned
// into posthero's format, with the same collections fetched from the
// Postman API: they must be the same. It needs POSTHERO_PARITY_DIR, a
// directory with api/<uid>.json (GET /collections/<uid> from the API) and
// sync/<uid>.json (the sync service's answer to GET
// /collection/<uid>?populate=true&responses=true). The captures are real
// workspaces' data, so they stay out of the repository.
func TestParity(t *testing.T) {
	dir := os.Getenv("POSTHERO_PARITY_DIR")
	if dir == "" {
		t.Skip("POSTHERO_PARITY_DIR is not set")
	}
	files, _ := filepath.Glob(filepath.Join(dir, "api", "*.json"))
	if len(files) == 0 {
		t.Fatalf("no captures in %s/api", dir)
	}
	// As Collection does: a collection's own updatedAt is the list's.
	own := map[string]string{}
	var list []envelope[collectionDTO]
	if readCapture(t, filepath.Join(dir, "sync-collections.json"), &list) {
		for _, s := range toSummaries(list) {
			own[s.UID] = s.UpdatedAt
		}
	}
	diffs := map[string][]string{}
	compared := 0
	for _, f := range files {
		name := strings.TrimSuffix(filepath.Base(f), ".json")
		want := apiCollection(t, f)
		got := syncCollection(t, filepath.Join(dir, "sync", name+".json"))
		if !reflect.DeepEqual(want, want.Clone()) || !reflect.DeepEqual(got, got.Clone()) {
			t.Fatalf("deep copy changed captured collection %s", name)
		}
		if at, ok := own[name]; ok {
			got.Info.UpdatedAt = at
		}
		compared++
		for _, d := range diff("", plainJSON(t, want), plainJSON(t, got)) {
			key := regexp.MustCompile(`\[\d+\]`).ReplaceAllString(d.path, "[]")
			key = regexp.MustCompile(`(\.item\[\])+`).ReplaceAllString(key, ".item*")
			diffs[key] = append(diffs[key], name+": "+d.String())
		}
	}
	keys := make([]string, 0, len(diffs))
	for k := range diffs {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		t.Errorf("%s: %d differences, e.g.\n    %s", k, len(diffs[k]), strings.Join(diffs[k][:min(2, len(diffs[k]))], "\n    "))
	}
	t.Logf("compared %d collections", compared)
}

func apiCollection(t *testing.T, path string) *postman.Collection {
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		Collection *postman.Collection `json:"collection"`
	}
	if err := json.Unmarshal(data, &out); err != nil || out.Collection == nil {
		t.Fatalf("%s: %v", path, err)
	}
	return out.Collection
}

func syncCollection(t *testing.T, path string) *postman.Collection {
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var ack []struct {
		Body struct {
			Data *collectionDTO `json:"data"`
		} `json:"body"`
	}
	if err := decode(data, &ack); err != nil || len(ack) == 0 || ack[0].Body.Data == nil {
		t.Fatalf("%s: %v", path, err)
	}
	return toCollection(ack[0].Body.Data)
}

// plainJSON is a collection as plain JSON values, the way posthero writes it.
func plainJSON(t *testing.T, c *postman.Collection) any {
	data, err := jsonx.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	var v any
	if err := json.Unmarshal(data, &v); err != nil {
		t.Fatal(err)
	}
	return v
}

type difference struct {
	path      string
	want, got any
}

func (d difference) String() string {
	return fmt.Sprintf("%s: api %s, sync %s", d.path, short(d.want), short(d.got))
}

func short(v any) string {
	if v == nil {
		return "(none)"
	}
	data, _ := json.Marshal(v)
	s := string(data)
	if len(s) > 160 {
		s = s[:160] + "…"
	}
	return s
}

// diff lists where two JSON values differ. Items are matched by position.
func diff(path string, want, got any) []difference {
	switch w := want.(type) {
	case map[string]any:
		g, ok := got.(map[string]any)
		if !ok {
			return []difference{{path, want, got}}
		}
		var out []difference
		keys := map[string]bool{}
		for k := range w {
			keys[k] = true
		}
		for k := range g {
			keys[k] = true
		}
		names := make([]string, 0, len(keys))
		for k := range keys {
			names = append(names, k)
		}
		sort.Strings(names)
		for _, k := range names {
			out = append(out, diff(path+"."+k, w[k], g[k])...)
		}
		return out
	case []any:
		g, ok := got.([]any)
		if !ok {
			return []difference{{path, want, got}}
		}
		var out []difference
		for i := 0; i < max(len(w), len(g)); i++ {
			var a, b any
			if i < len(w) {
				a = w[i]
			}
			if i < len(g) {
				b = g[i]
			}
			out = append(out, diff(fmt.Sprintf("%s[%d]", path, i), a, b)...)
		}
		return out
	}
	if fmt.Sprint(want) != fmt.Sprint(got) {
		return []difference{{path, want, got}}
	}
	return nil
}

// TestParityLists compares the lists, workspaces, environments and globals
// the same way. It needs POSTHERO_PARITY_DIR with api-collections.json,
// api-environments.json and api-workspaces.json (the API's lists),
// sync-collections.json (GET /collection?workspace=), sync-environments.json
// (POST /list/environment?workspace=) and sync-workspaces.json (GET
// /workspace), and env-api/ and env-sync/ with environments by uid and
// globals.json.
func TestParityLists(t *testing.T) {
	dir := os.Getenv("POSTHERO_PARITY_DIR")
	if dir == "" {
		t.Skip("POSTHERO_PARITY_DIR is not set")
	}
	same := func(what string, want, got any) {
		t.Helper()
		for _, d := range diff("", plain(t, want), plain(t, got)) {
			t.Errorf("%s: %s", what, d)
		}
	}
	var apiCols struct {
		Collections []postman.Summary `json:"collections"`
	}
	var syncCols []envelope[collectionDTO]
	if readAPI(t, filepath.Join(dir, "api-collections.json"), &apiCols) && readCapture(t, filepath.Join(dir, "sync-collections.json"), &syncCols) {
		same("collections", apiCols.Collections, toSummaries(syncCols))
	}
	var apiEnvs struct {
		Environments []postman.Summary `json:"environments"`
	}
	var syncEnvs struct {
		Data []envSummaryDTO `json:"data"`
	}
	if readAPI(t, filepath.Join(dir, "api-environments.json"), &apiEnvs) && readCapture(t, filepath.Join(dir, "sync-environments.json"), &syncEnvs) {
		// The API lists environments in no order of its own.
		byUID := func(l []postman.Summary) map[string]postman.Summary {
			m := map[string]postman.Summary{}
			for _, s := range l {
				m[s.UID] = s
			}
			return m
		}
		same("environments", byUID(apiEnvs.Environments), byUID(toEnvSummaries(syncEnvs.Data)))
	}
	var apiWS struct {
		Workspaces []postman.Workspace `json:"workspaces"`
	}
	var syncWS []envelope[workspaceDTO]
	if readAPI(t, filepath.Join(dir, "api-workspaces.json"), &apiWS) && readCapture(t, filepath.Join(dir, "sync-workspaces.json"), &syncWS) {
		byID := func(l []postman.Workspace) map[string]postman.Workspace {
			m := map[string]postman.Workspace{}
			for _, w := range l {
				m[w.ID] = w
			}
			return m
		}
		same("workspaces", byID(apiWS.Workspaces), byID(toWorkspaces(syncWS)))
	}
	envs, _ := filepath.Glob(filepath.Join(dir, "env-api", "*.json"))
	for _, f := range envs {
		name := filepath.Base(f)
		if name == "globals.json" {
			var api struct {
				Values []postman.EnvValue `json:"values"`
			}
			var sync envelope[globalsDTO]
			if readAPI(t, f, &api) && readCapture(t, filepath.Join(dir, "env-sync", name), &sync) {
				same("globals", api.Values, envValues(sync.Data.Values))
			}
			continue
		}
		var api struct {
			Environment *postman.Environment `json:"environment"`
		}
		var sync envelope[*environmentDTO]
		if readAPI(t, f, &api) && readCapture(t, filepath.Join(dir, "env-sync", name), &sync) && sync.Data != nil {
			same("environment "+name, api.Environment, toEnvironment(sync.Data))
		}
	}
}

// readAPI reads an answer of the Postman API.
func readAPI(t *testing.T, path string, v any) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		t.Logf("skipped: %v", err)
		return false
	}
	if err := json.Unmarshal(data, v); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	return true
}

// readCapture reads the body of a captured sync service answer ([{body,
// statusCode}]).
func readCapture(t *testing.T, path string, v any) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		t.Logf("skipped: %v", err)
		return false
	}
	var ack []struct {
		Body json.RawMessage `json:"body"`
	}
	if err := json.Unmarshal(data, &ack); err != nil || len(ack) == 0 {
		t.Fatalf("%s: %v", path, err)
	}
	if err := decode(ack[0].Body, v); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	return true
}

// plain is a value as plain JSON values.
func plain(t *testing.T, v any) any {
	data, err := jsonx.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var out any
	_ = json.Unmarshal(data, &out)
	return out
}
