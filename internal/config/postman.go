package config

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/shinyvision/posthero/internal/postman"
	"github.com/shinyvision/posthero/internal/postman/syncapi"
)

// PostmanClient keeps the installation identity independently of login.
// Only a configured browser session needs an online version check.
func PostmanClient(ctx context.Context, browser bool) (syncapi.Options, string, error) {
	return postmanClient(ctx, browser, syncapi.LatestVersion)
}

func postmanClient(ctx context.Context, browser bool, latest func(context.Context, string) (string, error)) (syncapi.Options, string, error) {
	opts := syncapi.Options{Version: syncapi.AppVersion}
	id, err := installationID()
	if err != nil {
		return opts, "", fmt.Errorf("couldn't keep the Postman installation ID: %w", err)
	}
	opts.AppID = id
	path, cacheErr := inDir(CacheDir, "postman-version")
	if cacheErr == nil {
		if raw, err := os.ReadFile(path); err == nil && syncapi.ValidAppVersion(strings.TrimSpace(string(raw))) {
			opts.Version = strings.TrimSpace(string(raw))
		}
	}
	if !browser {
		return opts, "", nil
	}
	version, err := latest(ctx, opts.Version)
	if err != nil {
		return opts, "couldn't check Postman's version; using " + opts.Version, nil
	}
	if !syncapi.ValidAppVersion(version) {
		return opts, "Postman's version check returned an invalid version; using " + opts.Version, nil
	}
	opts.Version = version
	if cacheErr == nil {
		cacheErr = writeFile(path, []byte(version+"\n"), 0600)
	}
	if cacheErr != nil {
		return opts, "couldn't cache Postman's version: " + cacheErr.Error(), nil
	}
	return opts, "", nil
}

var installationIDPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

func installationID() (string, error) {
	path, err := configPath("postman-installation-id")
	if err != nil {
		return "", err
	}
	read := func() (string, error) {
		raw, err := os.ReadFile(path)
		if err != nil {
			return "", err
		}
		id := strings.TrimSpace(string(raw))
		if !installationIDPattern.MatchString(id) {
			return "", fmt.Errorf("invalid installation ID in %s", path)
		}
		return id, nil
	}
	if id, err := read(); !errors.Is(err, os.ErrNotExist) {
		return id, err
	}
	// Publish a complete file without replacing another process's ID. Two
	// simultaneous first launches must choose the same installation.
	f, err := os.CreateTemp(filepath.Dir(path), ".postman-installation-*")
	if err != nil {
		return "", err
	}
	defer os.Remove(f.Name())
	id := postman.NewID()
	_, err = f.WriteString(id + "\n")
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return "", err
	}
	if err = os.Link(f.Name(), path); err != nil && !errors.Is(err, os.ErrExist) {
		return "", err
	}
	return read()
}
