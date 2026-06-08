package util

import (
	"fmt"
	"strings"
)

func CompactErrors(errs []error, limit int) string {
	if len(errs) == 0 {
		return ""
	}
	if limit <= 0 {
		limit = len(errs)
	}
	parts := make([]string, 0, min(limit, len(errs)))
	for i, err := range errs {
		if i >= limit {
			break
		}
		parts = append(parts, err.Error())
	}
	if len(errs) > limit {
		parts = append(parts, fmt.Sprintf("+%d more", len(errs)-limit))
	}
	return strings.Join(parts, " | ")
}
