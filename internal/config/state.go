package config

import (
	"encoding/json"
	"os"

	"github.com/shinyvision/posthero/internal/jsonx"
)

// State is what posthero remembers between runs, so it opens where it was
// left.
type State struct {
	// Workspace is the open workspace's id, and WorkspaceName its name.
	Workspace     string `json:"workspace,omitempty"`
	WorkspaceName string `json:"workspaceName,omitempty"`
	// Open is the request in the editor ("collection-id/item-id").
	Open string `json:"open,omitempty"`
	// Env is the environment picked per collection (collection id → env id).
	Env map[string]string `json:"env,omitempty"`
	// Expanded are the tree's open collections and folders.
	Expanded []string `json:"expanded,omitempty"`
	// Cursor is the tree node under the cursor.
	Cursor string `json:"cursor,omitempty"`
	// Recent are the requests opened last, newest first.
	Recent []string `json:"recent,omitempty"`
	// TreeWidth is the tree's width in cells; EditorShare the editor's share
	// of the height on the right, in percent. 0 is the default.
	TreeWidth   int  `json:"treeWidth,omitempty"`
	EditorShare int  `json:"editorShare,omitempty"`
	TreeHidden  bool `json:"treeHidden,omitempty"`
	// Local are the workspaces in local mode (by id): posthero changes a
	// copy on this machine, which :push sends to Postman.
	Local map[string]bool `json:"local,omitempty"`
}

// LoadState reads the state; a missing or broken file gives an empty one.
func LoadState() State {
	var s State
	path, err := StatePath("state.json")
	if err != nil {
		return s
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return s
	}
	_ = json.Unmarshal(data, &s)
	return s
}

// SaveState writes the state.
func SaveState(s State) error {
	path, err := StatePath("state.json")
	if err != nil {
		return err
	}
	data, err := jsonx.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return writeFile(path, append(data, '\n'), 0o600)
}

// AddRecent puts id first in the recent requests, keeping 100.
func (s *State) AddRecent(id string) {
	out := []string{id}
	for _, k := range s.Recent {
		if k != id && len(out) < 100 {
			out = append(out, k)
		}
	}
	s.Recent = out
}
