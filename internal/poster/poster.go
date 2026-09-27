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

	"kari/internal/config"
	"kari/internal/httpclient"
	"kari/internal/logging"
	"kari/internal/tmdb"
	"kari/internal/util"

	"golang.org/x/sync/singleflight"
)

// log scopes every line from this package with its identity.
var posterLog = logging.With("component", "poster")

// tmdbPosterSize is the full-resolution original for every TMDB poster.
// Anything smaller risks a silent downgrade through a media-type mismatch
// upstream of here; the disk/memory caches make the bigger download once,
// and pixel-level terminals (kitty/sixel) show every pixel of it.
const tmdbPosterSize = "original"

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
	http       *http.Client
	keyPool    *tmdb.KeyPool
	anilistURL string
	tmdbBase   string // overrides config.TMDBAPIBase in tests

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
	key := cacheKey(tmdbID, title, "")

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
// tmdbID is non-zero it is looked up on TMDB at full original
// resolution; otherwise title is looked up on AniList (extraLarge).
// Results are cached in-memory for the process lifetime and on disk
// under ~/.config/kari/posters so repeat runs skip the network.
func (c *Client) FetchImage(ctx context.Context, tmdbID int, mediaType, title string) (image.Image, error) {
	key := cacheKey(tmdbID, title, tmdbPosterSize)

	if img, ok := c.imgCache.Get(key); ok {
		return img, nil
	}

	if data, err := readDiskCache(key); err == nil {
		if img, _, derr := image.Decode(bytes.NewReader(data)); derr == nil {
			c.imgCache.Set(key, img)
			return img, nil
		}
		// Disk entry is truncated/corrupt (e.g. killed mid-write before
		// atomic writes): fall through and re-download rather than
		// poisoning every future run.
	}

	posterURL, uerr := c.resolveURL(ctx, tmdbID, mediaType, title)
	if uerr != nil {
		return nil, uerr
	}
	if posterURL == "" {
		return nil, fmt.Errorf("poster: no artwork available")
	}
	data, err := c.download(ctx, posterURL)
	if err != nil {
		return nil, err
	}
	if werr := writeDiskCache(key, data); werr != nil {
		posterLog.Debug("poster disk cache write failed", "key", key, "err", werr)
	}

	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("poster: decode failed: %w", err)
	}

	c.imgCache.Set(key, img)
	return img, nil
}

// FetchImageURL returns the decoded image at a direct artwork URL —
// manga covers stamped onto SearchResult.CoverURL by providers at search
// time, which resolve to no TMDB/AniList catalog. It shares the same
// in-memory and on-disk caches as FetchImage under a URL-derived key.
// referer travels with the download when the image host enforces
// hotlink protection (stamped as SearchResult.CoverReferer); empty means
// no Referer override beyond the caller default.
func (c *Client) FetchImageURL(ctx context.Context, rawURL, referer string) (image.Image, error) {
	sum := sha1.Sum([]byte(strings.ToLower(strings.TrimSpace(rawURL))))
	key := "url-" + hex.EncodeToString(sum[:])

	if img, ok := c.imgCache.Get(key); ok {
		return img, nil
	}

	if data, err := readDiskCache(key); err == nil {
		if img, _, derr := image.Decode(bytes.NewReader(data)); derr == nil {
			c.imgCache.Set(key, img)
			return img, nil
		}
		// Corrupt disk entry: re-download below instead of failing
		// permanently.
	}

	if referer == "" {
		referer = config.WeebCentralReferer
	}
	data, err := c.downloadReferer(ctx, rawURL, referer)
	if err != nil {
		return nil, err
	}
	if werr := writeDiskCache(key, data); werr != nil {
		posterLog.Debug("poster disk cache write failed", "key", key, "err", werr)
	}

	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("poster: decode failed: %w", err)
	}

	c.imgCache.Set(key, img)
	return img, nil
}

// resolveURL maps catalog media to its poster URL via TMDB or AniList.
func (c *Client) resolveURL(ctx context.Context, tmdbID int, mediaType, title string) (string, error) {
	if tmdbID != 0 {
		return c.tmdbPosterURL(ctx, tmdbID, mediaType)
	}
	return c.anilistPosterURL(ctx, title)
}

func (c *Client) download(ctx context.Context, target string) ([]byte, error) {
	return c.downloadReferer(ctx, target, "https://anilist.co/")
}

// downloadReferer fetches raw bytes with a caller-chosen Referer. The
// Referer is load-bearing, not decorative: image CDNs serve
// placeholders to foreign referers, so direct manga covers must travel
// with their source site as Referer while catalog art keeps the
// AniList one.
func (c *Client) downloadReferer(ctx context.Context, target, referer string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", config.DesktopUserAgent)
	req.Header.Set("Referer", referer)
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

func cacheKey(tmdbID int, title, size string) string {
	if tmdbID != 0 {
		// The size rides in the key so a size upgrade (w500 → original)
		// never serves stale smaller art from either cache.
		if size == "" {
			size = tmdbPosterSize
		}
		return fmt.Sprintf("tmdb-%d-%s", tmdbID, size)
	}
	sum := sha1.Sum([]byte(strings.ToLower(strings.TrimSpace(title))))
	return "anilist-" + hex.EncodeToString(sum[:])
}

func diskCachePath(key string) (string, error) {
	dir, err := diskCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, key+".img"), nil
}

// diskCacheDir resolves (creating) the on-disk poster cache.
func diskCacheDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		home = os.Getenv("HOME")
	}
	baseDir := filepath.Join(home, ".config", "kari")
	newDir := filepath.Join(baseDir, "cache", "posters")
	legacyDir := filepath.Join(baseDir, "posters")

	if _, err := os.Stat(newDir); err == nil {
		return newDir, nil
	}
	if _, err := os.Stat(legacyDir); err == nil {
		_ = os.MkdirAll(filepath.Dir(newDir), 0o755)
		if err := os.Rename(legacyDir, newDir); err == nil {
			return newDir, nil
		}
	}
	if err := os.MkdirAll(newDir, 0o755); err != nil {
		return "", err
	}
	return newDir, nil
}

// maxDiskCacheBytes caps the on-disk poster cache; oldest posters are
// evicted at startup past it.
const maxDiskCacheBytes = 64 << 20

// PruneDiskCache evicts the oldest cached posters until the on-disk
// poster cache fits its cap. Best-effort: call once at startup.
func PruneDiskCache() error {
	dir, err := diskCacheDir()
	if err != nil {
		return err
	}
	removed, err := util.PruneDirToSize(dir, maxDiskCacheBytes)
	if err != nil {
		return err
	}
	if removed > 0 {
		posterLog.Debug("pruned poster cache", "removed", removed)
	}
	return nil
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
	return util.AtomicWriteFile(path, data, 0o644)
}
