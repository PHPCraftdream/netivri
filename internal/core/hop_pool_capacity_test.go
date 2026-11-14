package core

import (
	"errors"
	"fmt"
	"syscall"
	"testing"
)

func TestIsCapacityErrorRecognizesErrnosAndTexts(t *testing.T) {
	for _, err := range []error{
		fmt.Errorf("dial: %w", syscall.EMFILE),
		fmt.Errorf("dial: %w", syscall.ENFILE),
		fmt.Errorf("dial: %w", syscall.ENOBUFS),
		errors.Join(errors.New("other"), syscall.EMFILE),
		errors.New("connection limit reached"),
	} {
		if !isCapacityError(err) {
			t.Errorf("isCapacityError(%v) = false, want true", err)
		}
	}
	for _, err := range []error{nil, errors.New("connection refused"), fmt.Errorf("dial: %w", syscall.ECONNREFUSED)} {
		if isCapacityError(err) {
			t.Errorf("isCapacityError(%v) = true, want false", err)
		}
	}
}
