//go:build !windows

package hopstate

import "os"

func renameReplace(from, to string) error { return os.Rename(from, to) }
