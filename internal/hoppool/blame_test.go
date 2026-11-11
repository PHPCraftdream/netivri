package hoppool

import "testing"

func TestBlameExhaustiveStageKindTable(t *testing.T) {
	stages := []Stage{0, Connect, Handshake, Target, Refusal, UnknownStage, Stage(255)}
	kinds := []Kind{0, Unreachable, NextUnreachable, Ambiguous, AuthPolicy, Capacity, UnknownKind, Kind(255)}
	want := map[[2]uint8]Verdict{
		{uint8(Connect), uint8(Unreachable)}:       {{Role: CurrentHop, Strength: Hard}},
		{uint8(Handshake), uint8(Unreachable)}:     {{Role: CurrentHop, Strength: Hard}},
		{uint8(Handshake), uint8(NextUnreachable)}: {{Role: NextHop, Strength: Hard}},
		{uint8(Target), uint8(NextUnreachable)}:    {{Role: NextHop, Strength: Hard}},
		{uint8(Handshake), uint8(Ambiguous)}:       {{Role: CurrentHop, Strength: Soft}, {Role: NextHop, Strength: Soft}},
		{uint8(Refusal), uint8(AuthPolicy)}:        {{Role: CurrentHop, Strength: Soft}, {Role: NextHop, Strength: Inconclusive}},
	}

	for _, stage := range stages {
		for _, kind := range kinds {
			key := [2]uint8{uint8(stage), uint8(kind)}
			got := Blame(stage, kind)
			expected, supported := want[key]
			if !supported {
				if len(got) != 0 {
					t.Errorf("Blame(%d, %d) = %#v, want nobody", stage, kind, got)
				}
				continue
			}
			if len(got) != len(expected) {
				t.Errorf("Blame(%d, %d) length = %d, want %#v", stage, kind, len(got), expected)
				continue
			}
			for i := range expected {
				if got[i] != expected[i] {
					t.Errorf("Blame(%d, %d)[%d] = %#v, want %#v", stage, kind, i, got[i], expected[i])
				}
			}
			if len(got) > 0 {
				got[0] = Assessment{Role: NextHop, Strength: Hard}
				again := Blame(stage, kind)
				for i := range expected {
					if again[i] != expected[i] {
						t.Errorf("mutating verdict for (%d, %d) changed future verdict: %#v", stage, kind, again)
						break
					}
				}
			}
		}
	}
}

func TestCapacityNeverAttributesAnyStage(t *testing.T) {
	for stage := Stage(0); stage <= Stage(255); stage++ {
		if got := Blame(stage, Capacity); len(got) != 0 {
			t.Errorf("capacity at stage %d attributed %#v", stage, got)
		}
		if stage == Stage(255) {
			break
		}
	}
}
