package logging

import (
	"net/url"
	"strings"
)

// sensitiveQueryKeys are query parameter names whose values grant access to
// upstream media or identify a user. They are masked in every log record.
var sensitiveQueryKeys = map[string]struct{}{
	"access_token":  {},
	"api_key":       {},
	"apikey":        {},
	"auth":          {},
	"expires":       {},
	"hdnts":         {},
	"key":           {},
	"password":      {},
	"refresh_token": {},
	"secret":        {},
	"session":       {},
	"sig":           {},
	"signature":     {},
	"token":         {},
}

// Redact masks secrets in every URL embedded in s. It is applied to retry
// client messages and to the URL fields providers attach to log records, so a
// signed CDN link or an API key never reaches the log file.
func Redact(s string) string {
	var b strings.Builder
	pos := 0
	for {
		start, ok := nextURLStart(s, pos)
		if !ok {
			break
		}
		end := start
		for end < len(s) && !strings.ContainsRune(" \t\r\n\"'<>])},;", rune(s[end])) {
			end++
		}
		b.WriteString(s[pos:start])
		urlPart := s[start:end]
		trailing := ""
		for len(urlPart) > 0 && strings.ContainsRune(".,;!?", rune(urlPart[len(urlPart)-1])) {
			trailing = string(urlPart[len(urlPart)-1]) + trailing
			urlPart = urlPart[:len(urlPart)-1]
		}
		b.WriteString(redactURL(urlPart))
		b.WriteString(trailing)
		pos = end
	}
	b.WriteString(s[pos:])
	return b.String()
}

// nextURLStart returns the offset of the next http(s) scheme at or after from.
func nextURLStart(s string, from int) (int, bool) {
	httpAt := strings.Index(s[from:], "http://")
	httpsAt := strings.Index(s[from:], "https://")
	switch {
	case httpAt < 0 && httpsAt < 0:
		return 0, false
	case httpsAt < 0 || (httpAt >= 0 && httpAt < httpsAt):
		return from + httpAt, true
	default:
		return from + httpsAt, true
	}
}

// redactURL masks sensitive query values in one URL, leaving all other parts
// untouched. Unparsable URLs are returned as-is: a malformed string is not a
// credential, and mangling it would make diagnostics harder.
func redactURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.RawQuery == "" {
		return raw
	}
	q := u.Query()
	changed := false
	for key := range q {
		if _, sensitive := sensitiveQueryKeys[strings.ToLower(key)]; sensitive {
			q.Set(key, "REDACTED")
			changed = true
		}
	}
	if !changed {
		return raw
	}
	u.RawQuery = q.Encode()
	return u.String()
}
