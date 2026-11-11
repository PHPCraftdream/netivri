package hoppool

// Stage identifies the boundary at which an attempt failed.
type Stage uint8

const (
	// Connect is the transport connection to the selected hop.
	Connect Stage = iota + 1
	// Handshake is the caller-selected current-hop or next-hop handshake boundary.
	Handshake
	// Target is failure after the hop handshake while reaching the target.
	Target
	// Refusal is an explicit policy or authentication refusal.
	Refusal
	// UnknownStage prevents attribution for an unrecognized boundary.
	UnknownStage
)

// Kind identifies the observed failure class.
type Kind uint8

const (
	// Unreachable is a connection-level unreachable failure.
	Unreachable Kind = iota + 1
	// NextUnreachable is a SOCKS reply 3–6 or HTTP 502/503/504 indicating the next hop is unreachable.
	NextUnreachable
	// Ambiguous is a handshake failure without reliable blame.
	Ambiguous
	// AuthPolicy is an authentication or policy refusal.
	AuthPolicy
	// Capacity is a resource-capacity failure, not a hop-health event.
	Capacity
	// UnknownKind prevents attribution for an unrecognized failure.
	UnknownKind
)

// HopRole identifies which side of the selected boundary receives a verdict.
type HopRole uint8

const (
	// CurrentHop is the hop performing the attempted handshake or connection.
	CurrentHop HopRole = iota + 1
	// NextHop is the downstream hop or target beyond the current boundary.
	NextHop
)

// Assessment assigns a health strength to one relative hop role.
type Assessment struct {
	// Role identifies the relative hop being assessed.
	Role HopRole
	// Strength is the health contribution for that hop.
	Strength Strength
}

// Verdict is the set of relative-hop assessments for a failure.
// Blame supports Unreachable at Connect/Handshake, NextUnreachable at Handshake/Target,
// Ambiguous at Handshake, and AuthPolicy at Refusal.
type Verdict []Assessment

// Blame returns the relative-hop verdict for a failure boundary.
func Blame(stage Stage, kind Kind) Verdict {
	switch {
	case (stage == Connect || stage == Handshake) && kind == Unreachable:
		return Verdict{{Role: CurrentHop, Strength: Hard}}
	case (stage == Handshake || stage == Target) && kind == NextUnreachable:
		return Verdict{{Role: NextHop, Strength: Hard}}
	case stage == Handshake && kind == Ambiguous:
		return Verdict{{Role: CurrentHop, Strength: Soft}, {Role: NextHop, Strength: Soft}}
	case stage == Refusal && kind == AuthPolicy:
		return Verdict{{Role: CurrentHop, Strength: Soft}, {Role: NextHop, Strength: Inconclusive}}
	default:
		return nil
	}
}
