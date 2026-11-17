package anivault

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"kari/internal/provider"
)

type playbackRequest struct {
	shortID string
	number  int
	audio   string
}

// ResolveSource collects the sources resolved for an episode.
func (c *Client) ResolveSource(ctx context.Context, mediaID string, episode provider.Episode) ([]provider.MediaSource, error) {
	sources := make([]provider.MediaSource, 0)
	err := c.resolve(ctx, mediaID, episode, func(batch []provider.MediaSource) error {
		sources = append(sources, batch...)
		return nil
	})
	return sources, err
}

// ResolveStream delivers the resolved sources through updates.
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
	if playbackAudio(episode) == provider.AudioDub {
		return fmt.Errorf("resolve anivault sources: %w", provider.ErrNoSources)
	}
	request, err := c.playbackCoordinates(ctx, mediaID, episode)
	if err != nil {
		return fmt.Errorf("resolve anivault episode: %w", err)
	}
	watch, err := c.fetchAnimeHeavenStream(ctx, request)
	if err != nil {
		return fmt.Errorf("resolve anivault stream: %w", err)
	}
	watch.Source = sourceAnimeHeaven
	sources := provider.FilterDirectSources(watchSources(watch))
	for i := range sources {
		sources[i].Backend = backendAnimeHeaven
		if watch.Server != "" && !strings.EqualFold(watch.Server, backendAnimeHeaven) {
			sources[i].Backend += " · " + watch.Server
		}
	}
	if len(sources) == 0 {
		return fmt.Errorf("resolve anivault sources: %w", provider.ErrNoSources)
	}
	return emit(sources)
}

func (c *Client) playbackCoordinates(
	ctx context.Context,
	mediaID string,
	episode provider.Episode,
) (playbackRequest, error) {
	request := playbackRequest{number: episode.Episode, audio: playbackAudio(episode)}
	parts, owned := ownedWatchParts(episode)
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

// playbackAudio returns the episode's declared audio, preferring the explicit
// field and falling back to an owned watch handle's audio segment.
func playbackAudio(episode provider.Episode) string {
	if audio := strings.ToLower(strings.TrimSpace(episode.Audio)); audio != "" {
		return audio
	}
	parts, owned := ownedWatchParts(episode)
	if !owned {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(parts[4]))
}

// ownedWatchParts splits an AniVault watch handle and reports whether it is
// one this provider owns.
func ownedWatchParts(episode provider.Episode) ([]string, bool) {
	parts := strings.Split(strings.TrimSpace(episode.ID), "/")
	owned := len(parts) == 6 && parts[0] == "watch" && parts[1] == providerID && parts[2] == "hoshika"
	return parts, owned
}

func (c *Client) fetchAnimeHeavenStream(ctx context.Context, request playbackRequest) (watchResponse, error) {
	query := url.Values{
		"hoshikaId": {request.shortID},
		"ep":        {strconv.Itoa(request.number)},
		"audio":     {request.audio},
	}
	body, err := c.get(ctx, "/api/"+sourceAnimeHeaven+"_stream.php?"+query.Encode())
	if err != nil {
		return watchResponse{}, err
	}
	var watch watchResponse
	if err := json.Unmarshal(body, &watch); err != nil {
		return watchResponse{}, fmt.Errorf("decode stream: %w: %w", provider.ErrUpstreamChanged, err)
	}
	return watch, nil
}
