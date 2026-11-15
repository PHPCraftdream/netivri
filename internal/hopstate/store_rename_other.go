//go:build !windows

package hopstate

import "os"

func renameReplace(from, to string) error {
	return os.Rename(from, to) //nolint:gosec // G703: both paths are the operator-owned state file and its temp sibling under the configured home
}
