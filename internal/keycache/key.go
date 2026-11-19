// Package keycache keeps the step caches of sandboxed agents: generic,
// keyed by what a step's own config declares, and filled by learning.
//
// A step (or its workflow) names its cache with a key template, rendered
// over the step's facts the way a memory scope key is. A launch of that step
// is shown the key's most recent published copy (an overlay with the shared
// copy as its read-only lower layer on Linux, a copy-on-write clone on
// macOS), or else the first of its restore_from keys that has one. When the
// step's run succeeds, conductor publishes what the run filled under its
// home to the step's own key: the directories runs of that key keep filling
// substantially (learned paths), never agent state or credentials.
//
// Core knows no ecosystem, repository or branch: a key is opaque, and which
// directories a cache holds is learned from what runs write.
package keycache

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"
)

// Key names one cache. Its identity is all three fields: the namespace
// (the pack instance whose step declared it; "" for the operator's own
// config), the template as written, and what the template rendered to for
// the run. Two steps share a cache only when all three agree, so a key a
// template renders from facts an event's sender controls can never equal,
// or be published over, a key of a different template — whatever text the
// facts carry.
type Key struct {
	Namespace string `json:"namespace,omitempty"`
	Template  string `json:"template"`
	Value     string `json:"value"`
}

// ID is the key's directory name in the store: a digest of its identity,
// so no rendered value ever becomes a path. Each field is length-prefixed,
// so no text in one field can stand in for another's.
func (k Key) ID() string {
	h := sha256.New()
	h.Write([]byte("keycache/v1"))
	for _, f := range []string{k.Namespace, k.Template, k.Value} {
		fmt.Fprintf(h, "\x00%d:%s", len(f), f)
	}
	return hex.EncodeToString(h.Sum(nil)[:16])
}

// String is the key as logs and the CLI show it.
func (k Key) String() string {
	if k.Namespace != "" {
		return k.Namespace + ":" + k.Value
	}
	return k.Value
}

// valueRE is the charset a rendered key may use.
var valueRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+=:#@/-]{0,199}$`)

// CheckValue refuses a rendered key that is not plain: letters, digits and
// `. _ + = : # @ / -`, starting with a letter or digit, at most 200
// characters, with no `..` and no empty path segment. A fact that renders
// anything else (a space, a quote, a newline, a glob) refuses the key, and
// the step runs without a cache.
func CheckValue(v string) error {
	if !valueRE.MatchString(v) || strings.Contains(v, "..") || strings.Contains(v, "//") || strings.HasSuffix(v, "/") {
		return fmt.Errorf("cache key %q: must be 1-200 of A-Z a-z 0-9 . _ + = : # @ / -, start with a letter or digit, without '..' or an empty segment", v)
	}
	return nil
}

// Valid checks a key before it reaches the store.
func (k Key) Valid() error {
	if strings.TrimSpace(k.Template) == "" {
		return fmt.Errorf("cache key: no template")
	}
	if strings.ContainsAny(k.Namespace, "\x00\n/") {
		return fmt.Errorf("cache key: bad namespace %q", k.Namespace)
	}
	return CheckValue(k.Value)
}
