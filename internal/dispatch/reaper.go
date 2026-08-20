package dispatch

import (
	"context"
	"encoding/json"
	"os/exec"
	"strings"
	"time"
)

// Reaper archives conductor agents that requested archive-when-done once they
// go idle. It polls the local daemon only (`paseo ls`) — no GitHub API.
type Reaper struct {
	PaseoBin string
	Interval time.Duration
	Log      func(string, ...any)
}

// Run reaps on an interval until ctx is cancelled.
func (r *Reaper) Run(ctx context.Context) {
	if r.Interval <= 0 {
		r.Interval = time.Minute
	}
	t := time.NewTicker(r.Interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			r.reap(ctx)
		}
	}
}

func (r *Reaper) reap(ctx context.Context) {
	out, err := exec.CommandContext(ctx, r.PaseoBin, "ls", "--json",
		"--label", "conductor=1", "--label", "archive=1").Output()
	if err != nil {
		return
	}
	var agents []struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	}
	if err := json.Unmarshal(out, &agents); err != nil {
		return
	}
	for _, a := range agents {
		if a.ID == "" {
			continue
		}
		switch strings.ToLower(a.Status) {
		case "idle", "completed", "done", "":
			if err := exec.CommandContext(ctx, r.PaseoBin, "archive", a.ID).Run(); err == nil && r.Log != nil {
				r.Log("reaper: archived idle agent %s", a.ID)
			}
		}
	}
}
