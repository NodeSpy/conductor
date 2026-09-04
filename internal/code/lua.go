package code

import (
	"fmt"

	lua "github.com/yuin/gopher-lua"
)

// execLua runs a `run: lua` step on gopher-lua, a pure-Go Lua 5.1 VM (chosen
// over cgo bindings specifically to keep the zero-cgo cross-compiled
// release). In-process and local-only, like js/go-embed/risor. The step's
// data is the `ctx` global (a Lua table); the script `return`s its result — a
// table with string keys becomes the step's outputs, an array-like table a
// value: list, any other value lands under value:.
//
// Sandboxing comes from the globals conductor exposes: only the base, table,
// string, and math libraries are opened (no os, io, debug, or package), and
// the base library's file/chunk loaders (dofile, loadfile, load, loadstring)
// are removed.
func (e *Executor) execLua(spec Spec, data map[string]any) (map[string]any, error) {
	L := lua.NewState(lua.Options{SkipOpenLibs: true})
	defer L.Close()
	for _, o := range []struct {
		name string
		fn   lua.LGFunction
	}{
		{lua.BaseLibName, lua.OpenBase},
		{lua.TabLibName, lua.OpenTable},
		{lua.StringLibName, lua.OpenString},
		{lua.MathLibName, lua.OpenMath},
	} {
		L.Push(L.NewFunction(o.fn))
		L.Push(lua.LString(o.name))
		L.Call(1, 0)
	}
	for _, g := range []string{"dofile", "loadfile", "load", "loadstring"} {
		L.SetGlobal(g, lua.LNil)
	}
	L.SetGlobal("ctx", goToLua(L, data))

	if err := L.DoString(spec.Code); err != nil {
		return nil, fmt.Errorf("lua: %w", err)
	}
	if L.GetTop() == 0 {
		return map[string]any{}, nil
	}
	return wrapValue(luaToGo(L.Get(-1))), nil
}

// goToLua converts a Go value (the JSON-shaped step data) into a Lua value.
func goToLua(L *lua.LState, v any) lua.LValue {
	switch x := v.(type) {
	case nil:
		return lua.LNil
	case bool:
		return lua.LBool(x)
	case string:
		return lua.LString(x)
	case int:
		return lua.LNumber(x)
	case int64:
		return lua.LNumber(x)
	case float64:
		return lua.LNumber(x)
	case map[string]any:
		t := L.NewTable()
		for k, e := range x {
			t.RawSetString(k, goToLua(L, e))
		}
		return t
	case []any:
		t := L.NewTable()
		for i, e := range x {
			t.RawSetInt(i+1, goToLua(L, e))
		}
		return t
	case []string:
		t := L.NewTable()
		for i, e := range x {
			t.RawSetInt(i+1, lua.LString(e))
		}
		return t
	}
	return lua.LString(fmt.Sprintf("%v", v))
}

// luaToGo converts a Lua return value back into JSON-shaped Go data. A table
// is a map when it has any string key, else a 1..n array.
func luaToGo(v lua.LValue) any {
	switch x := v.(type) {
	case *lua.LNilType:
		return nil
	case lua.LBool:
		return bool(x)
	case lua.LString:
		return string(x)
	case lua.LNumber:
		f := float64(x)
		if f == float64(int64(f)) {
			return int64(f)
		}
		return f
	case *lua.LTable:
		asMap := map[string]any{}
		var asList []any
		listOK := true
		n := 0
		x.ForEach(func(k, e lua.LValue) {
			n++
			if ks, ok := k.(lua.LString); ok {
				asMap[string(ks)] = luaToGo(e)
				listOK = false
				return
			}
			if kn, ok := k.(lua.LNumber); ok && float64(kn) == float64(n) {
				asList = append(asList, luaToGo(e))
				return
			}
			listOK = false
		})
		if listOK && len(asMap) == 0 {
			return asList
		}
		// Mixed tables keep their numeric entries under stringified keys.
		x.ForEach(func(k, e lua.LValue) {
			if kn, ok := k.(lua.LNumber); ok {
				asMap[kn.String()] = luaToGo(e)
			}
		})
		return asMap
	}
	return v.String()
}
