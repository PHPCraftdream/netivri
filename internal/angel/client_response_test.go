package angel

import (
	"errors"
	"io"
	"strings"
	"testing"
)

type controlResponseErrorReader struct {
	err error
}

func (r controlResponseErrorReader) Read([]byte) (int, error) {
	return 0, r.err
}

func TestReadControlResponseAtLimit(t *testing.T) {
	want := strings.Repeat("x", maxControlResponseSize)
	got, err := readControlResponse(strings.NewReader(want))
	if err != nil {
		t.Fatalf("readControlResponse at limit: %v", err)
	}
	if len(got) != maxControlResponseSize || string(got) != want {
		t.Errorf("response length = %d, want %d with unchanged content", len(got), maxControlResponseSize)
	}
}

func TestReadControlResponseOverLimit(t *testing.T) {
	response := strings.Repeat("x", maxControlResponseSize+1)
	if _, err := readControlResponse(strings.NewReader(response)); !errors.Is(err, errControlResponseTooLarge) {
		t.Fatalf("readControlResponse over limit error = %v, want oversized response", err)
	}
}

func TestReadControlResponsePreservesReaderError(t *testing.T) {
	wantErr := errors.New("reader failure")
	reader := io.MultiReader(strings.NewReader("partial response"), controlResponseErrorReader{err: wantErr})
	if _, err := readControlResponse(reader); !errors.Is(err, wantErr) {
		t.Fatalf("readControlResponse error = %v, want reader error", err)
	}
}
