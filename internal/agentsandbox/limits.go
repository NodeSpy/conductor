package agentsandbox

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/NodeSpy/conductor/internal/sandbox"
)

// Fallout limits: whatever an agent does inside its sandbox, the box it
// runs on keeps working. Each Linux launch runs in a systemd user scope with
// a memory and a tasks cap, inside one slice whose own cap bounds all agents
// together; a launch does not start — and a running one is stopped — when
// the disk it writes to is nearly full. macOS has no cgroups: only the disk
// guard applies there.

// AgentSlice is the systemd user slice every agent launch's scope runs in;
// the daemon caps it (sandbox.limits.aggregate_memory).
const AgentSlice = "conductor-agents.slice"

// Default limits.
const (
	DefaultLimitMemory  = "50%"
	DefaultLimitPids    = 8192
	DefaultDiskFreeFrac = 0.05
)

// LimitSpec is sandbox.limits for one launch; the zero value is the
// default.
type LimitSpec struct {
	Off bool `json:"off,omitempty"`
	// Memory is systemd syntax ("4G", "50%"); "" = DefaultLimitMemory.
	Memory string `json:"memory,omitempty"`
	// Pids caps tasks; 0 = DefaultLimitPids.
	Pids int `json:"pids,omitempty"`
	// CPU is a CPUQuota ("200%"; "" = none); CPUWeight the scope's
	// CPUWeight (0 = systemd's default).
	CPU       string `json:"cpu,omitempty"`
	CPUWeight int    `json:"cpu_weight,omitempty"`
	// Required: refuse the launch when the limits cannot be applied.
	Required bool `json:"required,omitempty"`
	// DiskFreeBytes / DiskFreeFrac: the free space the disk guard keeps
	// (the larger applies); both 0 = DefaultDiskFreeFrac; DiskOff: none.
	DiskFreeBytes int64   `json:"disk_free_bytes,omitempty"`
	DiskFreeFrac  float64 `json:"disk_free_frac,omitempty"`
	DiskOff       bool    `json:"disk_off,omitempty"`
}

// limitsAvailable reports whether this box can run a launch in a systemd
// user scope (a var for tests).
var limitsAvailable = func(goos string) bool { return goos == "linux" && sandbox.ScopeAvailable() }

var limitsWarned sync.Once

// applyLimits puts the launch's cgroup limits on spec (Linux), or says why
// it cannot: an error when they are required, a one-time event otherwise.
func applyLimits(spec *sandbox.Spec, l LimitSpec, goos string, event func(string, map[string]any)) error {
	if l.Off || goos != "linux" {
		return nil
	}
	if !limitsAvailable(goos) {
		if l.Required {
			return fmt.Errorf("agentsandbox: sandbox.limits.required is set and this box has no systemd user manager to apply them (systemd-run --user)")
		}
		limitsWarned.Do(func() {
			if event != nil {
				event("limits_unavailable", map[string]any{"reason": "no systemd user manager (systemd-run --user): agent launches run without memory and task caps"})
			}
		})
		return nil
	}
	spec.Memory, spec.Pids, spec.Slice, spec.Unit = l.Memory, l.Pids, AgentSlice, scopeUnitName()
	spec.CPU, spec.CPUWeight = l.CPU, l.CPUWeight
	if spec.Memory == "" {
		spec.Memory = DefaultLimitMemory
	}
	if spec.Pids == 0 {
		spec.Pids = DefaultLimitPids
	}
	return nil
}

// diskMinimum is the free space the guard keeps on a filesystem of total
// bytes.
func (l LimitSpec) diskMinimum(total uint64) uint64 {
	frac := l.DiskFreeFrac
	if frac == 0 && l.DiskFreeBytes == 0 {
		frac = DefaultDiskFreeFrac
	}
	min := uint64(float64(total) * frac)
	if b := uint64(l.DiskFreeBytes); b > min {
		min = b
	}
	return min
}

// diskLow reports a path whose filesystem is below the guard's minimum.
func (l LimitSpec) diskLow(paths ...string) string {
	if l.Off || l.DiskOff {
		return ""
	}
	for _, p := range paths {
		free, total, err := diskSpace(p)
		if err != nil || total == 0 {
			continue
		}
		if min := l.diskMinimum(total); free < min {
			return fmt.Sprintf("%s has %s free, below the %s the disk guard keeps (sandbox.limits.disk_free_min)", p, humanBytes(free), humanBytes(min))
		}
	}
	return ""
}

func humanBytes(b uint64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%dB", b)
	}
	div, exp := uint64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f%ciB", float64(b)/float64(div), "KMGTPE"[exp])
}

// CapAgentSlice sets the caps on all agent launches together: the memory
// and CPU quota of the slice their scopes run in
// (sandbox.limits.aggregate_memory, aggregate_cpu; "" leaves one as it
// is). A no-op where the limits do not apply.
func CapAgentSlice(goos, memory, cpu string) error {
	if goos != "linux" || (memory == "" && cpu == "") || !limitsAvailable(goos) {
		return nil
	}
	props := []string{"--user", "set-property", "--runtime", AgentSlice}
	if memory != "" {
		props = append(props, "MemoryMax="+memory)
	}
	if cpu != "" {
		props = append(props, "CPUQuota="+cpu)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "systemctl", props...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("cap %s: %v: %s", AgentSlice, err, strings.TrimSpace(string(out)))
	}
	return nil
}
