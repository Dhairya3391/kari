package miruro

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"kari/internal/provider"
)

// This file maps hydrated __data.json values onto the typed shapes Miruro
// emits: search items, episode entries, and watch tracks. All access is by
// key name — never by array position — so unrelated catalog growth cannot
// shift the parse.

// searchItem is one catalog entry from the search payload.
type searchItem struct {
	showID      string
	externalIDs map[string][]string
	titleRomaji string
	titleEng    string
	titleNative string
	format      string
	seasonYear  int
	coverURL    string
}

// searchItems extracts catalog entries from a search __data.json body. It
// scans every data node for the root holding the items list, so layout
// payloads and future envelope fields are ignored.
func searchItems(body []byte) ([]searchItem, error) {
	docs, err := parseDoc(body)
	if err != nil {
		return nil, err
	}
	for _, doc := range docs {
		if doc.Type == "redirect" {
			continue
		}
		for _, node := range doc.Nodes {
			if node.Type != "data" || len(node.Data) == 0 {
				continue
			}
			h := hydrator{arr: node.Data}
			root, ok := asMap(h.hydrate(0))
			if !ok {
				continue
			}
			rawItems, ok := asList(root["items"])
			if !ok {
				continue
			}
			items := make([]searchItem, 0, len(rawItems))
			for _, raw := range rawItems {
				m, ok := asMap(raw)
				if !ok {
					continue
				}
				// Only catalog entries carry external_ids; anything
				// else is a different list sharing the envelope.
				if _, ok := m["external_ids"]; !ok {
					continue
				}
				items = append(items, decodeSearchItem(m))
			}
			return items, nil
		}
	}
	return nil, fmt.Errorf("miruro search: no catalog in response: %w", provider.ErrUpstreamChanged)
}

// decodeSearchItem maps one hydrated catalog entry by key name.
func decodeSearchItem(m map[string]any) searchItem {
	var it searchItem
	it.showID, _ = asString(m["id"])
	if ext, ok := asMap(m["external_ids"]); ok {
		it.externalIDs = make(map[string][]string, len(ext))
		for k, v := range ext {
			l, ok := asList(v)
			if !ok {
				continue
			}
			for _, e := range l {
				if s, ok := asString(e); ok && strings.TrimSpace(s) != "" {
					it.externalIDs[k] = append(it.externalIDs[k], s)
				}
			}
		}
	}
	if title, ok := asMap(m["title"]); ok {
		it.titleRomaji, _ = asString(title["romaji"])
		it.titleEng, _ = asString(title["english"])
		it.titleNative, _ = asString(title["native"])
	}
	it.format, _ = asString(m["format"])
	if n, ok := toInt(m["season_year"]); ok {
		it.seasonYear = n
	}
	it.coverURL, _ = asString(m["cover_url"])
	return it
}

// firstExternalID returns the first non-blank value for key, or "".
func firstExternalID(ids map[string][]string, key string) string {
	for _, v := range ids[key] {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// firstExternalIDInt returns the first numeric value for key, or 0.
func firstExternalIDInt(ids map[string][]string, key string) int {
	if n, err := strconv.Atoi(firstExternalID(ids, key)); err == nil && n > 0 {
		return n
	}
	return 0
}

// pickTitle selects the most user-friendly title, preferring English,
// then Romaji, then Native.
func pickTitle(english, romaji, native string) string {
	for _, s := range []string{english, romaji, native} {
		if v := strings.TrimSpace(s); v != "" {
			return v
		}
	}
	return ""
}

// episodeEntry is one row of a watch payload's episode list.
type episodeEntry struct {
	number int
	title  string
	filler bool
}

// watchEpisodes extracts the full episode roster from a watch __data.json
// body by locating the chunk holding the episodes list.
func watchEpisodes(body []byte) ([]episodeEntry, error) {
	slots, err := hydratedChunk(body, "episodes")
	if err != nil {
		return nil, err
	}
	var out []episodeEntry
	for _, slot := range slots {
		m, ok := asMap(slot)
		if !ok {
			continue
		}
		if _, ok := m["episodes"]; ok {
			continue
		}
		num, ok := toInt(m["number"])
		if !ok || num <= 0 {
			continue
		}
		ep := episodeEntry{number: num}
		ep.title, _ = asString(m["title"])
		ep.filler, _ = m["filler"].(bool)
		out = append(out, ep)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("miruro episodes: empty roster: %w", provider.ErrNoEpisodes)
	}
	return out, nil
}

// streamEntry is one playable file of a server.
type streamEntry struct {
	url     string
	format  string
	quality string
}

// serverEntry is one backend serving a provider's track.
type serverEntry struct {
	server  string
	referer string
	streams []streamEntry
}

// providerEntry is one source backend group of a track.
type providerEntry struct {
	name string
	// track is the audio track this group was listed under
	// (sub/ssub/dub), deciding the soft-sub declaration.
	track     string
	servers   []serverEntry
	subtitles []subtitleEntry
}

// subtitleEntry is one external subtitle track of a provider.
type subtitleEntry struct {
	language string
	label    string
	file     string
	def      bool
}

// trackEntry is one audio track (sub/ssub/dub) of an episode.
type trackEntry struct {
	track     string
	providers []providerEntry
}

// watchTracks extracts the episode's audio tracks plus the upstream
// provider ranking from a watch __data.json body.
func watchTracks(body []byte) ([]trackEntry, []string, error) {
	slots, err := hydratedChunk(body, "tracks")
	if err != nil {
		return nil, nil, err
	}
	var tracks []trackEntry
	var order []string
	for _, slot := range slots {
		m, ok := asMap(slot)
		if !ok {
			continue
		}
		switch {
		case hasKey(m, "tracks"):
			l, _ := asList(m["tracks"])
			for _, raw := range l {
				tm, ok := asMap(raw)
				if !ok {
					continue
				}
				track, _ := asString(tm["track"])
				te := trackEntry{track: track}
				if pl, ok := asList(tm["providers"]); ok {
					for _, praw := range pl {
						pm, ok := asMap(praw)
						if !ok {
							continue
						}
						te.providers = append(te.providers, decodeProvider(pm))
					}
				}
				tracks = append(tracks, te)
			}
		case hasKey(m, "providerOrder"):
			if l, ok := asList(m["providerOrder"]); ok {
				for _, raw := range l {
					if s, ok := asString(raw); ok && strings.TrimSpace(s) != "" {
						order = append(order, s)
					}
				}
			}
		}
	}
	if len(tracks) == 0 {
		return nil, nil, fmt.Errorf("miruro tracks: none listed: %w", provider.ErrNoSources)
	}
	return tracks, order, nil
}

// hydratedChunk locates the streamed chunk array holding a dict with key
// (episodes or tracks) in a watch __data.json body and returns it fully
// hydrated: every slot holds final values, no index pointers remain.
func hydratedChunk(body []byte, key string) ([]any, error) {
	docs, err := parseDoc(body)
	if err != nil {
		return nil, err
	}
	for _, doc := range docs {
		if doc.Type == "redirect" || len(doc.Data) == 0 {
			continue
		}
		for _, slot := range doc.Data {
			if m, ok := asMap(slot); ok && hasKey(m, key) {
				h := hydrator{arr: doc.Data}
				out := make([]any, 0, len(doc.Data))
				for i := range doc.Data {
					out = append(out, h.hydrate(i))
				}
				return out, nil
			}
		}
	}
	return nil, fmt.Errorf("miruro: no %s chunk in response: %w", key, provider.ErrUpstreamChanged)
}

// decodeProvider maps one hydrated provider group by key name.
func decodeProvider(pm map[string]any) providerEntry {
	pe := providerEntry{}
	pe.name, _ = asString(pm["provider"])
	if sl, ok := asList(pm["servers"]); ok {
		for _, sraw := range sl {
			sm, ok := asMap(sraw)
			if !ok {
				continue
			}
			se := serverEntry{}
			se.server, _ = asString(sm["server"])
			if hm, ok := asMap(sm["headers"]); ok {
				se.referer, _ = asString(hm["Referer"])
			}
			if tl, ok := asList(sm["streams"]); ok {
				for _, traw := range tl {
					tm, ok := asMap(traw)
					if !ok {
						continue
					}
					u, _ := asString(tm["url"])
					if strings.TrimSpace(u) == "" {
						continue
					}
					st := streamEntry{url: strings.TrimSpace(u)}
					st.format, _ = asString(tm["format"])
					st.quality, _ = asString(tm["quality"])
					se.streams = append(se.streams, st)
				}
			}
			pe.servers = append(pe.servers, se)
		}
	}
	if sl, ok := asList(pm["subtitles"]); ok {
		for _, sraw := range sl {
			sm, ok := asMap(sraw)
			if !ok {
				continue
			}
			se := subtitleEntry{}
			se.language, _ = asString(sm["language"])
			se.label, _ = asString(sm["label"])
			se.file, _ = asString(sm["file"])
			se.def, _ = sm["default"].(bool)
			if strings.TrimSpace(se.file) == "" {
				continue
			}
			pe.subtitles = append(pe.subtitles, se)
		}
	}
	return pe
}

// hasKey reports whether a raw chunk dict carries key. Raw keys are
// literal strings; only values are index pointers.
func hasKey(m map[string]any, key string) bool {
	_, ok := m[key]
	return ok
}

// redirectSlug extracts the canonical watch slug from a redirect
// document's location (/watch/<id>/<slug>?ep=N).
func redirectSlug(body []byte) string {
	docs, err := parseDoc(body)
	if err != nil {
		return ""
	}
	for _, doc := range docs {
		if doc.Type != "redirect" || doc.Location == "" {
			continue
		}
		parts := strings.Split(strings.Trim(doc.Location, "/"), "/")
		if len(parts) >= 3 && parts[0] == "watch" {
			slug := strings.SplitN(parts[2], "?", 2)[0]
			if u, err := url.PathUnescape(slug); err == nil {
				slug = u
			}
			return strings.TrimSpace(slug)
		}
	}
	return ""
}
