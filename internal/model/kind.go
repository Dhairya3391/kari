// Package model holds playback aggregates and the static behavior table for
// content kinds. Callers look up behavior (tab order, reader vs player flow,
// item nouns) instead of branching on provider names or mode strings.
package model

import "kari/internal/provider"

// Kind identifies one content mode. The zero value is KindUnknown, which has
// no spec entry and renders as a plain title.
type Kind uint8

// Content kinds. Order here is fixed by iota; tab order comes from
// KindSpec.Tab so the two never get confused.
const (
	KindUnknown Kind = iota
	KindAnime
	KindCartoon
	KindJellyfin
	KindLive
	KindManga
	KindMovie
	KindTV
	KindStremio
)

// KindSpec describes static behavior for one Kind so callers never switch
// on the kind itself.
type KindSpec struct {
	// Key is the on-disk / wire key ("anime", "movies", …).
	Key string
	// Label is the display name ("Anime", "Movies", …).
	Label string
	// Tab is the search-tab order; -1 hides the tab.
	Tab int
	// Reader marks paged reading media (manga): chapters plus pages, no
	// playable sources.
	Reader bool
	// Live marks live media: no episode list, sources load straight from
	// Preview.
	Live bool
	// HasItems reports whether Enter opens an item list first. When false,
	// Enter goes directly to Preview.
	HasItems bool
	// ItemNoun is "episode" or "chapter".
	ItemNoun string
}

// kindSpecs is the only table mapping kinds to behavior. Add a row here
// when a new kind appears; no call site may switch on Kind.
var kindSpecs = map[Kind]*KindSpec{
	KindAnime:    {Key: string(provider.ModeAnime), Label: "Anime", Tab: 0, HasItems: true, ItemNoun: "episode"},
	KindCartoon:  {Key: string(provider.ModeCartoon), Label: "Cartoon", Tab: 1, HasItems: true, ItemNoun: "episode"},
	KindStremio:  {Key: string(provider.ModeStremio), Label: "Stremio", Tab: 2, HasItems: true, ItemNoun: "episode"},
	KindLive:     {Key: string(provider.ModeLive), Label: "Live", Tab: 3, Live: true, ItemNoun: "episode"},
	KindManga:    {Key: string(provider.ModeManga), Label: "Reading", Tab: 4, Reader: true, HasItems: true, ItemNoun: "chapter"},
	KindMovie:    {Key: string(provider.ModeMovies), Label: "Movies", Tab: 5, ItemNoun: "episode"},
	KindTV:       {Key: string(provider.ModeTV), Label: "TV", Tab: 6, HasItems: true, ItemNoun: "episode"},
	KindJellyfin: {Key: string(provider.ModeJellyfin), Label: "Jellyfin", Tab: 7, HasItems: true, ItemNoun: "episode"},
}

// kindByKey indexes the same table by wire key.
var kindByKey = func() map[string]Kind {
	out := make(map[string]Kind, len(kindSpecs))
	for k, s := range kindSpecs {
		out[s.Key] = k
	}
	return out
}()

// Spec returns the static behavior row for the kind, or nil for
// KindUnknown. Callers must handle nil by rendering a plain title.
func (k Kind) Spec() *KindSpec {
	return kindSpecs[k]
}

// FromKey parses a wire key ("anime", "movies", …) into a Kind. Unknown
// keys yield KindUnknown, never an error, so persisted values always load.
func FromKey(key string) Kind {
	if k, ok := kindByKey[key]; ok {
		return k
	}
	return KindUnknown
}

// Key returns the wire key for the kind, or "" for KindUnknown.
func (k Kind) Key() string {
	if s := k.Spec(); s != nil {
		return s.Key
	}
	return ""
}
