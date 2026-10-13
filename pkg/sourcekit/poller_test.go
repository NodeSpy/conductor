package sourcekit

import (
	"context"
	"sync"
	"testing"
	"time"
)

func TestPollerRunsNowBacksOffAndNudges(t *testing.T) {
	p := NewPoller(10*time.Millisecond, 80*time.Millisecond, true)
	var mu sync.Mutex
	var at []time.Time
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		p.Run(ctx, func(context.Context) bool { mu.Lock(); at = append(at, time.Now()); mu.Unlock(); return false })
		close(done)
	}()
	time.Sleep(200 * time.Millisecond)
	mu.Lock()
	n := len(at)
	mu.Unlock()
	if n < 2 || n > 6 {
		t.Fatalf("passes in 200ms with 10ms doubling to 80ms = %d", n)
	}
	before := n
	p.Nudge()
	time.Sleep(20 * time.Millisecond)
	mu.Lock()
	after := len(at)
	mu.Unlock()
	if after <= before {
		t.Fatal("a nudge did not run a pass promptly")
	}
	cancel()
	<-done
}
