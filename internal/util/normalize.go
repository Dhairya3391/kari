package util

import (
	"regexp"
	"strings"
)

var reTag = regexp.MustCompile(`(?s)<[^>]+>`)

func NormalizeSpace(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

func NormalizeSearchQuery(s string) string {
	return NormalizeSpace(strings.ToLower(s))
}

func StripTags(s string) string {
	return reTag.ReplaceAllString(s, "")
}

func HTMLUnescape(s string) string {
	r := strings.NewReplacer("&amp;", "&", "&quot;", `"`, "&#39;", "'", "&lt;", "<", "&gt;", ">")
	return r.Replace(s)
}

func SquashRepeatedRunes(s string) string {
	runes := []rune(s)
	if len(runes) <= 1 {
		return s
	}
	out := make([]rune, 0, len(runes))
	prev := rune(0)
	for i, r := range runes {
		if i == 0 || r != prev {
			out = append(out, r)
		}
		prev = r
	}
	return string(out)
}
