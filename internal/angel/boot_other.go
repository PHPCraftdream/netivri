//go:build !linux && !darwin && !windows

package angel

import "time"

// osBootTime is unknown on the other systems: an unclean stop keeps its
// general wording there.
func osBootTime() (time.Time, bool) { return time.Time{}, false }
