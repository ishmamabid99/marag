package publisher

import (
	"fmt"
	"regexp"
	"strings"
)

var scpLike = regexp.MustCompile(`^git@[^:]+:(.+)$`)

// parseOwnerName extracts owner + repo name from an https or scp-like git URL.
func parseOwnerName(remoteURL string) (string, string, error) {
	u := strings.TrimSuffix(strings.TrimSpace(remoteURL), ".git")
	if m := scpLike.FindStringSubmatch(u); m != nil {
		u = m[1] // "owner/name"
	} else if i := strings.Index(u, "://"); i >= 0 {
		rest := u[i+3:]
		if s := strings.IndexByte(rest, '/'); s >= 0 {
			u = rest[s+1:] // strip host → "owner/name/..."
		} else {
			return "", "", fmt.Errorf("publisher: no path in url %q", remoteURL)
		}
	} else {
		return "", "", fmt.Errorf("publisher: unrecognized git url %q", remoteURL)
	}
	parts := strings.Split(strings.Trim(u, "/"), "/")
	if len(parts) < 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("publisher: cannot parse owner/name from %q", remoteURL)
	}
	return parts[0], parts[1], nil
}
