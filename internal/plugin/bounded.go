package plugin

import (
	"fmt"
	"io"
)

// DefaultMaxMessageBytes caps a single plugin→daemon JSON-RPC message. Plugin
// output is untrusted (§8.2): a compromised or buggy plugin must not be able to
// stream a 10 GB result and OOM the daemon. Newline-delimited framing lets us
// bound per message rather than for the whole (long-lived) connection.
const DefaultMaxMessageBytes = 8 << 20 // 8 MiB

// boundedReader caps the number of bytes that may pass between newlines. The
// JSON decoder reads until it has one complete value; if that value (one
// message) exceeds the cap, Read fails and the connection tears down — a hard
// ceiling on any single response.
type boundedReader struct {
	r     io.Reader
	max   int
	since int // bytes read since the last newline
}

func newBoundedReader(r io.Reader, max int) *boundedReader {
	if max <= 0 {
		max = DefaultMaxMessageBytes
	}
	return &boundedReader{r: r, max: max}
}

func (b *boundedReader) Read(p []byte) (int, error) {
	n, err := b.r.Read(p)
	for i := 0; i < n; i++ {
		if p[i] == '\n' {
			b.since = 0
			continue
		}
		b.since++
		if b.since > b.max {
			return i, fmt.Errorf("plugin: message exceeded %d bytes — refusing oversized output", b.max)
		}
	}
	return n, err
}
