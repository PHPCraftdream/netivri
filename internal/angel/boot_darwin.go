//go:build darwin

package angel

import (
	"time"

	"golang.org/x/sys/unix"
)

func osBootTime() (time.Time, bool) {
	tv, err := unix.SysctlTimeval("kern.boottime")
	if err != nil || tv.Sec <= 0 {
		return time.Time{}, false
	}
	return time.Unix(tv.Sec, int64(tv.Usec)*1000).UTC(), true
}
