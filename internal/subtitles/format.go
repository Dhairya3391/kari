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

// log scopes every line from this package.
var subLog = logging.With("component", "subtitles")

// ProcessSubtitleData unzips, converts encoding, and normalizes subtitle bytes into UTF-8 SRT/text.
func ProcessSubtitleData(data []byte) ([]byte, string) {
	if len(data) < 2 {
		return data, detectFormatByContent(data)
	}

	if isGZIP(data) {
		decompressed, err := decompressGZIP(data)
		if err != nil {
			subLog.Debug("gzip decompression failed", "err", err)
			return data, "gzip-failed"
		}
		data = decompressed
		subLog.Debug("gzip decompressed", "size", len(data))
	}

	if isZIP(data) {
		extracted, err := extractFromZIP(data)
		if err != nil {
			subLog.Debug("zip extraction failed", "err", err)
			return data, "zip-failed"
		}
		data = extracted
		subLog.Debug("zip extracted", "size", len(data))
	}

	data = stripBOM(data)

	data = convertToUTF8(data)

	if isVTT(data) {
		converted, err := vttToSRT(data)
		if err != nil {
			subLog.Debug("vtt-to-srt conversion failed", "err", err)
			return data, "vtt-convert-failed"
		}
		subLog.Debug("converted vtt to srt")
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

var vttTagRegex = regexp.MustCompile(`<[^>]+>`)

func formatSRTTimestamp(ts string) string {
	ts = strings.TrimSpace(ts)
	ts = strings.ReplaceAll(ts, ".", ",")
	if strings.Count(ts, ":") == 1 {
		ts = "00:" + ts
	}
	return ts
}

func vttToSRT(data []byte) ([]byte, error) {
	text := string(data)
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")

	var (
		blocks   []string
		inHeader = true
		curIndex = 1
	)

	lines := strings.Split(text, "\n")
	for i := 0; i < len(lines); i++ {
		line := strings.TrimSpace(lines[i])
		if inHeader {
			if strings.HasPrefix(line, "WEBVTT") || strings.HasPrefix(line, "NOTE") || strings.HasPrefix(line, "STYLE") || strings.HasPrefix(line, "REGION") {
				continue
			}
			if line == "" {
				inHeader = false
				continue
			}
			if strings.Contains(line, "-->") {
				inHeader = false
			} else {
				continue
			}
		}

		if strings.Contains(line, "-->") {
			parts := strings.Split(line, "-->")
			if len(parts) != 2 {
				continue
			}
			start := strings.TrimSpace(parts[0])
			endPart := strings.TrimSpace(parts[1])
			endFields := strings.Fields(endPart)
			if len(endFields) == 0 {
				continue
			}
			end := endFields[0]

			timeLine := formatSRTTimestamp(start) + " --> " + formatSRTTimestamp(end)

			var textLines []string
			for i+1 < len(lines) {
				nextLine := strings.TrimSpace(lines[i+1])
				if nextLine == "" {
					i++
					break
				}
				if strings.Contains(nextLine, "-->") {
					break
				}
				cleaned := vttTagRegex.ReplaceAllString(nextLine, "")
				cleaned = strings.TrimSpace(cleaned)
				if cleaned != "" {
					textLines = append(textLines, cleaned)
				}
				i++
			}

			if len(textLines) > 0 {
				block := fmt.Sprintf("%d\n%s\n%s", curIndex, timeLine, strings.Join(textLines, "\n"))
				blocks = append(blocks, block)
				curIndex++
			}
		}
	}

	return []byte(strings.Join(blocks, "\n\n") + "\n"), nil
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
