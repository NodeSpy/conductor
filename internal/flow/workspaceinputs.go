package flow

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/NodeSpy/conductor/internal/blob"
	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/dispatch"
)

// inputsKey is where a step's materialized `workspace.inputs` sit in the
// scope its prompt and code see: {{.workspace_inputs.<name>.path}}, and
// ctx.workspace_inputs in a code step. Not `inputs`: that is the
// workflow's own inputs.
const inputsKey = "workspace_inputs"

// stepInputs is a step's materialized `workspace.inputs`: one directory of
// the step's own holding a fresh copy of each, removed when the step ends.
type stepInputs struct {
	dir  string
	list []dispatch.Input
}

// remove deletes the step's copies. Safe on nil.
func (s *stepInputs) remove() {
	if s != nil && s.dir != "" {
		_ = os.RemoveAll(s.dir)
	}
}

// scope is the inputs as the step's scope sees them, by name.
func (s *stepInputs) scope() map[string]any {
	m := make(map[string]any, len(s.list))
	for _, in := range s.list {
		m[in.Name] = map[string]any{"path": in.Path, "kind": in.Kind, "name": in.BlobName, "media_type": in.MediaType}
	}
	return m
}

// env is the inputs as env vars: CONDUCTOR_INPUT_<NAME>=<path>.
func (s *stepInputs) env() map[string]string {
	m := make(map[string]string, len(s.list))
	for _, in := range s.list {
		m["CONDUCTOR_INPUT_"+strings.ToUpper(in.Name)] = in.Path
	}
	return m
}

// withScope returns data with the inputs added under inputsKey, leaving
// data itself (shared with the rest of the run) untouched.
func (s *stepInputs) withScope(data map[string]any) map[string]any {
	out := make(map[string]any, len(data)+1)
	for k, v := range data {
		out[k] = v
	}
	out[inputsKey] = s.scope()
	return out
}

// promptNote tells an agent where its inputs are.
func (s *stepInputs) promptNote() string {
	var b strings.Builder
	b.WriteString("\n\nInput artifacts for this step (fresh copies of your own, outside your workspace; read or change them freely, they are removed when the step ends and are never pushed):\n")
	for _, in := range s.list {
		fmt.Fprintf(&b, "- %s (%s): %s\n", in.Name, in.Kind, in.Path)
	}
	return b.String()
}

// workspaceInputs makes the step's `workspace.inputs`: a fresh directory of
// its own under the runner's InputsDir, and in it, under each input's name,
// a copy of the blob — unpacked (with blob.Untar's safety rules) when it is
// a tar archive, copied as its file otherwise. Every blob must be one this
// run holds. nil when the step names none; on error nothing is left behind.
func (r *Runner) workspaceInputs(ctx context.Context, step config.Step, data map[string]any) (_ *stepInputs, err error) {
	if len(step.Workspace.Inputs) == 0 {
		return nil, nil
	}
	if agentAuthored(ctx) {
		return nil, fmt.Errorf("workspace.inputs: refused on an agent-authored step")
	}
	if r.InputsDir == "" {
		return nil, fmt.Errorf("workspace.inputs: no inputs directory is configured")
	}
	if err := os.MkdirAll(r.InputsDir, 0o700); err != nil {
		return nil, fmt.Errorf("workspace.inputs: %w", err)
	}
	dir, err := os.MkdirTemp(r.InputsDir, "step-*")
	if err != nil {
		return nil, fmt.Errorf("workspace.inputs: %w", err)
	}
	s := &stepInputs{dir: dir}
	defer func() {
		if err != nil {
			s.remove()
		}
	}()
	for _, in := range step.Workspace.Inputs {
		field := "workspace.inputs." + in.Name
		src, h, err := r.resolveBlobRef(ctx, field, in.From, data)
		if err != nil {
			return nil, err
		}
		got, err := materializeInput(filepath.Join(dir, in.Name), src, h)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", field, err)
		}
		got.Name = in.Name
		s.list = append(s.list, got)
	}
	return s, nil
}

// materializeInput puts a copy of the blob at src under dst: the unpacked
// tree when it is a tar archive (Kind "dir", Path dst), else the file itself
// at dst/<its name> (Kind "file", Path that file).
func materializeInput(dst, src string, h blob.Handle) (dispatch.Input, error) {
	in := dispatch.Input{BlobName: h.Meta.Name, MediaType: h.Meta.MediaType}
	f, err := os.Open(src)
	if err != nil {
		return in, err
	}
	defer f.Close()
	if isTar(f, h.Meta) {
		if err := blob.Untar(f, dst); err != nil {
			return in, err
		}
		in.Kind, in.Path = "dir", dst
		return in, nil
	}
	if err := os.Mkdir(dst, 0o700); err != nil {
		return in, err
	}
	name := filepath.Base(h.Meta.Name)
	if name == "." || name == ".." || name == string(filepath.Separator) || name == "" || strings.ContainsAny(name, `/\`) {
		name = filepath.Base(dst)
	}
	p := filepath.Join(dst, name)
	out, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return in, err
	}
	_, err = io.Copy(out, f)
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return in, err
	}
	in.Kind, in.Path = "file", p
	return in, nil
}

// isTar reports whether the blob is a tar archive: by its media type or
// name when it carries them, else by the ustar magic at offset 257. f is
// left at its start.
func isTar(f *os.File, m blob.Meta) bool {
	mt := strings.ToLower(strings.TrimSpace(strings.SplitN(m.MediaType, ";", 2)[0]))
	switch {
	case mt == "application/x-tar" || mt == "application/tar":
		return true
	case strings.HasSuffix(strings.ToLower(m.Name), ".tar"):
		return true
	case mt != "":
		return false
	}
	var magic [5]byte
	n, _ := f.ReadAt(magic[:], 257)
	return n == len(magic) && string(magic[:]) == "ustar"
}

// SweepInputs removes step input directories under dir older than maxAge:
// what a daemon that died mid-step left behind. It returns how many.
func SweepInputs(dir string, maxAge time.Duration) int {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	n := 0
	for _, e := range ents {
		fi, err := e.Info()
		if err != nil || !strings.HasPrefix(e.Name(), "step-") || time.Since(fi.ModTime()) < maxAge {
			continue
		}
		if os.RemoveAll(filepath.Join(dir, e.Name())) == nil {
			n++
		}
	}
	return n
}
