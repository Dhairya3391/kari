package piratex

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"kari/internal/config"
	"kari/internal/logging"
	"kari/internal/util"
)

// bysePlayback is the encrypted playback payload the byse embed exposes at
// GET {host}/api/videos/{code}. The master URL is AES-GCM encrypted: the AES
// key is the concatenation of a version-selected subset of key_parts, and the
// iv/payload are URL-safe base64. Decryption mirrors the site's JS bundle.
type bysePlayback struct {
	Version  any      `json:"version"`
	KeyParts []string `json:"key_parts"`
	IV       string   `json:"iv"`
	Payload  string   `json:"payload"`
}

type byseSources struct {
	Sources []struct {
		URL    string `json:"url"`
		Height int    `json:"height"`
		Label  string `json:"label"`
	} `json:"sources"`
}

// resolveByseEmbed finds an index11 byse embed on the episode page and resolves
// its merged HLS master. Returns false when the page carries no byse embed or
// any step of the chain fails (the transport is optional — as-cdn may suffice).
func (c *Client) resolveByseEmbed(ctx context.Context, page, episodeURL string) (*resolvedStream, []audioTrack, []subtitleTrack, bool) {
	m := rePlayer11.FindStringSubmatch(page)
	if len(m) < 2 {
		return nil, nil, nil, false
	}
	playerURL := fmt.Sprintf("%s/public/player/index11.php?id=%s", baseURL, m[1])

	for attempt := 0; attempt < 2; attempt++ {
		body, status, err := c.getBytes(ctx, playerURL, map[string]string{"Referer": episodeURL})
		if err != nil || status != http.StatusOK {
			return nil, nil, nil, false
		}
		emb := reByseEmbed.FindStringSubmatch(string(body))
		if len(emb) < 2 {
			return nil, nil, nil, false
		}
		code := emb[1]
		host := strings.Split(emb[0], "/e/")[0]
		if host == "" {
			continue
		}

		apiURL := host + "/api/videos/" + code
		apiBody, apiStatus, err := c.getBytes(ctx, apiURL, map[string]string{"Referer": playerURL})
		if err != nil || apiStatus != http.StatusOK {
			continue
		}
		var apiResp struct {
			Playback bysePlayback `json:"playback"`
		}
		if err := json.Unmarshal(apiBody, &apiResp); err != nil || apiResp.Playback.Payload == "" {
			continue
		}

		plain, err := decryptByse(apiResp.Playback)
		if err != nil {
			logging.Debugf("piratex byse decrypt failed: %v", err)
			continue
		}
		var sources byseSources
		if err := json.Unmarshal(plain, &sources); err != nil || len(sources.Sources) == 0 {
			continue
		}
		masterURL := strings.TrimSpace(sources.Sources[0].URL)
		if masterURL == "" {
			continue
		}

		referer := host + "/e/" + code + "/"
		audio, subs := parseMediaTracks(ctx, c, masterURL, map[string]string{"Referer": referer})

		return &resolvedStream{
			URL:       masterURL,
			Server:    "byse",
			Height:    sources.Sources[0].Height,
			Label:     sources.Sources[0].Label,
			Master:    true,
			Referer:   referer,
			UserAgent: config.DesktopUserAgent,
		}, audio, subs, true
	}
	return nil, nil, nil, false
}

// parseMediaTracks reads the EXT-X-MEDIA audio/subtitle groups a byse master
// declares, so the player can list/select languages and offer subtitle tracks.
func parseMediaTracks(ctx context.Context, c *Client, masterURL string, headers map[string]string) ([]audioTrack, []subtitleTrack) {
	var audio []audioTrack
	var subs []subtitleTrack
	masterBody, status, err := c.getBytes(ctx, masterURL, headers)
	if err != nil || status != http.StatusOK {
		return audio, subs
	}
	for _, line := range strings.Split(string(masterBody), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "#EXT-X-MEDIA:") {
			continue
		}
		attrs := parseAttrs(strings.TrimPrefix(line, "#EXT-X-MEDIA:"))
		if attrs["TYPE"] == "" || attrs["URI"] == "" {
			continue
		}
		name := util.NormalizeSpace(attrs["NAME"])
		if name == "" {
			name = attrs["LANGUAGE"]
		}
		switch attrs["TYPE"] {
		case "AUDIO":
			audio = append(audio, audioTrack{Language: attrs["LANGUAGE"], Name: name, URL: absoluteURL(masterURL, attrs["URI"])})
		case "SUBTITLES":
			subs = append(subs, subtitleTrack{Language: attrs["LANGUAGE"], Name: name, URL: absoluteURL(masterURL, attrs["URI"])})
		}
	}
	return audio, subs
}

func decryptByse(p bysePlayback) ([]byte, error) {
	parts := byseKeyParts(p.Version, p.KeyParts)
	var key []byte
	for _, part := range parts {
		b, err := b64u(part)
		if err != nil {
			return nil, fmt.Errorf("byse key part: %w", err)
		}
		key = append(key, b...)
	}
	iv, err := b64u(p.IV)
	if err != nil {
		return nil, fmt.Errorf("byse iv: %w", err)
	}
	ct, err := b64u(p.Payload)
	if err != nil {
		return nil, fmt.Errorf("byse payload: %w", err)
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("byse aes: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("byse gcm: %w", err)
	}
	plain, err := gcm.Open(nil, iv, ct, nil)
	if err != nil {
		return nil, fmt.Errorf("byse decrypt: %w", err)
	}
	return plain, nil
}

// byseKeyParts selects which key_parts form the AES key, mirroring ws() in the
// site's JS bundle: a version in [1,20] picks parts[v-1] and parts[31-v].
func byseKeyParts(version any, parts []string) []string {
	v := toInt(version)
	if v < 1 || v > 20 {
		return parts
	}
	var sel []string
	for _, o := range [2]int{v, 31 - v} {
		if o >= 1 && o <= len(parts) && parts[o-1] != "" {
			sel = append(sel, parts[o-1])
		}
	}
	if len(sel) == 0 {
		return parts
	}
	return sel
}

// b64u decodes the URL-safe base64 used by the byse payload (same normalization
// the Python reference applied: - → +, _ → /, then standard padding).
func b64u(s string) ([]byte, error) {
	s = strings.ReplaceAll(s, "-", "+")
	s = strings.ReplaceAll(s, "_", "/")
	if m := len(s) % 4; m != 0 {
		s += strings.Repeat("=", 4-m)
	}
	return base64.StdEncoding.DecodeString(s)
}
