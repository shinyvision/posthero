package store

import (
	"context"
	"fmt"
	"sync"

	"github.com/shinyvision/posthero/internal/postman"
)

// fetchChanges reconciles notifications against authoritative membership.
// Notifications are invalidations, not replacements: a late delete must not
// remove a recreated entity, and a moved collection may still be readable in
// another workspace. Only invalidated entities are fetched; untouched lists,
// open collections, and globals are left alone.
func fetchChanges(ctx context.Context, p Plan) (Fetched, error) {
	f := Fetched{versions: p.versions}
	wanted := map[string]map[string]bool{}
	for _, ch := range p.Changes {
		if ch.Action == "reconnect" || (ch.Kind != "collection" && ch.Kind != "environment") || ch.UID == "" {
			p.Changes = nil
			return Fetch(ctx, p)
		}
		if wanted[ch.Kind] == nil {
			wanted[ch.Kind] = map[string]bool{}
		}
		wanted[ch.Kind][ch.UID] = true
	}
	var mu sync.Mutex
	var groups sync.WaitGroup
	// The shared limit applies to environments as well as collections.
	slots := make(chan struct{}, 6)
	for kind, ids := range wanted {
		groups.Add(1)
		go func() {
			defer groups.Done()
			var list []postman.Summary
			var err error
			if kind == "collection" {
				list, err = p.API.Collections(ctx, p.Workspace)
			} else {
				list, err = p.API.Environments(ctx, p.Workspace)
			}
			if err != nil {
				mu.Lock()
				f.Errors = append(f.Errors, fmt.Errorf("%s list: %w", kind, err))
				mu.Unlock()
				return
			}
			present := map[string]bool{}
			var workers sync.WaitGroup
			for _, sum := range list {
				present[sum.ID], present[sum.UID] = true, true
				if !ids[sum.ID] && !ids[sum.UID] {
					continue
				}
				select {
				case slots <- struct{}{}:
				case <-ctx.Done():
					workers.Wait()
					return
				}
				workers.Add(1)
				go func() {
					defer workers.Done()
					defer func() { <-slots }()
					var col *postman.Collection
					var env *postman.Environment
					var err error
					if kind == "collection" {
						col, err = p.API.Collection(ctx, sum.UID)
					} else {
						env, err = p.API.Environment(ctx, sum.UID)
					}
					mu.Lock()
					defer mu.Unlock()
					if err != nil {
						// A deletion racing the list will be reconciled on retry.
						f.Errors = append(f.Errors, fmt.Errorf("%s: %w", sum.Name, err))
						return
					}
					if kind == "collection" {
						f.Cols = append(f.Cols, &Col{Summary: sum, Data: col})
					} else {
						f.Envs = append(f.Envs, &Env{Summary: sum, Data: env})
					}
				}()
			}
			workers.Wait()
			mu.Lock()
			defer mu.Unlock()
			for ref := range ids {
				if present[ref] {
					continue
				}
				_, id := postman.SplitUID(ref)
				if kind == "collection" {
					f.Gone = append(f.Gone, id)
				} else {
					f.GoneEnvs = append(f.GoneEnvs, id)
				}
			}
		}()
	}
	groups.Wait()
	return f, ctx.Err()
}
