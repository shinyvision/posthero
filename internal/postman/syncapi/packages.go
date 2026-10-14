package syncapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	pathpkg "path"
	"strings"

	"github.com/shinyvision/posthero/internal/postman"
)

var _ postman.PackageProvider = (*Client)(nil)

// Package follows the desktop app's package-library proxy and Packman
// bundle routes. Saved dependency IDs take precedence over name lookup.
func (c *Client) Package(ctx context.Context, collection, name, id string) (string, error) {
	if postman.ExternalPackage(name) || postman.ExternalPackage(id) {
		if !postman.ExternalPackage(id) {
			id = name
		}
		return c.externalPackage(ctx, id)
	}
	if id == "" {
		cursor := ""
		seen := map[string]bool{}
		for {
			var page struct {
				Data []struct{ ID, Name string }
				Meta struct{ NextCursor string }
			}
			if err := c.packageRead(ctx, "", "/packages?isDeleted=false&cursor="+url.QueryEscape(cursor), &page); err != nil {
				return "", err
			}
			for _, p := range page.Data {
				if p.Name == name || p.Name == postman.TeamPackageName(name) {
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
	var metadata envelope[struct{ IndexScript struct{ ID string } }]
	path := "/packages/" + url.PathEscape(id)
	if err := c.packageRead(ctx, "", path, &metadata); err != nil {
		return "", err
	}
	if metadata.Data.IndexScript.ID == "" {
		return "", fmt.Errorf("Postman sent no script ID for package %q", name)
	}
	var script envelope[struct{ Contents struct{ Src *string } }]
	if err := c.packageRead(ctx, collection, path+"/scripts/"+url.PathEscape(metadata.Data.IndexScript.ID)+"?include=contents", &script); err != nil {
		return "", err
	}
	if script.Data.Contents.Src == nil {
		return "", fmt.Errorf("Postman sent no source for package %q", name)
	}
	return *script.Data.Contents.Src, nil
}

func (c *Client) packageRead(ctx context.Context, collection, path string, out any) error {
	return c.proxyRead(ctx, collection, map[string]any{
		"service": "package-library-service", "method": "get", "path": path, "body": map[string]any{},
	}, out)
}

// proxyRead is only for package GETs and the feature-list read below.
func (c *Client) proxyRead(ctx context.Context, collection string, data, out any) error {
	token, err := c.sessionToken(ctx, "")
	if err != nil {
		return err
	}
	for attempt := 0; ; attempt++ {
		headers := map[string]string{"x-access-token": token, "user-agent": c.agent, "x-app-version": c.opts.Version}
		if collection != "" {
			headers["x-src-collection"] = collection
		}
		resp, err := c.sock.Request(ctx, "post", "/ws/proxy", data, headers)
		// These particular proxy calls only read. Other /ws/proxy calls may
		// mutate data, so the socket deliberately does not replay them.
		if attempt == 0 && errors.Is(err, ErrDisconnected) && ctx.Err() == nil {
			continue
		}
		if err != nil {
			return err
		}
		if resp.Status >= 400 {
			err = serviceError(resp)
			if attempt == 0 && postman.IsUnauthorized(err) {
				next, renewErr := c.sessionToken(ctx, token)
				if renewErr != nil {
					return renewErr
				}
				if next != token {
					token = next
					continue
				}
			}
			return err
		}
		return decode(resp.Body, out)
	}
}

func (c *Client) externalPackage(ctx context.Context, identifier string) (string, error) {
	registry, name, _ := strings.Cut(identifier, ":")
	if (registry != "npm" && registry != "jsr") || name == "" {
		return "", fmt.Errorf("invalid package identifier %q", identifier)
	}
	token, err := c.sessionToken(ctx, "")
	if err != nil {
		return "", err
	}
	var handshake struct{ Token string }
	for attempt := 0; ; attempt++ {
		body, _, err := c.packageHTTP(ctx, strings.TrimRight(c.opts.RuntimeAgentURL, "/")+"/handshake/token?source=packman", "x-access-token", token)
		if attempt == 0 && postman.IsUnauthorized(err) {
			next, renewErr := c.sessionToken(ctx, token)
			if renewErr != nil {
				return "", renewErr
			}
			if next != token {
				token = next
				continue
			}
		}
		if err != nil {
			return "", err
		}
		if err := json.Unmarshal(body, &handshake); err != nil {
			return "", fmt.Errorf("invalid Postman package token response: %w", err)
		}
		break
	}
	if handshake.Token == "" {
		return "", fmt.Errorf("Postman sent no package token")
	}
	if err := c.checkPackageAccess(ctx, handshake.Token, identifier); err != nil {
		return "", err
	}
	body, header, err := c.packageHTTP(ctx, strings.TrimRight(c.opts.PackageURL, "/")+"/v1/bundle/"+registry+"/"+url.PathEscape(name), "Authorization", "Bearer "+handshake.Token)
	if err != nil {
		return "", err
	}
	if len(body) == 0 || header.Get("Package-Version") == "" {
		return "", fmt.Errorf("Postman sent an incomplete bundle for %q", identifier)
	}
	return string(body), nil
}

func (c *Client) checkPackageAccess(ctx context.Context, token, identifier string) error {
	// The policy endpoint returns 403 for plans without access controls.
	// Follow the app's entitlement check, rather than ignoring a policy 403
	// that could also mean the caller cannot read an enforced policy.
	user, err := c.self(ctx)
	if err != nil {
		return err
	}
	var features envelope[struct {
		Features map[string]struct{ Value []string }
	}]
	if err := c.proxyRead(ctx, "", map[string]any{
		"service": "features", "method": "POST", "path": "/features/list?entityType=user&entityValue=" + url.QueryEscape(user),
		"body": map[string]any{"features": []string{"external_package_settings"}},
	}, &features); err != nil {
		return err
	}
	setting, found := features.Data.Features["external_package_settings"]
	if !found {
		return fmt.Errorf("Postman sent no package access entitlement")
	}
	controlled := false
	for _, value := range setting.Value {
		if value == "access_control" {
			controlled = true
		}
	}
	if !controlled {
		return nil
	}
	body, _, err := c.packageHTTP(ctx, strings.TrimRight(c.opts.PackageURL, "/")+"/v1/package-access", "Authorization", "Bearer "+token)
	// The official app treats a missing policy as unrestricted access.
	if postman.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var policy struct {
		Access *struct {
			Scope    string
			Packages []string
		}
		Registry json.RawMessage
	}
	if err := json.Unmarshal(body, &policy); err != nil {
		return fmt.Errorf("invalid package access policy: %w", err)
	}
	if policy.Access == nil {
		if len(policy.Registry) > 0 {
			return nil
		}
		return fmt.Errorf("Postman sent no package access policy")
	}
	if policy.Access.Scope == "" || policy.Access.Scope == "all" {
		return nil
	}
	if policy.Access.Scope == "restricted" {
		_, name, _ := strings.Cut(identifier, ":")
		if strings.LastIndex(name, "@") <= 0 {
			identifier += "@"
		}
		for _, pattern := range policy.Access.Packages {
			if packagePattern(pattern, identifier) {
				return nil
			}
		}
	}
	return fmt.Errorf("package %q is not permitted by this team's package access rules", identifier)
}

// Package rules use shell globs. Match path segments separately so a '*'
// cannot accidentally authorize another npm scope. Unsupported extended
// glob/brace patterns fail closed instead of broadening the team's policy.
func packagePattern(pattern, identifier string) bool {
	if strings.ContainsAny(pattern, "{}()!") {
		return false
	}
	parts, names := strings.Split(pattern, "/"), strings.Split(identifier, "/")
	var match func(int, int) bool
	match = func(p, n int) bool {
		if p == len(parts) {
			return n == len(names)
		}
		if parts[p] == "**" {
			return match(p+1, n) || n < len(names) && match(p, n+1)
		}
		if n == len(names) {
			return false
		}
		ok, err := pathpkg.Match(parts[p], names[n])
		return err == nil && ok && match(p+1, n+1)
	}
	return match(0, 0)
}

// Package credentials belong to these services; do not forward them to a
// redirect target. Bound bundles before passing them to the JS compiler.
func (c *Client) packageHTTP(ctx context.Context, address, authHeader, token string) ([]byte, http.Header, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set(authHeader, token)
	req.Header.Set("User-Agent", c.agent)
	client := *c.http
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := client.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, nil, &postman.Error{Status: resp.StatusCode, Message: "package service refused the request"}
	}
	const maxSize = 10 << 20
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxSize+1))
	if err != nil {
		return nil, nil, err
	}
	if len(body) > maxSize {
		return nil, nil, fmt.Errorf("Postman package exceeds 10 MiB")
	}
	return body, resp.Header, nil
}
