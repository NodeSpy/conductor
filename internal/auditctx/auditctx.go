// Package auditctx carries who-and-where fields for audit rows on a request's
// context: the front end that knows them (the dashboard, from its session)
// sets them, and the service that writes the row (the bot service) adds them.
package auditctx

import "context"

type key struct{}

// With returns ctx carrying fields. A later With replaces an earlier one.
func With(ctx context.Context, fields map[string]any) context.Context {
	if len(fields) == 0 {
		return ctx
	}
	c := make(map[string]any, len(fields))
	for k, v := range fields {
		c[k] = v
	}
	return context.WithValue(ctx, key{}, c)
}

// Merge adds ctx's fields to e, never replacing a key e already has: the
// row's own fields (its operator, its event) stay what the service decided.
func Merge(ctx context.Context, e map[string]any) {
	if ctx == nil {
		return
	}
	f, _ := ctx.Value(key{}).(map[string]any)
	for k, v := range f {
		if _, ok := e[k]; !ok {
			e[k] = v
		}
	}
}
