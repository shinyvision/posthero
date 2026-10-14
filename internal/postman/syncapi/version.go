package syncapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"
)

const updateStatusURL = "https://dl.pstmn.io/update/status"

var versionPattern = regexp.MustCompile(`^(0|[1-9][0-9]{0,5})\.(0|[1-9][0-9]{0,5})\.(0|[1-9][0-9]{0,5})$`)

// ValidAppVersion accepts stable release versions, including cached values.
func ValidAppVersion(version string) bool { return versionPattern.MatchString(version) }

func userAgent(version string) string {
	return strings.Replace(UserAgent, "Postman/"+AppVersion, "Postman/"+version, 1)
}

// LatestVersion checks the same stable update service as the desktop app.
// It downloads metadata only, sends no credentials, and bounds startup delay.
func LatestVersion(ctx context.Context, current string) (string, error) {
	platform := map[string]string{"linux": "linux", "darwin": "osx", "windows": "windows"}[runtime.GOOS]
	arch := map[string]string{"amd64": "64", "arm64": "arm64", "386": "32"}[runtime.GOARCH]
	if platform == "" || arch == "" {
		return "", fmt.Errorf("no Postman update platform for %s/%s", runtime.GOOS, runtime.GOARCH)
	}
	return latestVersion(ctx, updateStatusURL, platform+"_"+arch, current)
}

func latestVersion(ctx context.Context, address, platform, current string) (string, error) {
	if !ValidAppVersion(current) {
		current = AppVersion
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	query := url.Values{"currentVersion": {current}, "platform": {platform}, "channel": {"stable"}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, address+"?"+query.Encode(), nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", userAgent(current))
	req.Header.Set("Accept", "application/json")
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNoContent {
		return current, nil
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("Postman update service returned HTTP %d", resp.StatusCode)
	}
	var update struct{ Version string }
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&update); err != nil {
		return "", fmt.Errorf("invalid Postman update response: %w", err)
	}
	if !ValidAppVersion(update.Version) {
		return "", fmt.Errorf("Postman update service sent an invalid version")
	}
	// An old CDN response must not move this installation backwards.
	before, after := strings.Split(current, "."), strings.Split(update.Version, ".")
	for i := range before {
		a, _ := strconv.Atoi(before[i])
		b, _ := strconv.Atoi(after[i])
		if b > a {
			return update.Version, nil
		}
		if b < a {
			return current, nil
		}
	}
	return current, nil
}
