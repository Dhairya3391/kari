package subtitles

import (
	"archive/zip"
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"regexp"
	"strings"
	"unicode/utf8"

	"golang.org/x/text/encoding/charmap"

	"kari/internal/logging"
)

func (c *Client) processSubtitleData(data []byte) ([]byte, string) {
	if len(data) < 2 {
		return data, detectFormatByContent(data)
	}

	if isGZIP(data) {
		decompressed, err := decompressGZIP(data)
		if err != nil {
			logging.Debugf("gzip decompression failed: %v", err)
			return data, "gzip-failed"
		}
		data = decompressed
		logging.Debugf("gzip decompressed, size: %d", len(data))
	}

	if isZIP(data) {
		extracted, err := extractFromZIP(data)
		if err != nil {
			logging.Debugf("zip extraction failed: %v", err)
			return data, "zip-failed"
		}
		data = extracted
		logging.Debugf("zip extracted, size: %d", len(data))
	}

	data = stripBOM(data)

	data = convertToUTF8(data)

	if isVTT(data) {
		converted, err := vttToSRT(data)
		if err != nil {
			logging.Debugf("vtt to srt conversion failed: %v", err)
			return data, "vtt-convert-failed"
		}
		logging.Debugf("converted vtt to srt")
		return converted, "srt-from-vtt"
	}

	detected := detectFormatByContent(data)
	return data, detected
}

func isGZIP(data []byte) bool {
	return len(data) >= 3 && data[0] == 0x1f && data[1] == 0x8b && data[2] == 0x08
}

func isZIP(data []byte) bool {
	return len(data) >= 2 && data[0] == 0x50 && data[1] == 0x4b
}

func isVTT(data []byte) bool {
	if len(data) < 6 {
		return false
	}
	header := strings.TrimSpace(string(data[:min(20, len(data))]))
	return strings.HasPrefix(header, "WEBVTT")
}

func isSRT(data []byte) bool {
	if len(data) < 10 {
		return false
	}
	text := string(data[:min(100, len(data))])
	return srtTimestampPattern.MatchString(text)
}

var srtTimestampPattern = regexp.MustCompile(`^\d+[\r\n]+\d{2}:\d{2}:\d{2},\d{3}`)
var srtIndexPattern = regexp.MustCompile(`^\d+$`)

func decompressGZIP(data []byte) ([]byte, error) {
	reader, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	return io.ReadAll(reader)
}

func extractFromZIP(data []byte) ([]byte, error) {
	reader, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, err
	}
	for _, f := range reader.File {
		name := strings.ToLower(f.Name)
		if strings.HasSuffix(name, ".srt") || strings.HasSuffix(name, ".vtt") || strings.HasSuffix(name, ".ass") {
			rc, err := f.Open()
			if err != nil {
				continue
			}
			data, err := io.ReadAll(rc)
			rc.Close()
			if err != nil {
				continue
			}
			return data, nil
		}
	}
	return nil, fmt.Errorf("no subtitle found in zip")
}

func stripBOM(data []byte) []byte {
	if len(data) >= 3 && data[0] == 0xef && data[1] == 0xbb && data[2] == 0xbf {
		return data[3:]
	}
	return data
}

func convertToUTF8(data []byte) []byte {
	if utf8.Valid(data) {
		return data
	}

	decoded, err := charmap.ISO8859_1.NewDecoder().Bytes(data)
	if err == nil && utf8.Valid(decoded) {
		return decoded
	}

	text := string(data)
	cleaned := strings.Map(func(r rune) rune {
		if r == '\uFFFD' {
			return -1
		}
		if r < 32 && r != '\n' && r != '\r' && r != '\t' {
			return -1
		}
		return r
	}, text)

	return []byte(cleaned)
}

func vttToSRT(data []byte) ([]byte, error) {
	text := string(data)
	text = strings.ReplaceAll(text, "WEBVTT", "")
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")

	var lines []string
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.Contains(line, " --> ") {
			line = strings.ReplaceAll(line, ".", ",")
		}
		lines = append(lines, line)
	}

	if len(lines) > 0 {
		lines = normalizeSRTIndices(lines)
	}

	return []byte(strings.Join(lines, "\n")), nil
}

func normalizeSRTIndices(lines []string) []string {
	var result []string
	counter := 1
	for i := 0; i < len(lines); i++ {
		line := lines[i]
		if srtIndexPattern.MatchString(line) {
			result = append(result, fmt.Sprintf("%d", counter))
			counter++
		} else {
			result = append(result, line)
		}
	}
	return result
}

func detectFormatByContent(data []byte) string {
	if isVTT(data) {
		return "vtt"
	}
	if isSRT(data) {
		return "srt"
	}
	text := string(data[:min(50, len(data))])
	if strings.Contains(text, "[Script Info]") || strings.Contains(text, "[Events]") {
		return "ass"
	}
	return "unknown"
}
