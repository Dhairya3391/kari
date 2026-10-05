package miruro

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"

	"kari/internal/httpclient"
	"kari/internal/lang"
	"kari/internal/provider"
	"kari/internal/provider/anilist"
	"kari/internal/provider/kit"
)

// This file fetches Miruro's __data.json endpoints, resolves AniList IDs
// to watch coordinates, and ranks episode streams for mpv.

// get fetches url with the request context, returning the capped body.
// Non-2xx responses become typed HTTP errors.
func (c *Client) get(ctx context.Context, rawURL string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, fmt.Errorf("miruro: build request: %w", err)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("miruro: fetch %s: %w", redactURL(rawURL), err)
	}
	if resp.StatusCode != http.StatusOK {
		_ = resp.Body.Close()
		return nil, &provider.HTTPError{Code: resp.StatusCode, URL: redactURL(rawURL)}
	}
	body, err := httpclient.ReadCapped(resp)
	if err != nil {
		return nil, fmt.Errorf("miruro: read response: %w", err)
	}
	return body, nil
}

// queryEscape encodes a search query for the URL query string.
func queryEscape(q string) string {
	return url.QueryEscape(strings.TrimSpace(q))
}

// redactURL strips query strings (signed stream tokens, search terms) for
// errors and logs.
func redactURL(rawURL string) string {
	if i := strings.Index(rawURL, "?"); i >= 0 {
		return rawURL[:i]
	}
	return rawURL
}

// resolveShow maps an AniList ID to Miruro watch coordinates, consulting
// the cache first. knownShowID short-circuits the catalog search when the
// caller already holds the Miruro show ID (own episode handles do);
// otherwise titleHint drives one exact-match search, and an empty hint
// falls back to AniList title candidates (cold cross-provider resolves).
func (c *Client) resolveShow(ctx context.Context, anilistID, titleHint, knownShowID string) (showRef, error) {
	anilistID = strings.TrimSpace(anilistID)
	knownShowID = strings.TrimSpace(knownShowID)
	c.mu.Lock()
	if anilistID != "" {
		if ref, ok := c.byAniList[anilistID]; ok && ref.showID != "" && ref.slug != "" {
			c.mu.Unlock()
			return ref, nil
		}
	}
	slug := c.slugs[knownShowID]
	c.mu.Unlock()

	showID := knownShowID
	if showID == "" {
		if anilistID == "" {
			return showRef{}, fmt.Errorf("miruro resolve: missing series id: %w", provider.ErrNoEpisodes)
		}
		titles := showTitles(titleHint)
		found, err := c.searchShowByTitles(ctx, anilistID, titles)
		if err != nil {
			return showRef{}, err
		}
		showID = found
	}
	if slug == "" {
		var err error
		slug, err = c.watchSlug(ctx, showID)
		if err != nil {
			return showRef{}, err
		}
	}
	ref := showRef{showID: showID, slug: slug}
	c.mu.Lock()
	if anilistID != "" {
		c.byAniList[anilistID] = ref
	}
	c.slugs[showID] = slug
	c.mu.Unlock()
	return ref, nil
}

// showTitles returns titleHint as the single search candidate, or nil when
// the caller has no title and AniList candidates must be fetched instead.
func showTitles(titleHint string) []string {
	if strings.TrimSpace(titleHint) == "" {
		return nil
	}
	return []string{strings.TrimSpace(titleHint)}
}

// searchShowByTitles runs one Miruro search per title and returns the show
// ID whose AniList mapping equals anilistID. With no titles (cold
// cross-provider resolve), AniList supplies the candidates. Matching is
// exact on the upstream's own external IDs — never fuzzy.
func (c *Client) searchShowByTitles(ctx context.Context, anilistID string, titles []string) (string, error) {
	if len(titles) == 0 {
		var err error
		titles, err = c.anilistTitles(ctx, anilistID)
		if err != nil {
			return "", err
		}
	}
	for _, title := range titles {
		if strings.TrimSpace(title) == "" {
			continue
		}
		body, err := c.get(ctx, c.base+"/search/__data.json?q="+queryEscape(title))
		if err != nil {
			return "", fmt.Errorf("miruro resolve: search: %w", err)
		}
		items, err := searchItems(body)
		if err != nil {
			return "", fmt.Errorf("miruro resolve: search: %w", err)
		}
		for _, it := range items {
			if firstExternalID(it.externalIDs, "anilist") == anilistID {
				return it.showID, nil
			}
		}
	}
	return "", fmt.Errorf("miruro resolve: anilist %s absent from catalog: %w", anilistID, provider.ErrNoEpisodes)
}

// anilistTitles fetches AniList title candidates for a cold resolve that
// carries no search hint (cross-provider episodes on a cold cache).
func (c *Client) anilistTitles(ctx context.Context, anilistID string) ([]string, error) {
	id, err := strconv.Atoi(anilistID)
	if err != nil || id <= 0 {
		return nil, fmt.Errorf("miruro resolve: invalid anilist id %q: %w", anilistID, provider.ErrNoEpisodes)
	}
	media, err := anilist.FetchMediaWithEndpoint(ctx, c.http, id, c.gqlBase)
	if err != nil {
		return nil, fmt.Errorf("miruro resolve: anilist titles: %w", err)
	}
	var titles []string
	seen := make(map[string]bool)
	for _, t := range append([]string{
		media.Title.English,
		media.Title.UserPreferred,
		media.Title.Romaji,
		media.Title.Native,
	}, media.Synonyms...) {
		if t = strings.TrimSpace(t); t != "" && !seen[t] {
			seen[t] = true
			titles = append(titles, t)
		}
	}
	if len(titles) == 0 {
		return nil, fmt.Errorf("miruro resolve: no titles for anilist %s: %w", anilistID, provider.ErrNoEpisodes)
	}
	return titles, nil
}

// watchSlug returns the canonical watch slug for showID, following the
// server's redirect from a placeholder slug.
func (c *Client) watchSlug(ctx context.Context, showID string) (string, error) {
	body, err := c.get(ctx, c.watchURL(showID, placeholderSlug, 1))
	if err != nil {
		return "", fmt.Errorf("miruro resolve: slug probe: %w", err)
	}
	if slug := redirectSlug(body); slug != "" {
		return slug, nil
	}
	// No redirect: the placeholder was accepted (or the payload is the
	// episode data itself); either way the probe slug loads the page.
	return placeholderSlug, nil
}

// watchURL builds a watch __data.json URL for an episode number.
func (c *Client) watchURL(showID, slug string, episode int) string {
	if episode <= 0 {
		episode = 1
	}
	return fmt.Sprintf("%s/watch/%s/%s/__data.json?ep=%d", c.base, showID, slug, episode)
}

// fetchWatch loads a watch payload, following one canonical-slug redirect
// when the cached slug went stale.
func (c *Client) fetchWatch(ctx context.Context, ref showRef, episode int) ([]byte, error) {
	body, err := c.get(ctx, c.watchURL(ref.showID, ref.slug, episode))
	if err != nil {
		return nil, err
	}
	if slug := redirectSlug(body); slug != "" && slug != ref.slug {
		c.mu.Lock()
		c.slugs[ref.showID] = slug
		for id, r := range c.byAniList {
			if r.showID == ref.showID {
				r.slug = slug
				c.byAniList[id] = r
			}
		}
		c.mu.Unlock()
		body, err = c.get(ctx, c.watchURL(ref.showID, slug, episode))
		if err != nil {
			return nil, err
		}
	}
	return body, nil
}

// watchEpisodes loads the full episode roster for a show.
func (c *Client) watchEpisodes(ctx context.Context, ref showRef, episode int) ([]episodeEntry, error) {
	body, err := c.fetchWatch(ctx, ref, episode)
	if err != nil {
		return nil, fmt.Errorf("miruro episodes: %w", err)
	}
	eps, err := watchEpisodes(body)
	if err != nil {
		return nil, fmt.Errorf("miruro episodes: %w", err)
	}
	return eps, nil
}

// watchTracks loads the episode's audio tracks plus provider ranking.
func (c *Client) watchTracks(ctx context.Context, ref showRef, episode int) ([]trackEntry, []string, error) {
	body, err := c.fetchWatch(ctx, ref, episode)
	if err != nil {
		return nil, nil, fmt.Errorf("miruro resolve: %w", err)
	}
	tracks, order, err := watchTracks(body)
	if err != nil {
		return nil, nil, fmt.Errorf("miruro resolve: %w", err)
	}
	return tracks, order, nil
}

// rankStreams flattens the requested audio track's providers into direct
// mpv sources in upstream provider order. A sub request takes the soft-sub
// (ssub) and hard-sub (sub) pools; dub takes dub only.
func rankStreams(tracks []trackEntry, order []string, category string) []provider.MediaSource {
	wanted := map[string]bool{provider.AudioSub: true}
	if category == provider.AudioDub {
		wanted = map[string]bool{provider.AudioDub: true}
	} else {
		wanted["ssub"] = true
	}
	byProvider := make(map[string][]providerEntry, len(tracks))
	for _, t := range tracks {
		name := strings.ToLower(strings.TrimSpace(t.track))
		if !wanted[name] {
			continue
		}
		for _, p := range t.providers {
			if strings.TrimSpace(p.name) == "" {
				continue
			}
			byProvider[p.name] = append(byProvider[p.name], providerEntry{name: p.name, servers: p.servers, subtitles: p.subtitles, track: name})
		}
	}
	// Upstream ranking first, then any provider the order list omits.
	var names []string
	seen := make(map[string]bool, len(byProvider))
	for _, n := range order {
		if _, ok := byProvider[n]; ok && !seen[n] {
			seen[n] = true
			names = append(names, n)
		}
	}
	for n := range byProvider {
		if !seen[n] {
			seen[n] = true
			names = append(names, n)
		}
	}
	var out []provider.MediaSource
	for _, n := range names {
		// Soft-sub pools sort before hard-sub pools of the same backend.
		entries := byProvider[n]
		for i := range entries {
			if entries[i].track == "ssub" {
				out = append(out, serverSources(entries[i])...)
			}
		}
		for i := range entries {
			if entries[i].track != "ssub" {
				out = append(out, serverSources(entries[i])...)
			}
		}
	}
	// Challenged hosts stay listed; the service and ranking sorts
	// place them last globally.
	return out
}

// deadHosts are stream hosts that never serve playable video, verified
// live: megap.shiora.top answers master playlists whose every segment is
// a TikTok CDN ad image (305/305 segments, no video track), so mpv can
// only ever show a slideshow. The aggregator would otherwise keep ranking
// these rows above working backends.
var deadHosts = []string{"shiora.top"}

// deadHost reports whether rawURL is served by a known-dead host.
func deadHost(rawURL string) bool {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return false
	}
	host := strings.ToLower(u.Hostname())
	for _, dead := range deadHosts {
		if host == dead || strings.HasSuffix(host, "."+dead) {
			return true
		}
	}
	return false
}

// serverSources maps one provider pool's servers to media sources,
// skipping servers without streams (iframe embeds) and non-video files.
func serverSources(pe providerEntry) []provider.MediaSource {
	subs := make([]provider.SubtitleOption, 0, len(pe.subtitles))
	for _, s := range pe.subtitles {
		if !kit.IsSubtitleURL(s.file) {
			continue
		}
		tag := lang.Normalize(strings.TrimSpace(s.language))
		if tag == "" {
			tag = lang.Normalize(strings.TrimSpace(s.label))
		}
		if tag == "" {
			tag = "en"
		}
		subs = append(subs, provider.SubtitleOption{URL: s.file, Language: tag, Default: s.def})
	}
	subType := ""
	switch pe.track {
	case "ssub":
		// Soft-sub pool: clean video plus toggleable external tracks.
		subType = provider.SubTypeSoft
	case provider.AudioSub:
		// Hard-sub pool: the HLS playlists carry no subtitle
		// renditions (AES segments, burned-in video), so the video
		// itself is hard-subtitled even when the pool also lists
		// downloadable sidecar files.
		subType = provider.SubTypeHard
	}
	var out []provider.MediaSource
	for _, srv := range pe.servers {
		if strings.TrimSpace(srv.server) == "" && strings.TrimSpace(srv.referer) == "" && len(srv.streams) == 0 {
			continue
		}
		for _, st := range srv.streams {
			typ := streamType(st)
			if typ == "" {
				continue
			}
			if deadHost(st.url) {
				continue
			}
			quality := kit.TagQuality(kit.QualityLabel(st.quality, typ), srv.server, "")
			out = append(out, provider.MediaSource{
				URL:       st.url,
				Quality:   quality,
				Referer:   strings.TrimSpace(srv.referer),
				Type:      typ,
				Subtitles: subs,
				SubType:   subType,
				ExtraArgs: demuxerQuirk(st.url, typ),
			})
		}
	}
	return out
}

// streamType classifies a stream URL for the player layer: explicit mp4
// (or an mp4-shaped URL) becomes progressive, everything else HLS.
func streamType(st streamEntry) string {
	if strings.EqualFold(strings.TrimSpace(st.format), "mp4") || kit.IsMp4URL(st.url) {
		return provider.SourceTypeMP4
	}
	if strings.EqualFold(strings.TrimSpace(st.format), "hls") || strings.Contains(strings.ToLower(st.url), ".m3u8") {
		return provider.SourceTypeHLS
	}
	return ""
}

// demuxerQuirk forces the HLS demuxer for playlists mpv cannot sniff: no
// .m3u8 path suffix and a misleading content type (Vidplay answers
// image/jpeg). Extensioned playlists need nothing.
func demuxerQuirk(rawURL, typ string) []string {
	if typ != provider.SourceTypeHLS {
		return nil
	}
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return nil
	}
	if strings.HasSuffix(strings.ToLower(u.Path), ".m3u8") {
		return nil
	}
	if p := strings.ToLower(path.Base(u.Path)); strings.Contains(p, ".m3u8") {
		return nil
	}
	return []string{"--demuxer=lavf", "--demuxer-lavf-format=hls"}
}
