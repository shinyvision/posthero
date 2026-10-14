package script

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shinyvision/posthero/internal/postman"
)

func TestPackageModulesAndSavedDependencies(t *testing.T) {
	f := newFixture(t)
	calls := map[string]int{}
	f.env.Package = func(ctx context.Context, name, id string) (string, error) {
		calls[id]++
		if name != "@team/helper" {
			t.Fatalf("unexpected import: %q", name)
		}
		return fmt.Sprintf(`let count = 0; module.exports = { next: () => ++count, id: %q, event: pm.info.eventName, upper: require('lodash').toUpper('ok') };`, id), nil
	}
	s, err := NewEngine().NewSession(context.Background(), f.env)
	if err != nil {
		t.Fatal(err)
	}
	for _, step := range []struct{ id, event string }{{"one", "prerequest"}, {"two", "prerequest"}, {"one", "test"}} {
		dependencies, _ := json.Marshal(map[string]any{"@team/helper": map[string]string{"id": step.id}})
		code := fmt.Sprintf(`const a = pm.require('@team/helper'); const b = pm.require('@team/helper');
pm.test('package module', () => {
  pm.expect(a).to.equal(b); pm.expect(a.next()).to.equal(1); pm.expect(b.next()).to.equal(2);
  pm.expect(a.id).to.equal(%q); pm.expect(a.event).to.equal(%q); pm.expect(a.upper).to.equal('OK');
});`, step.id, step.event)
		script := postman.Script{Exec: postman.Lines{code}, Extra: map[string]json.RawMessage{"packages": dependencies}}
		if err := s.RunScript(script, step.event, nil); err != nil {
			t.Fatal(err)
		}
	}
	if r := s.Result(); r.Passed() != 3 || r.Failed() != 0 {
		t.Fatalf("%+v", r)
	}
	if calls["one"] != 1 || calls["two"] != 1 {
		t.Fatalf("sources fetched more than once: %v", calls)
	}
}

func TestPackageErrorsAndCancellation(t *testing.T) {
	for _, mode := range []string{"unavailable", "forbidden", "syntax", "exception", "fetch timeout", "execution timeout"} {
		t.Run(mode, func(t *testing.T) {
			f := newFixture(t)
			f.env.Timeout = 100 * time.Millisecond
			if mode != "unavailable" {
				f.env.Package = func(ctx context.Context, name, id string) (string, error) {
					switch mode {
					case "forbidden":
						return "", fmt.Errorf("forbidden")
					case "syntax":
						return "const = broken", nil
					case "exception":
						return "throw Error('broken package')", nil
					case "fetch timeout":
						<-ctx.Done()
						return "", ctx.Err()
					default:
						return "while(true) {}", nil
					}
				}
			}
			start := time.Now()
			r, err := run(t, f, "prerequest", `pm.require('npm:example@1.0.0')`)
			if err == nil || len(r.Errors) != 1 {
				t.Fatalf("error=%v result=%+v", err, r)
			}
			if time.Since(start) > 2*time.Second {
				t.Fatal("package loading escaped the script deadline")
			}
		})
	}
}

func TestPackagesCannotRequireHostFiles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "host.js")
	if err := os.WriteFile(path, []byte(`module.exports = 'host file'`), 0600); err != nil {
		t.Fatal(err)
	}
	f := newFixture(t)
	f.env.Package = func(context.Context, string, string) (string, error) {
		return fmt.Sprintf(`module.exports = require(%q);`, path), nil
	}
	_, err := run(t, f, "prerequest", `pm.require('npm:example@1.0.0')`)
	if err == nil || !strings.Contains(err.Error(), "module") {
		t.Fatalf("expected missing module, got %v", err)
	}
}
