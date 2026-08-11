package wco

import (
	"context"
	htmlpkg "html"
	"io"
	"net/http"

	"net/url"
	"sort"
	"strings"

	"kari/internal/config"
	"kari/internal/logging"
	"kari/internal/provider"
)

func (c *Client) FetchEpisodes(ctx context.Context, series provider.SearchResult) ([]provider.Episode, error) {
	mediaID := series.ID
	logging.Debugf("wco FetchEpisodes start mediaID=%q", mediaID)
	html, err := c.fetchSeriesPage(ctx, mediaID)
	if err != nil {
		logging.Errorf("wco FetchEpisodes fetchSeriesPage failed mediaID=%q err=%v", mediaID, err)
		return nil, err
	}

	var episodes []provider.Episode
	modernLinks := c.extractModernEpisodeLinks(html)
	if len(modernLinks) > 0 {
		episodes = buildEpisodesFromLinks(modernLinks, true)
	} else {
		start := strings.Index(html, `id="episodeList"`)
		searchBlock := html
		if start >= 0 {
			searchBlock = html[start:]
		}
		matches := episodeLinkRe.FindAllStringSubmatch(searchBlock, -1)
		links := make([]episodeLink, 0, len(matches))
		for _, m := range matches {
			url := htmlpkg.UnescapeString(strings.TrimSpace(m[1]))
			title := normalizeEpisodeTitle(m[2])
			if strings.Contains(url, "/anime/") || parseEpisodeNumber(title, url) <= 0 {
				continue
			}
			links = append(links, episodeLink{
				URL:        url,
				Title:      title,
				SeasonHint: seasonHintBefore(searchBlock, m[0]),
			})
		}
		episodes = buildEpisodesFromLinks(links, false)
	}
	if len(episodes) == 0 {
		logging.Warnf("wco FetchEpisodes found no episodes mediaID=%q", mediaID)
		return nil, provider.ErrNoEpisodes
	}

	sort.Slice(episodes, func(i, j int) bool {
		// Sort non-numbered episodes to the end
		if episodes[i].Episode <= 0 && episodes[j].Episode > 0 {
			return false
		}
		if episodes[i].Episode > 0 && episodes[j].Episode <= 0 {
			return true
		}
		// Sort by season
		if episodes[i].Season != episodes[j].Season {
			return episodes[i].Season < episodes[j].Season
		}
		// Sort by episode number
		if episodes[i].Episode != episodes[j].Episode {
			return episodes[i].Episode < episodes[j].Episode
		}
		// Sort by title
		return strings.ToLower(episodes[i].Title) < strings.ToLower(episodes[j].Title)
	})

	logging.Debugf("wco FetchEpisodes success mediaID=%q episodes=%d", mediaID, len(episodes))
	return episodes, nil
}

func (c *Client) fetchSeriesPage(ctx context.Context, seriesURL string) (string, error) {
	logging.Debugf("wco fetchSeriesPage url=%q", seriesURL)
	u, err := url.Parse(seriesURL)
	if err != nil {
		return "", err
	}
	q := u.Query()
	q.Set("season", "all")
	u.RawQuery = q.Encode()
	target := u.String()
	if !strings.Contains(target, "season=all") {
		target = strings.TrimRight(seriesURL, "/") + "/?season=all"
	}

	resp, err := c.doRequest(ctx, http.MethodGet, target, map[string]string{"Referer": config.BaseURL + "/"}, nil)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return "", &provider.HTTPError{Code: resp.StatusCode, URL: target}
	}
	raw, err := io.ReadAll(resp.Body)

	if err != nil {
		return "", err
	}
	return string(raw), nil
}

func (c *Client) fetchEpisodePage(ctx context.Context, episodeURL string) (string, error) {
	logging.Debugf("wco fetchEpisodePage url=%q", episodeURL)
	resp, err := c.doRequest(ctx, http.MethodGet, episodeURL, map[string]string{"Referer": config.BaseURL + "/"}, nil)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return "", &provider.HTTPError{Code: resp.StatusCode, URL: episodeURL}
	}
	raw, err := io.ReadAll(resp.Body)

	if err != nil {
		return "", err
	}
	return string(raw), nil
}
