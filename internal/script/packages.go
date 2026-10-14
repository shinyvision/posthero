package script

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/dop251/goja"
	"github.com/shinyvision/posthero/internal/postman"
)

// PackageResolver fetches the source using the active Postman backend.
type PackageResolver func(ctx context.Context, name, id string) (string, error)

// RunScript retains saved package dependencies instead of flattening a
// script into text. Each script has its own CommonJS module instances.
func (s *Session) RunScript(script postman.Script, event string, location []string) error {
	var dependencies map[string]struct {
		ID string `json:"id"`
	}
	if raw := script.Extra["packages"]; len(raw) > 0 {
		if err := json.Unmarshal(raw, &dependencies); err != nil {
			return s.fail(fmt.Errorf("invalid script package dependencies: %w", err))
		}
	}
	s.packageIDs = map[string]string{}
	for name, dependency := range dependencies {
		s.packageIDs[name] = dependency.ID
	}
	defer func() { s.packageIDs = nil }()
	return s.Run(script.Exec.Text(), event, location)
}

func (s *Session) requirePackage(name string) goja.Value {
	if module := s.packages[name]; module != nil {
		return module.Get("exports")
	}
	if s.env.Package == nil {
		panic(s.rt.NewGoError(fmt.Errorf("pm.require(%q): connect to Postman to load packages", name)))
	}
	id := s.packageIDs[name]
	key := name + "\x00" + id
	source, ok := s.packageSources[key]
	if !ok {
		var err error
		source, err = s.env.Package(s.runCtx, name, id)
		if err != nil {
			panic(s.rt.NewGoError(fmt.Errorf("pm.require(%q): %w", name, err)))
		}
		s.packageSources[key] = source
	}
	program, err := compileScript(name+".js", "(function (module, exports, require, pm) {\n"+source+"\n})")
	if err != nil {
		panic(s.rt.NewGoError(err))
	}
	value, err := s.rt.RunProgram(program)
	if err != nil {
		panic(err)
	}
	fn, ok := goja.AssertFunction(value)
	if !ok {
		panic(s.rt.NewTypeError("package %s did not compile to a module", name))
	}
	module := s.rt.NewObject()
	exports := s.rt.NewObject()
	_ = module.Set("exports", exports)
	s.packages[name] = module // permit cyclic CommonJS imports
	if _, err = fn(goja.Undefined(), module, exports, s.rt.Get("require"), s.rt.Get("pm")); err != nil {
		delete(s.packages, name)
		panic(err)
	}
	return module.Get("exports")
}
