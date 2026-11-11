package main

import (
	"flag"
	"fmt"
	"strconv"
	"strings"

	"github.com/PHPCraftdream/netivri/internal/cli"
	"github.com/PHPCraftdream/netivri/internal/config"
)

// HopEdit describes one replacement or one incremental change to a chain.
// Remove indices are one-based and refer to the unmodified original list.
type HopEdit struct {
	Replace *config.HopList
	Add     config.HopList
	Remove  []int
}

type repeatedFlag []string

func (v *repeatedFlag) Set(value string) error { *v = append(*v, value); return nil }
func (v *repeatedFlag) String() string {
	return strings.Join(*v, ",")
}

func addRepeatedStringFlag(fs *flag.FlagSet, name, usage string) *repeatedFlag {
	value := new(repeatedFlag)
	fs.Var(value, name, usage)
	return value
}

func parseHopIndices(values []string, flagName string) ([]int, error) {
	if len(values) == 0 {
		return nil, cli.Usage("%s: expected one or more one-based indices", flagName)
	}
	indices := make([]int, 0, len(values))
	seen := make(map[int]struct{}, len(values))
	for _, raw := range values {
		digitsOnly := raw != ""
		for _, digit := range raw {
			if digit < '0' || digit > '9' {
				digitsOnly = false
				break
			}
		}
		i, err := strconv.Atoi(raw)
		if !digitsOnly || err != nil || i < 1 {
			return nil, cli.Usage("%s: indices must be positive whole numbers", flagName)
		}
		if _, ok := seen[i]; ok {
			return nil, cli.Usage("%s: duplicate index", flagName)
		}
		seen[i] = struct{}{}
		indices = append(indices, i)
	}
	return indices, nil
}

// applyHopEdit is parameterized by an immutable per-call validator so unit
// tests can exercise list mechanics with a test-only capacity.
func applyHopEdit(original config.HopList, edit HopEdit, validate func(config.HopList) error) (config.HopList, error) {
	if validate == nil {
		return nil, fmt.Errorf("hop validator is required")
	}
	if edit.Replace != nil {
		if edit.Add != nil || edit.Remove != nil {
			return nil, fmt.Errorf("replacement and incremental hop edits cannot be combined")
		}
		result := cloneHopList(*edit.Replace)
		if err := validate(result); err != nil {
			return nil, err
		}
		return result, nil
	}
	if edit.Add != nil && edit.Remove != nil {
		return nil, fmt.Errorf("adding and removing hops cannot be combined")
	}
	result := cloneHopList(original)
	if edit.Remove != nil {
		remove := make(map[int]struct{}, len(edit.Remove))
		for _, index := range edit.Remove {
			if index < 1 || index > len(original) {
				return nil, fmt.Errorf("hop index is out of range")
			}
			if _, duplicate := remove[index]; duplicate {
				return nil, fmt.Errorf("duplicate hop index")
			}
			remove[index] = struct{}{}
		}
		result = make(config.HopList, 0, len(original)-len(remove))
		for i, hop := range original {
			if _, drop := remove[i+1]; !drop {
				result = append(result, hop)
			}
		}
	}
	result = append(result, edit.Add...)
	if err := validate(result); err != nil {
		return nil, err
	}
	return result, nil
}

func cloneHopList(hops config.HopList) config.HopList {
	if hops == nil {
		return nil
	}
	return append(config.HopList(nil), hops...)
}

func validateCLIHopList(hops config.HopList, field string) error {
	return config.ValidateHopList(field, hops)
}

func validateHopEdit(original config.HopList, edit HopEdit, field string) (config.HopList, error) {
	return applyHopEdit(original, edit, func(hops config.HopList) error {
		return validateCLIHopList(hops, field)
	})
}

func replaceHopList(hops config.HopList) *config.HopList {
	copy := cloneHopList(hops)
	return &copy
}

func chainDisabled(hops config.HopList) bool {
	return len(hops) == 1 && strings.EqualFold(hops[0], "none")
}

// redactChains removes hop credentials and always returns a non-nil JSON array.
func redactChains(hops config.HopList) []string {
	out := make([]string, len(hops))
	for i, hop := range hops {
		out[i] = redactChain(hop)
	}
	return out
}

// chainsHaveAuth reports whether any configured hop contains credentials.
func chainsHaveAuth(hops config.HopList) bool {
	for _, hop := range hops {
		if chainHasAuth(hop) {
			return true
		}
	}
	return false
}

func hopUpdate(original config.HopList, replacement *config.HopList, edit *HopEdit, field string) (config.HopList, error) {
	if replacement != nil && edit != nil {
		return nil, cli.Usage("%s: replacement and incremental edits cannot be combined", field)
	}
	if replacement != nil {
		return validateHopEdit(original, HopEdit{Replace: replacement}, field)
	}
	if edit == nil {
		return cloneHopList(original), nil
	}
	return validateHopEdit(original, *edit, field)
}
