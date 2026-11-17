package service

import (
	"strings"

	"kari/internal/provider"
)

// tierKey identifies the descriptive slot a playable row occupies in the
// source list. A master and its explicit tier share the slot once both carry
// the same quality label.
type tierKey struct {
	quality  string
	backend  string
	language string
	typeName string
}

// shadowKey folds a source into the descriptive identity used for dedupe.
func shadowKey(src provider.MediaSource) tierKey {
	return tierKey{
		quality:  strings.ToLower(strings.TrimSpace(src.Quality)),
		backend:  strings.ToLower(strings.TrimSpace(src.Backend)),
		language: strings.ToLower(strings.TrimSpace(src.Language)),
		typeName: strings.ToLower(strings.TrimSpace(src.Type)),
	}
}

// dropShadowedTiers removes explicit tier rows that became duplicates when
// their master was relabeled to a real quality. The master is kept: mpv plays
// its highest tier with all attached renditions, while a tier URL alone can be
// video-only. Only rows whose exact descriptive slot matches a relabeled row
// are dropped, so distinct backends and languages stay visible.
func dropShadowedTiers(sources []provider.MediaSource, relabeled []bool) []provider.MediaSource {
	if len(relabeled) == 0 {
		return sources
	}
	shadowed := make(map[tierKey]bool)
	for i, src := range sources {
		if i < len(relabeled) && relabeled[i] {
			shadowed[shadowKey(src)] = true
		}
	}
	if len(shadowed) == 0 {
		return sources
	}
	out := make([]provider.MediaSource, 0, len(sources))
	for i, src := range sources {
		if i < len(relabeled) && !relabeled[i] && shadowed[shadowKey(src)] {
			continue
		}
		out = append(out, src)
	}
	return out
}
