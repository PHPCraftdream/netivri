// Added by the netivri authors: see patch 8 in third_party/README.md.
package vision

import "github.com/metacubex/mihomo/common/buf"

// maxPaddingLen is the most ApplyPadding appends behind the data: the long
// padding of a short buffer, 500 random bytes plus up to 900 bytes of fill.
const maxPaddingLen = 500 + 900

// EnsureHeadroom returns buffer when ApplyPadding can wrap it in place: room
// for the padding header in front and for the padding behind the data. Else it
// returns a copy that has it and releases buffer. ApplyPadding panics on a
// buffer without the room (ExtendHeader and Extend overflow), and a panic in
// the goroutine of one connection takes the whole process down.
func EnsureHeadroom(buffer *buf.Buffer) *buf.Buffer {
	rear := 0
	if n := buffer.Len(); n < 900 {
		rear = maxPaddingLen - n
	}
	if buffer.Start() >= PaddingHeaderLen && buffer.FreeLen() >= rear {
		return buffer
	}
	wrapped := buf.NewSize(PaddingHeaderLen + buffer.Len() + rear)
	wrapped.Resize(PaddingHeaderLen, 0)
	_, _ = wrapped.Write(buffer.Bytes())
	buffer.Release()
	return wrapped
}
