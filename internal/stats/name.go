package stats

import (
	"strings"
	"unicode"
)

// maxNameRunes bounds a node display name.
const maxNameRunes = 64

// SanitizeName cleans a node display name for output: control characters
// become spaces, runs of spaces collapse to one, leading and trailing
// spaces go, and the result is capped at 64 runes. An empty result means
// the name is not shown. Display only: never a key or used for matching.
func SanitizeName(name string) string {
	var b strings.Builder
	b.Grow(len(name))
	for _, r := range name {
		if unicode.IsControl(r) {
			r = ' '
		}
		b.WriteRune(r)
	}
	clean := b.String()
	for strings.Contains(clean, "  ") {
		clean = strings.ReplaceAll(clean, "  ", " ")
	}
	clean = strings.TrimSpace(clean)
	runes := []rune(clean)
	if len(runes) <= maxNameRunes {
		return clean
	}
	runes = runes[:maxNameRunes]
	return strings.TrimRight(string(runes), " ")
}
