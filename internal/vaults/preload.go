package vaults

import "context"

// PreloadListable reads every entry of every healthy, listable vault
// (conductor, file) into a name → {key → value} map — the `.vaults.<name>.
// <key>` template data, resolved at load/reload and cached. Every value is
// tainted on the way through Read. Path-keyed vaults (onepassword, pass,
// hashicorp) cannot enumerate and are read via {{ vault "name" "key" }}
// instead. Read failures skip the entry (the template field renders empty;
// `secrets check` reports the vault itself).
func PreloadListable(ctx context.Context) map[string]map[string]string {
	out := map[string]map[string]string{}
	for _, name := range Names() {
		b, err := Use(name)
		if err != nil {
			continue
		}
		l, ok := b.(Lister)
		if !ok {
			continue
		}
		keys, err := l.List(ctx)
		if err != nil {
			continue
		}
		m := make(map[string]string, len(keys))
		for _, k := range keys {
			v, err := Read(ctx, name, k)
			if err != nil {
				continue
			}
			m[k] = v
		}
		out[name] = m
	}
	return out
}
