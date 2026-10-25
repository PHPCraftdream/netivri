package stats

import (
	"strings"
	"testing"
)

func TestSanitizeName(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"", ""},
		{"Finland 1", "Finland 1"},
		{"  spaced  out  ", "spaced out"},
		{"a\nb\tc", "a b c"},
		{"a\x00b\x1fc", "a b c"},
		{"\n\t \x00 \r", ""},
		{"a  \n  b", "a b"},
		{"emoji \U0001F1EB\U0001F1EE ok", "emoji \U0001F1EB\U0001F1EE ok"},
		{"日本語 テスト", "日本語 テスト"},
		{"https://x/y is a name", "https://x/y is a name"},
		// 70 ASCII runes: cut at 64.
		{strings.Repeat("a", 70), strings.Repeat("a", 64)},
		// 64 ASCII runes: unchanged.
		{strings.Repeat("a", 64), strings.Repeat("a", 64)},
		// 65 ASCII runes: cut at 64.
		{strings.Repeat("a", 65), strings.Repeat("a", 64)},
		// 22 CJK runes (66 bytes): unchanged, the cap counts runes.
		{strings.Repeat("日", 22), strings.Repeat("日", 22)},
		// 64 CJK runes (192 bytes): unchanged.
		{strings.Repeat("日", 64), strings.Repeat("日", 64)},
		// 65 CJK runes: cut at the rune boundary after 64.
		{strings.Repeat("日", 65), strings.Repeat("日", 64)},
		// 63 ASCII plus one emoji: exactly 64 runes, the emoji survives.
		{strings.Repeat("a", 63) + "\U0001F1EB", strings.Repeat("a", 63) + "\U0001F1EB"},
		// 64 ASCII plus one emoji (65 runes): cut at the rune boundary before it.
		{strings.Repeat("a", 64) + "\U0001F1EB", strings.Repeat("a", 64)},
		// 16 emoji (64 runes, 256 bytes): kept whole.
		{strings.Repeat("\U0001F1EB", 16), strings.Repeat("\U0001F1EB", 16)},
		// "a b " 30 times (120 runes): the 64-rune cut leaves a trailing
		// space, which is trimmed (63 runes ending in "b").
		{strings.Repeat("a b ", 30), strings.TrimRight(strings.Repeat("a b ", 16), " ")},
	}
	for _, tc := range cases {
		if got := SanitizeName(tc.in); got != tc.want {
			t.Errorf("SanitizeName(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
