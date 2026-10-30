package browser

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"testing"
	"time"
)

// A cancelled file transfer must not leave stale frames for the next transfer
// to absorb: a container's reply stream on the single file channel is drained
// (the shim told to end, its remaining frames consumed) before the lock is
// freed, so a transfer that starts after a cancel never reaches the shim until
// the prior stream has closed. Regression guard for the round-3 ctx early-return
// (#2), which released the lock mid-stream and let one transfer read another's
// frames (and leaked a put's open file).
func TestMuxCancelledTransferQuiescesBeforeNext(t *testing.T) {
	hr, hw := io.Pipe() // shim stdout → host: host reads hr, test writes hw
	cr, cw := io.Pipe() // host → shim stdin: host writes cw, test reads cr
	m := NewMux(hr, cw, noDial)
	t.Cleanup(func() { hw.Close(); cw.Close() })

	type frame struct {
		typ byte
		ch  uint32
		p   []byte
	}
	frames := make(chan frame, 64)
	go func() {
		var h [9]byte
		for {
			if _, err := io.ReadFull(cr, h[:]); err != nil {
				return
			}
			n := binary.BigEndian.Uint32(h[5:9])
			p := make([]byte, n)
			if n > 0 {
				if _, err := io.ReadFull(cr, p); err != nil {
					return
				}
			}
			frames <- frame{h[0], binary.BigEndian.Uint32(h[1:5]), p}
		}
	}()
	waitFileReq := func() {
		for {
			select {
			case f := <-frames:
				if f.typ == frameFile {
					return
				}
			case <-time.After(2 * time.Second):
				t.Fatal("no file request reached the shim")
			}
		}
	}

	// Transfer A: a Get we cancel while the shim has not yet replied.
	ctxA, cancelA := context.WithCancel(context.Background())
	aErr := make(chan error, 1)
	go func() { _, e := m.Get(ctxA, "/a", 0, io.Discard); aErr <- e }()
	waitFileReq()
	cancelA()
	if err := <-aErr; !errors.Is(err, context.Canceled) {
		t.Fatalf("A: want canceled, got %v", err)
	}

	// Transfer B starts while A's shim stream is still open.
	var bOut bytes.Buffer
	bErr := make(chan error, 1)
	go func() { _, e := m.Get(context.Background(), "/b", 0, &bOut); bErr <- e }()

	// B must not reach the shim yet: A's cancelled transfer holds the file lock
	// until its shim stream is drained. (A's abort-close frame may appear; B's
	// request must not.)
	saw := time.After(300 * time.Millisecond)
	parked := false
	for !parked {
		select {
		case f := <-frames:
			if f.typ == frameFile {
				t.Fatal("B reached the shim before A's cancelled stream was drained — A's stale frames can cross into B")
			}
		case <-saw:
			parked = true
		}
	}

	// Close A's stream; A's drain completes and frees the lock, then B proceeds.
	if err := muxFrame(hw, frameClose, chanFile, nil); err != nil {
		t.Fatal(err)
	}
	waitFileReq()
	if err := muxFrame(hw, frameData, chanFile, []byte("REAL-B")); err != nil {
		t.Fatal(err)
	}
	if err := muxFrame(hw, frameFile, chanFile, []byte("ok")); err != nil {
		t.Fatal(err)
	}
	if err := muxFrame(hw, frameClose, chanFile, nil); err != nil {
		t.Fatal(err)
	}
	if err := <-bErr; err != nil {
		t.Fatalf("B failed: %v", err)
	}
	if bOut.String() != "REAL-B" {
		t.Fatalf("B received %q, want REAL-B (a stale frame of A's crossed in)", bOut.String())
	}
}
