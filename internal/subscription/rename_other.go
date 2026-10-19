//go:build !windows

package subscription

// POSIX rename over an existing target is atomic and never "busy".
func transientRenameError(error) bool { return false }
