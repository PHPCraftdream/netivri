package hopstate

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/PHPCraftdream/netivri/internal/hoppool"
	"github.com/ktav-lang/golang/pure"
)

type testDocument struct {
	Version int         `json:"version"`
	Hops    []diskEntry `json:"hops"`
}

func validTestDocument(entries []diskEntry) ([]byte, error) {
	text, err := pure.Dumps(testDocument{Version: 1, Hops: entries})
	return []byte(text), err
}

func TestStoreRoundTripUTCAndSecretFree(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "state.ktav"))
	raw := "https://synthetic-user:synthetic-password@Proxy.Example/path?token=synthetic-query#fragment"
	key, ok := HopKey(raw)
	if !ok {
		t.Fatal("synthetic proxy URL was rejected")
	}
	localTime := time.Date(2026, 11, 11, 12, 13, 14, 123456789, time.FixedZone("synthetic-zone", 5*60*60+30*60))
	if err := store.Save([]hoppool.Entry{{Key: key, Trips: 3, NextProbe: localTime}}); err != nil {
		t.Fatal("save failed")
	}
	data, err := os.ReadFile(store.path)
	if err != nil {
		t.Fatal("saved state could not be read")
	}
	if bytes.Contains(data, []byte(raw)) || bytes.Contains(data, []byte("synthetic-password")) || bytes.Contains(data, []byte("synthetic-query")) {
		t.Fatal("saved state contains URL material")
	}
	if !bytes.Contains(data, []byte("2026-11-11T06:43:14.123456789Z")) {
		t.Fatal("saved probe time is not RFC3339 UTC")
	}
	got, err := store.Load()
	if err != nil || len(got) != 1 || got[0].Key != key || got[0].Trips != 3 || !got[0].NextProbe.Equal(localTime) || got[0].NextProbe.Location() != time.UTC {
		t.Fatal("saved state did not round-trip")
	}
}

func TestStorePreservesMaximumTrips(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "state.ktav"))
	key := strings.Repeat("a", 64)
	maxTrips := int(^uint(0) >> 1)
	if err := store.Save([]hoppool.Entry{{Key: key, Trips: maxTrips, NextProbe: time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC)}}); err != nil {
		t.Fatal("save failed")
	}
	got, err := store.Load()
	if err != nil || len(got) != 1 || got[0].Trips != maxTrips {
		t.Fatal("large trips value did not round-trip exactly")
	}
}

func TestStoreInvalidYearPreservesOldFile(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "state.ktav"))
	key := strings.Repeat("a", 64)
	if err := store.Save([]hoppool.Entry{{Key: key, Trips: 1, NextProbe: time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC)}}); err != nil {
		t.Fatal("initial save failed")
	}
	before, err := os.ReadFile(store.path)
	if err != nil {
		t.Fatal("initial state could not be read")
	}
	if err := store.Save([]hoppool.Entry{{Key: key, Trips: 2, NextProbe: time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)}}); err == nil {
		t.Fatal("save accepted a timestamp outside RFC3339 range")
	}
	after, err := os.ReadFile(store.path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("invalid timestamp changed the previous file")
	}
}

func TestStoreMissingFileIsEmpty(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "missing.ktav"))
	got, err := store.Load()
	if err != nil || len(got) != 0 {
		t.Fatal("missing state was not treated as empty")
	}
}

func TestStoreCorruptTruncatedUnknownVersionAndByteLimit(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "state.ktav"))
	entries := []diskEntry{{Key: strings.Repeat("a", 64), Trips: 1, NextProbe: "2026-11-11T00:00:00Z"}}
	validDocument, err := validTestDocument(entries)
	if err != nil {
		t.Fatal("could not encode valid fixture")
	}
	unknownVersionText, err := pure.Dumps(testDocument{Version: 2, Hops: entries})
	if err != nil {
		t.Fatal("could not encode unknown-version fixture")
	}
	paddedAtLimit := append(append([]byte(nil), validDocument...), bytes.Repeat([]byte{' '}, maxStoreBytes-len(validDocument))...)
	if err := os.WriteFile(store.path, paddedAtLimit, 0o600); err != nil {
		t.Fatal("could not write boundary fixture")
	}
	got, err := store.Load()
	if err != nil || len(got) != 1 || got[0].Key != entries[0].Key {
		t.Fatal("valid document at byte limit did not load")
	}

	cases := []struct {
		name string
		data []byte
	}{
		{name: "corrupt", data: []byte("version: [")},
		{name: "truncated", data: []byte("version: 1\nhops: [")},
		{name: "unknown version", data: []byte(unknownVersionText)},
		{name: "byte limit", data: append(append([]byte(nil), paddedAtLimit...), ' ')},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := os.WriteFile(store.path, tc.data, 0o600); err != nil {
				t.Fatal("fixture write failed")
			}
			_, err := store.Load()
			if !errors.Is(err, ErrUnreadable) {
				t.Fatal("unreadable state did not return ErrUnreadable")
			}
		})
	}
}

func TestStoreRejectsMoreThanEntryLimit(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "state.ktav"))
	entries := make([]diskEntry, maxEntries+1)
	for i := range entries {
		entries[i] = diskEntry{Key: fmt.Sprintf("%064x", i+1), Trips: 1, NextProbe: "2026-11-11T00:00:00Z"}
	}
	text, err := validTestDocument(entries)
	if err != nil {
		t.Fatal("fixture encoding failed")
	}
	boundaryText, err := validTestDocument(entries[:maxEntries])
	if err != nil {
		t.Fatal("boundary fixture encoding failed")
	}
	if err := os.WriteFile(store.path, boundaryText, 0o600); err != nil {
		t.Fatal("boundary fixture write failed")
	}
	got, err := store.Load()
	if err != nil || len(got) != maxEntries {
		t.Fatal("valid entry-limit boundary did not load")
	}
	if err := os.WriteFile(store.path, text, 0o600); err != nil {
		t.Fatal("fixture write failed")
	}
	_, err = store.Load()
	if !errors.Is(err, ErrUnreadable) {
		t.Fatal("oversized entry list did not return ErrUnreadable")
	}
}

func TestStoreDropsInvalidEntriesOnLoadAndSave(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "state.ktav"))
	goodKey := strings.Repeat("a", 64)
	text, err := validTestDocument([]diskEntry{
		{Key: goodKey, Trips: 2, NextProbe: "2026-11-11T00:00:00Z"},
		{Key: "", Trips: 1, NextProbe: "invalid"},
		{Key: "not-a-hash", Trips: 1, NextProbe: "invalid"},
		{Key: strings.Repeat("b", 64), Trips: -1, NextProbe: "invalid"},
	})
	if err != nil {
		t.Fatal("fixture encoding failed")
	}
	if err := os.WriteFile(store.path, text, 0o600); err != nil {
		t.Fatal("fixture write failed")
	}
	got, err := store.Load()
	if err != nil || len(got) != 1 || got[0].Key != goodKey {
		t.Fatal("invalid stored entries were not discarded")
	}
	if err := store.Save([]hoppool.Entry{
		{Key: goodKey, Trips: 2},
		{Key: "http://synthetic-user:synthetic-password@proxy.example", Trips: 1},
		{Key: strings.Repeat("b", 64), Trips: -1},
	}); err != nil {
		t.Fatal("save of normalized entries failed")
	}
	data, err := os.ReadFile(store.path)
	if err != nil {
		t.Fatal("normalized state could not be read")
	}
	if bytes.Contains(data, []byte("synthetic-password")) || bytes.Contains(data, []byte("proxy.example")) {
		t.Fatal("raw URL key was persisted")
	}
	got, err = store.Load()
	if err != nil || len(got) != 1 || got[0].Key != goodKey {
		t.Fatal("save did not discard invalid entries")
	}
}

func TestStoreEmptySaveRemovesFileIdempotently(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "state.ktav"))
	key := strings.Repeat("c", 64)
	if err := store.Save([]hoppool.Entry{{Key: key, Trips: 1}}); err != nil {
		t.Fatal("initial save failed")
	}
	for range 2 {
		if err := store.Save(nil); err != nil {
			t.Fatal("empty save failed")
		}
		if _, err := os.Stat(store.path); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("empty save did not remove state file")
		}
	}
}

func TestStoreFileModeIsPrivate(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX mode bits do not apply on Windows")
	}
	store := NewStore(filepath.Join(t.TempDir(), "state.ktav"))
	if err := store.Save([]hoppool.Entry{{Key: strings.Repeat("d", 64), Trips: 1}}); err != nil {
		t.Fatal("save failed")
	}
	info, err := os.Stat(store.path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatal("state file is not mode 0600")
	}
}

func TestStoreEncoderFailurePreservesOldFileAndRedactsError(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "state.ktav"))
	if err := store.Save([]hoppool.Entry{{Key: strings.Repeat("e", 64), Trips: 1}}); err != nil {
		t.Fatal("initial save failed")
	}
	before, err := os.ReadFile(store.path)
	if err != nil {
		t.Fatal("initial state could not be read")
	}
	store.encodeFn = func([]diskEntry) (string, error) {
		return "", errors.New("synthetic-password from https://synthetic-user:synthetic-password@proxy.example")
	}
	err = store.Save([]hoppool.Entry{{Key: strings.Repeat("f", 64), Trips: 1}})
	if err == nil || strings.Contains(err.Error(), "synthetic-password") || strings.Contains(err.Error(), "proxy.example") || strings.Contains(err.Error(), store.path) {
		t.Fatal("encoder failure leaked its details")
	}
	after, err := os.ReadFile(store.path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("encoder failure changed the previous file")
	}
}

func TestStoreLoadErrorsAreRedacted(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "state.ktav"))
	const corrupt = "version: ["
	if err := os.WriteFile(store.path, []byte(corrupt), 0o600); err != nil {
		t.Fatal("fixture write failed")
	}
	_, err := store.Load()
	if !errors.Is(err, ErrUnreadable) || strings.Contains(err.Error(), corrupt) || strings.Contains(err.Error(), store.path) {
		t.Fatal("read error leaked file content")
	}
}
