//go:build linux

package sandbox

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"unsafe"

	"golang.org/x/sys/unix"
)

// TestNoExecJailRefusesDropAndExec runs a payload through RunEnter with
// NoExecBase (the plugin jail's exec policy) and has it try to run a copy of
// a program from every place it can write — a writable bind, /tmp,
// /dev/shm, the jail's root — and from a memfd (by path and by fd), and to
// execveat an executable fd. All are refused; the payload's own binary
// (an Exec bind) still runs, and no_new_privs is set.
func TestNoExecJailRefusesDropAndExec(t *testing.T) {
	switch os.Getenv("NOEXEC_ROLE") {
	case "probe":
		os.Exit(runNoExecProbe())
	case "outer":
		os.Exit(runNoExecOuter())
	case "ran":
		os.Exit(0)
	case "escaped":
		// The probe's execveat of an fd succeeded and replaced it.
		fmt.Println("execveat(AT_EMPTY_PATH) ran a program")
		os.Exit(9)
	}
	if _, err := exec.LookPath("unshare"); err != nil {
		t.Skip("unshare not on PATH")
	}
	if err := exec.Command("unshare", "--user", "--map-root-user", "--mount", "true").Run(); err != nil {
		t.Skipf("requires unprivileged root-mapped user namespaces: %v", err)
	}
	work := t.TempDir()
	argv := []string{"unshare", "--user", "--map-root-user", "--pid", "--fork",
		"--mount-proc", "--kill-child", "--",
		os.Args[0], "-test.run=^TestNoExecJailRefusesDropAndExec$"}
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Env = append(os.Environ(), "NOEXEC_ROLE=outer", "NOEXEC_WORK="+work)
	out, err := cmd.CombinedOutput()
	if ee, ok := err.(*exec.ExitError); ok && ee.ExitCode() == jailEnvCannot {
		t.Skipf("environment cannot build the jail\n%s", out)
	}
	if err != nil {
		t.Fatalf("the jail let a payload run a program it dropped, or broke: %v\n%s", err, out)
	}
	t.Logf("%s", out)
}

func runNoExecOuter() int {
	os.Setenv("NOEXEC_ROLE", "probe")
	return RunEnter(EnterOpts{
		Binds: []BindMount{
			{Path: os.Getenv("NOEXEC_WORK")},
			{Path: filepath.Dir(os.Args[0]), RO: true, Exec: true},
		},
		NoExecBase: true,
		Argv:       []string{os.Args[0], "-test.run=^TestNoExecJailRefusesDropAndExec$"},
	})
}

func runNoExecProbe() int {
	var failed []string
	fail := func(f string, a ...any) {
		// Printed at once: a successful execveat below replaces this process.
		failed = append(failed, fmt.Sprintf(f, a...))
		fmt.Println(failed[len(failed)-1])
	}
	self := os.Args[0]
	run := func(path string) error {
		c := exec.Command(path, "-test.run=^TestNoExecJailRefusesDropAndExec$")
		c.Env = append(os.Environ(), "NOEXEC_ROLE=ran")
		return c.Run()
	}
	if err := run(self); err != nil {
		fail("its own binary (an Exec bind) did not run: %v", err)
	}
	if b, _ := os.ReadFile("/proc/self/status"); !strings.Contains(string(b), "NoNewPrivs:\t1") {
		fail("no_new_privs is not set")
	}
	src, err := os.ReadFile(self)
	if err != nil {
		fail("read own binary: %v", err)
	}
	for _, dir := range []string{os.Getenv("NOEXEC_WORK"), "/tmp", "/dev/shm", "/dev", "/"} {
		p := filepath.Join(dir, "dropped")
		if err := os.WriteFile(p, src, 0o755); err != nil {
			fmt.Printf("drop into %s: not writable (%v)\n", dir, err)
			continue
		}
		if err := run(p); err == nil {
			fail("ran a program dropped into %s", dir)
		} else {
			fmt.Printf("exec from %s: %v\n", dir, err)
		}
	}
	// memfd: refused outright unless sealed non-executable.
	if fd, err := unix.MemfdCreate("payload", 0); err == nil {
		f := os.NewFile(uintptr(fd), "memfd")
		_, _ = io.Copy(f, strings.NewReader(string(src)))
		if err := run(fmt.Sprintf("/proc/self/fd/%d", fd)); err == nil {
			fail("ran a program from a memfd")
		}
		f.Close()
		fail("memfd_create without MFD_NOEXEC_SEAL was allowed")
	} else if err != unix.EPERM {
		fail("memfd_create: %v, want EPERM", err)
	}
	if fd, err := unix.MemfdCreate("shm", mfdNoexecSeal); err == nil {
		unix.Close(fd)
	} else if err != unix.EINVAL { // EINVAL: a kernel before 6.3
		fail("memfd_create(MFD_NOEXEC_SEAL): %v", err)
	}
	// execveat of an fd: refused by the filter (here an executable one, so
	// only the filter can be what refuses it).
	fd, err := unix.Open(self, unix.O_RDONLY|unix.O_CLOEXEC, 0)
	if err != nil {
		fail("open own binary: %v", err)
	} else {
		empty, _ := unix.BytePtrFromString("")
		argv0, _ := unix.BytePtrFromString(self)
		env0, _ := unix.BytePtrFromString("NOEXEC_ROLE=escaped")
		argv := []*byte{argv0, nil}
		envv := []*byte{env0, nil}
		_, _, errno := unix.Syscall6(unix.SYS_EXECVEAT, uintptr(fd), uintptr(unsafe.Pointer(empty)),
			uintptr(unsafe.Pointer(&argv[0])), uintptr(unsafe.Pointer(&envv[0])), unix.AT_EMPTY_PATH, 0)
		if errno != unix.EPERM {
			fail("execveat(AT_EMPTY_PATH) = %v, want EPERM", errno)
		}
	}
	if len(failed) > 0 {
		return 1
	}
	return 0
}
