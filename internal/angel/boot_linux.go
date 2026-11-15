//go:build linux

package angel

import (
	"os"
	"strconv"
	"strings"
	"time"
)

func osBootTime() (time.Time, bool) {
	data, err := os.ReadFile("/proc/stat")
	if err != nil {
		return time.Time{}, false
	}
	return parseBtime(string(data))
}

// parseBtime reads the "btime" line of /proc/stat: the boot time in seconds
// since the epoch.
func parseBtime(stat string) (time.Time, bool) {
	for _, line := range strings.Split(stat, "\n") {
		value, ok := strings.CutPrefix(line, "btime ")
		if !ok {
			continue
		}
		sec, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
		if err != nil || sec <= 0 {
			return time.Time{}, false
		}
		return time.Unix(sec, 0).UTC(), true
	}
	return time.Time{}, false
}
