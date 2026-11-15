package angel

import "time"

// bootMargin is how much later than the start of an angel the computer must
// have booted for the boot to count as after it: it absorbs the rounding of the
// uptime and a small step of the clock.
const bootMargin = time.Minute

// bootTime returns when the computer booted and false when this system does not
// tell. It is a seam, replaced by tests: the real one is osBootTime of the
// platform. Linux reads btime of /proc/stat and macOS kern.boottime, which stay
// put across a sleep; Windows counts the uptime back from now (it includes a
// sleep), and with Fast Startup a shutdown followed by a start keeps the uptime
// going, so only a restart or a full boot moves it there.
var bootTime = osBootTime

// rebootedSince reports whether the computer booted after since (by more than
// bootMargin) and when. An angel that did not stop cleanly and started before
// the boot ended with the restart (or a power loss); one that started after the
// boot was stopped some other way. False when the boot time is unknown.
func rebootedSince(since time.Time) (time.Time, bool) {
	if since.IsZero() {
		return time.Time{}, false
	}
	boot, ok := bootTime()
	if !ok || !boot.After(since.Add(bootMargin)) {
		return time.Time{}, false
	}
	return boot.UTC(), true
}
