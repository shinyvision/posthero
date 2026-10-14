package postman

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// PackageProvider supplies CommonJS sources for pm.require. ID is the
// script's saved dependency ID, if present; name is its import specifier.
type PackageProvider interface {
	Package(ctx context.Context, collection, name, id string) (string, error)
}

// ExternalPackage identifies a registry import rather than a team package.
func ExternalPackage(name string) bool {
	return strings.HasPrefix(name, "npm:") || strings.HasPrefix(name, "jsr:")
}

// Package reads a team package using the public API. Registry bundles use
// Postman's desktop package service, which requires a browser session.
func (c *Client) Package(ctx context.Context, collection, name, id string) (string, error) {
	if ExternalPackage(name) || ExternalPackage(id) {
		return "", fmt.Errorf("pm.require(%q): npm/JSR packages need a Postman browser session", name)
	}
	if id == "" {
		cursor := ""
		seen := map[string]bool{}
		for {
			var page struct {
				Data []struct{ ID, Name string }
				Meta struct{ NextCursor string }
			}
			if err := c.do(ctx, http.MethodGet, "/packages?cursor="+url.QueryEscape(cursor), nil, &page); err != nil {
				return "", err
			}
			for _, p := range page.Data {
				if p.Name == name || p.Name == TeamPackageName(name) {
					id = p.ID
					break
				}
			}
			if id != "" || page.Meta.NextCursor == "" {
				break
			}
			cursor = page.Meta.NextCursor
			if seen[cursor] {
				return "", fmt.Errorf("Postman's package listing repeated a cursor")
			}
			seen[cursor] = true
		}
	}
	if id == "" {
		return "", fmt.Errorf("cannot find package %q in this Postman team", name)
	}
	var out struct {
		Script *string `json:"script"`
		Data   *struct {
			Script *string `json:"script"`
		} `json:"data"`
	}
	if err := c.do(ctx, http.MethodGet, "/packages/"+url.PathEscape(id), nil, &out); err != nil {
		return "", err
	}
	if out.Data != nil {
		out.Script = out.Data.Script
	}
	if out.Script == nil {
		return "", fmt.Errorf("Postman sent no script for package %q", name)
	}
	return *out.Script, nil
}

// TeamPackageName removes the current team's import namespace.
func TeamPackageName(name string) string {
	if strings.HasPrefix(name, "@") {
		if _, rest, ok := strings.Cut(name, "/"); ok {
			return rest
		}
	}
	return name
}
