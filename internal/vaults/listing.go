package vaults

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/NodeSpy/conductor/internal/secrets"
)

// Metadata-only listing, for a login picker (the dashboard's "add a login
// from a vault"): what is in a vault — item names, websites, field labels
// and types — and NEVER a value. Each backend lists what its API gives
// without values:
//
//   - onepassword: `op item list` / `op item get --format json`, decoded
//     into types that have no value field at all, so a value never survives
//     the parse, let alone leaves the daemon; the sign-in fields are mapped
//     (username, password, one-time code) from their purpose and type;
//   - conductor, file: entry names;
//   - pass: entry names, from the store's directory (never `pass show`);
//   - hashicorp: secret paths (KV v2 LIST on metadata; a secret's field
//     names need its data, so a field is named by hand: path#field).
//
// Every reference a listing offers is in conductor's canonical syntax:
// {{ vault "<name>" "<key>" }}.

// ItemMeta is one item as a picker shows it — never a value.
type ItemMeta struct {
	// ID is what ItemDetail takes.
	ID string `json:"id"`
	// Vault is the item's vault or folder inside the backend (1Password's
	// vault name); empty when the backend has none.
	Vault    string      `json:"vault,omitempty"`
	Title    string      `json:"title"`
	Category string      `json:"category,omitempty"`
	URLs     []string    `json:"urls,omitempty"`
	Fields   []FieldMeta `json:"fields,omitempty"`
	// Mapping is the reference each sign-in field would use (username,
	// password, otp), detected from the item's fields.
	Mapping map[string]string `json:"mapping,omitempty"`
}

// FieldMeta is one field: its label and type, and its reference.
type FieldMeta struct {
	Label   string `json:"label"`
	Type    string `json:"type,omitempty"`
	Purpose string `json:"purpose,omitempty"`
	Section string `json:"section,omitempty"`
	Ref     string `json:"ref"`
}

// ItemLister is the optional metadata face. key is a backend key (what a
// reference's second argument would be).
type ItemLister interface {
	ListItems(ctx context.Context, query string) ([]ItemMeta, error)
	ItemDetail(ctx context.Context, id string) (ItemMeta, error)
}

// maxItems bounds one listing.
const maxItems = 500

// ListItems lists a vault's items, metadata only. query filters by title,
// vault or website (case-insensitive substring).
func ListItems(ctx context.Context, name, query string) ([]ItemMeta, error) {
	b, err := Use(name)
	if err != nil {
		return nil, err
	}
	var items []ItemMeta
	switch l := b.(type) {
	case ItemLister:
		if items, err = l.ListItems(ctx, query); err != nil {
			return nil, fmt.Errorf("vault %q: %w", name, err)
		}
	case Lister:
		keys, err := l.List(ctx)
		if err != nil {
			return nil, fmt.Errorf("vault %q: %w", name, err)
		}
		for _, k := range keys {
			items = append(items, ItemMeta{ID: k, Title: k, Fields: []FieldMeta{{Label: "value", Ref: k}}})
		}
	default:
		return nil, fmt.Errorf("vault %q (%s) cannot list its entries — enter the reference by hand: {{ vault %q \"<key>\" }}", name, Type(name), name)
	}
	q := strings.ToLower(strings.TrimSpace(query))
	var out []ItemMeta
	for _, it := range items {
		if q != "" && !itemMatches(it, q) {
			continue
		}
		out = append(out, canonical(name, it))
		if len(out) == maxItems {
			break
		}
	}
	return out, nil
}

// ItemDetail is one item's fields and their sign-in mapping, metadata only.
func ItemDetail(ctx context.Context, name, id string) (ItemMeta, error) {
	b, err := Use(name)
	if err != nil {
		return ItemMeta{}, err
	}
	l, ok := b.(ItemLister)
	if !ok {
		// A flat vault: the entry is its one value.
		return canonical(name, ItemMeta{ID: id, Title: id, Fields: []FieldMeta{{Label: "value", Ref: id}}}), nil
	}
	it, err := l.ItemDetail(ctx, id)
	if err != nil {
		return ItemMeta{}, fmt.Errorf("vault %q: %w", name, err)
	}
	return canonical(name, it), nil
}

func itemMatches(it ItemMeta, q string) bool {
	if strings.Contains(strings.ToLower(it.Title), q) || strings.Contains(strings.ToLower(it.Vault), q) {
		return true
	}
	for _, u := range it.URLs {
		if strings.Contains(strings.ToLower(u), q) {
			return true
		}
	}
	return false
}

// canonical turns an item's backend keys into references in conductor's
// syntax.
func canonical(name string, it ItemMeta) ItemMeta {
	ref := func(key string) string {
		if key == "" {
			return ""
		}
		return secrets.Source{Kind: secrets.SourceVault, Vault: name, Key: key}.String()
	}
	fields := make([]FieldMeta, len(it.Fields))
	for i, f := range it.Fields {
		f.Ref = ref(f.Ref)
		fields[i] = f
	}
	it.Fields = fields
	if len(it.Mapping) > 0 {
		m := make(map[string]string, len(it.Mapping))
		for k, v := range it.Mapping {
			m[k] = ref(v)
		}
		it.Mapping = m
	}
	return it
}

// ---------------------------------------------------------------------------
// onepassword
// ---------------------------------------------------------------------------

// opItem is `op item list/get --format json` with NO value field: the
// value (and an OTP field's current code) is dropped by the decode itself.
type opItem struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Category string `json:"category"`
	Vault    struct {
		Name string `json:"name"`
	} `json:"vault"`
	URLs []struct {
		Href string `json:"href"`
	} `json:"urls"`
	Fields []opField `json:"fields"`
}

type opField struct {
	ID        string `json:"id"`
	Type      string `json:"type"`
	Purpose   string `json:"purpose"`
	Label     string `json:"label"`
	Reference string `json:"reference"`
	Section   *struct {
		Label string `json:"label"`
	} `json:"section"`
}

func (o *OnePasswordVault) op(ctx context.Context, args ...string) ([]byte, error) {
	run := o.Exec
	if run == nil {
		run = execHelper
	}
	if o.Account != "" {
		args = append(args, "--account", o.Account)
	}
	var env []string
	if o.ServiceAccount != "" {
		env = append(env, "OP_SERVICE_ACCOUNT_TOKEN="+o.ServiceAccount)
	}
	out, err := run(ctx, "", env, "op", args...)
	if err != nil {
		return nil, err
	}
	return []byte(out), nil
}

// ListItems implements ItemLister.
func (o *OnePasswordVault) ListItems(ctx context.Context, query string) ([]ItemMeta, error) {
	raw, err := o.op(ctx, "item", "list", "--format", "json")
	if err != nil {
		return nil, err
	}
	var items []opItem
	err = json.Unmarshal(raw, &items)
	wipe(raw)
	if err != nil {
		return nil, fmt.Errorf("op item list: unexpected output")
	}
	out := make([]ItemMeta, 0, len(items))
	for _, it := range items {
		out = append(out, ItemMeta{ID: it.ID, Vault: it.Vault.Name, Title: it.Title, Category: it.Category, URLs: opURLs(it)})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Vault != out[j].Vault {
			return out[i].Vault < out[j].Vault
		}
		return out[i].Title < out[j].Title
	})
	return out, nil
}

// opIDRE is what an item ID looks like (op's own: 26 lowercase base32).
var opIDRE = regexp.MustCompile(`^[a-z0-9]{26}$`)

// ItemDetail implements ItemLister: the item's fields, labels and types,
// and the sign-in mapping.
func (o *OnePasswordVault) ItemDetail(ctx context.Context, id string) (ItemMeta, error) {
	if !opIDRE.MatchString(id) {
		return ItemMeta{}, fmt.Errorf("%q is not a 1Password item id", id)
	}
	raw, err := o.op(ctx, "item", "get", id, "--format", "json")
	if err != nil {
		return ItemMeta{}, err
	}
	var it opItem
	err = json.Unmarshal(raw, &it)
	wipe(raw)
	if err != nil {
		return ItemMeta{}, fmt.Errorf("op item get: unexpected output")
	}
	meta := ItemMeta{ID: it.ID, Vault: it.Vault.Name, Title: it.Title, Category: it.Category, URLs: opURLs(it), Mapping: map[string]string{}}
	for _, f := range it.Fields {
		key, ok := opKey(f)
		if !ok {
			continue
		}
		fm := FieldMeta{Label: f.Label, Type: f.Type, Purpose: f.Purpose, Ref: key}
		if f.Section != nil {
			fm.Section = f.Section.Label
		}
		meta.Fields = append(meta.Fields, fm)
	}
	meta.Mapping = opMapping(it.Fields)
	return meta, nil
}

func opURLs(it opItem) []string {
	var out []string
	for _, u := range it.URLs {
		if u.Href != "" {
			out = append(out, u.Href)
		}
	}
	return out
}

// opKey is a field's backend key: op's own secret reference with the
// retired scheme dropped (the backend adds it back for `op read`); a
// one-time password reads its current code (?attribute=otp), so its seed
// never leaves 1Password.
func opKey(f opField) (string, bool) {
	ref, ok := strings.CutPrefix(f.Reference, "op://")
	if !ok || ref == "" || strings.ContainsAny(ref, "\r\n\"") {
		return "", false
	}
	if f.Type == "OTP" && !strings.Contains(ref, "?") {
		ref += "?attribute=otp"
	}
	return ref, true
}

var (
	userLabelRE = regexp.MustCompile(`(?i)^(user ?name|e-?mail|login|user ?id|account)$`)
	passLabelRE = regexp.MustCompile(`(?i)password|passphrase`)
)

// opMapping picks the username, password and one-time code fields: by
// 1Password's purpose first, then type, then label.
func opMapping(fields []opField) map[string]string {
	m := map[string]string{}
	pick := func(slot string, ok func(opField) bool) {
		if m[slot] != "" {
			return
		}
		for _, f := range fields {
			if key, valid := opKey(f); valid && ok(f) {
				m[slot] = key
				return
			}
		}
	}
	pick("username", func(f opField) bool { return f.Purpose == "USERNAME" })
	pick("username", func(f opField) bool { return f.Type == "EMAIL" })
	pick("username", func(f opField) bool { return f.Type == "STRING" && userLabelRE.MatchString(f.Label) })
	pick("password", func(f opField) bool { return f.Purpose == "PASSWORD" })
	pick("password", func(f opField) bool { return f.Type == "CONCEALED" && passLabelRE.MatchString(f.Label) })
	pick("otp", func(f opField) bool { return f.Type == "OTP" })
	return m
}

// wipe overwrites a buffer that held a helper's output.
func wipe(b []byte) {
	for i := range b {
		b[i] = 0
	}
}

// ---------------------------------------------------------------------------
// pass: names from the store directory; never `pass show`.
// ---------------------------------------------------------------------------

// storeDir is the password store's root.
func (p *PassVault) storeDir() string {
	if p.Dir != "" {
		return expandHome(p.Dir)
	}
	if d := os.Getenv("PASSWORD_STORE_DIR"); d != "" {
		return d
	}
	h, _ := os.UserHomeDir()
	return filepath.Join(h, ".password-store")
}

// List implements Lister: entry names under the prefix (keys, as a
// reference names them), from the store's .gpg file names.
func (p *PassVault) List(ctx context.Context) ([]string, error) {
	root := p.storeDir()
	base := root
	if p.Prefix != "" {
		base = filepath.Join(root, filepath.FromSlash(strings.Trim(p.Prefix, "/")))
	}
	var out []string
	err := filepath.WalkDir(base, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && strings.HasPrefix(d.Name(), ".") && path != base {
			return filepath.SkipDir // .git, .extensions
		}
		if d.Type()&fs.ModeSymlink != 0 || d.IsDir() || !strings.HasSuffix(d.Name(), ".gpg") {
			return nil
		}
		rel, rerr := filepath.Rel(base, path)
		if rerr != nil {
			return nil
		}
		out = append(out, strings.TrimSuffix(filepath.ToSlash(rel), ".gpg"))
		if len(out) > maxItems {
			return fs.SkipAll
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("pass: list %s: %w", base, err)
	}
	sort.Strings(out)
	return out, nil
}

// ---------------------------------------------------------------------------
// hashicorp: KV v2 LIST on metadata — paths, never data.
// ---------------------------------------------------------------------------

// ListItems implements ItemLister: one level of secret paths under query
// (a path prefix; "" = the mount's root). A folder lists as category
// "folder", its ID the prefix to list next.
func (h *HashicorpVault) ListItems(ctx context.Context, query string) ([]ItemMeta, error) {
	prefix := strings.Trim(query, "/")
	if strings.Contains(prefix, "..") {
		return nil, fmt.Errorf("hashicorp: %q escapes the mount", query)
	}
	path := "/v1/" + h.mount() + "/metadata/"
	if prefix != "" {
		path += prefix + "/"
	}
	out, err := h.do(ctx, "LIST", path, nil)
	if err != nil {
		if strings.Contains(err.Error(), "HTTP 404") {
			return nil, nil
		}
		return nil, err
	}
	d, _ := out["data"].(map[string]any)
	keys, _ := d["keys"].([]any)
	var items []ItemMeta
	for _, k := range keys {
		s, _ := k.(string)
		if s == "" {
			continue
		}
		full := s
		if prefix != "" {
			full = prefix + "/" + s
		}
		if strings.HasSuffix(s, "/") {
			items = append(items, ItemMeta{ID: strings.TrimSuffix(full, "/"), Title: s, Category: "folder"})
			continue
		}
		items = append(items, ItemMeta{ID: full, Title: s, Fields: []FieldMeta{{Label: "value", Ref: full}}})
	}
	return items, nil
}

// ItemDetail implements ItemLister: a secret's default field (its other
// fields are named by hand: path#field — reading their names needs the
// data).
func (h *HashicorpVault) ItemDetail(ctx context.Context, id string) (ItemMeta, error) {
	if _, _, err := splitKey(id); err != nil {
		return ItemMeta{}, err
	}
	return ItemMeta{ID: id, Title: id, Fields: []FieldMeta{{Label: "value", Ref: id}}}, nil
}
