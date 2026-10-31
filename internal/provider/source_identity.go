package provider

import (
	"net/url"
	"strings"
)

// CanonicalSourceURL normalizes a stream URL for cross-provider identity.
// Rotating signature parameters are omitted while media-selecting parameters
// remain significant.
func CanonicalSourceURL(rawURL string) string {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return ""
	}
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Host == "" {
		return strings.TrimSuffix(rawURL, "/")
	}
	parsed.Scheme = strings.ToLower(parsed.Scheme)
	parsed.Host = strings.ToLower(parsed.Host)
	parsed.Fragment = ""
	query := parsed.Query()
	for key := range query {
		if volatileSourceParam(key) {
			query.Del(key)
		}
	}
	parsed.RawQuery = query.Encode()
	parsed.ForceQuery = false
	return strings.TrimSuffix(parsed.String(), "/")
}

func volatileSourceParam(key string) bool {
	switch strings.ToLower(strings.TrimSpace(key)) {
	case "token", "expires", "expire", "expiry", "exp", "signature", "sig",
		"auth", "hdnts", "policy", "key-pair-id":
		return true
	}
	return false
}
