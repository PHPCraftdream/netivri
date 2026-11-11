package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

// MaxHopsPerSide is the maximum number of configured hops on either side of a route.
const MaxHopsPerSide = 16

// HopList is an ordered list of proxy hops. Ktav scalars are accepted for
// compatibility; a singleton is rendered as the legacy scalar form.
type HopList []string

func (h *HopList) UnmarshalJSON(data []byte) error {
	data = bytes.TrimSpace(data)
	if len(data) == 0 {
		return fmt.Errorf("hop list must be a string or array of strings")
	}
	if data[0] == '"' {
		var hop string
		if err := json.Unmarshal(data, &hop); err != nil {
			return err
		}
		if hop == "" {
			*h = nil
		} else {
			*h = HopList{hop}
		}
		return nil
	}
	if bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		return fmt.Errorf("hop list must be a string or array of strings")
	}
	var hops []string
	if err := json.Unmarshal(data, &hops); err != nil || hops == nil {
		return fmt.Errorf("hop list must be a string or array of strings")
	}
	*h = HopList(hops)
	return nil
}

func (h HopList) MarshalJSON() ([]byte, error) {
	if len(h) == 1 {
		return json.Marshal(h[0])
	}
	return json.Marshal([]string(h))
}

// ValidateHopList applies the production limit and validates every hop. Its
// errors include field names and positions, never hop URLs or credentials.
func ValidateHopList(field string, hops HopList) error {
	return validateHopListLimit(field, hops, MaxHopsPerSide)
}

// validateHopListLimit accepts a per-call limit for tests; production callers
// use ValidateHopList and the exported MaxHopsPerSide constant.
func validateHopListLimit(field string, hops HopList, maxHops int) error {
	if len(hops) == 0 {
		return nil
	}
	none := false
	seen := make(map[string]struct{}, len(hops))
	for i, hop := range hops {
		itemField := field
		if len(hops) > 1 {
			itemField = fmt.Sprintf("%s[%d]", field, i)
		}
		if hop == "" {
			return invalid("%s must not contain empty hop entries", itemField)
		}
		if err := checkScalar(itemField, hop); err != nil {
			return err
		}
		if strings.EqualFold(hop, "none") {
			none = true
		} else {
			if _, duplicate := seen[hop]; duplicate {
				return invalid("%s must not contain duplicate hop URLs", field)
			}
			seen[hop] = struct{}{}
		}
		if err := validateChainURL(itemField, hop); err != nil {
			return err
		}
	}
	if none && len(hops) > 1 {
		return invalid("%s: none must stand alone (must not mix none with hop URLs)", field)
	}
	if len(hops) > maxHops {
		return invalid("%s: must contain at most %d hops per side", field, maxHops)
	}
	return nil
}

// InheritHops selects the nearest nonempty value; it never merges lists.
func InheritHops(own, parent HopList) HopList {
	if len(own) != 0 {
		return own
	}
	return parent
}
