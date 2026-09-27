// Megaplay helpers shared by the anime providers: AES-256-CBC token
// decryption for Megaplay getSources responses and HMAC-SHA256 CDN URL
// signing. The keys are global to the Megaplay infrastructure backing
// anikoto, anilight, and anicine's anime embeds.
package kit

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"
)

var (
	megaplayAESKey = []byte("i?LMTAx0Q6,:}50U\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00")
	megaplayAESIV  = []byte("W0;27ToaUpl_P%'c")
	cdnHMACSecret  = []byte("MpCdnT0k3n!9f2K#xQ7vL5mR8wN1pY4s")
	m3u8PathRegex  = regexp.MustCompile(`(?i)/([a-f0-9]{32})/([a-f0-9]{32})/`)
)

// MegaplaySourcesResp is the decrypted getSources payload shape: subtitle
// tracks, optional skip cues, and the encrypted stream token.
type MegaplaySourcesResp struct {
	Tracks []MegaplayTrack `json:"tracks"`
	Intro  *MegaplayCue    `json:"intro"`
	Outro  *MegaplayCue    `json:"outro"`
	Enc    string          `json:"enc"`
}

// MegaplayTrack is one subtitle/caption track from getSources.
type MegaplayTrack struct {
	File    string `json:"file"`
	Label   string `json:"label"`
	Kind    string `json:"kind"`
	Default bool   `json:"default"`
}

// MegaplayCue marks a skippable section boundary in seconds.
type MegaplayCue struct {
	Start float64 `json:"start"`
	End   float64 `json:"end"`
}

type decryptedMegaplayPayload struct {
	File string `json:"file"`
}

// DecryptMegaplayEnc decrypts the AES-256-CBC token from Megaplay
// getSources into the raw master playlist URL.
func DecryptMegaplayEnc(encStr string) (string, error) {
	b64Str := strings.ReplaceAll(encStr, "-", "+")
	b64Str = strings.ReplaceAll(b64Str, "_", "/")
	if pad := len(b64Str) % 4; pad != 0 {
		b64Str += strings.Repeat("=", 4-pad)
	}

	raw, err := base64.StdEncoding.DecodeString(b64Str)
	if err != nil {
		return "", fmt.Errorf("decode b64: %w", err)
	}

	block, err := aes.NewCipher(megaplayAESKey)
	if err != nil {
		return "", fmt.Errorf("aes cipher: %w", err)
	}

	if len(raw) < aes.BlockSize || len(raw)%aes.BlockSize != 0 {
		return "", fmt.Errorf("invalid ciphertext length: %d", len(raw))
	}

	mode := cipher.NewCBCDecrypter(block, megaplayAESIV)
	decrypted := make([]byte, len(raw))
	mode.CryptBlocks(decrypted, raw)

	unpadded, err := pkcs7Unpad(decrypted, aes.BlockSize)
	if err != nil {
		return "", fmt.Errorf("unpad: %w", err)
	}

	var payload decryptedMegaplayPayload
	if err := json.Unmarshal(unpadded, &payload); err != nil {
		return "", fmt.Errorf("unmarshal payload: %w", err)
	}
	return payload.File, nil
}

func pkcs7Unpad(data []byte, blockSize int) ([]byte, error) {
	if len(data) == 0 {
		return nil, fmt.Errorf("empty data")
	}
	padding := int(data[len(data)-1])
	if padding == 0 || padding > blockSize || padding > len(data) {
		return nil, fmt.Errorf("invalid padding byte: %d", padding)
	}
	for i := len(data) - padding; i < len(data); i++ {
		if data[i] != byte(padding) {
			return nil, fmt.Errorf("padding mismatch")
		}
	}
	return data[:len(data)-padding], nil
}

// SignCDNURL generates a signed master.m3u8 URL with HMAC-SHA256 token (24h TTL).
func SignCDNURL(rawM3U8 string, ttlSecs int64) string {
	matches := m3u8PathRegex.FindStringSubmatch(rawM3U8)
	if len(matches) < 3 {
		return rawM3U8
	}

	pathKey := fmt.Sprintf("%s/%s", strings.ToLower(matches[1]), strings.ToLower(matches[2]))
	exp := time.Now().Unix() + ttlSecs
	msg := fmt.Sprintf("%d|%s", exp, pathKey)

	prefix := base64.RawURLEncoding.EncodeToString([]byte(msg))

	mac := hmac.New(sha256.New, cdnHMACSecret)
	mac.Write([]byte(msg))
	sig := mac.Sum(nil)
	suffix := base64.RawURLEncoding.EncodeToString(sig)

	token := fmt.Sprintf("%s.%s", prefix, suffix)
	sep := "?"
	if strings.Contains(rawM3U8, "?") {
		sep = "&"
	}
	return fmt.Sprintf("%s%stoken=%s", rawM3U8, sep, token)
}
