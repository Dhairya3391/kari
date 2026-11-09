package service

import (
	"bufio"
	"context"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"kari/internal/httpclient"
	"kari/internal/lang"
	"kari/internal/provider"
)

// discoverManifestAudio adds track labels advertised by an HLS master
// playlist. A short per-source deadline keeps optional metadata from holding
// up provider resolution for long.
func discoverManifestAudio(ctx context.Context, sources []provider.MediaSource) []provider.MediaSource {
	client := httpclient.NewWithTimeout(3 * time.Second)
	var wg sync.WaitGroup
	semaphore := make(chan struct{}, 8)
	for i := range sources {
		if sources[i].Type != provider.SourceTypeHLS && !isHLSURL(sources[i].URL) {
			continue
		}
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			select {
			case semaphore <- struct{}{}:
				defer func() { <-semaphore }()
			case <-ctx.Done():
				return
			}
			src := &sources[index]
			requestCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
			defer cancel()
			req, err := http.NewRequestWithContext(requestCtx, http.MethodGet, src.URL, nil)
			if err != nil {
				return
			}
			if src.Referer != "" {
				req.Header.Set("Referer", src.Referer)
			}
			if src.UserAgent != "" {
				req.Header.Set("User-Agent", src.UserAgent)
			}
			if src.CookieHeader != "" {
				req.Header.Set("Cookie", src.CookieHeader)
			}
			resp, err := client.Do(req)
			if err != nil {
				return
			}
			body, readErr := io.ReadAll(io.LimitReader(resp.Body, 256<<10))
			_ = resp.Body.Close()
			if readErr != nil || resp.StatusCode < 200 || resp.StatusCode >= 300 {
				return
			}
			if labels := hlsAudioLabels(string(body)); len(labels) > 0 {
				src.AudioLabel = strings.Join(labels, " · ")
			}
		}(i)
	}
	wg.Wait()
	return sources
}

func isHLSURL(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && strings.HasSuffix(strings.ToLower(u.Path), ".m3u8")
}

func hlsAudioLabels(manifest string) []string {
	labels := make(map[string]struct{})
	scanner := bufio.NewScanner(strings.NewReader(manifest))
	scanner.Buffer(make([]byte, 1024), 64<<10)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "#EXT-X-MEDIA:") || !strings.Contains(line, "TYPE=AUDIO") {
			continue
		}
		attrs := parseHLSAttributes(strings.TrimPrefix(line, "#EXT-X-MEDIA:"))
		name := strings.TrimSpace(attrs["NAME"])
		language := strings.TrimSpace(attrs["LANGUAGE"])
		if name == "" || strings.EqualFold(name, "audio") || strings.EqualFold(name, "unknown") {
			if language == "" || strings.EqualFold(language, "und") || strings.EqualFold(language, "unknown") {
				continue
			}
			name = lang.Name(language)
		}
		if name != "" {
			labels[name] = struct{}{}
		}
	}
	out := make([]string, 0, len(labels))
	for label := range labels {
		out = append(out, label)
	}
	sort.Strings(out)
	return out
}

func parseHLSAttributes(raw string) map[string]string {
	attrs := make(map[string]string)
	var key, value strings.Builder
	quoted, inValue := false, false
	flush := func() {
		if key.Len() > 0 {
			attrs[strings.TrimSpace(key.String())] = strings.Trim(strings.TrimSpace(value.String()), "\"")
		}
		key.Reset()
		value.Reset()
		inValue = false
	}
	for _, char := range raw {
		switch {
		case char == '"':
			quoted = !quoted
		case char == '=' && !quoted && !inValue:
			inValue = true
		case char == ',' && !quoted:
			flush()
		default:
			if inValue {
				value.WriteRune(char)
			} else {
				key.WriteRune(char)
			}
		}
	}
	flush()
	return attrs
}
