//go:build linux

package controller

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// A warm's sandbox dies with conductor: a daemon that crashes leaves
// nothing writing the warm scratch its next start removes.
func TestWarmDiesWithConductor(t *testing.T) {
	if os.Getenv("WARM_DEATH_ROLE") == "parent" {
		cmd := exec.Command("sleep", "60")
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		unlock := warmDeathSignal(cmd.SysProcAttr)
		defer unlock()
		if err := cmd.Start(); err != nil {
			os.Exit(3)
		}
		fmt.Println(cmd.Process.Pid)
		os.Exit(0) // conductor dies, its warm still running
	}
	parent := exec.Command(os.Args[0], "-test.run=^TestWarmDiesWithConductor$")
	parent.Env = append(os.Environ(), "WARM_DEATH_ROLE=parent")
	out, err := parent.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := parent.Start(); err != nil {
		t.Fatal(err)
	}
	line, _ := bufio.NewReader(out).ReadString('\n')
	_ = parent.Wait()
	pid, err := strconv.Atoi(strings.TrimSpace(line))
	if err != nil || pid <= 1 {
		t.Fatalf("child pid %q", line)
	}
	for i := 0; i < 100; i++ {
		b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
		// Gone, or a zombie no one reaps yet: not running.
		if err != nil || strings.Contains(string(b), ") Z ") {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	if b, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid)); err == nil && strings.HasPrefix(string(b), "sleep") {
		_ = syscall.Kill(pid, syscall.SIGKILL)
		t.Fatal("the warm outlived conductor")
	}
}
