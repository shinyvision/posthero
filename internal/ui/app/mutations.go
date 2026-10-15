package app

import (
	"context"
	"fmt"

	"github.com/shinyvision/posthero/internal/postman"
	"github.com/shinyvision/posthero/internal/store"
)

// Capture on the UI thread, before a dialog or background command can
// outlive the version the user acted on.
func mutationSnapshot(c *store.Col) *store.Col {
	if c.Path != "" {
		return c
	}
	s := *c
	s.Data = c.Data.Clone()
	return &s
}

func mutationConflict() error {
	return fmt.Errorf("changed in Postman since you last saw it; refresh and review the change before trying again")
}

func checkName(before, wanted, now string) error {
	if now != before && now != wanted {
		return mutationConflict()
	}
	return nil
}

func latestCollection(ctx context.Context, api postman.API, uid string) (*postman.Collection, error) {
	now, err := api.Collection(ctx, uid)
	if err != nil {
		return nil, fmt.Errorf("couldn't check the latest collection; nothing was changed: %w", err)
	}
	return now, nil
}

func checkCollection(ctx context.Context, api postman.API, uid string, before *postman.Collection) error {
	now, err := latestCollection(ctx, api, uid)
	if err != nil {
		return err
	}
	if before == nil || !sameJSON(before, now) {
		return mutationConflict()
	}
	return nil
}

func checkEnvironment(ctx context.Context, api postman.API, uid string, before *postman.Environment) error {
	now, err := api.Environment(ctx, uid)
	if err != nil {
		return err
	}
	if before == nil || !sameJSON(before, now) {
		return mutationConflict()
	}
	return nil
}

// Destructive changes check the whole affected subtree, including examples,
// and its location. A collection timestamp alone misses edits to children.
func checkItem(before, now *postman.Collection, id string) error {
	b, beforeParents := before.Find(id)
	n, nowParents := now.Find(id)
	bp, bn := placeOf(before, id)
	np, nn := placeOf(now, id)
	if b == nil || n == nil || !sameJSON(b, n) || bp != np || bn != nn {
		return mutationConflict()
	}
	if len(beforeParents) != len(nowParents) {
		return mutationConflict()
	}
	for i, parent := range beforeParents {
		if parent.ID != nowParents[i].ID {
			return mutationConflict()
		}
	}
	return nil
}

// Layout protects the destination and the anchor of a move without blocking
// it just because an unrelated request's body changed.
func collectionLayout(c *postman.Collection) []string {
	var out []string
	c.Walk(func(it *postman.Item, parents []*postman.Item) bool {
		parent := ""
		if len(parents) > 0 {
			parent = parents[len(parents)-1].ID
		}
		out = append(out, parent+":"+it.ID)
		return true
	})
	return out
}

func checkTransfer(ctx context.Context, api postman.API, src, dst *store.Col, id string, copy bool) error {
	now, err := latestCollection(ctx, api, src.Summary.UID)
	if err != nil {
		return err
	}
	if !copy {
		if err := checkItem(src.Data, now, id); err != nil {
			return err
		}
	}
	if src.Summary.ID != dst.Summary.ID {
		now, err = latestCollection(ctx, api, dst.Summary.UID)
		if err != nil {
			return err
		}
	}
	if !sameJSON(collectionLayout(dst.Data), collectionLayout(now)) {
		return mutationConflict()
	}
	return nil
}
