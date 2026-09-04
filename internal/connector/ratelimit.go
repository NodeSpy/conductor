package connector

import (
	"context"
	"sync"
	"time"
)

// rateLimiter is a minimal sliding-window limiter for a connector's outbound
// verb calls (policy rate_limits.per_minute). Invocations past the cap block
// until the window frees rather than erroring — verbs are usually feedback
// (comments, reactions) where late beats dropped.
type rateLimiter struct {
	perMinute int
	mu        sync.Mutex
	stamps    []time.Time
	now       func() time.Time
	sleep     func(context.Context, time.Duration) error
}

func newRateLimiter(perMinute int) *rateLimiter {
	return &rateLimiter{
		perMinute: perMinute,
		now:       time.Now,
		sleep: func(ctx context.Context, d time.Duration) error {
			t := time.NewTimer(d)
			defer t.Stop()
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-t.C:
				return nil
			}
		},
	}
}

// wait blocks until an invocation slot is free in the rolling minute.
func (r *rateLimiter) wait(ctx context.Context) error {
	for {
		r.mu.Lock()
		cut := r.now().Add(-time.Minute)
		kept := r.stamps[:0]
		for _, s := range r.stamps {
			if s.After(cut) {
				kept = append(kept, s)
			}
		}
		r.stamps = kept
		if len(r.stamps) < r.perMinute {
			r.stamps = append(r.stamps, r.now())
			r.mu.Unlock()
			return nil
		}
		wait := time.Minute - r.now().Sub(r.stamps[0])
		r.mu.Unlock()
		if wait < 10*time.Millisecond {
			wait = 10 * time.Millisecond
		}
		if err := r.sleep(ctx, wait); err != nil {
			return err
		}
	}
}
