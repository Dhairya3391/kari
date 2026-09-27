package subtitles

import (
	"archive/zip"
	"bufio"
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"golang.org/x/text/encoding/charmap"

	"kari/internal/httpclient"
)

// FileExtension returns the local file extension for a normalized subtitle
// format returned by ProcessSubtitleData.
func FileExtension(format string) string {
	switch format {
	case "ass":
		return ".ass"
	case "vtt":
		return ".vtt"
	default:
		return ".srt"
	}
}

// ProcessSubtitleData unzips, converts encoding, and validates subtitle
// bytes. It rejects HTML, JSON, images, malformed archives, and unknown data.
func ProcessSubtitleData(data []byte) ([]byte, string, error) {
	if len(data) == 0 {
		return nil, "", fmt.Errorf("empty subtitle data")
	}
	if len(data) > httpclient.MaxBodyBytes {
		return nil, "", httpclient.ErrBodyTooLarge
	}
	if isImage(data) {
		return nil, "", fmt.Errorf("image data is not a subtitle")
	}
	if looksLikeHTML(data) {
		return nil, "", fmt.Errorf("HTML response is not a subtitle")
	}
	if looksLikeJSON(data) {
		return nil, "", fmt.Errorf("JSON response is not a subtitle")
	}

	if isGZIP(data) {
		decompressed, err := decompressGZIP(data)
		if err != nil {
			return nil, "", fmt.Errorf("decompress gzip subtitle: %w", err)
		}
		data = decompressed
	}

	if isZIP(data) {
		extracted, err := extractFromZIP(data)
		if err != nil {
			return nil, "", fmt.Errorf("extract zip subtitle: %w", err)
		}
		data = extracted
	}

	data = stripBOM(data)
	data = convertToUTF8(data)

	if isVTT(data) {
		converted, err := vttToSRT(data)
		if err != nil {
			return nil, "", fmt.Errorf("convert vtt subtitle: %w", err)
		}
		if len(converted) == 0 {
			return nil, "", fmt.Errorf("vtt subtitle contains no cues")
		}
		return converted, "srt-from-vtt", nil
	}

	detected := detectFormatByContent(data)
	if detected == "unknown" {
		return nil, "", fmt.Errorf("unsupported subtitle content")
	}
	return data, detected, nil
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
	text := string(data[:min(512, len(data))])
	return srtTimestampPattern.MatchString(text)
}

var srtTimestampPattern = regexp.MustCompile(`(?m)(?:^|\r?\n)\s*(?:\d+\s*)?\d{1,2}:\d{2}:\d{2}[,.]\d{3}\s*-->`)

func decompressGZIP(data []byte) ([]byte, error) {
	reader, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	decoded, err := io.ReadAll(io.LimitReader(reader, httpclient.MaxBodyBytes+1))
	if err != nil {
		return nil, err
	}
	if len(decoded) > httpclient.MaxBodyBytes {
		return nil, httpclient.ErrBodyTooLarge
	}
	return decoded, nil
}

func extractFromZIP(data []byte) ([]byte, error) {
	reader, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, err
	}
	for _, f := range reader.File {
		name := strings.ToLower(f.Name)
		if !strings.HasSuffix(name, ".srt") &&
			!strings.HasSuffix(name, ".vtt") &&
			!strings.HasSuffix(name, ".ass") &&
			!strings.HasSuffix(name, ".ssa") {
			continue
		}
		if f.UncompressedSize64 > httpclient.MaxBodyBytes {
			return nil, httpclient.ErrBodyTooLarge
		}
		rc, err := f.Open()
		if err != nil {
			continue
		}
		decoded, readErr := io.ReadAll(io.LimitReader(rc, httpclient.MaxBodyBytes+1))
		closeErr := rc.Close()
		if readErr != nil {
			continue
		}
		if closeErr != nil {
			continue
		}
		if len(decoded) > httpclient.MaxBodyBytes {
			return nil, httpclient.ErrBodyTooLarge
		}
		return decoded, nil
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
	scanner := bufio.NewScanner(bytes.NewReader(data))
	var (
		buf      bytes.Buffer
		inHeader = true
		curIndex = 1
	)
	buf.Grow(len(data))

	var (
		timeLine  string
		textLines []string
	)

	flushBlock := func() {
		if timeLine != "" && len(textLines) > 0 {
			if curIndex > 1 {
				buf.WriteString("\n\n")
			}
			buf.WriteString(strconv.Itoa(curIndex))
			buf.WriteByte('\n')
			buf.WriteString(timeLine)
			buf.WriteByte('\n')
			for j, tl := range textLines {
				if j > 0 {
					buf.WriteByte('\n')
				}
				buf.WriteString(tl)
			}
			curIndex++
		}
		timeLine = ""
		textLines = textLines[:0]
	}

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
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
			flushBlock()
			parts := strings.Split(line, "-->")
			if len(parts) == 2 {
				start := strings.TrimSpace(parts[0])
				endPart := strings.TrimSpace(parts[1])
				endFields := strings.Fields(endPart)
				if len(endFields) > 0 {
					end := endFields[0]
					timeLine = formatSRTTimestamp(start) + " --> " + formatSRTTimestamp(end)
				}
			}
			continue
		}

		if timeLine != "" {
			if line == "" {
				flushBlock()
			} else {
				cleaned := vttTagRegex.ReplaceAllString(line, "")
				cleaned = strings.TrimSpace(cleaned)
				if cleaned != "" {
					textLines = append(textLines, cleaned)
				}
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	flushBlock()

	if buf.Len() > 0 {
		buf.WriteByte('\n')
	}
	return buf.Bytes(), nil
}

func detectFormatByContent(data []byte) string {
	if isVTT(data) {
		return "vtt"
	}
	if isSRT(data) {
		return "srt"
	}
	if isASS(data) {
		return "ass"
	}
	return "unknown"
}

func isASS(data []byte) bool {
	text := strings.ToLower(string(data[:min(4096, len(data))]))
	return strings.Contains(text, "[script info]") ||
		strings.Contains(text, "[v4+ styles]") ||
		strings.Contains(text, "[v4 styles]") ||
		strings.Contains(text, "[events]")
}

func isImage(data []byte) bool {
	return len(data) >= 12 &&
		(string(data[:4]) == "RIFF" && string(data[8:12]) == "WEBP") ||
		(len(data) >= 8 && string(data[:8]) == "\x89PNG\r\n\x1a\n") ||
		(len(data) >= 3 && data[0] == 0xff && data[1] == 0xd8 && data[2] == 0xff) ||
		(len(data) >= 6 && (string(data[:6]) == "GIF87a" || string(data[:6]) == "GIF89a"))
}

func looksLikeHTML(data []byte) bool {
	text := strings.ToLower(strings.TrimSpace(string(data[:min(512, len(data))])))
	return strings.HasPrefix(text, "<!doctype html") ||
		strings.HasPrefix(text, "<html") ||
		strings.Contains(text, "<head") ||
		strings.Contains(text, "<body")
}

func looksLikeJSON(data []byte) bool {
	trimmed := strings.TrimSpace(string(data[:min(16, len(data))]))
	return strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[")
}
