package kv

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// HTTPStore is the generic REST KVBackend: every operation is one
// POST <base_url> with a JSON body naming the op and its arguments, and the
// response body is the op's result JSON. The REMOTE SHIM owns atomicity for
// the read-modify-write ops (incr/setnx/merge/append/remove/pop) — conductor
// sends one request per op and never composes multi-request transactions, so
// a conforming shim must apply each op transactionally on its side.
//
// Request body:
//
//	{ "op": "get|set|setnx|merge|delete|incr|append|remove|contains|
//	         first|last|index|slice|len|pop|list",
//	  "namespace": "…", "key": "…",
//	  "value": …,            set/setnx (any JSON), merge (object)
//	  "items": […],          append/remove
//	  "item": …,             contains
//	  "unique": bool,        append
//	  "by": int,             incr
//	  "ttl_ms": int,         set/setnx
//	  "index": int,          index
//	  "start": int, "end": int, "end_set": bool,   slice
//	  "front": bool,         pop
//	  "prefix": "…" }        list
//
// Response (HTTP 200): the verb's result object — {value, found} for reads,
// {value, created} for setnx, {value} for merge/incr, {value, len} for
// append/remove/slice, {contains}, {len}, {value, found, len} for pop,
// {keys, entries} for list. Any non-2xx status fails the op; the body's
// {"error": "…"} (or its text) becomes the error message.
type HTTPStore struct {
	base   string
	client *http.Client
	// auth decorates each request (the stores: builder wires the REST
	// connector's auth block — bearer/basic/header/oauth2 — through this).
	auth func(*http.Request) error
}

// NewHTTPStore builds the backend. client nil → a 30s-timeout default;
// auth nil → unauthenticated.
func NewHTTPStore(baseURL string, client *http.Client, auth func(*http.Request) error) *HTTPStore {
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	return &HTTPStore{base: baseURL, client: client, auth: auth}
}

func (h *HTTPStore) Close() error { return nil }

// call posts one op and decodes the result object.
func (h *HTTPStore) call(req map[string]any) (map[string]any, error) {
	op, _ := req["op"].(string)
	if v, ok := req["key"]; ok {
		if s, _ := v.(string); s == "" {
			return nil, fmt.Errorf("kv: %s: key is required", op)
		}
	}
	if ns, _ := req["namespace"].(string); ns == "" {
		req["namespace"] = DefaultNamespace
	}
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("kv: http: encode %s: %w", op, err)
	}
	r, err := http.NewRequest(http.MethodPost, h.base, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	r.Header.Set("Content-Type", "application/json")
	if h.auth != nil {
		if err := h.auth(r); err != nil {
			return nil, fmt.Errorf("kv: http: auth: %w", err)
		}
	}
	resp, err := h.client.Do(r)
	if err != nil {
		return nil, fmt.Errorf("kv: http %s: %w", op, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, fmt.Errorf("kv: http %s: read: %w", op, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var e struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(raw, &e) == nil && e.Error != "" {
			return nil, fmt.Errorf("kv: %s", e.Error)
		}
		return nil, fmt.Errorf("kv: http %s: HTTP %d: %s", op, resp.StatusCode, bytes.TrimSpace(raw))
	}
	out := map[string]any{}
	if len(bytes.TrimSpace(raw)) > 0 {
		if err := json.Unmarshal(raw, &out); err != nil {
			return nil, fmt.Errorf("kv: http %s: bad response: %w", op, err)
		}
	}
	return out, nil
}

func httpInt(v any) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	}
	return 0
}

func (h *HTTPStore) Get(namespace, key string) (any, bool, error) {
	out, err := h.call(map[string]any{"op": "get", "namespace": namespace, "key": key})
	if err != nil {
		return nil, false, err
	}
	found, _ := out["found"].(bool)
	if !found {
		return nil, false, nil
	}
	return out["value"], true, nil
}

func (h *HTTPStore) Set(namespace, key string, value any, ttl time.Duration) error {
	_, err := h.call(map[string]any{"op": "set", "namespace": namespace, "key": key,
		"value": value, "ttl_ms": ttl.Milliseconds()})
	return err
}

func (h *HTTPStore) SetNX(namespace, key string, value any, ttl time.Duration) (any, bool, error) {
	out, err := h.call(map[string]any{"op": "setnx", "namespace": namespace, "key": key,
		"value": value, "ttl_ms": ttl.Milliseconds()})
	if err != nil {
		return nil, false, err
	}
	created, _ := out["created"].(bool)
	return out["value"], created, nil
}

func (h *HTTPStore) Merge(namespace, key string, patch map[string]any) (map[string]any, error) {
	out, err := h.call(map[string]any{"op": "merge", "namespace": namespace, "key": key, "value": patch})
	if err != nil {
		return nil, err
	}
	v, _ := out["value"].(map[string]any)
	return v, nil
}

func (h *HTTPStore) Delete(namespace, key string) error {
	_, err := h.call(map[string]any{"op": "delete", "namespace": namespace, "key": key})
	return err
}

func (h *HTTPStore) Incr(namespace, key string, by int64) (int64, error) {
	out, err := h.call(map[string]any{"op": "incr", "namespace": namespace, "key": key, "by": by})
	if err != nil {
		return 0, err
	}
	return int64(httpInt(out["value"])), nil
}

func listResult(out map[string]any) []any {
	v, _ := out["value"].([]any)
	if v == nil {
		v = []any{}
	}
	return v
}

func (h *HTTPStore) Append(namespace, key string, items []any, unique bool) ([]any, error) {
	out, err := h.call(map[string]any{"op": "append", "namespace": namespace, "key": key,
		"items": items, "unique": unique})
	if err != nil {
		return nil, err
	}
	return listResult(out), nil
}

func (h *HTTPStore) Remove(namespace, key string, items []any) ([]any, error) {
	out, err := h.call(map[string]any{"op": "remove", "namespace": namespace, "key": key, "items": items})
	if err != nil {
		return nil, err
	}
	return listResult(out), nil
}

func (h *HTTPStore) Contains(namespace, key string, item any) (bool, error) {
	out, err := h.call(map[string]any{"op": "contains", "namespace": namespace, "key": key, "item": item})
	if err != nil {
		return false, err
	}
	c, _ := out["contains"].(bool)
	return c, nil
}

func (h *HTTPStore) foundValue(op string, req map[string]any) (any, bool, error) {
	req["op"] = op
	out, err := h.call(req)
	if err != nil {
		return nil, false, err
	}
	found, _ := out["found"].(bool)
	if !found {
		return nil, false, nil
	}
	return out["value"], true, nil
}

func (h *HTTPStore) First(namespace, key string) (any, bool, error) {
	return h.foundValue("first", map[string]any{"namespace": namespace, "key": key})
}

func (h *HTTPStore) Last(namespace, key string) (any, bool, error) {
	return h.foundValue("last", map[string]any{"namespace": namespace, "key": key})
}

func (h *HTTPStore) Index(namespace, key string, idx int) (any, bool, error) {
	return h.foundValue("index", map[string]any{"namespace": namespace, "key": key, "index": idx})
}

func (h *HTTPStore) Slice(namespace, key string, start, end int, endSet bool) ([]any, error) {
	out, err := h.call(map[string]any{"op": "slice", "namespace": namespace, "key": key,
		"start": start, "end": end, "end_set": endSet})
	if err != nil {
		return nil, err
	}
	return listResult(out), nil
}

func (h *HTTPStore) Len(namespace, key string) (int, error) {
	out, err := h.call(map[string]any{"op": "len", "namespace": namespace, "key": key})
	if err != nil {
		return 0, err
	}
	return httpInt(out["len"]), nil
}

func (h *HTTPStore) Pop(namespace, key string, front bool) (any, bool, int, error) {
	out, err := h.call(map[string]any{"op": "pop", "namespace": namespace, "key": key, "front": front})
	if err != nil {
		return nil, false, 0, err
	}
	found, _ := out["found"].(bool)
	if !found {
		return nil, false, 0, nil
	}
	return out["value"], true, httpInt(out["len"]), nil
}

func (h *HTTPStore) List(namespace, prefix string) ([]string, map[string]any, error) {
	out, err := h.call(map[string]any{"op": "list", "namespace": namespace, "prefix": prefix})
	if err != nil {
		return nil, nil, err
	}
	var keys []string
	if raw, ok := out["keys"].([]any); ok {
		for _, k := range raw {
			keys = append(keys, fmt.Sprint(k))
		}
	}
	entries, _ := out["entries"].(map[string]any)
	if entries == nil {
		entries = map[string]any{}
	}
	return keys, entries, nil
}
