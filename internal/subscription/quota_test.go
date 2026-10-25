package subscription

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestStoreRoundTripsQuota(t *testing.T) {
	cases := []struct {
		name  string
		quota *Quota
	}{
		{"all fields", &Quota{Upload: 12, Download: 34, Total: 107374182400, Expire: time.Unix(1763500000, 0).UTC()}},
		{"only upload", &Quota{Upload: 12}},
		{"no expire", &Quota{Upload: 1, Download: 2, Total: 3}},
		{"expire at the cap", &Quota{Upload: 1, Expire: time.Unix(253402300799, 0).UTC()}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			s := NewStore(dir)
			snap := testSnap(time.Now().UTC().Truncate(time.Second))
			snap.Quota = tc.quota
			if err := s.Save("main", snap); err != nil {
				t.Fatalf("Save: %v", err)
			}
			got, err := s.Load("main")
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if !reflect.DeepEqual(got.Quota, tc.quota) {
				t.Errorf("Quota = %+v, want %+v", got.Quota, tc.quota)
			}
		})
	}
}

func TestStoreLegacyJSONWithoutQuotaLoadsNil(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "subscriptions")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	legacy := `{"fetched_at": "2024-01-02T03:04:05Z", "skipped": 0, "nodes": [` +
		`{"id": "aaaaaaaaaaaa", "name": "plain", "type": "ss", "server": "example.invalid", "port": 8388,` +
		` "raw": {"type": "ss", "server": "example.invalid", "port": 8388, "cipher": "aes-128-gcm", "password": "pass"}}]}`
	if err := os.WriteFile(filepath.Join(dir, "main.json"), []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := NewStore(dir).Load("main")
	if err != nil {
		t.Fatalf("Load of a legacy file: %v", err)
	}
	if got.Quota != nil {
		t.Errorf("Quota = %+v, want nil", got.Quota)
	}
}
