package config

import (
	"reflect"
	"strings"

	"gopkg.in/yaml.v3"
)

// PATHS: every config field that names a file or directory carries a `path:`
// tag saying how a leading ~ is handled, and expandPaths — run once, at the
// end of Load — is the one place it happens:
//
//	path:"local"     a path on this box: ~ and ~/ expand to the user's home
//	path:"remote"    a path (or a section of paths) on another box; never
//	                 expanded here (hosts.cwd, a host's isolation:)
//	path:"step"      a step field. Steps are fingerprinted from their
//	                 definition and may run on another box, so the pass does
//	                 not enter a Step; a local code step's workdir expands
//	                 where it runs (internal/code), a step's isolation fs:
//	                 where the sandbox is built (internal/sandbox)
//	path:"ref"       a use:/source reference: a name, a repo or a local path,
//	                 expanded where it is parsed (ParseUse, parseSource)
//	path:"-"         named like a path but not one (a collection's repo-relative
//	                 dir, a wallet profile name)
//
// A runtime that runs on another host (host: set) is skipped whole: its bin,
// home and isolation name paths on that box. TestPathFieldsTagged fails when a
// field named like a path carries no tag.
//
// Connectors, stores and vaults keep their connection fields in a raw YAML
// node only the implementation decodes; there a value under a key named path,
// dir or file (or ending _path, _dir, _file) is expanded the same way.
const (
	pathLocal  = "local"
	pathRemote = "remote"
	pathStep   = "step"
	pathRef    = "ref"
	pathNone   = "-"
)

var stepType = reflect.TypeOf(Step{})

// ExpandHome expands a leading ~ or ~/ to the user's home directory — the
// expansion every path:"local" field gets, for the places a path is resolved
// outside Load (a step's fields, a CLI flag).
func ExpandHome(p string) string { return expandHome(p) }

// remotePathsHolder is a section whose paths all name another box when it
// says so.
type remotePathsHolder interface{ remotePaths() bool }

func (r RuntimeConfig) remotePaths() bool { return r.Host != "" }

// expandPaths expands ~ in every local path field. It is copy-on-write: a
// pointer, slice or map holding a changed value is replaced, never written
// through, so a value the config shares with a step (whose fingerprint is
// its definition) is left as written.
func (c *Config) expandPaths() {
	expandPathsIn(reflect.ValueOf(c).Elem())
	for name, r := range c.ConnectorsMap {
		if n, ok := expandNodePaths(&r.raw, connectorHeaderKeys); ok {
			r.raw = *n
			c.ConnectorsMap[name] = r
		}
	}
	for name, r := range c.Stores {
		if n, ok := expandNodePaths(&r.raw, nil); ok {
			r.raw = *n
			c.Stores[name] = r
		}
	}
	for name, r := range c.Vaults {
		if n, ok := expandNodePaths(&r.raw, nil); ok {
			r.raw = *n
			c.Vaults[name] = r
		}
	}
}

// expandPathsIn expands the tagged fields under v (settable) and reports
// whether anything changed.
func expandPathsIn(v reflect.Value) bool {
	switch v.Kind() {
	case reflect.Ptr:
		if v.IsNil() {
			return false
		}
		cp := reflect.New(v.Type().Elem())
		cp.Elem().Set(v.Elem())
		if !expandPathsIn(cp.Elem()) {
			return false
		}
		v.Set(cp)
		return true
	case reflect.Slice:
		if v.Len() == 0 {
			return false
		}
		cp := reflect.MakeSlice(v.Type(), v.Len(), v.Len())
		reflect.Copy(cp, v)
		changed := false
		for i := 0; i < cp.Len(); i++ {
			changed = expandPathsIn(cp.Index(i)) || changed
		}
		if changed {
			v.Set(cp)
		}
		return changed
	case reflect.Array:
		changed := false
		for i := 0; i < v.Len(); i++ {
			changed = expandPathsIn(v.Index(i)) || changed
		}
		return changed
	case reflect.Map:
		if v.Len() == 0 {
			return false
		}
		cp := reflect.MakeMapWithSize(v.Type(), v.Len())
		changed := false
		iter := v.MapRange()
		for iter.Next() {
			e := reflect.New(v.Type().Elem()).Elem()
			e.Set(iter.Value())
			changed = expandPathsIn(e) || changed
			cp.SetMapIndex(iter.Key(), e)
		}
		if changed {
			v.Set(cp)
		}
		return changed
	case reflect.Struct:
		return expandStructPaths(v)
	}
	return false
}

func expandStructPaths(v reflect.Value) bool {
	if v.Type() == stepType {
		return false
	}
	if r, ok := v.Interface().(remotePathsHolder); ok && r.remotePaths() {
		return false
	}
	changed := false
	for i := 0; i < v.NumField(); i++ {
		f := v.Type().Field(i)
		if !f.IsExported() || f.Tag.Get("yaml") == "-" {
			continue // a load-time derivation, not a path the operator wrote
		}
		fv := v.Field(i)
		switch f.Tag.Get("path") {
		case pathLocal:
			changed = expandPathValue(fv) || changed
		case "":
			changed = expandPathsIn(fv) || changed
		}
	}
	return changed
}

// expandPathValue expands a path:"local" string or []string field.
func expandPathValue(v reflect.Value) bool {
	switch v.Kind() {
	case reflect.String:
		if p := expandHome(v.String()); p != v.String() {
			v.SetString(p)
			return true
		}
	case reflect.Slice:
		if v.Type().Elem().Kind() != reflect.String {
			return false
		}
		out := make([]string, v.Len())
		changed := false
		for i := range out {
			out[i] = expandHome(v.Index(i).String())
			changed = changed || out[i] != v.Index(i).String()
		}
		if changed {
			v.Set(reflect.ValueOf(out).Convert(v.Type()))
		}
		return changed
	}
	return false
}

// connectorHeaderKeys are a connector's common header keys: not connection
// fields (options: are verb options; isolation: is a struct the pass handles).
var connectorHeaderKeys = map[string]bool{"use": true, "type": true, "network": true, "enabled": true,
	"options": true, "policy": true, "isolation": true, "allow_secrets": true, "allow_env": true, "isolate": true}

// pathKey reports whether a raw connection key names a path.
func pathKey(k string) bool {
	switch k {
	case "path", "dir", "file":
		return true
	}
	return strings.HasSuffix(k, "_path") || strings.HasSuffix(k, "_dir") || strings.HasSuffix(k, "_file")
}

// expandNodePaths returns a copy of n with ~ expanded in every scalar under a
// path-named mapping key (skipping the top-level keys in skip), and whether
// anything changed. n itself is never written: the node may be shared.
func expandNodePaths(n *yaml.Node, skip map[string]bool) (*yaml.Node, bool) {
	if n == nil {
		return nil, false
	}
	switch n.Kind {
	case yaml.DocumentNode, yaml.SequenceNode:
		return expandNodeChildren(n)
	case yaml.MappingNode:
		cp := *n
		cp.Content = append([]*yaml.Node(nil), n.Content...)
		changed := false
		for i := 0; i+1 < len(cp.Content); i += 2 {
			k, val := cp.Content[i], cp.Content[i+1]
			if skip[k.Value] {
				continue
			}
			if val.Kind == yaml.ScalarNode {
				if p := expandHome(val.Value); pathKey(k.Value) && p != val.Value {
					nv := *val
					nv.Value = p
					cp.Content[i+1], changed = &nv, true
				}
				continue
			}
			if nv, ok := expandNodePaths(val, nil); ok {
				cp.Content[i+1], changed = nv, true
			}
		}
		return &cp, changed
	}
	return n, false
}

// expandNodeChildren is expandNodePaths over a document's or a sequence's items.
func expandNodeChildren(n *yaml.Node) (*yaml.Node, bool) {
	cp := *n
	cp.Content = append([]*yaml.Node(nil), n.Content...)
	changed := false
	for i, ch := range cp.Content {
		if nv, ok := expandNodePaths(ch, nil); ok {
			cp.Content[i], changed = nv, true
		}
	}
	return &cp, changed
}
