package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
)

// auditLog is an append-only JSONL writer with size-based rotation. When the
// file exceeds maxSize it is renamed to "<path>.1" (replacing any prior one)
// and a fresh file is started, so the live log never grows unbounded.
type auditLog struct {
	mu      sync.Mutex
	path    string
	maxSize int64
	f       *os.File
	size    int64
}

func openAudit(path string, maxSize int64) (*auditLog, error) {
	if path == "" {
		return nil, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, err
	}
	sz := int64(0)
	if fi, err := f.Stat(); err == nil {
		sz = fi.Size()
	}
	return &auditLog{path: path, maxSize: maxSize, f: f, size: sz}, nil
}

func (a *auditLog) write(entry map[string]any) {
	b, err := json.Marshal(entry)
	if err != nil {
		return
	}
	b = append(b, '\n')
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.maxSize > 0 && a.size+int64(len(b)) > a.maxSize {
		a.rotate()
	}
	n, _ := a.f.Write(b)
	a.size += int64(n)
}

// rotate closes the current file, moves it to .1, and opens a fresh one.
// Caller holds a.mu.
func (a *auditLog) rotate() {
	_ = a.f.Close()
	_ = os.Rename(a.path, a.path+".1")
	f, err := os.OpenFile(a.path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		// Best-effort: reopen in append mode so we don't lose the writer.
		f, _ = os.OpenFile(a.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	}
	a.f = f
	a.size = 0
}

func (a *auditLog) close() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.f != nil {
		return a.f.Close()
	}
	return nil
}
