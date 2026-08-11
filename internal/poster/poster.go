// Package poster resolves and fetches poster artwork for search results and
// resolved media: TMDB for movies/TV (including Western cartoons, which TMDB
// catalogs like any other movie/show), and AniList for anime, which has no
// TMDB ID to key off of.
package poster

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"kari/internal/httpclient"
	"kari/internal/logging"
	"kari/internal/tmdb"
	"kari/internal/util"

	"golang.org/x/sync/singleflight"
)

const tmdbPosterSize = "w500"

// maxCachedImages/maxCachedDetails cap how many distinct titles' artwork and
// descriptions stay in memory at once, so a long session that browses many
// titles doesn't grow without limit. Decoded images are the heavier of the
// two (a 500x750 poster is a few MB as raw pixels), hence the smaller cap.
const (
	maxCachedImages  = 40
	maxCachedDetails = 150
)

// Details holds the descriptive text shown alongside a poster — the plot
// summary and genre list — separate from the artwork itself since it's
// plain text with no rendering/caching concerns of its own.
type Details struct {
	Overview string
	Genres   []string
	Rating   string
}

// Client fetches and caches poster images from TMDB and AniList.
type Client struct {
	http    *http.Client
	keyPool *tmdb.KeyPool

	imgCache     *util.BoundedCache[image.Image]
	detailsCache *util.BoundedCache[Details]

	// rawCache and sf dedupe the underlying TMDB "/movie|tv/{id}" fetch: a
	// title's poster (FetchImage) and its overview/genres (FetchDetails) are
	// both derived from the same TMDB response, but arrive as two separate
	// concurrent calls (see triggerPreviewPoster/triggerPreviewDetails in the
	// TUI). rawCache remembers that response by (tmdbID, mediaType) so the
	// second caller reuses it instead of re-fetching, and sf collapses the
	// two calls into a single in-flight HTTP request when they race.
	rawCache *util.BoundedCache[tmdbDetails]
	sf       singleflight.Group
}

// NewClient builds a poster Client. keyPool is the same TMDB key pool used
// elsewhere in the app; it may be nil if only anime posters will be fetched.
func NewClient(keyPool *tmdb.KeyPool) *Client {
	return &Client{
		http:         httpclient.New(),
		keyPool:      keyPool,
		imgCache:     util.NewBoundedCache[image.Image](maxCachedImages),
		detailsCache: util.NewBoundedCache[Details](maxCachedDetails),
		rawCache:     util.NewBoundedCache[tmdbDetails](maxCachedDetails),
	}
}

// FetchDetails returns the plot overview and genres for the given media,
// dispatching to TMDB or AniList the same way FetchImage does. It's cached
// in-memory (not on disk — it's cheap text, not worth the extra I/O) and
// independent of the poster image, so a missing/failed poster doesn't
// prevent showing a description and vice versa.
func (c *Client) FetchDetails(ctx context.Context, tmdbID int, mediaType, title string) (Details, error) {
	key := cacheKey(tmdbID, title)

	if d, ok := c.detailsCache.Get(key); ok {
		return d, nil
	}

	var (
		details Details
		err     error
	)
	if tmdbID != 0 {
		details, err = c.tmdbDetailsInfo(ctx, tmdbID, mediaType)
	} else {
		details, err = c.anilistDetailsInfo(ctx, title)
	}
	if err != nil {
		return Details{}, err
	}

	c.detailsCache.Set(key, details)
	return details, nil
}

// FetchImage returns a decoded poster image for the given media. When
// tmdbID is non-zero it is looked up on TMDB; otherwise title is looked up
// on AniList. Results are cached in-memory for the process lifetime and on
// disk under ~/.config/kari/posters so repeat runs skip the network.
func (c *Client) FetchImage(ctx context.Context, tmdbID int, mediaType, title string) (image.Image, error) {
	key := cacheKey(tmdbID, title)

	if img, ok := c.imgCache.Get(key); ok {
		return img, nil
	}

	data, err := readDiskCache(key)
	if err != nil {
		posterURL, uerr := c.resolveURL(ctx, tmdbID, mediaType, title)
		if uerr != nil {
			return nil, uerr
		}
		if posterURL == "" {
			return nil, fmt.Errorf("poster: no artwork available")
		}
		data, err = c.download(ctx, posterURL)
		if err != nil {
			return nil, err
		}
		if werr := writeDiskCache(key, data); werr != nil {
			logging.Debugf("poster: failed to write disk cache for %q: %v", key, werr)
		}
	}

	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("poster: decode failed: %w", err)
	}

	c.imgCache.Set(key, img)
	return img, nil
}

func (c *Client) resolveURL(ctx context.Context, tmdbID int, mediaType, title string) (string, error) {
	if tmdbID != 0 {
		return c.tmdbPosterURL(ctx, tmdbID, mediaType)
	}
	return c.anilistPosterURL(ctx, title)
}

func (c *Client) download(ctx context.Context, target string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("poster: download status %d", resp.StatusCode)
	}
	return io.ReadAll(resp.Body)
}

func cacheKey(tmdbID int, title string) string {
	if tmdbID != 0 {
		return fmt.Sprintf("tmdb-%d", tmdbID)
	}
	sum := sha1.Sum([]byte(strings.ToLower(strings.TrimSpace(title))))
	return "anilist-" + hex.EncodeToString(sum[:])
}

func diskCachePath(key string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(home, ".config", "kari", "posters")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	return filepath.Join(dir, key+".img"), nil
}

func readDiskCache(key string) ([]byte, error) {
	path, err := diskCachePath(key)
	if err != nil {
		return nil, err
	}
	return os.ReadFile(path)
}

func writeDiskCache(key string, data []byte) error {
	path, err := diskCachePath(key)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}
