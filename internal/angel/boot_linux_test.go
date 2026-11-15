//go:build linux

package angel

import (
	"testing"
	"time"
)

func TestParseBtime(t *testing.T) {
	stat := "cpu  1 2 3\nintr 5\nctxt 6\nbtime 1794730682\nprocesses 7\n"
	got, ok := parseBtime(stat)
	if want := time.Unix(1794730682, 0).UTC(); !ok || !got.Equal(want) || got.Location() != time.UTC {
		t.Fatalf("parseBtime = %v %v, want %v", got, ok, want)
	}
	for name, text := range map[string]string{
		"missing":    "cpu 1 2 3\nprocesses 7\n",
		"empty":      "",
		"not number": "btime soon\n",
		"zero":       "btime 0\n",
		"negative":   "btime -5\n",
		"other key":  "xbtime 1794730682\n",
	} {
		if got, ok := parseBtime(text); ok || !got.IsZero() {
			t.Errorf("%s: parseBtime = %v %v, want none", name, got, ok)
		}
	}
}
