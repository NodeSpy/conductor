package agentsandbox

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// Tripwires: decoy credentials in the identity home where an attacker
// looks and no build, test or agent CLI does — an SSH private key, a git
// credential store, a vault token. Each is a named pipe: a reader's open
// blocks until the watcher, polling with a non-blocking write-open, sees it
// and answers with a fake credential; so every read is seen, however
// quick, on Linux and macOS alike. (A file a project's own tests read —
// cloud SDK credentials, a docker or kube config, .netrc — would trip on
// honest work and is not a decoy.)

var decoys = []struct {
	rel  string
	body func() string
}{
	{".ssh/id_ed25519", func() string {
		return "-----BEGIN OPENSSH PRIVATE KEY-----\n" + randB64(300) + "\n-----END OPENSSH PRIVATE KEY-----\n"
	}},
	{".ssh/id_rsa", func() string {
		return "-----BEGIN OPENSSH PRIVATE KEY-----\n" + randB64(1600) + "\n-----END OPENSSH PRIVATE KEY-----\n"
	}},
	{".git-credentials", func() string {
		return "https://deploy:ghp_" + randAlnum(36) + "@github.com\n"
	}},
	{".vault-token", func() string { return "hvs." + randAlnum(24) + "\n" }},
	{".pypirc", func() string {
		return "[pypi]\nusername = __token__\npassword = pypi-" + randAlnum(60) + "\n"
	}},
}

func randB64(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	s := base64.StdEncoding.EncodeToString(b)
	var out strings.Builder
	for len(s) > 70 {
		out.WriteString(s[:70] + "\n")
		s = s[70:]
	}
	out.WriteString(s)
	return out.String()
}

func randAlnum(n int) string {
	const set = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, n)
	_, _ = rand.Read(b)
	for i := range b {
		b[i] = set[int(b[i])%len(set)]
	}
	return string(b)
}

// tripwire is one planted decoy.
type tripwire struct {
	home, rel, path string
	body            func() string
	poll            time.Duration
}

// plantTripwires makes each decoy a named pipe in home (replacing whatever
// the agent left at its path).
func plantTripwires(home string) ([]*tripwire, error) {
	var out []*tripwire
	for _, d := range decoys {
		w := &tripwire{home: home, rel: d.rel, path: filepath.Join(home, d.rel), body: d.body, poll: 100 * time.Millisecond}
		if err := w.plant(); err != nil {
			return nil, fmt.Errorf("agentsandbox: tripwire %s: %w", d.rel, err)
		}
		out = append(out, w)
	}
	return out, nil
}

func (w *tripwire) plant() error {
	if err := homeDir(w.home, filepath.Dir(w.path)); err != nil {
		return err
	}
	if fi, err := os.Lstat(w.path); err == nil {
		if fi.Mode()&os.ModeNamedPipe != 0 {
			return nil
		}
		if err := os.RemoveAll(w.path); err != nil {
			return err
		}
	}
	return syscall.Mkfifo(w.path, 0o600)
}

// removeTripwires takes the decoys out of home (detection off): a pipe
// nobody answers would hang its reader.
func removeTripwires(home string) {
	for _, d := range decoys {
		p := filepath.Join(home, d.rel)
		if fi, err := os.Lstat(p); err == nil && fi.Mode()&os.ModeNamedPipe != 0 {
			_ = os.Remove(p)
		}
	}
}

// fifoReaders names the processes holding p open (Linux; "" elsewhere).
var fifoReaders = func(string) string { return "" }

// watch answers every reader of the decoy, reporting each.
func (w *tripwire) watch(stop <-chan struct{}, hit func(reader string)) {
	t := time.NewTicker(w.poll)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
		}
		f, err := w.openReader()
		if err != nil {
			if !errors.Is(err, syscall.ENXIO) {
				// Gone or replaced: plant it again (the agent may not
				// remove a tripwire to read past it).
				_ = w.plant()
			}
			continue
		}
		reader := fifoReaders(w.path)
		_, _ = f.WriteString(w.body())
		f.Close()
		hit(reader)
	}
}

// openReader opens the decoy's write end only when a reader is waiting
// (ENXIO otherwise), never through a symlink, and only if it is still the
// pipe.
func (w *tripwire) openReader() (*os.File, error) {
	if err := homeDir(w.home, filepath.Dir(w.path)); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(w.path, os.O_WRONLY|syscall.O_NONBLOCK|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	if fi, err := f.Stat(); err != nil || fi.Mode()&os.ModeNamedPipe == 0 {
		f.Close()
		return nil, errors.New("not the decoy pipe")
	}
	return f, nil
}
