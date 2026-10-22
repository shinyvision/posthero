package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"

	"github.com/shinyvision/posthero/internal/postman"
)

// env lists the environments, shows one, or changes a variable's shared
// (initial) value in one.
func env(ctx context.Context, e *Env, args []string) error {
	since := ""
	args, err := e.flags("env", args, func(fs *flag.FlagSet) { fs.StringVar(&since, "since", "", "") })
	if err != nil {
		return err
	}
	if len(args) == 0 {
		ws, err := e.workspace(ctx)
		if err != nil {
			return err
		}
		list, err := e.API.Environments(ctx, ws.ID)
		if err != nil {
			return err
		}
		for _, s := range list {
			fmt.Fprintf(e.Out, "%s\t%s\t%s\n", s.Name, s.UID, s.UpdatedAt)
		}
		return nil
	}
	switch {
	case args[0] == "cat" && len(args) == 2:
		_, data, err := e.findEnv(ctx, false, args[1])
		if err != nil {
			return err
		}
		for _, v := range data.Values {
			value := string(v.Value)
			if v.Secret() && value != "" {
				value = "••••••"
			}
			line := v.Key + " = " + value
			if !v.On() {
				line = "# " + line
			}
			fmt.Fprintln(e.Out, line)
		}
		return nil
	case args[0] == "set" && len(args) == 4:
		sum, data, err := e.findEnv(ctx, true, args[1])
		if err != nil {
			return err
		}
		if since != "" && data.UpdatedAt > since {
			return fmt.Errorf("not saved: %s changed in Postman at %s, after %s", data.Name, data.UpdatedAt, since)
		}
		key, value := args[2], args[3]
		old, found := "", false
		for i := range data.Values {
			if data.Values[i].Key == key {
				old, found = string(data.Values[i].Value), true
				data.Values[i].Value = postman.Str(value)
			}
		}
		if !found {
			data.Values = append(data.Values, postman.EnvValue{Key: key, Value: postman.Str(value), Type: "default"})
		} else if old == value {
			fmt.Fprintln(e.Out, "no changes to save")
			return nil
		}
		if err := e.API.PutEnvironment(ctx, sum.UID, data); err != nil {
			return err
		}
		if found {
			fmt.Fprintf(e.Out, "saved %s: %s = %s (was %s)\n", data.Name, key, value, old)
		} else {
			fmt.Fprintf(e.Out, "saved %s: %s = %s (new)\n", data.Name, key, value)
		}
		return nil
	}
	return errors.New("env: give nothing (to list them), cat NAME, or set NAME KEY VALUE")
}

// findEnv finds an environment by name or id and fetches it.
func (e *Env) findEnv(ctx context.Context, write bool, name string) (postman.Summary, *postman.Environment, error) {
	ws, err := e.workspace(ctx)
	if write && err == nil {
		ws, err = e.writable(ctx)
	}
	if err != nil {
		return postman.Summary{}, nil, err
	}
	list, err := e.API.Environments(ctx, ws.ID)
	if err != nil {
		return postman.Summary{}, nil, err
	}
	sum, err := pick(list, name, ws.Name, func(s postman.Summary) (string, []string) { return s.Name, []string{s.ID, s.UID} })
	if err != nil {
		return postman.Summary{}, nil, err
	}
	data, err := e.API.Environment(ctx, sum.UID)
	return sum, data, err
}
