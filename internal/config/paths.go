// Package config holds what posthero keeps on disk: the login, the
// postherorc startup file (options, mappings, certificates), and state
// remembered between runs.
package config

import (
	"os"
	"path/filepath"
	"runtime"
)

// ConfigDir holds what people write or change on purpose: postherorc and the
// login. It is $XDG_CONFIG_HOME/posthero, usually ~/.config/posthero.
func ConfigDir() (string, error) { return xdgDir("XDG_CONFIG_HOME", ".config", "") }

// StateDir holds what posthero keeps on its own: where you were, current
// values of variables, tokens, cookies, drafts and the history. It is
// $XDG_STATE_HOME/posthero, usually ~/.local/state/posthero.
func StateDir() (string, error) { return xdgDir("XDG_STATE_HOME", ".local/state", "state") }

// CacheDir holds the workspaces as last fetched, so posthero starts at
// once: $XDG_CACHE_HOME/posthero.
func CacheDir() (string, error) {
	if d := os.Getenv("XDG_CACHE_HOME"); filepath.IsAbs(d) {
		return filepath.Join(d, "posthero"), nil
	}
	d, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "posthero"), nil
}

func xdgDir(env, unixDefault, macSub string) (string, error) {
	if d := os.Getenv(env); filepath.IsAbs(d) {
		return filepath.Join(d, "posthero"), nil
	}
	if runtime.GOOS == "windows" || runtime.GOOS == "darwin" {
		d, err := os.UserConfigDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(d, "posthero", macSub), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, unixDefault, "posthero"), nil
}

// configPath returns a file in ConfigDir, creating the directory.
func configPath(name string) (string, error) { return inDir(ConfigDir, name) }

// StatePath returns a file in StateDir, creating the directory.
func StatePath(name string) (string, error) { return inDir(StateDir, name) }

func inDir(dir func() (string, error), name string) (string, error) {
	d, err := dir()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(d, 0o700); err != nil {
		return "", err
	}
	return filepath.Join(d, name), nil
}

// writeFile writes atomically, so a crash never leaves half a file, and
// two writers never mix their bytes.
func writeFile(path string, data []byte, perm os.FileMode) error {
	path = followLinks(path) // write where a symlink points, keeping the link
	f, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	_, err = f.Write(data)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Chmod(tmp, perm)
	}
	if err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, path)
}

// followLinks is where a chain of symlinks ends, even when the file there
// doesn't exist yet.
func followLinks(path string) string {
	for range 40 {
		fi, err := os.Lstat(path)
		if err != nil || fi.Mode()&os.ModeSymlink == 0 {
			return path
		}
		target, err := os.Readlink(path)
		if err != nil {
			return path
		}
		if !filepath.IsAbs(target) {
			target = filepath.Join(filepath.Dir(path), target)
		}
		path = target
	}
	return path
}
