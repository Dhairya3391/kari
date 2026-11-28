package player

import (
	"sort"
	"strings"

	"kari/internal/provider"
)

// extraHeaderFields renders a source's additional provider headers as plain
// "Name: value" strings. Referer, User-Agent and Cookie are skipped because
// every player has a dedicated option for them.
func extraHeaderFields(source provider.MediaSource) []string {
	keys := make([]string, 0, len(source.ExtraHeaders))
	for key := range source.ExtraHeaders {
		switch strings.ToLower(strings.TrimSpace(key)) {
		case "referer", "user-agent", "cookie":
			continue
		}
		keys = append(keys, key)
	}
	sort.Strings(keys)
	fields := make([]string, 0, len(keys))
	for _, key := range keys {
		value := strings.TrimSpace(source.ExtraHeaders[key])
		if value == "" {
			continue
		}
		fields = append(fields, strings.TrimSpace(key)+": "+value)
	}
	return fields
}

// mpvExtraHeaderFields escapes commas because mpv and mpv-derived players
// split their header list options on commas.
func mpvExtraHeaderFields(source provider.MediaSource) []string {
	fields := extraHeaderFields(source)
	for i, field := range fields {
		fields[i] = strings.ReplaceAll(field, ",", `\,`)
	}
	return fields
}

// explicitOrigin returns an Origin declared by the addon. It takes precedence
// over the Origin derived from the Referer.
func explicitOrigin(source provider.MediaSource) string {
	for key, value := range source.ExtraHeaders {
		if strings.EqualFold(strings.TrimSpace(key), "origin") {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
