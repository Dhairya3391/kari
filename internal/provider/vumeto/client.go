// Package vumeto implements provider.Provider for Vumeto
// (https://vumeto.live), a TMDB-keyed catalog for movies, TV series, and
// cartoons.
//
// Search, season listings, and episode metadata come from the site's own /api
// endpoints. Playback resolves through the embed player's /_player/resolve
// API using a session token minted with each embed document. Only lanes whose
// URL is directly playable are returned — player-relative relay URLs need the
// iframe session and cannot be opened by mpv.
package vumeto

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"kari/internal/config"
	"kari/internal/httpclient"
	"kari/internal/logging"
	"kari/internal/provider"
	"kari/internal/provider/kit"
)

// providerID is the stable registry identifier.
const providerID = "vumeto"

// Endpoints groups the two origins Vumeto resolution depends on. The site
// origin also supplies the parentOrigin for the player session.
type Endpoints struct {
	Site   string
	Player string
}

var (
	_ provider.Provider      = (*Client)(nil)
	_ provider.EpisodeSource = (*Client)(nil)
	_ provider.Presenter     = (*Client)(nil)
)

// Client implements provider.Provider against Vumeto's catalog and player.
type Client struct {
	http      *http.Client
	endpoints Endpoints
}

// NewClient constructs the Vumeto provider with the production origins.
func NewClient() (*Client, error) {
	return NewClientWithEndpoints(Endpoints{
		Site:   config.VumetoBase,
		Player: config.VumetoPlayerBase,
	})
}

// NewClientWithEndpoints constructs the provider against explicit origins so
// offline tests can serve canned catalog and player payloads.
func NewClientWithEndpoints(endpoints Endpoints) (*Client, error) {
	endpoints = trimEndpoints(endpoints)
	if endpoints.Site == "" || endpoints.Player == "" {
		return nil, fmt.Errorf("vumeto: site and player endpoints are required")
	}
	return &Client{
		http:      httpclient.NewWithUserAgent(config.DesktopUserAgent),
		endpoints: endpoints,
	}, nil
}

// trimEndpoints removes trailing slashes so path concatenation stays stable.
func trimEndpoints(endpoints Endpoints) Endpoints {
	return Endpoints{
		Site:   strings.TrimRight(strings.TrimSpace(endpoints.Site), "/"),
		Player: strings.TrimRight(strings.TrimSpace(endpoints.Player), "/"),
	}
}

// Alias implements provider.Presenter with the user-facing display name.
func (c *Client) Alias() string { return "Vumeto" }

// Name implements provider.Provider with the canonical provider identifier.
func (c *Client) Name() string { return providerID }

// Modes implements provider.Provider, declaring the catalogs Vumeto serves.
func (c *Client) Modes() []provider.Mode {
	return []provider.Mode{
		{Name: provider.ModeMovies, Priority: 2},
		{Name: provider.ModeTV, Priority: 2},
		{Name: provider.ModeCartoon, Priority: 2},
	}
}

// Search resolves titles through Vumeto's catalog API, keeping only items
// whose kind matches the requested mode. Result IDs stay TMDB-keyed like the
// upstream catalog so cross-provider movie and TV resolution keeps working.
func (c *Client) Search(ctx context.Context, query string, mode provider.ContentType) ([]provider.SearchResult, error) {
	logging.Debug("search start", "provider", c.Name(), "mode", mode, "query", query)
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, fmt.Errorf("vumeto search: empty query")
	}
	target := fmt.Sprintf("%s/api/search?q=%s", c.endpoints.Site, url.QueryEscape(query))
	var payload searchResponse
	if err := c.getJSON(ctx, target, nil, &payload); err != nil {
		return nil, fmt.Errorf("vumeto search: %w", err)
	}
	wanted := modeKind(mode)
	results := make([]provider.SearchResult, 0, len(payload.Items))
	for _, item := range payload.Items {
		if item.ID <= 0 || !strings.EqualFold(item.Kind, wanted) {
			continue
		}
		title := kit.Clean(item.Title)
		if title == "" {
			continue
		}
		results = append(results, provider.SearchResult{
			Title:     title,
			ID:        strconv.Itoa(item.ID),
			Type:      mode,
			Year:      item.Year,
			MediaType: mediaType(item.Type),
			TMDBID:    item.ID,
			CoverURL:  item.Poster,
			Overview:  item.Overview,
			Genres:    item.Genres,
		})
	}
	if len(results) == 0 {
		return nil, provider.ErrNoResults
	}
	logging.Debug("search done", "provider", c.Name(), "results", len(results))
	return results, nil
}

// FetchEpisodes returns a synthetic episode for movies and the full season
// roster for series and cartoons.
func (c *Client) FetchEpisodes(ctx context.Context, series provider.SearchResult) ([]provider.Episode, error) {
	tmdbID := series.TMDBID
	if tmdbID <= 0 {
		var err error
		tmdbID, err = strconv.Atoi(strings.TrimSpace(series.ID))
		if err != nil || tmdbID <= 0 {
			return nil, fmt.Errorf("vumeto episodes: invalid TMDB id %q: %w", series.ID, provider.ErrNoEpisodes)
		}
	}
	if series.MediaType == provider.MediaTypeMovie {
		return []provider.Episode{{
			Title:  series.Title,
			ID:     strconv.Itoa(tmdbID),
			TMDBID: tmdbID,
		}}, nil
	}
	return c.seasonEpisodes(ctx, tmdbID)
}

// ResolveSource returns the player's directly playable streams for one
// episode. Movies use the movie embed; series carry season and episode data.
func (c *Client) ResolveSource(ctx context.Context, mediaID string, episode provider.Episode) ([]provider.MediaSource, error) {
	tmdbID := episode.TMDBID
	if tmdbID <= 0 {
		var err error
		tmdbID, err = strconv.Atoi(strings.TrimSpace(mediaID))
		if err != nil || tmdbID <= 0 {
			return nil, fmt.Errorf("vumeto resolve: invalid TMDB id %q: %w", mediaID, provider.ErrNoSources)
		}
	}
	return c.resolvePlayer(ctx, tmdbID, episode)
}
