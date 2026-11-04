package agentsandbox

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A binary is identified by what it is: the indexed file itself, a link to
// it, or a byte-for-byte copy under any name — never a different file of
// the same size.
func TestBinaryIdentityFollowsCopies(t *testing.T) {
	bin, work := t.TempDir(), t.TempDir()
	bash := bytes.Repeat([]byte("bash-binary-"), 1000)
	socat := bytes.Repeat([]byte("socat-bin---"), 1000) // same size as bash
	os.WriteFile(filepath.Join(bin, "bash"), bash, 0o755)
	os.WriteFile(filepath.Join(bin, "socat"), socat, 0o755)
	os.WriteFile(filepath.Join(bin, "python3"), []byte("py"), 0o755)
	ix := indexBinaries([]string{bin, bin, "relative/dir"})

	for path, want := range map[string]identity{
		filepath.Join(bin, "bash"):    {"bash", classShell},
		filepath.Join(bin, "socat"):   {"socat", classTunnel},
		filepath.Join(bin, "python3"): {"python3", classInterp},
	} {
		if id, ok := ix.identify(path); !ok || id != want {
			t.Errorf("%s: %+v %v", path, id, ok)
		}
	}
	copyTo := func(name string, b []byte) string {
		p := filepath.Join(work, name)
		os.WriteFile(p, b, 0o755)
		return p
	}
	if id, ok := ix.identify(copyTo("esbuild", bash)); !ok || id.name != "bash" || id.class != classShell {
		t.Errorf("a renamed copy of bash: %+v %v", id, ok)
	}
	if id, ok := ix.identify(copyTo("dep-sync", socat)); !ok || id.name != "socat" {
		t.Errorf("a renamed copy of socat: %+v %v", id, ok)
	}
	link := filepath.Join(work, "builder")
	if err := os.Link(filepath.Join(bin, "bash"), link); err == nil {
		if id, ok := ix.identify(link); !ok || id.name != "bash" {
			t.Errorf("a hard link to bash: %+v %v", id, ok)
		}
	}
	other := append([]byte(nil), bash...)
	other[0] ^= 1
	if id, ok := ix.identify(copyTo("tool", other)); ok {
		t.Errorf("a different file of the same size is identified: %+v", id)
	}
	if _, ok := ix.identify(filepath.Join(work, "missing")); ok {
		t.Error("a missing file is identified")
	}
	var nilIx *binaryIndex
	if _, ok := nilIx.identify("/bin/sh"); ok {
		t.Error("no index identifies")
	}
}

// A shell is judged by its binary and every descriptor; an interpreter
// keeps its own sockets; an allowance is for what a program is.
func TestClassifyByIdentityAndEveryDescriptor(t *testing.T) {
	bashID, socatID := identity{"bash", classShell}, identity{"socat", classTunnel}
	cases := []struct {
		name  string
		p     procInfo
		allow map[string]bool
		want  string
	}{
		{"shell, socket on fd 3", procInfo{args: []string{"bash"}, inetFDs: []int{3}}, nil, "reverse_shell"},
		{"renamed shell, socket on stdin", procInfo{args: []string{"./esbuild"}, id: bashID, stdio: [3]string{"inet"}, inetFDs: []int{0}}, nil, "reverse_shell"},
		{"renamed shell, socket on fd 5", procInfo{args: []string{"./builder"}, id: bashID, inetFDs: []int{5}}, nil, "reverse_shell"},
		{"renamed shell, no socket", procInfo{args: []string{"./builder"}, id: bashID}, nil, ""},
		{"renamed socat", procInfo{args: []string{"./dep-sync", "tcp:x:443"}, id: socatID}, nil, "tunnel_tool"},
		{"socat named after an allowed tool", procInfo{args: []string{"ssh"}, id: socatID}, map[string]bool{"ssh": true}, "tunnel_tool"},
		{"the allowed tool itself", procInfo{args: []string{"ssh"}, id: identity{"ssh", classTunnel}}, map[string]bool{"ssh": true}, ""},
		{"python with its own socket", procInfo{args: []string{"python3", "-m", "pip"}, inetFDs: []int{4}}, nil, ""},
		{"python on a socket", procInfo{args: []string{"python3"}, stdio: [3]string{"inet", "inet"}, inetFDs: []int{0, 1}}, nil, "reverse_shell"},
		{"node by identity, own socket", procInfo{args: []string{"./x"}, id: identity{"node", classInterp}, inetFDs: []int{9}}, nil, ""},
		{"an unknown binary", procInfo{args: []string{"./tool"}, inetFDs: []int{3}}, nil, ""},
	}
	for _, c := range cases {
		v := classify(c.p, c.allow, false, false)
		got := ""
		if v != nil {
			got = v.kind
		}
		if got != c.want {
			t.Errorf("%s: got %q (%+v), want %q", c.name, got, v, c.want)
		}
		if v != nil && c.p.id.name != "" && c.p.id.name != c.p.progName() && !strings.Contains(v.reason, "which is "+c.p.id.name+" run as") {
			t.Errorf("%s: the reason does not say what it is a copy of: %s", c.name, v.reason)
		}
	}
}
