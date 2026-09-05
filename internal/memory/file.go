package memory

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// dirBackend keeps one human-readable Markdown-with-frontmatter file per
// memory (`dir: <path>` / `type: file`): the frontmatter is
// id/tags/scope/source/created, the body is the text. Inspectable,
// hand-editable, and git-friendly — durable without a store. Hand-edited
// files are tolerated: a file without (or with broken) frontmatter still
// reads as a memory, with its ID from the filename and its created time from
// the file's mtime.
type dirBackend struct{ dir string }

// NewDirBackend backs memory with a directory of .md files, creating it if
// needed.
func NewDirBackend(dir string) (Backend, error) {
	dir = expandTilde(dir)
	if dir == "" {
		return nil, fmt.Errorf("memory: dir: is required for the file backend")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("memory: dir %s: %w", dir, err)
	}
	return dirBackend{dir: dir}, nil
}

func expandTilde(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, strings.TrimPrefix(p, "~"))
		}
	}
	return p
}

// frontmatter is the YAML header of one memory file. Text lives in the body.
type frontmatter struct {
	ID      string   `yaml:"id"`
	Tags    []string `yaml:"tags,omitempty"`
	Scope   string   `yaml:"scope"`
	Source  Source   `yaml:"source,omitempty"`
	Created string   `yaml:"created"`
}

func (b dirBackend) path(id string) string {
	// IDs are minted hex, but a hand-created file's ID is its filename — keep
	// writes safe against path characters either way.
	safe := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			return r
		}
		return '-'
	}, id)
	return filepath.Join(b.dir, safe+".md")
}

func (b dirBackend) Put(e Entry) error {
	fm := frontmatter{
		ID:      e.ID,
		Tags:    e.Tags,
		Scope:   e.Scope,
		Source:  e.Source,
		Created: e.Created.UTC().Format(time.RFC3339),
	}
	head, err := yaml.Marshal(fm)
	if err != nil {
		return err
	}
	content := "---\n" + string(head) + "---\n\n" + strings.TrimSpace(e.Text) + "\n"
	tmp := b.path(e.ID) + ".tmp"
	if err := os.WriteFile(tmp, []byte(content), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, b.path(e.ID))
}

func (b dirBackend) Delete(id string) (bool, error) {
	err := os.Remove(b.path(id))
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

func (b dirBackend) List() ([]Entry, error) {
	des, err := os.ReadDir(b.dir)
	if err != nil {
		return nil, err
	}
	var out []Entry
	for _, de := range des {
		name := de.Name()
		if de.IsDir() || !strings.HasSuffix(name, ".md") || strings.HasPrefix(name, ".") {
			continue
		}
		e, err := b.read(filepath.Join(b.dir, name))
		if err != nil {
			continue // an unreadable file must not break every recall
		}
		out = append(out, e)
	}
	return out, nil
}

// read parses one memory file, tolerating hand edits: missing/unparseable
// frontmatter degrades to a body-only memory with defaults filled from the
// file itself.
func (b dirBackend) read(path string) (Entry, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Entry{}, err
	}
	head, body, hasFM := splitFrontmatter(string(raw))
	e := Entry{Text: strings.TrimSpace(body), Scope: "global"}
	if hasFM {
		var fm frontmatter
		if yaml.Unmarshal([]byte(head), &fm) == nil {
			e.ID = fm.ID
			e.Tags = normTags(fm.Tags)
			if fm.Scope != "" {
				e.Scope = fm.Scope
			}
			e.Source = fm.Source
			if t, terr := time.Parse(time.RFC3339, fm.Created); terr == nil {
				e.Created = t
			}
		} else {
			// Broken frontmatter: keep the whole file as the text rather than
			// losing the hand-written content.
			e.Text = strings.TrimSpace(string(raw))
		}
	}
	if e.ID == "" {
		e.ID = strings.TrimSuffix(filepath.Base(path), ".md")
	}
	if e.Created.IsZero() {
		if fi, ferr := os.Stat(path); ferr == nil {
			e.Created = fi.ModTime().UTC()
		}
	}
	if e.Text == "" {
		return Entry{}, fmt.Errorf("memory: %s: empty memory", path)
	}
	return e, nil
}

// splitFrontmatter cuts a leading "---\n…\n---" block off a file.
func splitFrontmatter(s string) (head, body string, ok bool) {
	n := strings.ReplaceAll(s, "\r\n", "\n")
	if !strings.HasPrefix(n, "---\n") {
		return "", s, false
	}
	rest := n[len("---\n"):]
	end := strings.Index(rest, "\n---")
	if end < 0 {
		return "", s, false
	}
	head = rest[:end+1]
	body = rest[end+len("\n---"):]
	body = strings.TrimPrefix(body, "\n")
	return head, body, true
}

func (b dirBackend) Close() error { return nil }
