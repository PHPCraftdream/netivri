//go:build windows

package angel

import (
	"time"

	"golang.org/x/sys/windows"
)

var procGetTickCount64 = windows.NewLazySystemDLL("kernel32.dll").NewProc("GetTickCount64")

// osBootTime counts the uptime (milliseconds since the system started) back
// from now. The release builds are 64-bit, so the whole value is in the first
// result.
func osBootTime() (time.Time, bool) {
	if procGetTickCount64.Find() != nil {
		return time.Time{}, false
	}
	ms, _, _ := procGetTickCount64.Call()
	if ms == 0 {
		return time.Time{}, false
	}
	return time.Now().Round(0).Add(-time.Duration(ms) * time.Millisecond).UTC(), true
}
