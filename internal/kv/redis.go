package kv

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

// RedisStore is the redis KVBackend. Scalars/objects are stored as their
// JSON encoding under one redis string key; list values are native redis
// lists whose elements are canonical JSON encodings — so the single-op verbs
// map to native commands (SET/GET/SETNX, LPUSH/RPUSH, LPOP/RPOP, LRANGE,
// LREM, LLEN, LINDEX, PEXPIRE for ttl) and every multi-step read-modify-write
// (incr, setnx-on-lists, merge, append-unique, pop-with-length) runs as one
// Lua script (or a WATCH/MULTI transaction for merge), keeping the
// one-transaction atomicity guarantee.
type RedisStore struct {
	c   redis.UniversalClient
	ctx context.Context
}

// NewRedisStore wraps a connected client (the stores: builder makes one from
// url/password; tests hand in a miniredis-backed client).
func NewRedisStore(c redis.UniversalClient) *RedisStore {
	return &RedisStore{c: c, ctx: context.Background()}
}

func (r *RedisStore) Close() error { return r.c.Close() }

// rkey joins namespace and key with a unit separator (namespaces isolate by
// prefix; the separator can't appear in normal names).
func rkey(namespace, key string) string {
	if namespace == "" {
		namespace = DefaultNamespace
	}
	return namespace + "\x1f" + key
}

// disp is the namespace/key form used in error messages.
func disp(namespace, key string) string {
	if namespace == "" {
		namespace = DefaultNamespace
	}
	return namespace + "/" + key
}

func encJSON(v any) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", fmt.Errorf("kv: value is not JSON-serializable: %w", err)
	}
	return string(b), nil
}

func decJSON(s string) (any, error) {
	var v any
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		return nil, fmt.Errorf("kv: corrupt value: %w", err)
	}
	return v, nil
}

// luaPrelude names a stored JSON value's type inside scripts, mirroring
// typeName for the error contract.
const luaPrelude = `
local function jt(v)
  local c = string.sub(v, 1, 1)
  if c == '"' then return 'string'
  elseif c == '{' then return 'object'
  elseif c == 't' or c == 'f' then return 'bool'
  elseif c == 'n' then return 'null'
  else return 'number' end
end
local function listguard(k, d)
  local t = redis.call('TYPE', k)['ok']
  if t == 'none' or t == 'list' then return nil end
  local v = redis.call('GET', k)
  return 'kv: ' .. d .. ': existing value is ' .. jt(v) .. ', not a list'
end
`

var (
	// incr: number-or-absent check, floor, preserve ttl. KEYS[1]; ARGV: by, disp.
	scriptIncr = redis.NewScript(luaPrelude + `
local t = redis.call('TYPE', KEYS[1])['ok']
if t == 'list' then
  return redis.error_reply('kv: incr ' .. ARGV[2] .. ': current value is not a number (list)')
end
local cur = 0
if t ~= 'none' then
  local v = redis.call('GET', KEYS[1])
  local n = tonumber(v)
  if n == nil then
    return redis.error_reply('kv: incr ' .. ARGV[2] .. ': current value is not a number (' .. jt(v) .. ')')
  end
  cur = math.floor(n)
end
local out = cur + tonumber(ARGV[1])
local ttl = redis.call('PTTL', KEYS[1])
redis.call('SET', KEYS[1], tostring(out))
if ttl > 0 then redis.call('PEXPIRE', KEYS[1], ttl) end
return out
`)

	// setList: replace KEYS[1] with a native list. ARGV: ttlms, elems...
	scriptSetList = redis.NewScript(`
redis.call('DEL', KEYS[1])
for i = 2, #ARGV do redis.call('RPUSH', KEYS[1], ARGV[i]) end
local ttl = tonumber(ARGV[1])
if ttl > 0 then redis.call('PEXPIRE', KEYS[1], ttl) end
return redis.status_reply('OK')
`)

	// setnxList: create-only list write. ARGV: ttlms, elems... → 1 created.
	scriptSetNXList = redis.NewScript(`
if redis.call('EXISTS', KEYS[1]) == 1 then return 0 end
for i = 2, #ARGV do redis.call('RPUSH', KEYS[1], ARGV[i]) end
local ttl = tonumber(ARGV[1])
if ttl > 0 then redis.call('PEXPIRE', KEYS[1], ttl) end
return 1
`)

	// append: type guard + optional set semantics, one txn. ARGV: disp,
	// unique(0|1), elems... → the resulting list.
	scriptAppend = redis.NewScript(luaPrelude + `
local g = listguard(KEYS[1], ARGV[1] .. ' (append)')
if g then return redis.error_reply(g) end
local unique = ARGV[2] == '1'
for i = 3, #ARGV do
  local dup = false
  if unique then
    local cur = redis.call('LRANGE', KEYS[1], 0, -1)
    for _, have in ipairs(cur) do
      if have == ARGV[i] then dup = true break end
    end
  end
  if not dup then redis.call('RPUSH', KEYS[1], ARGV[i]) end
end
return redis.call('LRANGE', KEYS[1], 0, -1)
`)

	// remove: LREM every occurrence of each item. ARGV: disp, items... → list.
	scriptRemove = redis.NewScript(luaPrelude + `
local g = listguard(KEYS[1], ARGV[1] .. ' (remove)')
if g then return redis.error_reply(g) end
for i = 2, #ARGV do redis.call('LREM', KEYS[1], 0, ARGV[i]) end
return redis.call('LRANGE', KEYS[1], 0, -1)
`)

	// pop: take one element from an end + the remaining length, atomically.
	// ARGV: disp, from(front|back) → {elem|false, len}.
	scriptPop = redis.NewScript(luaPrelude + `
local g = listguard(KEYS[1], ARGV[1] .. ' (pop)')
if g then return redis.error_reply(g) end
local v
if ARGV[2] == 'front' then v = redis.call('LPOP', KEYS[1]) else v = redis.call('RPOP', KEYS[1]) end
if not v then return {false, 0} end
return {v, redis.call('LLEN', KEYS[1])}
`)

	// contains: membership by canonical-JSON equality with the type guard.
	// ARGV: disp, item → 0|1.
	scriptContains = redis.NewScript(luaPrelude + `
local g = listguard(KEYS[1], ARGV[1] .. ' (contains)')
if g then return redis.error_reply(g) end
for _, have in ipairs(redis.call('LRANGE', KEYS[1], 0, -1)) do
  if have == ARGV[2] then return 1 end
end
return 0
`)
)

func (r *RedisStore) Set(namespace, key string, value any, ttl time.Duration) error {
	if key == "" {
		return fmt.Errorf("kv: set: key is required")
	}
	if lst, ok := value.([]any); ok {
		args := make([]any, 0, len(lst)+1)
		args = append(args, ttl.Milliseconds())
		for _, it := range lst {
			enc, err := encJSON(it)
			if err != nil {
				return err
			}
			args = append(args, enc)
		}
		return scriptSetList.Run(r.ctx, r.c, []string{rkey(namespace, key)}, args...).Err()
	}
	enc, err := encJSON(value)
	if err != nil {
		return err
	}
	return r.c.Set(r.ctx, rkey(namespace, key), enc, max0(ttl)).Err()
}

// max0 clamps a ttl for go-redis (0 = no expiry).
func max0(d time.Duration) time.Duration {
	if d < 0 {
		return 0
	}
	return d
}

func (r *RedisStore) Get(namespace, key string) (any, bool, error) {
	if key == "" {
		return nil, false, fmt.Errorf("kv: get: key is required")
	}
	k := rkey(namespace, key)
	t, err := r.c.Type(r.ctx, k).Result()
	if err != nil {
		return nil, false, err
	}
	switch t {
	case "none":
		return nil, false, nil
	case "list":
		elems, err := r.c.LRange(r.ctx, k, 0, -1).Result()
		if err != nil {
			return nil, false, err
		}
		out := make([]any, len(elems))
		for i, e := range elems {
			v, err := decJSON(e)
			if err != nil {
				return nil, false, err
			}
			out[i] = v
		}
		return out, true, nil
	}
	raw, err := r.c.Get(r.ctx, k).Result()
	if errors.Is(err, redis.Nil) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	v, err := decJSON(raw)
	return v, err == nil, err
}

func (r *RedisStore) SetNX(namespace, key string, value any, ttl time.Duration) (any, bool, error) {
	if key == "" {
		return nil, false, fmt.Errorf("kv: setnx: key is required")
	}
	k := rkey(namespace, key)
	if lst, ok := value.([]any); ok {
		args := make([]any, 0, len(lst)+1)
		args = append(args, ttl.Milliseconds())
		for _, it := range lst {
			enc, err := encJSON(it)
			if err != nil {
				return nil, false, err
			}
			args = append(args, enc)
		}
		created, err := scriptSetNXList.Run(r.ctx, r.c, []string{k}, args...).Int()
		if err != nil {
			return nil, false, err
		}
		if created == 1 {
			return value, true, nil
		}
	} else {
		enc, err := encJSON(value)
		if err != nil {
			return nil, false, err
		}
		created, err := r.c.SetNX(r.ctx, k, enc, max0(ttl)).Result()
		if err != nil {
			return nil, false, err
		}
		if created {
			return value, true, nil
		}
	}
	cur, _, err := r.Get(namespace, key)
	return cur, false, err
}

// Merge is a WATCH/MULTI optimistic transaction: read, shallow-merge in Go,
// write back only if the key didn't change, retry on conflict.
func (r *RedisStore) Merge(namespace, key string, patch map[string]any) (map[string]any, error) {
	if key == "" {
		return nil, fmt.Errorf("kv: merge: key is required")
	}
	k := rkey(namespace, key)
	var out map[string]any
	for attempt := 0; attempt < 64; attempt++ {
		err := r.c.Watch(r.ctx, func(tx *redis.Tx) error {
			if t, err := tx.Type(r.ctx, k).Result(); err != nil {
				return err
			} else if t == "list" {
				return fmt.Errorf("kv: merge %s: existing value is list, not an object", disp(namespace, key))
			}
			merged := map[string]any{}
			raw, err := tx.Get(r.ctx, k).Result()
			switch {
			case errors.Is(err, redis.Nil):
			case err != nil:
				return err
			default:
				cur, derr := decJSON(raw)
				if derr != nil {
					return derr
				}
				obj, ok := cur.(map[string]any)
				if !ok {
					return fmt.Errorf("kv: merge %s: existing value is %s, not an object", disp(namespace, key), typeName(cur))
				}
				merged = obj
			}
			for pk, pv := range patch {
				merged[pk] = pv
			}
			enc, err := encJSON(merged)
			if err != nil {
				return err
			}
			ttl, err := tx.PTTL(r.ctx, k).Result()
			if err != nil {
				return err
			}
			_, err = tx.TxPipelined(r.ctx, func(p redis.Pipeliner) error {
				if ttl > 0 {
					p.Set(r.ctx, k, enc, ttl)
				} else {
					p.Set(r.ctx, k, enc, 0)
				}
				return nil
			})
			if err == nil {
				out = merged
			}
			return err
		}, k)
		if errors.Is(err, redis.TxFailedErr) {
			continue // key changed under us — retry
		}
		return out, err
	}
	return nil, fmt.Errorf("kv: merge %s: too much contention", disp(namespace, key))
}

func (r *RedisStore) Delete(namespace, key string) error {
	if key == "" {
		return fmt.Errorf("kv: delete: key is required")
	}
	return r.c.Del(r.ctx, rkey(namespace, key)).Err()
}

func (r *RedisStore) Incr(namespace, key string, by int64) (int64, error) {
	if key == "" {
		return 0, fmt.Errorf("kv: incr: key is required")
	}
	n, err := scriptIncr.Run(r.ctx, r.c, []string{rkey(namespace, key)}, by, disp(namespace, key)).Int64()
	return n, err
}

func (r *RedisStore) encodeItems(items []any) ([]any, error) {
	out := make([]any, 0, len(items))
	for _, it := range items {
		enc, err := encJSON(it)
		if err != nil {
			return nil, err
		}
		out = append(out, enc)
	}
	return out, nil
}

func decodeList(elems []any) ([]any, error) {
	out := make([]any, len(elems))
	for i, e := range elems {
		s, _ := e.(string)
		v, err := decJSON(s)
		if err != nil {
			return nil, err
		}
		out[i] = v
	}
	return out, nil
}

func (r *RedisStore) Append(namespace, key string, items []any, unique bool) ([]any, error) {
	if key == "" {
		return nil, fmt.Errorf("kv: append: key is required")
	}
	enc, err := r.encodeItems(items)
	if err != nil {
		return nil, err
	}
	u := "0"
	if unique {
		u = "1"
	}
	args := append([]any{disp(namespace, key), u}, enc...)
	res, err := scriptAppend.Run(r.ctx, r.c, []string{rkey(namespace, key)}, args...).Slice()
	if err != nil {
		return nil, err
	}
	return decodeList(res)
}

func (r *RedisStore) Remove(namespace, key string, items []any) ([]any, error) {
	if key == "" {
		return nil, fmt.Errorf("kv: remove: key is required")
	}
	enc, err := r.encodeItems(items)
	if err != nil {
		return nil, err
	}
	args := append([]any{disp(namespace, key)}, enc...)
	res, err := scriptRemove.Run(r.ctx, r.c, []string{rkey(namespace, key)}, args...).Slice()
	if err != nil {
		return nil, err
	}
	return decodeList(res)
}

func (r *RedisStore) Contains(namespace, key string, item any) (bool, error) {
	if key == "" {
		return false, fmt.Errorf("kv: contains: key is required")
	}
	enc, err := encJSON(item)
	if err != nil {
		return false, err
	}
	n, err := scriptContains.Run(r.ctx, r.c, []string{rkey(namespace, key)}, disp(namespace, key), enc).Int()
	return n == 1, err
}

// listTypeErr remaps a native WRONGTYPE reply to the contract's error.
func (r *RedisStore) listTypeErr(namespace, key, op string, err error) error {
	if err == nil || !strings.Contains(err.Error(), "WRONGTYPE") {
		return err
	}
	raw, gerr := r.c.Get(r.ctx, rkey(namespace, key)).Result()
	tn := "string"
	if gerr == nil {
		if v, derr := decJSON(raw); derr == nil {
			tn = typeName(v)
		}
	}
	return fmt.Errorf("kv: %s: existing value is %s, not a list (%s)", disp(namespace, key), tn, op)
}

func (r *RedisStore) Index(namespace, key string, idx int) (any, bool, error) {
	if key == "" {
		return nil, false, fmt.Errorf("kv: index: key is required")
	}
	k := rkey(namespace, key)
	n, err := r.c.LLen(r.ctx, k).Result()
	if err = r.listTypeErr(namespace, key, "index", err); err != nil {
		return nil, false, err
	}
	i, ok := resolveIndex(idx, int(n))
	if !ok {
		return nil, false, nil
	}
	raw, err := r.c.LIndex(r.ctx, k, int64(i)).Result()
	if errors.Is(err, redis.Nil) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	v, err := decJSON(raw)
	return v, err == nil, err
}

func (r *RedisStore) First(namespace, key string) (any, bool, error) {
	return r.Index(namespace, key, 0)
}

func (r *RedisStore) Last(namespace, key string) (any, bool, error) {
	return r.Index(namespace, key, -1)
}

func (r *RedisStore) Slice(namespace, key string, start, end int, endSet bool) ([]any, error) {
	if key == "" {
		return nil, fmt.Errorf("kv: slice: key is required")
	}
	k := rkey(namespace, key)
	n64, err := r.c.LLen(r.ctx, k).Result()
	if err = r.listTypeErr(namespace, key, "slice", err); err != nil {
		return nil, err
	}
	n := int(n64)
	clamp := func(i int) int {
		if i < 0 {
			i += n
		}
		if i < 0 {
			return 0
		}
		if i > n {
			return n
		}
		return i
	}
	lo := clamp(start)
	hi := n
	if endSet {
		hi = clamp(end)
	}
	if lo >= hi {
		return []any{}, nil
	}
	elems, err := r.c.LRange(r.ctx, k, int64(lo), int64(hi-1)).Result()
	if err != nil {
		return nil, err
	}
	out := make([]any, len(elems))
	for i, e := range elems {
		v, err := decJSON(e)
		if err != nil {
			return nil, err
		}
		out[i] = v
	}
	return out, nil
}

func (r *RedisStore) Len(namespace, key string) (int, error) {
	if key == "" {
		return 0, fmt.Errorf("kv: len: key is required")
	}
	n, err := r.c.LLen(r.ctx, rkey(namespace, key)).Result()
	if err = r.listTypeErr(namespace, key, "len", err); err != nil {
		return 0, err
	}
	return int(n), nil
}

func (r *RedisStore) Pop(namespace, key string, front bool) (any, bool, int, error) {
	if key == "" {
		return nil, false, 0, fmt.Errorf("kv: pop: key is required")
	}
	from := "back"
	if front {
		from = "front"
	}
	res, err := scriptPop.Run(r.ctx, r.c, []string{rkey(namespace, key)}, disp(namespace, key), from).Slice()
	if err != nil {
		return nil, false, 0, err
	}
	raw, ok := res[0].(string)
	if !ok {
		return nil, false, 0, nil // empty/absent
	}
	length := 0
	if n, ok := res[1].(int64); ok {
		length = int(n)
	}
	v, err := decJSON(raw)
	return v, err == nil, length, err
}

// globEscape escapes redis MATCH glob metacharacters in a literal prefix.
func globEscape(s string) string {
	r := strings.NewReplacer(`\`, `\\`, "*", `\*`, "?", `\?`, "[", `\[`, "]", `\]`)
	return r.Replace(s)
}

func (r *RedisStore) List(namespace, prefix string) ([]string, map[string]any, error) {
	nsp := rkey(namespace, prefix)
	pattern := globEscape(nsp) + "*"
	entries := map[string]any{}
	var keys []string
	iter := r.c.Scan(r.ctx, 0, pattern, 200).Iterator()
	for iter.Next(r.ctx) {
		full := iter.Val()
		name := strings.TrimPrefix(full, rkey(namespace, ""))
		v, found, err := r.Get(namespace, name)
		if err != nil || !found {
			continue
		}
		if _, dup := entries[name]; dup {
			continue // SCAN may repeat keys
		}
		keys = append(keys, name)
		entries[name] = v
	}
	if err := iter.Err(); err != nil {
		return nil, nil, err
	}
	sort.Strings(keys)
	return keys, entries, nil
}
