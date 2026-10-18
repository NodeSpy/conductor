package main

import (
	"bytes"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/NodeSpy/conductor/internal/avatar"
	"github.com/NodeSpy/conductor/internal/config"
)

func avatarConfig(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	config.SetStateDir(filepath.Join(dir, "state"))
	t.Cleanup(func() { config.SetStateDir("") })
	p := filepath.Join(dir, "config.yaml")
	cfg := `# my config
runtimes:
  paseo: { use: paseo, default: true }
  clibot: { use: cli, tool: claude-code }
operators:
  ops: { ssh_key: "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIExampleExampleExampleExampleExampleExample ops@x" }
bots:
  sre:
    description: first-line SRE   # keep this comment
    avatar: { preset: owl, color: teal }
  plain:
    description: no face
`
	if err := os.WriteFile(p, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func photo(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for x := 0; x < w; x++ {
		img.SetNRGBA(x, h/2, color.NRGBA{200, 10, 10, 255})
	}
	var b bytes.Buffer
	_ = png.Encode(&b, img)
	return b.Bytes()
}

// `conductor bot avatar set`: re-encoded, planned on a copy, a diff; not a
// widening; nothing written until asked; then the picture beside the config
// and one avatar line, the bot's face kept as the fallback; the config loads
// with the picture resolved.
func TestBotAvatarSetPlan(t *testing.T) {
	p := avatarConfig(t)
	before, _ := os.ReadFile(p)
	plan, err := planAvatar(p, "sre", photo(t, 900, 600), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !plan.Valid || plan.Widening || plan.Image != "avatars/sre.png" || plan.Replaces {
		t.Fatalf("plan: %+v", plan)
	}
	if !strings.Contains(plan.Diff, "+    avatar: {image: avatars/sre.png, preset: owl, color: teal}") || !strings.Contains(plan.Diff, "-    avatar: { preset: owl, color: teal }") {
		t.Fatalf("diff:\n%s", plan.Diff)
	}
	if now, _ := os.ReadFile(p); !bytes.Equal(now, before) {
		t.Fatal("a plan wrote the config")
	}
	ents, _ := os.ReadDir(filepath.Dir(p))
	for _, e := range ents {
		if strings.HasPrefix(e.Name(), ".conductor-plan") || e.Name() == "avatars" {
			t.Fatalf("a plan left %s behind", e.Name())
		}
	}
	if err := writeAvatar(p, plan); err != nil {
		t.Fatal(err)
	}
	img, err := os.ReadFile(filepath.Join(filepath.Dir(p), "avatars", "sre.png"))
	if err != nil || avatar.Format(img) != "png" {
		t.Fatalf("the picture: %v", err)
	}
	if dec, err := png.Decode(bytes.NewReader(img)); err != nil || dec.Bounds().Dx() != 512 || dec.Bounds().Dy() != 512 {
		t.Fatalf("not re-encoded to 512 square: %v", err)
	}
	after, _ := os.ReadFile(p)
	if !strings.Contains(string(after), "# my config") {
		t.Fatal("the file's comments were lost")
	}
	cfg, err := config.Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if a := cfg.Bots["sre"].Avatar; a.Image != "avatars/sre.png" || a.Preset != "owl" || a.Color != "teal" || a.ImagePath == "" {
		t.Fatalf("loaded avatar: %+v", a)
	}
	// Again: it replaces the picture. Clearing keeps the face.
	again, err := planAvatar(p, "sre", photo(t, 64, 64), &avatar.Crop{X: 0, Y: 0, Side: 32})
	if err != nil || !again.Replaces || !again.Valid {
		t.Fatalf("second set: %+v %v", again, err)
	}
	clr, err := planAvatar(p, "sre", nil, nil)
	if err != nil || !clr.Valid || !strings.Contains(clr.Diff, "+    avatar: {preset: owl, color: teal}") {
		t.Fatalf("clear: %+v %v", clr, err)
	}
	// A bot with no face: set adds image alone; clear removes avatar: entirely.
	pl, err := planAvatar(p, "plain", photo(t, 10, 10), nil)
	if err != nil || !strings.Contains(pl.Diff, "+    avatar: {image: avatars/plain.png}") {
		t.Fatalf("plain: %+v %v", pl, err)
	}
	// The config changed since the plan: refused.
	_ = os.WriteFile(p, append(after, []byte("# edited\n")...), 0o600)
	if err := writeAvatar(p, pl); err == nil || !strings.Contains(err.Error(), "changed since this plan") {
		t.Fatalf("a stale plan: %v", err)
	}
}

// Uploads that are not a plain PNG, JPEG or WebP are refused before anything
// is planned: an SVG, a file whose name lies, a polyglot, an oversize file.
func TestBotAvatarRefusals(t *testing.T) {
	p := avatarConfig(t)
	for name, c := range map[string]struct {
		data []byte
		want string
	}{
		"svg":      {[]byte(`<svg xmlns="http://www.w3.org/2000/svg" onload="alert(1)"/>`), "SVG is not accepted"},
		"html":     {[]byte("<html><script>x</script></html>"), "not a PNG, JPEG or WebP image"},
		"polyglot": {append(photo(t, 8, 8), []byte("<html><script>x</script>")...), "bytes after the image's end"},
		"oversize": {make([]byte, avatar.MaxUpload+1), "over the cap of"},
	} {
		if _, err := planAvatar(p, "sre", c.data, nil); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := planAvatar(p, "nobody", photo(t, 8, 8), nil); err == nil || !strings.Contains(err.Error(), `no bot "nobody"`) {
		t.Fatalf("an unknown bot: %v", err)
	}
	if _, err := parseCrop("1,2"); err == nil {
		t.Fatal("a two-part crop")
	}
}

// The operator API: plan (multipart) → token → apply; the picture is served
// re-encoded; a bot with none is a 404 (the dashboard shows its face or
// initials); a token is single-use and bound to its bot.
func TestBotAvatarAPI(t *testing.T) {
	p := avatarConfig(t)
	cfg, err := config.Load(p)
	if err != nil {
		t.Fatal(err)
	}
	cur := cfg
	mux := http.NewServeMux()
	avatarRoutes(mux, func() *config.Config { return cur }, func() string { return p })
	defer func() { planFull = true }()
	do := func(method, path string, body *bytes.Buffer, ctype string) *httptest.ResponseRecorder {
		if body == nil {
			body = &bytes.Buffer{}
		}
		r := httptest.NewRequest(method, path, body)
		if ctype != "" {
			r.Header.Set("Content-Type", ctype)
		}
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w
	}
	upload := func(data []byte, extra map[string]string) (*bytes.Buffer, string) {
		var b bytes.Buffer
		mw := multipart.NewWriter(&b)
		if data != nil {
			fw, _ := mw.CreateFormFile("file", "me.png")
			_, _ = fw.Write(data)
		}
		for k, v := range extra {
			_ = mw.WriteField(k, v)
		}
		_ = mw.Close()
		return &b, mw.FormDataContentType()
	}
	if w := do("GET", "/v1/bots/sre/avatar", nil, ""); w.Code != http.StatusNotFound || !strings.Contains(w.Body.String(), "face or its initials") {
		t.Fatalf("no picture yet: %d %s", w.Code, w.Body)
	}
	body, ct := upload([]byte("<svg/>"), nil)
	if w := do("POST", "/v1/bots/sre/avatar/plan", body, ct); w.Code != http.StatusUnprocessableEntity || !strings.Contains(w.Body.String(), "SVG") {
		t.Fatalf("an SVG plan: %d %s", w.Code, w.Body)
	}
	body, ct = upload(photo(t, 300, 200), map[string]string{"crop_x": "50", "crop_y": "0", "crop_side": "200"})
	w := do("POST", "/v1/bots/sre/avatar/plan", body, ct)
	var plan map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &plan)
	tok, _ := plan["token"].(string)
	if w.Code != 200 || plan["valid"] != true || plan["widening"] != false || tok == "" {
		t.Fatalf("plan: %d %s", w.Code, w.Body)
	}
	form := func(tok string) (*bytes.Buffer, string) {
		return bytes.NewBufferString(url.Values{"token": {tok}}.Encode()), "application/x-www-form-urlencoded"
	}
	b, c := form(tok)
	if w := do("POST", "/v1/bots/plain/avatar/apply", b, c); w.Code != http.StatusNotFound {
		t.Fatalf("another bot's token: %d", w.Code)
	}
	b, c = form(tok)
	if w := do("POST", "/v1/bots/sre/avatar/apply", b, c); w.Code != http.StatusNotFound {
		t.Fatalf("a token taken by a wrong-bot attempt still applied: %d", w.Code)
	}
	// Plan again, apply properly.
	body, ct = upload(photo(t, 300, 200), nil)
	w = do("POST", "/v1/bots/sre/avatar/plan", body, ct)
	_ = json.Unmarshal(w.Body.Bytes(), &plan)
	b, c = form(plan["token"].(string))
	if w := do("POST", "/v1/bots/sre/avatar/apply", b, c); w.Code != 200 || !strings.Contains(w.Body.String(), `"written":true`) {
		t.Fatalf("apply: %d %s", w.Code, w.Body)
	}
	b, c = form(plan["token"].(string))
	if w := do("POST", "/v1/bots/sre/avatar/apply", b, c); w.Code != http.StatusNotFound {
		t.Fatalf("a token applied twice: %d", w.Code)
	}
	// After a reload, served re-encoded.
	if cur, err = config.Load(p); err != nil {
		t.Fatal(err)
	}
	w = do("GET", "/v1/bots/sre/avatar", nil, "")
	if w.Code != 200 || w.Header().Get("Content-Type") != "image/png" || w.Header().Get("X-Content-Type-Options") != "nosniff" || avatar.Format(w.Body.Bytes()) != "png" {
		t.Fatalf("serve: %d %v", w.Code, w.Header())
	}
	// A file swapped in by hand that is not an image is refused at serve
	// time, never passed through.
	_ = os.WriteFile(filepath.Join(filepath.Dir(p), "avatars", "sre.png"), []byte("<svg/>"), 0o644)
	if w := do("GET", "/v1/bots/sre/avatar", nil, ""); w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("a swapped file: %d", w.Code)
	}
	if w := do("GET", "/v1/bots/nobody/avatar", nil, ""); w.Code != http.StatusNotFound {
		t.Fatalf("unknown bot: %d", w.Code)
	}
}
