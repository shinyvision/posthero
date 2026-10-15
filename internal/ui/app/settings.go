package app

import (
	"context"
	"fmt"

	tea "charm.land/bubbletea/v2"

	"github.com/shinyvision/posthero/internal/postman"
	"github.com/shinyvision/posthero/internal/store"
	"github.com/shinyvision/posthero/internal/ui/editmodal"
)

type ownSettings struct {
	description string
	auth        *postman.Auth
	events      []postman.Event
}

func collectionSettings(c *postman.Collection) ownSettings {
	return ownSettings{descOf(c.Info.Description), c.Auth, c.Event}
}

func folderSettings(f *postman.Item) ownSettings {
	return ownSettings{descOf(f.Description), f.Auth, f.Event}
}

// settingsPatch applies the editor's changes to the latest settings. Auth
// is one field; scripts are independent per event, including their metadata.
// If both sides changed the same field, stop before writing anything.
func settingsPatch(base, mine, now ownSettings) (postman.ItemPatch, error) {
	var p postman.ItemPatch
	conflict := func(field string) error {
		return fmt.Errorf("the %s changed in Postman while you were editing; nothing was saved. Reopen the settings to reconcile your changes", field)
	}
	if mine.description != base.description {
		if now.description != base.description && now.description != mine.description {
			return p, conflict("description")
		}
		p.Description = &mine.description
	}
	if !sameJSON(mine.auth, base.auth) {
		if !sameJSON(now.auth, base.auth) && !sameJSON(now.auth, mine.auth) {
			return p, conflict("auth")
		}
		p.Auth = &mine.auth
	}
	byEvent := func(events []postman.Event, listen string) []postman.Event {
		var out []postman.Event
		for _, e := range events {
			if e.Listen == listen {
				out = append(out, e)
			}
		}
		return out
	}
	// Saving a newly added script assigns it an ID. That identity change
	// isn't an edit to its content and must not conflict with a later :w.
	sameEvents := func(a, b []postman.Event) bool {
		a, b = append([]postman.Event(nil), a...), append([]postman.Event(nil), b...)
		for i := range a {
			a[i].Script.ID = ""
		}
		for i := range b {
			b[i].Script.ID = ""
		}
		return sameJSON(a, b)
	}
	replacement := func(listen string) []postman.Event {
		after, current := byEvent(mine.events, listen), byEvent(now.events, listen)
		for i := range after {
			if after[i].Script.ID == "" && i < len(current) {
				after[i].Script.ID = current[i].Script.ID
			}
		}
		return after
	}
	changed := map[string]bool{}
	for _, e := range append(append([]postman.Event(nil), base.events...), mine.events...) {
		before, after := byEvent(base.events, e.Listen), byEvent(mine.events, e.Listen)
		if sameEvents(before, after) {
			continue
		}
		current := byEvent(now.events, e.Listen)
		if !sameEvents(current, before) && !sameEvents(current, after) {
			return p, conflict(e.Listen + " script")
		}
		changed[e.Listen] = true
	}
	if len(changed) > 0 {
		var events []postman.Event
		inserted := map[string]bool{}
		for _, e := range now.events {
			if !changed[e.Listen] {
				events = append(events, e)
			} else if !inserted[e.Listen] {
				events = append(events, replacement(e.Listen)...)
				inserted[e.Listen] = true
			}
		}
		for _, e := range mine.events {
			if changed[e.Listen] && !inserted[e.Listen] {
				events = append(events, replacement(e.Listen)...)
				inserted[e.Listen] = true
			}
		}
		p.Event = &events
	}
	return p, nil
}

func (m *Model) saveFolderSettings(c *store.Col, base, mine *postman.Item, u *undoEntry) tea.Cmd {
	api, uid, colID, to := m.api, c.Summary.UID, c.Summary.ID, m.saveTarget()
	base, mine = base.Clone(), mine.Clone()
	return m.run("saving "+mine.Name, func(ctx context.Context) tea.Msg {
		now, err := api.Collection(ctx, uid)
		if err != nil {
			return editmodal.ResultMsg{Err: fmt.Errorf("couldn't read the latest collection; nothing was saved: %w", err)}
		}
		folder, _ := now.Find(mine.ID)
		if folder == nil || !folder.Folder {
			return editmodal.ResultMsg{Err: fmt.Errorf("%s isn't in the collection any more", mine.Name)}
		}
		patch, err := settingsPatch(folderSettings(base), folderSettings(mine), folderSettings(folder))
		if err != nil {
			return editmodal.ResultMsg{Err: err}
		}
		if patch != (postman.ItemPatch{}) {
			if err := api.PatchFolder(ctx, uid, postman.Ref(folder.ID, folder.UID), patch); err != nil {
				return editmodal.ResultMsg{Err: err}
			}
		}
		col, err := api.Collection(ctx, uid)
		if u != nil && err == nil {
			// Undo only the fields this save changed, against the version
			// actually saved. Keep teammate edits incorporated by the merge,
			// and detect later edits even if Watch has refreshed the tree.
			before := folder.Clone()
			after, _ := col.Find(folder.ID)
			if after != nil {
				after = after.Clone()
				undo := *u
				undo.kind = undoRestore
				undo.restore = func() tea.Cmd { return m.saveFolderSettings(c, after, before, nil) }
				u = &undo
			}
		}
		return editmodal.ResultMsg{Then: changedMsg{colID: colID, col: col, err: err, undo: u, note: "saved " + folder.Name + " " + to}}
	})
}
