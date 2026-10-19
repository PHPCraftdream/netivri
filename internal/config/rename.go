package config

import (
	"os"
	"time"
)

const renameAttempts = 8

// renameFn is replaced in tests.
var renameFn = os.Rename

// renameReplace renames from over to. Transient "target busy" failures
// (Windows reports them while another writer or a scanner has the target
// open) are retried with a short, bounded backoff.
func renameReplace(from, to string) error {
	var err error
	for i := 0; i < renameAttempts; i++ {
		if err = renameFn(from, to); err == nil || !transientRenameError(err) {
			return err
		}
		time.Sleep(time.Duration(i+1) * 10 * time.Millisecond)
	}
	return err
}
