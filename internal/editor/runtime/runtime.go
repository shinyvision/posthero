// Package runtime holds posthero's Vim runtime files (the filetype and
// syntax of its documents) and writes them where Neovim and Vim can find
// them.
package runtime

import (
	"embed"
	"io/fs"
	"os"
	"path/filepath"
)

//go:embed files
var files embed.FS

// Write puts the runtime files in dir (a directory for 'runtimepath') and
// returns it.
func Write(dir string) (string, error) {
	err := fs.WalkDir(files, "files", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := files.ReadFile(path)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel("files", path)
		dst := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
			return err
		}
		if old, err := os.ReadFile(dst); err == nil && string(old) == string(data) {
			return nil
		}
		return os.WriteFile(dst, data, 0o600)
	})
	return dir, err
}
