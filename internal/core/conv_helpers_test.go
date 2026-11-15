package core

import (
	"fmt"
	"math"
	"net/http"
	"testing"
)

// getWithContext is client.Get bound to the test context.
func getWithContext(t *testing.T, client *http.Client, url string) (*http.Response, error) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	return client.Do(req) //nolint:gosec // G704: test helper requests only URLs of test-owned servers
}

// mustByte converts v to a byte, panicking when v does not fit (helpers
// without a testing handle).
func mustByte(v int) byte {
	if v < 0 || v > math.MaxUint8 {
		panic(fmt.Sprintf("value %d does not fit a byte", v))
	}
	return byte(v)
}

// mustUint16 converts v to a uint16, panicking when v does not fit.
func mustUint16(v int) uint16 {
	if v < 0 || v > math.MaxUint16 {
		panic(fmt.Sprintf("value %d does not fit a uint16", v))
	}
	return uint16(v)
}

// toByte converts v to a byte, failing the test when v does not fit.
func toByte(t testing.TB, v int) byte {
	t.Helper()
	if v < 0 || v > math.MaxUint8 {
		t.Fatalf("value %d does not fit a byte", v)
		return 0
	}
	return byte(v)
}

// toUint16 converts v to a uint16, failing the test when v does not fit.
func toUint16(t testing.TB, v int) uint16 {
	t.Helper()
	if v < 0 || v > math.MaxUint16 {
		t.Fatalf("value %d does not fit a uint16", v)
		return 0
	}
	return uint16(v)
}
