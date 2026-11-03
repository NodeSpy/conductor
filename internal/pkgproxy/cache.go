package pkgproxy

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Cache is the proxy's on-disk store, shared by every agent: only content
// that was verified goes in, each artifact is written once (concurrent
// requests for the same one wait for a single fill), and the total size is
// bounded — past Max the least recently served artifacts are evicted.
type Cache struct {
	dir  string
	root *os.Root
	max  int64

	mu       sync.Mutex
	size     int64
	flight   map[string]*fill
	evicting bool
}

type fill struct {
	done chan struct{}
	err  error
}

const tmpDir = ".tmp"

// OpenCache opens (creating) the cache at dir, bounded to max bytes.
func OpenCache(dir string, max int64) (*Cache, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	_ = os.RemoveAll(filepath.Join(dir, tmpDir)) // fills a previous run never finished
	if err := os.Mkdir(filepath.Join(dir, tmpDir), 0o700); err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	c := &Cache{dir: dir, root: root, max: max, flight: map[string]*fill{}}
	for _, f := range c.files() {
		c.size += f.size
	}
	return c, nil
}

// Dir is the cache's directory.
func (c *Cache) Dir() string { return c.dir }

// Close releases the cache.
func (c *Cache) Close() { _ = c.root.Close() }

// checkKey refuses a key that is not a plain relative slash path: no empty,
// dot or dot-dot element, nothing absolute, nothing in the temp area. Keys
// are built from validated, escaped names already; this is the backstop,
// with os.Root beneath it.
func checkKey(key string) error {
	if key == "" || strings.HasPrefix(key, "/") || strings.Contains(key, "\\") || strings.ContainsRune(key, 0) {
		return fmt.Errorf("pkgproxy: bad cache key %q", key)
	}
	for _, el := range strings.Split(key, "/") {
		if el == "" || el == "." || el == ".." || el == tmpDir {
			return fmt.Errorf("pkgproxy: bad cache key %q", key)
		}
	}
	if path.Clean(key) != key {
		return fmt.Errorf("pkgproxy: bad cache key %q", key)
	}
	return nil
}

// Open opens a cached artifact, marking it recently used. The error is
// fs.ErrNotExist (wrapped) when it is not cached.
func (c *Cache) Open(key string) (*os.File, error) {
	if err := checkKey(key); err != nil {
		return nil, err
	}
	f, err := c.root.Open(filepath.FromSlash(key))
	if err != nil {
		return nil, err
	}
	now := time.Now()
	_ = c.root.Chtimes(filepath.FromSlash(key), now, now)
	return f, nil
}

// Fill caches key once: if it is absent, write runs (one caller at a time
// per key; the others wait) into a temp file that becomes the artifact only
// when write succeeds. It returns whether this call fetched it.
func (c *Cache) Fill(key string, write func(tmp *os.File) error) (fetched bool, err error) {
	if err := checkKey(key); err != nil {
		return false, err
	}
	for {
		if _, err := c.root.Stat(filepath.FromSlash(key)); err == nil {
			return false, nil
		}
		c.mu.Lock()
		if f, ok := c.flight[key]; ok {
			c.mu.Unlock()
			<-f.done
			if f.err != nil {
				return false, f.err
			}
			continue
		}
		f := &fill{done: make(chan struct{})}
		c.flight[key] = f
		c.mu.Unlock()

		f.err = c.fillOnce(key, write)
		c.mu.Lock()
		delete(c.flight, key)
		c.mu.Unlock()
		close(f.done)
		return f.err == nil, f.err
	}
}

func (c *Cache) fillOnce(key string, write func(tmp *os.File) error) error {
	tmp, err := os.CreateTemp(filepath.Join(c.dir, tmpDir), "fill-")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := write(tmp); err != nil {
		tmp.Close()
		return err
	}
	fi, err := tmp.Stat()
	if err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	rel := filepath.FromSlash(key)
	if err := c.root.MkdirAll(filepath.Dir(rel), 0o700); err != nil {
		return err
	}
	if err := c.root.Rename(filepath.Join(tmpDir, filepath.Base(tmpName)), rel); err != nil {
		return err
	}
	c.mu.Lock()
	c.size += fi.Size()
	over := c.size > c.max && !c.evicting
	if over {
		c.evicting = true
	}
	c.mu.Unlock()
	if over {
		c.evict()
	}
	return nil
}

type cachedFile struct {
	rel  string
	size int64
	used time.Time
}

func (c *Cache) files() []cachedFile {
	var out []cachedFile
	_ = fs.WalkDir(c.root.FS(), ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if p == tmpDir {
				return fs.SkipDir
			}
			return nil
		}
		if fi, err := d.Info(); err == nil && fi.Mode().IsRegular() {
			out = append(out, cachedFile{rel: p, size: fi.Size(), used: fi.ModTime()})
		}
		return nil
	})
	return out
}

// evict removes the least recently used artifacts until the cache is at
// 90% of its bound. An artifact being served stays readable to that reader
// (it holds the file open).
func (c *Cache) evict() {
	defer func() {
		c.mu.Lock()
		c.evicting = false
		c.mu.Unlock()
	}()
	fl := c.files()
	sort.Slice(fl, func(i, j int) bool { return fl[i].used.Before(fl[j].used) })
	var total int64
	for _, f := range fl {
		total += f.size
	}
	target := c.max / 10 * 9
	for _, f := range fl {
		if total <= target {
			break
		}
		if err := c.root.Remove(f.rel); err == nil || errors.Is(err, fs.ErrNotExist) {
			total -= f.size
		}
	}
	c.mu.Lock()
	c.size = total
	c.mu.Unlock()
}

// Size is the cache's current total size in bytes.
func (c *Cache) Size() int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.size
}
