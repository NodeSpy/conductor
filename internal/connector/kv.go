package connector

import (
	"context"
	"fmt"
	"path/filepath"
	"time"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/core"
	"github.com/NodeSpy/conductor/internal/kv"
)

// kvDecl declares the built-in durable state store. The connector is
// registered unconditionally (like the manual source): no connection block,
// no credentials — Build synthesizes the instance when the config doesn't
// name one, and the name is reserved.
var kvDecl = &TypeDecl{
	Type: "kv",
	Desc: "Built-in durable key/value store (bbolt file beside conductor's data); always available, no configuration.",
	Verbs: []VerbDecl{
		{
			Name: "get", Desc: "read a key",
			Options: Schema{
				"key":       {Type: TString, Required: true},
				"namespace": {Type: TString, Desc: "keyspace (default \"default\")"},
				"default":   {Type: TAny, Desc: "value returned when the key is absent/expired"},
			},
			Outputs: Schema{
				"value": {Type: TAny},
				"found": {Type: TBool},
			},
		},
		{
			Name: "set", Desc: "write a key (any JSON-serializable value)",
			Options: Schema{
				"key":       {Type: TString, Required: true},
				"value":     {Type: TAny, Required: true},
				"namespace": {Type: TString},
				"ttl":       {Type: TString, Desc: "expiry duration (e.g. 30m, 24h); omitted = no expiry"},
			},
			Outputs: Schema{},
		},
		{
			Name: "setnx", Desc: "write a key only if absent (create-once)",
			Options: Schema{
				"key":       {Type: TString, Required: true},
				"value":     {Type: TAny, Required: true},
				"namespace": {Type: TString},
				"ttl":       {Type: TString, Desc: "expiry duration when created"},
			},
			Outputs: Schema{
				"value":   {Type: TAny, Desc: "the key's resulting value (the existing one when created=false)"},
				"created": {Type: TBool},
			},
		},
		{
			Name: "merge", Desc: "shallow-merge an object into the object at key (upsert)",
			Options: Schema{
				"key":       {Type: TString, Required: true},
				"value":     {Type: TMap, Required: true},
				"namespace": {Type: TString},
			},
			Outputs: Schema{"value": {Type: TMap}},
		},
		{
			Name: "delete", Desc: "remove a key",
			Options: Schema{
				"key":       {Type: TString, Required: true},
				"namespace": {Type: TString},
			},
			Outputs: Schema{},
		},
		{
			Name: "append", Desc: "append to the list at key (created as [] if absent)",
			Options: Schema{
				"key":       {Type: TString, Required: true},
				"item":      {Type: TAny, Desc: "one value to append"},
				"items":     {Type: TList, Desc: "several values to append"},
				"unique":    {Type: TBool, Desc: "set semantics: skip values already present"},
				"namespace": {Type: TString},
			},
			Outputs: Schema{
				"value": {Type: TList},
				"len":   {Type: TInt},
			},
		},
		{
			Name: "remove", Desc: "remove all occurrences of item(s) from the list at key",
			Options: Schema{
				"key":       {Type: TString, Required: true},
				"item":      {Type: TAny},
				"items":     {Type: TList},
				"namespace": {Type: TString},
			},
			Outputs: Schema{
				"value": {Type: TList},
				"len":   {Type: TInt},
			},
		},
		{
			Name: "contains", Desc: "membership test on the list at key (false when absent)",
			Options: Schema{
				"key":       {Type: TString, Required: true},
				"item":      {Type: TAny, Required: true},
				"namespace": {Type: TString},
			},
			Outputs: Schema{"contains": {Type: TBool}},
		},
		{
			Name: "first", Desc: "first element of the list at key",
			Options: Schema{
				"key":       {Type: TString, Required: true},
				"namespace": {Type: TString},
			},
			Outputs: Schema{"value": {Type: TAny}, "found": {Type: TBool}},
		},
		{
			Name: "last", Desc: "last element of the list at key",
			Options: Schema{
				"key":       {Type: TString, Required: true},
				"namespace": {Type: TString},
			},
			Outputs: Schema{"value": {Type: TAny}, "found": {Type: TBool}},
		},
		{
			Name: "index", Desc: "element at index of the list at key (negative counts from the end)",
			Options: Schema{
				"key":       {Type: TString, Required: true},
				"index":     {Type: TInt, Required: true},
				"namespace": {Type: TString},
			},
			Outputs: Schema{"value": {Type: TAny}, "found": {Type: TBool}},
		},
		{
			Name: "slice", Desc: "sub-list [start:end) of the list at key; negatives allowed, bounds clamp",
			Options: Schema{
				"key":       {Type: TString, Required: true},
				"start":     {Type: TInt, Desc: "default 0"},
				"end":       {Type: TInt, Desc: "EXCLUSIVE; default = length"},
				"namespace": {Type: TString},
			},
			Outputs: Schema{"value": {Type: TList}, "len": {Type: TInt}},
		},
		{
			Name: "len", Desc: "length of the list at key (0 when absent)",
			Options: Schema{
				"key":       {Type: TString, Required: true},
				"namespace": {Type: TString},
			},
			Outputs: Schema{"len": {Type: TInt}},
		},
		{
			Name: "pop", Desc: "atomically remove and return an element from an end of the list at key",
			Options: Schema{
				"key":       {Type: TString, Required: true},
				"from":      {Type: TString, Desc: "front | back (default back)"},
				"namespace": {Type: TString},
			},
			Outputs: Schema{
				"value": {Type: TAny},
				"found": {Type: TBool, Desc: "false on empty/absent (no error, no mutation)"},
				"len":   {Type: TInt, Desc: "length after the pop"},
			},
		},
		{
			Name: "incr", Desc: "atomically add to a numeric key (absent = 0)",
			Options: Schema{
				"key":       {Type: TString, Required: true},
				"by":        {Type: TInt, Desc: "amount to add (default 1)"},
				"namespace": {Type: TString},
			},
			Outputs: Schema{"value": {Type: TInt}},
		},
		{
			Name: "list", Desc: "live keys (and values) in a namespace",
			Options: Schema{
				"namespace": {Type: TString},
				"prefix":    {Type: TString, Desc: "key prefix filter"},
			},
			Outputs: Schema{
				"keys":    {Type: TList},
				"entries": {Type: TMap},
			},
		},
	},
}

func init() { RegisterType(kvDecl, newKVImpl) }

type kvImpl struct{}

func newKVImpl(name string, ref config.ConnectorRef, deps Deps) (Impl, error) {
	// Point boltdb stores (including the implicit "default") at the daemon's
	// data dir (beside the state file); the default opens lazily on first
	// use. A loaded config always carries a state file (applyDefaults); a
	// hand-built one without it (tests) keeps whatever dir is already set.
	if deps.Config != nil && deps.Config.Store.StateFile != "" {
		kv.SetDataDir(filepath.Dir(deps.Config.Store.StateFile))
	}
	return kvImpl{}, nil
}

func (kvImpl) Validate() error          { return nil }
func (kvImpl) DeclaredEvents() []string { return nil }
func (kvImpl) Source(triggers []CompiledTrigger) (core.Integration, error) {
	if len(triggers) == 0 {
		return nil, nil
	}
	return nil, fmt.Errorf("the kv store has no source events")
}

func (kvImpl) Invoke(ctx context.Context, verb string, opts map[string]any) (map[string]any, error) {
	st, err := kv.Default()
	if err != nil {
		return nil, err
	}
	str := func(k string) string { s, _ := opts[k].(string); return s }
	namespace, key := str("namespace"), str("key")
	switch verb {
	case "get":
		v, found, err := st.Get(namespace, key)
		if err != nil {
			return nil, err
		}
		if !found {
			if d, ok := opts["default"]; ok {
				v = d
			} else {
				v = nil
			}
		}
		return map[string]any{"value": v, "found": found}, nil
	case "set":
		var ttl time.Duration
		if raw := str("ttl"); raw != "" {
			d, err := time.ParseDuration(raw)
			if err != nil {
				return nil, fmt.Errorf("kv.set: bad ttl %q: %w", raw, err)
			}
			ttl = d
		}
		if err := st.Set(namespace, key, opts["value"], ttl); err != nil {
			return nil, err
		}
		return map[string]any{}, nil
	case "setnx":
		var ttl time.Duration
		if raw := str("ttl"); raw != "" {
			d, err := time.ParseDuration(raw)
			if err != nil {
				return nil, fmt.Errorf("kv.setnx: bad ttl %q: %w", raw, err)
			}
			ttl = d
		}
		v, created, err := st.SetNX(namespace, key, opts["value"], ttl)
		if err != nil {
			return nil, err
		}
		return map[string]any{"value": v, "created": created}, nil
	case "merge":
		patch, ok := opts["value"].(map[string]any)
		if !ok {
			return nil, fmt.Errorf("kv.merge: value must be an object, got %T", opts["value"])
		}
		v, err := st.Merge(namespace, key, patch)
		if err != nil {
			return nil, err
		}
		return map[string]any{"value": v}, nil
	case "delete":
		if err := st.Delete(namespace, key); err != nil {
			return nil, err
		}
		return map[string]any{}, nil
	case "append":
		items, err := kvItems(opts)
		if err != nil {
			return nil, fmt.Errorf("kv.append: %w", err)
		}
		unique, _ := opts["unique"].(bool)
		v, err := st.Append(namespace, key, items, unique)
		if err != nil {
			return nil, err
		}
		return map[string]any{"value": v, "len": len(v)}, nil
	case "remove":
		items, err := kvItems(opts)
		if err != nil {
			return nil, fmt.Errorf("kv.remove: %w", err)
		}
		v, err := st.Remove(namespace, key, items)
		if err != nil {
			return nil, err
		}
		return map[string]any{"value": v, "len": len(v)}, nil
	case "contains":
		found, err := st.Contains(namespace, key, opts["item"])
		if err != nil {
			return nil, err
		}
		return map[string]any{"contains": found}, nil
	case "first":
		v, found, err := st.First(namespace, key)
		if err != nil {
			return nil, err
		}
		return map[string]any{"value": v, "found": found}, nil
	case "last":
		v, found, err := st.Last(namespace, key)
		if err != nil {
			return nil, err
		}
		return map[string]any{"value": v, "found": found}, nil
	case "index":
		idx, ok := kvInt(opts["index"])
		if !ok {
			return nil, fmt.Errorf("kv.index: index must be an integer, got %T", opts["index"])
		}
		v, found, err := st.Index(namespace, key, idx)
		if err != nil {
			return nil, err
		}
		return map[string]any{"value": v, "found": found}, nil
	case "slice":
		start := 0
		if raw, ok := opts["start"]; ok {
			s, okInt := kvInt(raw)
			if !okInt {
				return nil, fmt.Errorf("kv.slice: start must be an integer, got %T", raw)
			}
			start = s
		}
		end, endSet := 0, false
		if raw, ok := opts["end"]; ok {
			e, okInt := kvInt(raw)
			if !okInt {
				return nil, fmt.Errorf("kv.slice: end must be an integer, got %T", raw)
			}
			end, endSet = e, true
		}
		v, err := st.Slice(namespace, key, start, end, endSet)
		if err != nil {
			return nil, err
		}
		return map[string]any{"value": v, "len": len(v)}, nil
	case "len":
		n, err := st.Len(namespace, key)
		if err != nil {
			return nil, err
		}
		return map[string]any{"len": n}, nil
	case "pop":
		from := str("from")
		if from != "" && from != "front" && from != "back" {
			return nil, fmt.Errorf("kv.pop: from must be front or back, got %q", from)
		}
		v, found, n, err := st.Pop(namespace, key, from == "front")
		if err != nil {
			return nil, err
		}
		return map[string]any{"value": v, "found": found, "len": n}, nil
	case "incr":
		by := int64(1)
		switch b := opts["by"].(type) {
		case int:
			by = int64(b)
		case int64:
			by = b
		case float64:
			by = int64(b)
		}
		v, err := st.Incr(namespace, key, by)
		if err != nil {
			return nil, err
		}
		return map[string]any{"value": v}, nil
	case "list":
		keys, entries, err := st.List(namespace, str("prefix"))
		if err != nil {
			return nil, err
		}
		ks := make([]any, len(keys))
		for i, k := range keys {
			ks[i] = k
		}
		return map[string]any{"keys": ks, "entries": entries}, nil
	}
	return nil, fmt.Errorf("kv: no verb %q", verb)
}

// kvInt coerces a rendered option (int from a type-preserving template,
// float64 from JSON) into an int.
func kvInt(v any) (int, bool) {
	switch n := v.(type) {
	case int:
		return n, true
	case int64:
		return int(n), true
	case float64:
		return int(n), true
	}
	return 0, false
}

// kvItems resolves the item|items option pair into the value list to
// append/remove (items wins when both are set; one of the two is required).
func kvItems(opts map[string]any) ([]any, error) {
	if raw, ok := opts["items"]; ok {
		lst, isList := raw.([]any)
		if !isList {
			return nil, fmt.Errorf("items must be a list, got %T", raw)
		}
		return lst, nil
	}
	if it, ok := opts["item"]; ok {
		return []any{it}, nil
	}
	return nil, fmt.Errorf("set item: (one value) or items: (a list)")
}
