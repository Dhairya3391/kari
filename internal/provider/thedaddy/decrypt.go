package thedaddy

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
)

// econfigParts is the number of chunks the embedded config is split into.
const econfigParts = 4

// econfigOrder is the order the chunks must be re-joined in. The fourth
// character of each chunk is padding deliberately inserted by the site.
var econfigOrder = [econfigParts]int{2, 0, 3, 1}

// econfigStream is the decrypted player configuration.
type econfigStream struct {
	StreamURL      string `json:"stream_url"`
	StreamURLNoP2P string `json:"stream_url_nop2p"`
}

// playableURL prefers the plain CDN URL. mpv cannot join the site's P2P
// swarm, so a P2P-only config is still probed through its regular stream URL.
func (e econfigStream) playableURL() string {
	// The non-P2P URL is always a plain CDN playlist, so it wins whenever it
	// exists; the P2P stream URL is kept as the last resort.
	if e.StreamURLNoP2P != "" {
		return e.StreamURLNoP2P
	}
	return e.StreamURL
}

// decryptEconfig decodes the base64 `window._econfig` payload into its JSON
// configuration. It returns nil when the payload is absent or malformed.
func decryptEconfig(payload string) (*econfigStream, error) {
	payload = strings.TrimSpace(payload)
	if payload == "" {
		return nil, fmt.Errorf("empty econfig")
	}
	raw, err := decodeBase64(payload)
	if err != nil {
		return nil, fmt.Errorf("decode econfig: %w", err)
	}
	chunkSize := (len(raw) + econfigParts - 1) / econfigParts
	if chunkSize <= 4 {
		return nil, fmt.Errorf("econfig too short")
	}

	ordered := make([][]byte, econfigParts)
	for i := 0; i < econfigParts; i++ {
		start := i * chunkSize
		end := start + chunkSize
		if start >= len(raw) {
			break
		}
		if end > len(raw) {
			end = len(raw)
		}
		chunk := raw[start:end]
		// Drop the fourth character, then base64-decode the remainder.
		if len(chunk) < 4 {
			continue
		}
		piece := append(append([]byte{}, chunk[:3]...), chunk[4:]...)
		decoded, decodeErr := decodeBase64(string(piece))
		if decodeErr != nil {
			return nil, fmt.Errorf("decode econfig chunk %d: %w", i, decodeErr)
		}
		ordered[econfigOrder[i]] = decoded
	}

	var joined []byte
	for _, part := range ordered {
		joined = append(joined, part...)
	}
	jsonBytes, err := decodeBase64(string(joined))
	if err != nil {
		return nil, fmt.Errorf("decode econfig json: %w", err)
	}
	var config econfigStream
	if err := json.Unmarshal(jsonBytes, &config); err != nil {
		return nil, fmt.Errorf("parse econfig: %w", err)
	}
	if config.StreamURL == "" && config.StreamURLNoP2P == "" {
		return nil, fmt.Errorf("econfig has no stream URL")
	}
	return &config, nil
}

// decodeBase64 accepts both padded and raw standard encodings.
func decodeBase64(value string) ([]byte, error) {
	value = strings.TrimRight(value, "=")
	decoded, err := base64.RawStdEncoding.DecodeString(value)
	if err != nil {
		return nil, err
	}
	return decoded, nil
}
