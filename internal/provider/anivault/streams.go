package anivault

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"sync"

	"golang.org/x/sync/errgroup"

	"kari/internal/lang"
	"kari/internal/logging"
	"kari/internal/provider"
)

type playbackRequest struct {
	shortID string
	number  int
	audio   string
}

type playbackBackend struct {
	id   string
	name string
}

type apiServer struct {
	Name string `json:"name"`
	Type string `json:"type"`
	Lang string `json:"lang"`
}

// UnmarshalJSON accepts both server shapes exposed by the site's integrations.
func (s *apiServer) UnmarshalJSON(data []byte) error {
	if len(data) > 0 && data[0] == '"' {
		return json.Unmarshal(data, &s.Name)
	}
	type server apiServer
	return json.Unmarshal(data, (*server)(s))
}

// ResolveSource collects the same incremental results used by the preview.
func (c *Client) ResolveSource(ctx context.Context, mediaID string, episode provider.Episode) ([]provider.MediaSource, error) {
	sources := make([]provider.MediaSource, 0)
	err := c.resolve(ctx, mediaID, episode, func(batch []provider.MediaSource) error {
		sources = append(sources, batch...)
		return nil
	})
	return sources, err
}

// ResolveStream delivers working sources without waiting for slower backends.
// Batches are additive; a failed backend never withdraws another one's results.
func (c *Client) ResolveStream(
	ctx context.Context,
	mediaID string,
	episode provider.Episode,
	updates chan<- []provider.MediaSource,
) error {
	return c.resolve(ctx, mediaID, episode, func(batch []provider.MediaSource) error {
		select {
		case updates <- batch:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	})
}

func (c *Client) resolve(
	ctx context.Context,
	mediaID string,
	episode provider.Episode,
	emit func([]provider.MediaSource) error,
) error {
	request, err := c.playbackCoordinates(ctx, mediaID, episode)
	if err != nil {
		return fmt.Errorf("resolve anivault episode: %w", err)
	}
	backends := []playbackBackend{
		{id: "reanime", name: "ReAnime"},
		{id: "aniwaves", name: "AniWaves"},
		{id: "animenosub", name: "AnimeNoSub"},
		{id: "anizone", name: "AniZone"},
		{id: "animesalt", name: "AnimeSalt"},
		{id: "watchanimeworld", name: "WatchAnimeWorld"},
	}
	if request.audio == provider.AudioSub {
		backends = append(backends, playbackBackend{id: sourceAnimeHeaven, name: "AnimeHeaven"})
	}
	var mu sync.Mutex
	seen := make(map[string]bool)
	failures := make([]error, 0, len(backends))
	published := 0
	unavailable := 0
	publish := func(sources []provider.MediaSource) error {
		mu.Lock()
		defer mu.Unlock()
		batch := make([]provider.MediaSource, 0, len(sources))
		for _, source := range provider.FilterDirectSources(sources) {
			key := source.TransportIdentity()
			if key == "" || seen[key] {
				continue
			}
			seen[key] = true
			batch = append(batch, source)
		}
		if len(batch) == 0 {
			return nil
		}
		if err := emit(batch); err != nil {
			return err
		}
		published += len(batch)
		return nil
	}
	group, groupCtx := errgroup.WithContext(ctx)
	// Each backend owns at most one in-flight request, bounding fan-out to seven.
	for _, backend := range backends {
		group.Go(func() error {
			if err := c.resolveBackend(groupCtx, backend, request, publish); err != nil {
				logging.Debug("source backend failed", "provider", providerID, "backend", backend.id, "error", err)
				mu.Lock()
				if errors.Is(err, provider.ErrAudioUnavailable) {
					unavailable++
				} else {
					failures = append(failures, fmt.Errorf("%s: %w", backend.name, err))
				}
				mu.Unlock()
			}
			// Independent backend failures must not cancel working siblings.
			return nil
		})
	}
	_ = group.Wait()
	if published > 0 {
		return nil
	}
	if ctx.Err() != nil {
		return fmt.Errorf("resolve anivault sources: %w", ctx.Err())
	}
	if unavailable == len(backends) {
		return fmt.Errorf("resolve anivault audio: %w", provider.ErrAudioUnavailable)
	}
	return fmt.Errorf("resolve anivault sources: %w", errors.Join(provider.ErrNoSources, errors.Join(failures...)))
}

func (c *Client) playbackCoordinates(
	ctx context.Context,
	mediaID string,
	episode provider.Episode,
) (playbackRequest, error) {
	request := playbackRequest{number: episode.Episode, audio: strings.ToLower(strings.TrimSpace(episode.Audio))}
	parts := strings.Split(strings.TrimSpace(episode.ID), "/")
	owned := len(parts) == 6 && parts[0] == "watch" && parts[1] == providerID && parts[2] == "hoshika"
	if owned {
		if !validCatalogID(parts[3]) {
			return request, fmt.Errorf("invalid hoshika handle: %w", provider.ErrNotFound)
		}
		request.shortID = parts[3]
		number, err := strconv.Atoi(parts[5])
		if err != nil || number <= 0 {
			return request, fmt.Errorf("invalid episode handle: %w", provider.ErrNotFound)
		}
		request.number = number
		if request.audio == "" {
			request.audio = parts[4]
		}
	}
	if request.number <= 0 {
		return request, fmt.Errorf("invalid episode number: %w", provider.ErrNotFound)
	}
	if request.audio == "" {
		request.audio = provider.AudioSub
	}
	if request.audio != provider.AudioSub && request.audio != provider.AudioDub {
		return request, fmt.Errorf("unsupported audio %q: %w", request.audio, provider.ErrAudioUnavailable)
	}
	if request.shortID == "" {
		shortID, err := c.resolveTitle(ctx, mediaID)
		if err != nil {
			return request, err
		}
		request.shortID = shortID
	}
	return request, nil
}

func (c *Client) resolveBackend(
	ctx context.Context,
	backend playbackBackend,
	request playbackRequest,
	publish func([]provider.MediaSource) error,
) error {
	watch, err := c.fetchWatch(ctx, backend, request, apiServer{})
	if err != nil {
		return err
	}
	initial := backendSources(watch, backend)
	if err := publish(initial); err != nil {
		return err
	}
	seen := make(map[string]bool)
	failures := make([]error, 0)
	compatible := false
	for _, server := range watch.Servers {
		key := server.Name + "\x00" + server.Lang
		if !server.supports(request.audio) || seen[key] {
			continue
		}
		seen[key] = true
		compatible = true
		if len(initial) > 0 && server.Name == watch.Server {
			continue
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		resolved, err := c.fetchWatch(ctx, backend, request, server)
		if err != nil {
			failures = append(failures, err)
			continue
		}
		if resolved.Server == "" {
			resolved.Server = server.Name
		}
		if err := publish(backendSources(resolved, backend)); err != nil {
			return err
		}
	}
	if len(initial) == 0 && len(watch.Servers) > 0 && !compatible {
		return provider.ErrAudioUnavailable
	}
	return errors.Join(failures...)
}

func (s apiServer) supports(audio string) bool {
	if strings.TrimSpace(s.Name) == "" {
		return false
	}
	kind := strings.ToLower(strings.TrimSpace(s.Type))
	name := strings.ToLower(strings.TrimSpace(s.Name))
	if kind == "" {
		switch {
		case strings.HasPrefix(name, "sub"):
			kind = provider.AudioSub
		case strings.HasPrefix(name, "dub"):
			kind = provider.AudioDub
		}
	}
	language := lang.Normalize(s.Lang)
	if audio == provider.AudioDub {
		return kind != provider.AudioSub && kind != "raw" && (language == "" || language == "en")
	}
	return kind != provider.AudioDub && (language == "" || language == "ja")
}

func (c *Client) fetchWatch(
	ctx context.Context,
	backend playbackBackend,
	request playbackRequest,
	server apiServer,
) (watchResponse, error) {
	query := url.Values{
		"hoshikaId": {request.shortID},
		"ep":        {strconv.Itoa(request.number)},
		"audio":     {request.audio},
	}
	if server.Name != "" {
		query.Set("server", server.Name)
	}
	if server.Lang != "" {
		query.Set("lang", server.Lang)
	}
	body, err := c.get(ctx, "/api/"+backend.id+"_stream.php?"+query.Encode())
	if err != nil {
		return watchResponse{}, err
	}
	var watch watchResponse
	if err := json.Unmarshal(body, &watch); err != nil {
		return watchResponse{}, fmt.Errorf("decode stream: %w: %w", provider.ErrUpstreamChanged, err)
	}
	return watch, nil
}

func backendSources(watch watchResponse, backend playbackBackend) []provider.MediaSource {
	watch.Source = backend.id
	sources := watchSources(watch)
	for i := range sources {
		sources[i].Backend = backend.name
		if watch.Server != "" && !strings.EqualFold(watch.Server, backend.name) {
			sources[i].Backend += " · " + watch.Server
		}
	}
	return sources
}
