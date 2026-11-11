package pool

import (
	"math"
	"time"
)

const maxSand = 8.0

// advanceSand applies lazy exponential decay without moving time backwards.
func (p *Pool) advanceSand(h *hop, now time.Time) {
	if !h.sandSet {
		h.sandAt = now
		h.sandSet = true
		return
	}
	if now.After(h.sandAt) {
		seconds := now.Sub(h.sandAt).Seconds()
		h.sand *= math.Exp2(-seconds / 30)
		h.sandAt = now
	}
}

// effectiveSand returns the decayed score without mutating stored state.
func effectiveSand(h *hop, now time.Time) float64 {
	sand := h.sand
	if h.sandSet && now.After(h.sandAt) {
		sand *= math.Exp2(-now.Sub(h.sandAt).Seconds() / 30)
	}
	return sand
}

func weight(sand float64) float64 {
	w := math.Exp(-math.Log(20) * sand / 8)
	if w < 0.05 {
		return 0.05
	}
	return w
}
