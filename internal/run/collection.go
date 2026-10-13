package run

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/shinyvision/posthero/internal/jsonx"
	"github.com/shinyvision/posthero/internal/postman"
	"github.com/shinyvision/posthero/internal/script"
	"github.com/shinyvision/posthero/internal/vars"
)

// RunOptions shape a collection run.
type RunOptions struct {
	// Iterations runs everything this many times (at least once; with data,
	// once per row unless set).
	Iterations int
	// Data are the rows of a data file, one per iteration.
	Data []map[string]any
	// Delay waits between requests.
	Delay time.Duration
	// Bail stops at the first failing test or request error.
	Bail bool
	// Only runs just these request ids (in collection order); empty runs
	// all in Folder.
	Only []string
	// Folder runs only what is in this folder (id); empty runs the whole
	// collection.
	Folder string
	// NoScripts sends without running scripts.
	NoScripts bool
	Options
}

// Step is one request of a run as it finishes.
type Step struct {
	Iteration int
	// Index is the request's position in the run's order.
	Index int
	*Execution
}

// Summary is how a run went.
type Summary struct {
	Steps                     []Step
	Requests, Failed, Skipped int
	TestsPassed, TestsFailed  int
	Duration                  time.Duration
	Stopped                   bool
	Errors                    int
	// Loop says the run was stopped because setNextRequest kept going
	// round.
	Loop bool
}

// Scopes makes the variables for an iteration: data is the iteration's
// row. The local scope lasts the whole run.
type Scopes func(data map[string]any) *vars.Set

// RunCollection runs requests in order, calling step after each.
func (r *Runner) RunCollection(ctx context.Context, c *postman.Collection, o RunOptions, scopes Scopes, step func(Step)) Summary {
	start := time.Now()
	order := Order(c, o.Folder, o.Only)
	iterations := o.Iterations
	if iterations <= 0 {
		iterations = max(1, len(o.Data))
	}
	var sum Summary
	for it := 0; it < iterations; it++ {
		var row map[string]any
		if len(o.Data) > 0 {
			row = o.Data[it%len(o.Data)]
		}
		v := scopes(row)
		sent := 0
		for i := 0; i < len(order); i++ {
			if sent++; sent > 100*len(order) {
				// setNextRequest going round in circles.
				sum.Stopped = true
				sum.Errors++
				sum.Loop = true
				sum.Duration = time.Since(start)
				return sum
			}
			if ctx.Err() != nil {
				sum.Stopped = true
				sum.Duration = time.Since(start)
				return sum
			}
			if (sum.Requests > 0 || it > 0) && o.Delay > 0 {
				select {
				case <-time.After(o.Delay):
				case <-ctx.Done():
					sum.Stopped = true
					sum.Duration = time.Since(start)
					return sum
				}
			}
			opts := o.Options
			opts.Info = script.Info{Iteration: it, IterationCount: iterations}
			opts.NoScripts = o.NoScripts
			ex := r.Execute(ctx, order[i], v, opts)
			s := Step{Iteration: it, Index: i, Execution: ex}
			sum.Steps = append(sum.Steps, s)
			sum.Requests++
			switch {
			case ex.Skipped:
				sum.Skipped++
			case ex.Err != nil:
				sum.Errors++
			}
			failed := ex.Err != nil
			for _, t := range ex.Tests() {
				switch {
				case t.Skipped:
				case t.Passed:
					sum.TestsPassed++
				default:
					sum.TestsFailed++
					failed = true
				}
			}
			if len(ex.Scripts.Errors) > 0 {
				failed = true
			}
			if failed {
				sum.Failed++
			}
			if step != nil {
				step(s)
			}
			if failed && o.Bail {
				sum.Stopped = true
				sum.Duration = time.Since(start)
				return sum
			}
			if next := ex.Scripts.Next; next != nil {
				if *next == "" {
					sum.Stopped = true
					sum.Duration = time.Since(start)
					return sum
				}
				j := find(order, *next)
				if j < 0 {
					ex.Err = fmt.Errorf("setNextRequest(%q): no request by that name in this run", *next)
					sum.Errors++
					continue
				}
				i = j - 1
			}
		}
	}
	sum.Duration = time.Since(start)
	return sum
}

// Order is the requests a run sends, in the order Postman sends them:
// depth first, as they are listed.
func Order(c *postman.Collection, folder string, only []string) []Target {
	pick := map[string]bool{}
	for _, id := range only {
		pick[id] = true
	}
	var out []Target
	c.Walk(func(it *postman.Item, parents []*postman.Item) bool {
		if it.Folder {
			return true
		}
		if folder != "" {
			in := false
			for _, p := range parents {
				in = in || p.ID == folder
			}
			if !in {
				return true
			}
		}
		if len(pick) > 0 && !pick[it.ID] {
			return true
		}
		out = append(out, Target{Collection: c, Item: it, Parents: append([]*postman.Item(nil), parents...)})
		return true
	})
	return out
}

func find(order []Target, name string) int {
	for i, t := range order {
		if t.Item.ID == name || t.Item.UID == name {
			return i
		}
	}
	for i, t := range order {
		if t.Item.Name == name {
			return i
		}
	}
	return -1
}

// LoadData reads a data file for a run: a JSON array of objects, or a CSV
// file with a header row.
func LoadData(path string) ([]map[string]any, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if strings.EqualFold(filepath.Ext(path), ".json") || strings.HasPrefix(strings.TrimSpace(string(data)), "[") {
		var rows []map[string]any
		if err := json.Unmarshal(data, &rows); err != nil {
			return nil, fmt.Errorf("%s isn't a JSON list of objects: %w", path, err)
		}
		return rows, nil
	}
	r := csv.NewReader(strings.NewReader(strings.TrimPrefix(string(data), "\uFEFF")))
	r.FieldsPerRecord = -1
	records, err := r.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if len(records) == 0 {
		return nil, nil
	}
	head := records[0]
	var rows []map[string]any
	for _, rec := range records[1:] {
		row := map[string]any{}
		for i, h := range head {
			if i < len(rec) {
				row[h] = rec[i]
			}
		}
		rows = append(rows, row)
	}
	return rows, nil
}

// DataScope turns a data row into the data scope.
func DataScope(row map[string]any) *vars.Scope {
	s := &vars.Scope{Kind: vars.Data, Name: "data"}
	for k, v := range row {
		text := ""
		switch x := v.(type) {
		case string:
			text = x
		case nil:
		default:
			b, _ := jsonx.Marshal(x)
			text = string(b)
		}
		var typed any
		if _, isStr := v.(string); !isStr {
			typed = v
		}
		s.Vars = append(s.Vars, vars.Var{Key: k, Value: text, Typed: typed})
	}
	return s
}
