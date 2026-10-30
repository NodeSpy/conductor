package procexec

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// The helper roles: a parent that starts a child with a parent-death signal
// and then waits to be killed; a child that execs from a thread other than
// the one it started on; and the program it execs, which only waits.
func TestMain(m *testing.M) {
	switch os.Getenv("PROCEXEC_ROLE") {
	case "parent":
		c := exec.Command(os.Args[0], "-test.run=^$")
		c.Env = role("child")
		c.Stdout = os.Stdout
		c.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL}
		if err := c.Start(); err != nil {
			fmt.Println("error", err)
			os.Exit(1)
		}
		fmt.Println("pid", c.Process.Pid)
		select {}
	case "child":
		var try func()
		try = func() {
			runtime.LockOSThread()
			if unix.Gettid() == os.Getpid() {
				// Hold the start-up thread so the exec runs on another.
				go try()
				select {}
			}
			fmt.Println("error", Exec(os.Args[0], []string{os.Args[0], "-test.run=^$"}, role("execd")))
			os.Exit(1)
		}
		go try()
		select {}
	case "execd":
		fmt.Println("execd")
		time.Sleep(time.Minute)
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// role is the environment with the helper role set (Getenv reads the first
// of duplicate keys, so the old one is dropped).
func role(r string) []string {
	var env []string
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "PROCEXEC_ROLE=") {
			env = append(env, kv)
		}
	}
	return append(env, "PROCEXEC_ROLE="+r)
}

// A process that re-execs from another thread still dies with its parent.
func TestExecKeepsTheParentDeathSignal(t *testing.T) {
	parent := exec.Command(os.Args[0], "-test.run=^$")
	parent.Env = role("parent")
	out, err := parent.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := parent.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = parent.Process.Kill(); _ = parent.Wait() })
	lines := bufio.NewScanner(out)
	child := 0
	for lines.Scan() {
		f := strings.Fields(lines.Text())
		if len(f) == 2 && f[0] == "pid" {
			child, _ = strconv.Atoi(f[1])
			t.Cleanup(func() { _ = syscall.Kill(child, syscall.SIGKILL) })
			continue
		}
		if len(f) > 0 && f[0] == "execd" {
			break
		}
		t.Fatalf("helper: %s", lines.Text())
	}
	if child == 0 {
		t.Fatal("the child never exec'd")
	}
	_ = parent.Process.Kill()
	_ = parent.Wait()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if !alive(child) {
			return
		}
	}
	t.Fatal("the exec'd process outlived its parent: the parent-death signal was lost")
}

// alive reports whether pid is a process that hasn't exited (a zombie has).
func alive(pid int) bool {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return false
	}
	s := string(b)
	i := strings.LastIndexByte(s, ')')
	return i < 0 || i+2 >= len(s) || s[i+2] != 'Z'
}
