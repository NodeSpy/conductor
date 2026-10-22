package browser

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"testing"
	"time"
)

func noDial() (net.Conn, error) { return nil, errors.New("no proxy in this test") }

// muxFrame writes one mux frame (type, channel, payload) to w.
func muxFrame(w io.Writer, typ byte, ch uint32, p []byte) error {
	var h [9]byte
	h[0] = typ
	binary.BigEndian.PutUint32(h[1:5], ch)
	binary.BigEndian.PutUint32(h[5:9], uint32(len(p)))
	if _, err := w.Write(h[:]); err != nil {
		return err
	}
	_, err := w.Write(p)
	return err
}

// stalledMux is a Mux whose shim reads the host's frames but never replies —
// a container alive but wedged mid-transfer.
func stalledMux(t *testing.T) *Mux {
	t.Helper()
	outR, outW := io.Pipe()     // shim's stdout: shim writes outW, host reads outR
	inR, inW := io.Pipe()       // shim's stdin: host writes inW, shim reads inR
	go io.Copy(io.Discard, inR) // drain so the host's send never blocks
	t.Cleanup(func() { outW.Close(); inW.Close() })
	return NewMux(outR, inW, noDial)
}

// respondingMux is a Mux whose shim answers each transfer request with an
// "ok" status and closes the channel.
func respondingMux(t *testing.T) *Mux {
	t.Helper()
	outR, outW := io.Pipe() // shim's stdout
	inR, inW := io.Pipe()   // shim's stdin
	t.Cleanup(func() { outW.Close(); inW.Close() })
	go func() {
		var h [9]byte
		for {
			if _, err := io.ReadFull(inR, h[:]); err != nil {
				return
			}
			n := binary.BigEndian.Uint32(h[5:9])
			if n > 0 {
				if _, err := io.CopyN(io.Discard, inR, int64(n)); err != nil {
					return
				}
			}
			// Reply on the file channel: a status frame, then close.
			if muxFrame(outW, frameFile, chanFile, []byte("ok")) != nil {
				return
			}
			if muxFrame(outW, frameClose, chanFile, nil) != nil {
				return
			}
		}
	}()
	return NewMux(outR, inW, noDial)
}

// A transfer whose shim stops answering unblocks on the context deadline
// instead of holding its lock forever, and the lock is released afterward.
func TestMuxTransferHonorsContext(t *testing.T) {
	m := stalledMux(t)

	run := func() error {
		ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
		defer cancel()
		done := make(chan error, 1)
		go func() { _, err := m.Get(ctx, "/x", 1<<20, io.Discard); done <- err }()
		select {
		case err := <-done:
			return err
		case <-time.After(3 * time.Second):
			t.Fatal("transfer ignored its context — blocked forever")
			return nil
		}
	}
	if err := run(); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("first transfer: want deadline, got %v", err)
	}
	// If the first transfer had not released fileMu, this one would block
	// past its own deadline.
	if err := run(); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("second transfer: fileMu not released: %v", err)
	}
}

// The transfer lock is per-Mux: one container's stalled transfer does not
// block another container's file I/O (a package-global lock would).
func TestMuxTransferIsolatedPerMux(t *testing.T) {
	stuck := stalledMux(t)
	good := respondingMux(t)

	// Hold stuck's fileMu with a transfer that will block until its own
	// (default) deadline.
	go func() { _, _ = stuck.Get(context.Background(), "/a", 1<<20, io.Discard) }()
	time.Sleep(100 * time.Millisecond) // let it grab stuck.fileMu

	done := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_, err := good.Get(ctx, "/b", 1<<20, io.Discard)
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("the other container's transfer failed: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("a stalled container's transfer blocked another's — the lock is not per-Mux")
	}
}
