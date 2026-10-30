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
	"github.com/NodeSpy/conductor/internal/configedit"
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
	if !plan.Valid || plan.Widening || !strings.HasPrefix(plan.Image, avatarContentDir+"/") || !isContentFile(filepath.Base(plan.Image)) || plan.Replaces {
		t.Fatalf("plan: %+v", plan)
	}
	if !strings.Contains(plan.Diff, "+    avatar: {image: "+plan.Image+", shape: circle, eyes: wide, color: teal}") || !strings.Contains(plan.Diff, "-    avatar: { preset: owl, color: teal }") {
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
	img, err := os.ReadFile(filepath.Join(filepath.Dir(p), plan.Image))
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
	if a := cfg.Bots["sre"].Avatar; a.Image != plan.Image || a.Shape != "circle" || a.Eyes != "wide" || a.Color != "teal" || a.ImagePath == "" {
		t.Fatalf("loaded avatar: %+v", a)
	}
	// Again: it replaces the picture. Clearing keeps the face.
	again, err := planAvatar(p, "sre", photo(t, 64, 64), &avatar.Crop{X: 0, Y: 0, Side: 32})
	if err != nil || !again.Replaces || !again.Valid || again.Image == plan.Image {
		t.Fatalf("second set: %+v %v", again, err)
	}
	clr, err := planAvatar(p, "sre", nil, nil)
	if err != nil || !clr.Valid || !strings.Contains(clr.Diff, "+    avatar: {shape: circle, eyes: wide, color: teal}") {
		t.Fatalf("clear: %+v %v", clr, err)
	}
	// A bot with no face: set adds image alone; clear removes avatar: entirely.
	pl, err := planAvatar(p, "plain", photo(t, 10, 10), nil)
	if err != nil || !strings.Contains(pl.Diff, "+    avatar: {image: "+pl.Image+"}") {
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
	avatarRoutes(mux, &avatarPlans{}, func() *config.Config { return cur }, func() string { return p })
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
	_ = os.WriteFile(filepath.Join(filepath.Dir(p), cur.Bots["sre"].Avatar.Image), []byte("<svg/>"), 0o644)
	if w := do("GET", "/v1/bots/sre/avatar", nil, ""); w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("a swapped file: %d", w.Code)
	}
	if w := do("GET", "/v1/bots/nobody/avatar", nil, ""); w.Code != http.StatusNotFound {
		t.Fatalf("unknown bot: %d", w.Code)
	}
}

// A TEAM'S PICTURE goes through the same pipeline: re-encoded, planned on a
// copy, written under a name of its own content with one
// avatar line on the team; its icon is kept as the fallback; the API plans,
// applies and serves it, and a team's token never applies to a bot.
func TestTeamAvatarSet(t *testing.T) {
	p := avatarConfig(t)
	src, _ := os.ReadFile(p)
	src = []byte(strings.Replace(string(src), "    description: first-line SRE   # keep this comment\n", "    description: first-line SRE   # keep this comment\n    team: platform\n", 1))
	src = append(src, []byte("teams:\n  platform:\n    description: the platform team\n    routing: { fallback: sre }\n    avatar: { icon: radar, color: teal }\n")...)
	_ = os.WriteFile(p, src, 0o600)
	plan, err := planAvatarFor(p, "team", "platform", photo(t, 400, 300), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !plan.Valid || !strings.HasPrefix(plan.Image, avatarContentDir+"/") || plan.Team != "platform" || plan.Bot != "" {
		t.Fatalf("plan: %+v", plan)
	}
	if !strings.Contains(plan.Diff, "+    avatar: {image: "+plan.Image+", icon: radar, color: teal}") {
		t.Fatalf("diff:\n%s", plan.Diff)
	}
	if err := writeAvatar(p, plan); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if a := cfg.Teams["platform"].Avatar; a.Image != plan.Image || a.ImagePath == "" || a.Icon != "radar" {
		t.Fatalf("loaded: %+v", a)
	}
	if _, err := planAvatarFor(p, "team", "nope", photo(t, 8, 8), nil); err == nil || !strings.Contains(err.Error(), `no team "nope"`) {
		t.Fatalf("an unknown team: %v", err)
	}
	if _, err := planAvatarFor(p, "team", "platform", []byte("<svg/>"), nil); err == nil || !strings.Contains(err.Error(), "SVG") {
		t.Fatalf("an SVG: %v", err)
	}
	clr, err := planAvatarFor(p, "team", "platform", nil, nil)
	if err != nil || !strings.Contains(clr.Diff, "+    avatar: {icon: radar, color: teal}") {
		t.Fatalf("clear keeps the icon: %+v %v", clr, err)
	}
	// The API.
	cur := cfg
	mux := http.NewServeMux()
	avatarRoutes(mux, &avatarPlans{}, func() *config.Config { return cur }, func() string { return p })
	defer func() { planFull = true }()
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/v1/teams/platform/avatar", nil))
	if w.Code != 200 || avatar.Format(w.Body.Bytes()) != "png" {
		t.Fatalf("serve: %d", w.Code)
	}
	var b bytes.Buffer
	mw := multipart.NewWriter(&b)
	fw, _ := mw.CreateFormFile("file", "t.png")
	_, _ = fw.Write(photo(t, 64, 64))
	_ = mw.Close()
	r := httptest.NewRequest("POST", "/v1/teams/platform/avatar/plan", &b)
	r.Header.Set("Content-Type", mw.FormDataContentType())
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	tok, _ := out["token"].(string)
	if w.Code != 200 || out["team"] != "platform" || tok == "" {
		t.Fatalf("plan: %d %s", w.Code, w.Body)
	}
	apply := func(path, tok string) int {
		r := httptest.NewRequest("POST", path, strings.NewReader(url.Values{"token": {tok}}.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w.Code
	}
	if c := apply("/v1/bots/platform/avatar/apply", tok); c != http.StatusNotFound {
		t.Fatalf("a team's token applied to a bot: %d", c)
	}
	if w := httptest.NewRecorder(); true {
		mux.ServeHTTP(w, httptest.NewRequest("GET", "/v1/teams/nope/avatar", nil))
		if w.Code != http.StatusNotFound {
			t.Fatalf("an unknown team's picture: %d", w.Code)
		}
	}
}

// `conductor team avatar` is an operator's: refused in a session.
func TestTeamAvatarRefusesASession(t *testing.T) {
	t.Setenv("CONDUCTOR_SKILL_TOKEN", "x")
	if err := cmdTeam([]string{"avatar", "set", "platform", "x.png"}); err == nil || !strings.Contains(err.Error(), "only an operator changes a team's picture") {
		t.Fatalf("in a session: %v", err)
	}
}

// The dashboard's avatar apply stages a config edit instead of writing the
// config: the picture lands under a name of its own content that nothing
// references yet, the plan's diff shows that name, the live config is
// untouched, and the edit — applied by the config pipeline — names it and
// loads. Staging removes nothing: a picture is kept while any session's
// staged edit, the live config or a backup names it, and collected only
// after an apply. Pictures named by hand are never collected.
func TestDashboardAvatarApplyStagesAConfigEdit(t *testing.T) {
	p := avatarConfig(t)
	dir := filepath.Dir(p)
	before, _ := os.ReadFile(p)
	m := &botStack{}
	defer func(old string) { botConfigPath = old }(botConfigPath)
	botConfigPath = p
	pipe := configedit.New(configedit.Options{Path: p, Validate: func(c string) error { _, err := config.Load(c); return err },
		Collect: func(refs [][]byte) { collectAvatars(p, refs) }})
	exists := func(rel string) bool { _, err := os.Stat(filepath.Join(dir, rel)); return err == nil }
	// stageAs plans a picture and stages it in owner's set, as the
	// dashboard's handler does (under the editor's lock).
	stageAs := func(owner string, img []byte, crop *avatar.Crop) string {
		t.Helper()
		plan, err := planAvatarFor(p, "bot", "sre", img, crop)
		if err != nil || !plan.Valid {
			t.Fatalf("plan: %v %+v", err, plan)
		}
		if !strings.Contains(plan.Diff, plan.Image) || !strings.HasPrefix(plan.Image, avatarContentDir+"/") {
			t.Fatalf("the plan's diff doesn't show the name it stages (%s):\n%s", plan.Image, plan.Diff)
		}
		tok := m.avatarPlans().put(plan)
		ch, err := pipe.As(owner).StageWith(func() (configedit.Change, error) {
			path, value, del, err := m.dashboardAvatarStage("bot", "sre", tok)
			return configedit.Change{Path: path, Value: value, Delete: del}, err
		})
		if err != nil || ch.Delete || strings.Join(ch.Path, ".") != "bots.sre.avatar" {
			t.Fatalf("stage: %+v %v", ch, err)
		}
		if _, _, _, err := m.dashboardAvatarStage("bot", "sre", tok); err == nil {
			t.Fatal("a plan token stages once")
		}
		name := ch.Value.(map[string]any)["image"].(string)
		if name != plan.Image || !isContentFile(filepath.Base(name)) || !exists(name) {
			t.Fatalf("staged image %q (planned %q)", name, plan.Image)
		}
		return name
	}
	apply := func(owner string) error {
		t.Helper()
		rev, err := pipe.As(owner).Review()
		if err != nil {
			t.Fatal(err)
		}
		_, err = pipe.As(owner).ApplyWith(rev.Token, nil)
		return err
	}
	// Pictures named by hand, a team's included: never collected, whatever
	// their names look like.
	for _, hand := range []string{"avatars/sre.png", "avatars/team-0123456789ab.png", "avatars/team-platform.png"} {
		if err := os.MkdirAll(filepath.Join(dir, "avatars"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, hand), photo(t, 8, 8), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// The review's race: two sessions stage different pictures for one bot.
	victim := stageAs("victim", photo(t, 64, 64), nil)
	if after, _ := os.ReadFile(p); string(after) != string(before) {
		t.Fatal("staging wrote the config")
	}
	other := stageAs("other", photo(t, 64, 64), &avatar.Crop{X: 0, Y: 0, Side: 20})
	if victim == other {
		t.Fatal("two different pictures got one name")
	}
	if !exists(victim) {
		t.Fatal("another session's upload removed a staged picture")
	}
	// An upload whose staged edit is dropped is unreferenced.
	dropped := stageAs("dropper", photo(t, 64, 64), &avatar.Crop{X: 10, Y: 10, Side: 30})
	pipe.As("dropper").Clear()

	if err := apply("other"); err != nil {
		t.Fatalf("the other session's apply: %v", err)
	}
	if !exists(victim) || !exists(other) {
		t.Fatal("an apply collected a picture a staged set or the live config names")
	}
	if exists(dropped) {
		t.Fatal("an unreferenced upload survived the apply")
	}
	if err := apply("victim"); err != nil {
		t.Fatalf("the victim's confirmed apply: %v", err)
	}
	cfg, err := config.Load(p)
	if err != nil || cfg.Bots["sre"].Avatar == nil || cfg.Bots["sre"].Avatar.Image != victim {
		t.Fatalf("after apply: %v %+v", err, cfg.Bots["sre"].Avatar)
	}
	// The other picture is in a backup now, so it's kept and a restore of
	// that backup loads.
	if !exists(other) {
		t.Fatal("a picture a backup names was collected")
	}
	hist, err := pipe.History()
	if err != nil || len(hist) == 0 {
		t.Fatalf("history: %v %v", hist, err)
	}
	var withOther string
	for _, h := range hist {
		b, _ := os.ReadFile(filepath.Join(dir, ".conductor-config-history", h.ID+".yaml"))
		if strings.Contains(string(b), other) {
			withOther = h.ID
		}
	}
	rev, _, err := pipe.RestoreReview(withOther)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pipe.Restore(withOther, rev.Token); err != nil {
		t.Fatalf("restoring the backup that names the other picture: %v", err)
	}
	for _, hand := range []string{"avatars/sre.png", "avatars/team-0123456789ab.png", "avatars/team-platform.png"} {
		if !exists(hand) {
			t.Fatalf("a picture named by hand was collected: %s", hand)
		}
	}
}
