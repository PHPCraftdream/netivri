package hopstate

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	hoppool "github.com/PHPCraftdream/netivri/pkg/hopkit/pool"
)

func TestStoreSidecarContentionAndBlockingOperations(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.ktav")
	first, second := NewStore(path), NewStore(path)
	unlock, err := first.lock()
	if err != nil {
		t.Fatal("could not acquire initial lock")
	}
	defer unlock()
	if contenderUnlock, acquired, err := second.tryLock(); err != nil || acquired || contenderUnlock != nil {
		t.Fatal("nonblocking contention check acquired a held lock")
	}
	unlock()
	unlock()
	if contenderUnlock, acquired, err := second.tryLock(); err != nil || !acquired || contenderUnlock == nil {
		t.Fatal("nonblocking contention check did not acquire a released lock")
	} else {
		contenderUnlock()
	}
	if _, err := os.Stat(path + storeLockSuffix); err != nil {
		t.Fatal("stable sidecar was removed")
	}

	for _, operation := range []struct {
		name string
		run  func(*Store) ([]hoppool.Entry, error)
	}{
		{name: "load", run: func(s *Store) ([]hoppool.Entry, error) { return s.Load() }},
		{name: "save", run: func(s *Store) ([]hoppool.Entry, error) { return nil, s.Save(nil) }},
	} {
		t.Run(operation.name, func(t *testing.T) {
			blocked := NewStore(filepath.Join(t.TempDir(), "state.ktav"))
			holder := NewStore(blocked.path)
			holdUnlock, err := holder.lock()
			if err != nil {
				t.Fatal("could not acquire operation lock")
			}
			defer holdUnlock()
			var before []byte
			if operation.name == "save" {
				if err := os.WriteFile(blocked.path, []byte("previous"), 0o600); err != nil {
					t.Fatal("could not write prior file")
				}
				before, err = os.ReadFile(blocked.path)
				if err != nil {
					t.Fatal("could not read prior file")
				}
			} else {
				fixture, err := validTestDocument([]diskEntry{{Key: strings.Repeat("a", 64), Trips: 1, NextProbe: "2026-11-11T00:00:00Z"}})
				if err != nil {
					t.Fatal("could not prepare load fixture")
				}
				if err := os.WriteFile(blocked.path, fixture, 0o600); err != nil {
					t.Fatal("could not write load fixture")
				}
			}

			attempted, done := make(chan struct{}), make(chan error, 1)
			unlocked := false
			var loaded []hoppool.Entry
			blocked.lockFn = func() (func(), error) {
				close(attempted)
				unlock, err := blocked.lock()
				if err != nil {
					return nil, err
				}
				return func() {
					unlock()
					unlocked = true
				}, nil
			}
			go func() {
				var err error
				loaded, err = operation.run(blocked)
				done <- err
			}()
			<-attempted
			if contenderUnlock, acquired, err := holder.tryLock(); err != nil || acquired || contenderUnlock != nil {
				if contenderUnlock != nil {
					contenderUnlock()
				}
				t.Fatal("operation did not contend on its stable sidecar")
			}
			if operation.name == "save" {
				after, err := os.ReadFile(blocked.path)
				if err != nil || string(before) != string(after) {
					t.Fatal("save changed the file before lock acquisition")
				}
			}
			holdUnlock()
			if err := <-done; err != nil {
				t.Fatal("operation failed after lock release")
			}
			if !unlocked {
				t.Fatal("operation did not release its lock")
			}
			if operation.name == "load" && (len(loaded) != 1 || loaded[0].Key != strings.Repeat("a", 64)) {
				t.Fatal("load did not read the state file")
			}
			if operation.name == "save" {
				if _, err := os.Stat(blocked.path); !errors.Is(err, os.ErrNotExist) {
					t.Fatal("save did not remove target after lock acquisition")
				}
			}
		})
	}
}
