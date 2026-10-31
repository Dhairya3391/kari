package tui

import (
	"regexp"
	"strings"
)

// reURL matches http(s) URLs so cleanErrorForUI can scrub them.
var reURL = regexp.MustCompile(`https?://\S+`)

func cleanErrorForUI(err error) string {
	if err == nil {
		return "Unknown error"
	}
	msg := err.Error()

	// Scrub transport detail: URLs embed upstream hostnames (i.e. provider
	// identities) and add noise without helping the user.
	for _, m := range reURL.FindAllString(msg, -1) {
		msg = strings.ReplaceAll(msg, m, "…")
	}

	lower := strings.ToLower(msg)
	if strings.Contains(lower, "rate limited") || strings.Contains(lower, "rate limit") || strings.Contains(lower, "429") {
		return "A provider is rate limited — please try again"
	}
	if strings.Contains(lower, "no results found") || strings.Contains(lower, "no results") {
		return "No results found"
	}
	if strings.Contains(lower, "no sources found") {
		return "No sources found"
	}
	if strings.Contains(lower, "deadline") || strings.Contains(lower, "timeout") {
		return "Request timed out — please try again"
	}
	if strings.Contains(lower, "connection") || strings.Contains(lower, "no such host") {
		return "Network connection failed — check your connection"
	}

	parts := strings.Split(msg, "; ")
	if len(parts) > 1 {
		var cleanParts []string
		for _, p := range parts {
			name := strings.Split(p, ":")[0]
			cleanParts = append(cleanParts, title(name))
		}
		return "No sources: " + strings.Join(cleanParts, ", ")
	}

	short := strings.TrimSpace(msg)
	if len(short) > 60 {
		short = short[:60] + "..."
	}
	return title(short)
}

func title(s string) string {
	if s == "" {
		return s
	}
	r := []rune(s)
	if r[0] >= 'a' && r[0] <= 'z' {
		r[0] -= 32
	}
	return string(r)
}
