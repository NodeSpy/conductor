package kv

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// kvShim is an httptest implementation of the HTTP store protocol, delegating
// every op to a real (bolt) backend — the shim side owns RMW atomicity, which
// the delegate provides. It doubles as the protocol's reference server.
func kvShim(t *testing.T, delegate KVBackend) http.Handler {
	t.Helper()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Op        string         `json:"op"`
			Namespace string         `json:"namespace"`
			Key       string         `json:"key"`
			Value     any            `json:"value"`
			Item      any            `json:"item"`
			Items     []any          `json:"items"`
			Unique    bool           `json:"unique"`
			By        int64          `json:"by"`
			TTLMs     int64          `json:"ttl_ms"`
			Index     int            `json:"index"`
			Start     int            `json:"start"`
			End       int            `json:"end"`
			EndSet    bool           `json:"end_set"`
			Front     bool           `json:"front"`
			Prefix    string         `json:"prefix"`
			Patch     map[string]any `json:"-"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, `{"error":"bad request"}`, 400)
			return
		}
		fail := func(err error) {
			w.WriteHeader(422)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": err.Error()})
		}
		ok := func(v map[string]any) { _ = json.NewEncoder(w).Encode(v) }
		ns, key, ttl := req.Namespace, req.Key, time.Duration(req.TTLMs)*time.Millisecond
		switch req.Op {
		case "get":
			v, found, err := delegate.Get(ns, key)
			if err != nil {
				fail(err)
				return
			}
			ok(map[string]any{"value": v, "found": found})
		case "set":
			if err := delegate.Set(ns, key, req.Value, ttl); err != nil {
				fail(err)
				return
			}
			ok(map[string]any{})
		case "setnx":
			v, created, err := delegate.SetNX(ns, key, req.Value, ttl)
			if err != nil {
				fail(err)
				return
			}
			ok(map[string]any{"value": v, "created": created})
		case "merge":
			patch, isObj := req.Value.(map[string]any)
			if !isObj {
				fail(errFor("merge: value must be an object"))
				return
			}
			v, err := delegate.Merge(ns, key, patch)
			if err != nil {
				fail(err)
				return
			}
			ok(map[string]any{"value": v})
		case "delete":
			if err := delegate.Delete(ns, key); err != nil {
				fail(err)
				return
			}
			ok(map[string]any{})
		case "incr":
			v, err := delegate.Incr(ns, key, req.By)
			if err != nil {
				fail(err)
				return
			}
			ok(map[string]any{"value": v})
		case "append":
			v, err := delegate.Append(ns, key, req.Items, req.Unique)
			if err != nil {
				fail(err)
				return
			}
			ok(map[string]any{"value": v, "len": len(v)})
		case "remove":
			v, err := delegate.Remove(ns, key, req.Items)
			if err != nil {
				fail(err)
				return
			}
			ok(map[string]any{"value": v, "len": len(v)})
		case "contains":
			c, err := delegate.Contains(ns, key, req.Item)
			if err != nil {
				fail(err)
				return
			}
			ok(map[string]any{"contains": c})
		case "first", "last", "index":
			var v any
			var found bool
			var err error
			switch req.Op {
			case "first":
				v, found, err = delegate.First(ns, key)
			case "last":
				v, found, err = delegate.Last(ns, key)
			default:
				v, found, err = delegate.Index(ns, key, req.Index)
			}
			if err != nil {
				fail(err)
				return
			}
			ok(map[string]any{"value": v, "found": found})
		case "slice":
			v, err := delegate.Slice(ns, key, req.Start, req.End, req.EndSet)
			if err != nil {
				fail(err)
				return
			}
			ok(map[string]any{"value": v, "len": len(v)})
		case "len":
			n, err := delegate.Len(ns, key)
			if err != nil {
				fail(err)
				return
			}
			ok(map[string]any{"len": n})
		case "pop":
			v, found, n, err := delegate.Pop(ns, key, req.Front)
			if err != nil {
				fail(err)
				return
			}
			ok(map[string]any{"value": v, "found": found, "len": n})
		case "list":
			keys, entries, err := delegate.List(ns, req.Prefix)
			if err != nil {
				fail(err)
				return
			}
			ks := make([]any, len(keys))
			for i, k := range keys {
				ks[i] = k
			}
			ok(map[string]any{"keys": ks, "entries": entries})
		default:
			fail(errFor("unknown op " + req.Op))
		}
	})
}

type shimErr string

func (e shimErr) Error() string { return string(e) }

func errFor(msg string) error { return shimErr(msg) }

// TestConformanceHTTP: the shared contract suite through the full JSON
// protocol — HTTPStore client → httptest shim → bolt delegate.
func TestConformanceHTTP(t *testing.T) {
	srv := httptest.NewServer(kvShim(t, openTemp(t)))
	defer srv.Close()
	runConformance(t, NewHTTPStore(srv.URL, srv.Client(), nil))
}

// TestHTTPStoreErrors: non-2xx replies surface the shim's error message;
// transport failures name the op.
func TestHTTPStoreErrors(t *testing.T) {
	srv := httptest.NewServer(kvShim(t, openTemp(t)))
	defer srv.Close()
	h := NewHTTPStore(srv.URL, srv.Client(), nil)
	_ = h.Set("", "scalar", 7.0, 0)
	if _, err := h.Merge("", "scalar", map[string]any{"a": 1.0}); err == nil ||
		!strContains(err.Error(), "scalar") || !strContains(err.Error(), "number") {
		t.Fatalf("shim error must round-trip: %v", err)
	}
	if _, _, err := h.Get("", ""); err == nil || !strContains(err.Error(), "key is required") {
		t.Fatalf("client-side key check: %v", err)
	}
	srv.Close()
	if _, _, err := h.Get("", "k"); err == nil || !strContains(err.Error(), "http get") {
		t.Fatalf("transport error: %v", err)
	}
}
