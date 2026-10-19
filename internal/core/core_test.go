package core

import (
	"context"
	"strings"
	"testing"
)

// Start must reject an empty HomeDir before touching any Mihomo global state,
// so this test must never start the core.
func TestStartRequiresHomeDir(t *testing.T) {
	inst, err := Start(context.Background(), Options{})
	if err == nil {
		if inst != nil {
			_ = inst.Close()
		}
		t.Fatal("Start with empty HomeDir: got nil error, want error")
	}
	if inst != nil {
		t.Fatal("Start with empty HomeDir returned a non-nil instance")
	}
	if !strings.Contains(err.Error(), "HomeDir") {
		t.Fatalf("error = %q, want it to mention HomeDir", err.Error())
	}
	if current != nil {
		t.Fatal("failed Start left the global instance claim set")
	}
}
