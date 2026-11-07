package core

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"testing"

	"github.com/metacubex/mihomo/common/buf"
	"github.com/metacubex/mihomo/transport/vless/vision"
)

// applyPadding runs vision.ApplyPadding and reports whether it panicked.
func applyPadding(b *buf.Buffer, uuid *[]byte) (panicked bool) {
	defer func() { panicked = recover() != nil }()
	vision.ApplyPadding(b, 0, uuid, true)
	return false
}

func TestApplyPaddingPanicsWithoutHeadroom(t *testing.T) {
	// the hazard EnsureHeadroom exists for: a buffer that came without room
	uuid := []byte("0123456789abcdef")
	if !applyPadding(buf.As([]byte("hello")), &uuid) {
		t.Fatal("ApplyPadding did not panic on a buffer with no room in front")
	}
}

func TestEnsureHeadroomMakesRoomForPadding(t *testing.T) {
	for _, n := range []int{0, 1, 10, 899, 900, 5000} {
		t.Run(fmt.Sprintf("len%d", n), func(t *testing.T) {
			data := bytes.Repeat([]byte{'d'}, n)
			uuid := []byte("0123456789abcdef")
			b := vision.EnsureHeadroom(buf.As(append([]byte(nil), data...)))
			if !bytes.Equal(b.Bytes(), data) {
				t.Fatalf("content changed: %d bytes, want %d", b.Len(), n)
			}
			if b.Start() < vision.PaddingHeaderLen {
				t.Fatalf("front room = %d, want >= %d", b.Start(), vision.PaddingHeaderLen)
			}
			if applyPadding(b, &uuid) {
				t.Fatal("ApplyPadding panicked on a buffer made by EnsureHeadroom")
			}
			frame := b.Bytes()
			if len(frame) < vision.PaddingHeaderLen+n {
				t.Fatalf("frame is %d bytes, want at least %d", len(frame), vision.PaddingHeaderLen+n)
			}
			// uuid(16) command(1) contentLen(2) paddingLen(2) content padding
			if !bytes.Equal(frame[:16], []byte("0123456789abcdef")) {
				t.Errorf("uuid not first: %x", frame[:16])
			}
			if got := int(binary.BigEndian.Uint16(frame[17:19])); got != n {
				t.Errorf("content length in header = %d, want %d", got, n)
			}
			padding := int(binary.BigEndian.Uint16(frame[19:21]))
			if len(frame) != vision.PaddingHeaderLen+n+padding {
				t.Errorf("frame = %d bytes, header says %d content + %d padding", len(frame), n, padding)
			}
			if !bytes.Equal(frame[21:21+n], data) {
				t.Error("content moved or changed inside the frame")
			}
		})
	}
}

func TestEnsureHeadroomKeepsABufferThatHasTheRoom(t *testing.T) {
	b := buf.NewSize(vision.PaddingHeaderLen + 10 + 1400)
	b.Resize(vision.PaddingHeaderLen, 0)
	_, _ = b.Write([]byte("0123456789"))
	if got := vision.EnsureHeadroom(b); got != b {
		t.Fatal("a buffer with the room in front and behind was copied")
	}
}

func TestEnsureHeadroomCopiesWhenOnlyTheRearIsShort(t *testing.T) {
	b := buf.NewSize(vision.PaddingHeaderLen + 10) // room in front, none behind
	b.Resize(vision.PaddingHeaderLen, 0)
	_, _ = b.Write([]byte("0123456789"))
	got := vision.EnsureHeadroom(b)
	if got == b {
		t.Fatal("a buffer with no room behind was kept")
	}
	uuid := []byte("0123456789abcdef")
	if applyPadding(got, &uuid) {
		t.Fatal("ApplyPadding panicked on the copy")
	}
}

func TestEnsureHeadroomLargeBufferNeedsNoRearRoom(t *testing.T) {
	// 900 bytes or more get no padding, so the room behind is not needed
	b := buf.NewSize(vision.PaddingHeaderLen + 900)
	b.Resize(vision.PaddingHeaderLen, 0)
	_, _ = b.Write(bytes.Repeat([]byte{'x'}, 900))
	if got := vision.EnsureHeadroom(b); got != b {
		t.Fatal("a 900 byte buffer with the front room was copied")
	}
}
