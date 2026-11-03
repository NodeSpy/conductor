package pkgproxy

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/mod/sumdb"
)

// knownSumDBKeys are checksum databases whose verifier key is built into
// the go command, so a bare name in config is enough.
var knownSumDBKeys = map[string]string{
	"sum.golang.org": "sum.golang.org+033de0ae+Ac4zctda0e5eza+HJyk9SxEdh+s3Ady8XpVPCzWyBhfx",
}

// SumDB is one checksum database: its name, verifier key and URL.
type SumDB struct {
	Name string
	Key  string // the verifier key ("name+hash+key")
	URL  string // where conductor fetches it ("https://name" by default)
	// GoEnv is the agent's GOSUMDB: the bare name for a database the go
	// command knows, else name+key.
	GoEnv string
}

// ParseSumDB parses GOSUMDB syntax: "name", "name+key" or "name+key url".
// "off" is refused — verification is not optional here.
func ParseSumDB(s string) (SumDB, error) {
	f := strings.Fields(s)
	if len(f) == 0 || len(f) > 2 {
		return SumDB{}, fmt.Errorf("pkgproxy: sumdb %q: want name, name+key or \"name+key url\"", s)
	}
	db := SumDB{}
	name, _, hasKey := strings.Cut(f[0], "+")
	if name == "off" || name == "" {
		return SumDB{}, fmt.Errorf("pkgproxy: sumdb %q: checksum verification cannot be off", s)
	}
	db.Name = name
	if hasKey {
		db.Key, db.GoEnv = f[0], f[0]
	} else {
		k, ok := knownSumDBKeys[name]
		if !ok {
			return SumDB{}, fmt.Errorf("pkgproxy: sumdb %q: no built-in key for %s; give name+key", s, name)
		}
		db.Key, db.GoEnv = k, name
	}
	if k, ok := knownSumDBKeys[name]; ok && db.Key == k {
		db.GoEnv = name
	}
	db.URL = "https://" + name
	if len(f) == 2 {
		u, err := url.Parse(f[1])
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
			return SumDB{}, fmt.Errorf("pkgproxy: sumdb %q: %q is not an http(s) URL", s, f[1])
		}
		db.URL = strings.TrimRight(f[1], "/")
	}
	return db, nil
}

// errSumNotFound marks a module the checksum database does not know (its
// text survives the sumdb client's error wrapping).
const errSumNotFound = "not found in the checksum database"

// sumOps is the sumdb client's storage and transport: the verifier key from
// config, the latest signed tree head and verified tiles/records on disk,
// and the database itself over the Fetcher.
type sumOps struct {
	db    SumDB
	dir   string // <cache>/sumdb — conductor's alone, never filled by an agent's request
	fetch *Fetcher
	onSec func(msg string)

	mu sync.Mutex
}

func (o *sumOps) ReadRemote(p string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	resp, err := o.fetch.Get(ctx, o.db.URL+p)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusGone:
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("%s %s: %s", errSumNotFound, o.db.Name, strings.TrimSpace(string(msg)))
	case resp.StatusCode != http.StatusOK:
		return nil, fmt.Errorf("checksum database %s: %s", o.db.Name, resp.Status)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 4<<20))
}

// configPath maps the client's config names: "key" is served from memory;
// "<name>/latest" lives on disk.
func (o *sumOps) configPath(file string) (string, error) {
	if file != o.db.Name+"/latest" {
		return "", fmt.Errorf("pkgproxy: unexpected sumdb config %q", file)
	}
	return filepath.Join(o.dir, "latest"), nil
}

func (o *sumOps) ReadConfig(file string) ([]byte, error) {
	if file == "key" {
		return []byte(o.db.Key), nil
	}
	p, err := o.configPath(file)
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(p)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	return b, err
}

func (o *sumOps) WriteConfig(file string, old, new []byte) error {
	p, err := o.configPath(file)
	if err != nil {
		return err
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	cur, err := os.ReadFile(p)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if !bytes.Equal(cur, old) {
		return sumdb.ErrWriteConflict
	}
	return writeAtomic(p, new)
}

// cachePath maps a client cache name ("<name>/tile/…", "<name>/lookup/…")
// under dir; the client builds those names from parsed tiles and escaped
// module paths.
func (o *sumOps) cachePath(file string) (string, bool) {
	rest, ok := strings.CutPrefix(file, o.db.Name+"/")
	if !ok || checkKey(rest) != nil {
		return "", false
	}
	return filepath.Join(o.dir, "cache", filepath.FromSlash(rest)), true
}

func (o *sumOps) ReadCache(file string) ([]byte, error) {
	p, ok := o.cachePath(file)
	if !ok {
		return nil, os.ErrNotExist
	}
	return os.ReadFile(p)
}

func (o *sumOps) WriteCache(file string, data []byte) {
	if p, ok := o.cachePath(file); ok {
		_ = writeAtomic(p, data)
	}
}

func (o *sumOps) Log(string) {}

func (o *sumOps) SecurityError(msg string) {
	if o.onSec != nil {
		o.onSec(msg)
	}
}

func writeAtomic(p string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(p), ".w-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), p)
}
