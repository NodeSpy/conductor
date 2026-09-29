package jail

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"time"
)

// The broker protocol: newline-delimited JSON frames over the dispatch's own
// unix socket. The shim opens a connection, sends one Request frame, and
// reads Reply frames until one carries Done. A host command streams its
// output as Out/Err frames before the final one.

// Env names the jail sets for its shims.
const (
	EnvSock  = "CONDUCTOR_JAIL_SOCK" // the broker socket, as seen inside the jail
	EnvToken = "CONDUCTOR_JAIL_ID"   // the dispatch's broker credential (its identity at the socket)
	EnvReal  = "CONDUCTOR_JAIL_REAL" // PATH-style list of dirs holding the real binaries (native runs)
)

// Request is one shim → broker call.
type Request struct {
	Op    string `json:"op"` // exec | git_list | git_fetch | git_push | sign | hook
	Token string `json:"token"`

	// exec
	Tool  string            `json:"tool,omitempty"`
	Args  []string          `json:"args,omitempty"`
	Cwd   string            `json:"cwd,omitempty"`
	Env   map[string]string `json:"env,omitempty"`
	Stdin []byte            `json:"stdin,omitempty"`

	// git
	Remote string            `json:"remote,omitempty"` // the helper's remote url (owner/repo)
	Wants  []GitWant         `json:"wants,omitempty"`
	Pushes []GitPush         `json:"pushes,omitempty"`
	Filter string            `json:"filter,omitempty"`
	Opts   map[string]string `json:"opts,omitempty"`

	// sign
	Format  string `json:"format,omitempty"` // ssh | openpgp
	Payload []byte `json:"payload,omitempty"`

	// hook
	Event string          `json:"event,omitempty"`
	Hook  json.RawMessage `json:"hook,omitempty"`
}

// GitWant is one object a fetch needs.
type GitWant struct {
	SHA  string `json:"sha"`
	Name string `json:"name"`
}

// GitPush is one ref update a push asks for.
type GitPush struct {
	SHA    string `json:"sha"` // the local commit ("" = delete)
	Src    string `json:"src"` // as git gave it
	Dst    string `json:"dst"` // the remote ref
	Force  bool   `json:"force"`
	OldSHA string `json:"old,omitempty"`
}

// Reply is one broker → shim frame.
type Reply struct {
	Out  []byte `json:"out,omitempty"`
	Err  []byte `json:"err,omitempty"`
	Done bool   `json:"done,omitempty"`

	Exit    int    `json:"exit,omitempty"`
	Refused string `json:"refused,omitempty"` // a policy refusal (shown to the agent)
	Error   string `json:"error,omitempty"`   // a failure to run at all

	// exec: run natively in the jail instead.
	Native bool `json:"native,omitempty"`

	// git
	Refs    []string          `json:"refs,omitempty"` // "<sha> <ref>" / "@<target> HEAD" lines
	Results map[string]string `json:"results,omitempty"`

	// sign
	Sig    []byte `json:"sig,omitempty"`
	Status []byte `json:"status,omitempty"`

	// hook
	Decision string `json:"decision,omitempty"` // "" | deny
	Reason   string `json:"reason,omitempty"`
}

// maxFrame bounds one frame (a host command's stdin rides in the request).
const maxFrame = 16 << 20

// dial connects to the broker named by the environment.
func dial() (net.Conn, string, error) {
	sock := os.Getenv(EnvSock)
	tok := os.Getenv(EnvToken)
	if sock == "" || tok == "" {
		return nil, "", fmt.Errorf("not inside a conductor jail (%s unset)", EnvSock)
	}
	c, err := net.DialTimeout("unix", sock, 10*time.Second)
	if err != nil {
		return nil, "", fmt.Errorf("conductor broker unreachable: %w", err)
	}
	return c, tok, nil
}

// Call sends one request and delivers every reply frame to onFrame, returning
// the final one.
func Call(req Request, onFrame func(Reply)) (Reply, error) {
	c, tok, err := dial()
	if err != nil {
		return Reply{}, err
	}
	defer c.Close()
	req.Token = tok
	if err := json.NewEncoder(c).Encode(req); err != nil {
		return Reply{}, err
	}
	r := bufio.NewReaderSize(c, 64<<10)
	for {
		line, err := readFrame(r)
		if err != nil {
			if err == io.EOF {
				return Reply{}, fmt.Errorf("conductor broker closed the connection")
			}
			return Reply{}, err
		}
		var rep Reply
		if err := json.Unmarshal(line, &rep); err != nil {
			return Reply{}, fmt.Errorf("conductor broker: bad frame: %w", err)
		}
		if onFrame != nil {
			onFrame(rep)
		}
		if rep.Done {
			return rep, nil
		}
	}
}

func readFrame(r *bufio.Reader) ([]byte, error) {
	var buf []byte
	for {
		chunk, isPrefix, err := r.ReadLine()
		if err != nil {
			return nil, err
		}
		buf = append(buf, chunk...)
		if len(buf) > maxFrame {
			return nil, fmt.Errorf("frame over %d bytes", maxFrame)
		}
		if !isPrefix {
			return buf, nil
		}
	}
}

// frameWriter serializes Reply frames onto a connection.
type frameWriter struct {
	enc *json.Encoder
	mu  chan struct{}
}

func newFrameWriter(w io.Writer) *frameWriter {
	fw := &frameWriter{enc: json.NewEncoder(w), mu: make(chan struct{}, 1)}
	fw.mu <- struct{}{}
	return fw
}

func (f *frameWriter) send(r Reply) error {
	<-f.mu
	defer func() { f.mu <- struct{}{} }()
	return f.enc.Encode(r)
}

// streamWriter adapts a frame stream to an io.Writer (stdout or stderr).
type streamWriter struct {
	fw  *frameWriter
	err bool
}

func (s streamWriter) Write(p []byte) (int, error) {
	b := append([]byte(nil), p...)
	var r Reply
	if s.err {
		r.Err = b
	} else {
		r.Out = b
	}
	if err := s.fw.send(r); err != nil {
		return 0, err
	}
	return len(p), nil
}

// shortArgs renders a command line for the audit trail and watch, bounded.
func shortArgs(tool string, args []string) string {
	parts := append([]string{tool}, args...)
	for i, p := range parts {
		if strings.ContainsAny(p, " \t\n\"'") {
			parts[i] = fmt.Sprintf("%q", p)
		}
	}
	s := strings.Join(parts, " ")
	if len(s) > 300 {
		s = s[:300] + "…"
	}
	return s
}

var jsonUnmarshal = json.Unmarshal
