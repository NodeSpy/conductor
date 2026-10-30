package main

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/NodeSpy/conductor/internal/avatar"
	"github.com/NodeSpy/conductor/internal/bot"
	"github.com/NodeSpy/conductor/internal/config"
)

// A bot's own picture (docs/wiki/Bots.md#identity):
//
//	conductor bot avatar set <bot> <file> [--crop x,y,side] [--write]
//	conductor bot avatar clear <bot> [--write]
//
// Setting one is a config change made like every other: the upload is
// re-encoded (a square PNG of at most 512px, pixels only), the change is
// validated on a copy of the config, and shown as a diff. Nothing is written
// without --write; then the image lands beside the config under a name of
// its own content (avatars/content/<sha>.png), named in the bot's entry as
// `avatar: { image: avatars/content/<sha>.png }`. Not a
// widening: a picture changes nothing a bot may do. The dashboard gets the
// same plan, and an apply, over the operator API.

// avatarPlan is a planned avatar change.
type avatarPlan struct {
	Bot      string `json:"bot,omitempty"`
	Team     string `json:"team,omitempty"`
	Image    string `json:"image,omitempty"` // the path the config will name
	Replaces bool   `json:"replaces,omitempty"`
	Bytes    int    `json:"bytes,omitempty"`
	Diff     string `json:"diff"`
	Valid    bool   `json:"valid"`
	Error    string `json:"error,omitempty"`
	Widening bool   `json:"widening"`
	Config   string `json:"-"`
	png      []byte
	cfgHash  string
	next     *config.BotAvatar // the avatar the entry ends with (nil: none)
}

// key is what a plan is for: "bot:<name>" or "team:<name>".
func (p *avatarPlan) key() string {
	if p.Team != "" {
		return "team:" + p.Team
	}
	return "bot:" + p.Bot
}

func sha(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }

// planAvatar plans setting (img non-nil) or clearing a bot's picture. Its
// face (a shape and eyes, or an icon, with its color) stays as the fallback where an
// image can't go.
func planAvatar(cfgPath, name string, img []byte, crop *avatar.Crop) (*avatarPlan, error) {
	return planAvatarFor(cfgPath, "bot", name, img, crop)
}

// planAvatarFor is planAvatar for a bot or a team (a team's face is an icon
// with its color; it has no face).
func planAvatarFor(cfgPath, kind, name string, img []byte, crop *avatar.Crop) (*avatarPlan, error) {
	src, err := os.ReadFile(cfgPath)
	if err != nil {
		return nil, err
	}
	cur, err := config.Load(cfgPath)
	if err != nil {
		return nil, fmt.Errorf("your config does not load as it is: %w", err)
	}
	var cur0 *config.BotAvatar
	set := config.SetBotAvatar
	plan := &avatarPlan{Bot: name, cfgHash: sha(src)}
	switch kind {
	case "team":
		t, ok := cur.Teams[name]
		if !ok {
			return nil, fmt.Errorf("no team %q", name)
		}
		cur0, set = t.Avatar, config.SetTeamAvatar
		plan.Bot, plan.Team = "", name
	default:
		m, ok := cur.Bots[name]
		if !ok {
			return nil, fmt.Errorf("no bot %q", name)
		}
		cur0 = m.Avatar
	}
	var face config.BotAvatar
	if cur0 != nil {
		face = config.BotAvatar{Shape: cur0.Shape, Eyes: cur0.Eyes, Preset: cur0.Preset, Icon: cur0.Icon, Color: cur0.Color}
	}
	dir := filepath.Dir(cfgPath)
	var next *config.BotAvatar
	stagedName := ""
	if img != nil {
		if plan.png, err = avatar.Normalize(img, crop, avatar.Size); err != nil {
			return nil, err
		}
		// The config names the picture by its content: the diff shows the
		// name that is written (or staged), and every upload has its own.
		plan.Image, plan.Bytes = avatarContentRel(plan.png), len(plan.png)
		plan.Replaces = cur0 != nil && cur0.Image != ""
		a := face
		a.Image = plan.Image
		next = &a
		// The copy is validated against the new picture, staged beside the
		// config under a temporary name.
		f, err := os.CreateTemp(dir, ".conductor-plan-avatar-*.png")
		if err != nil {
			return nil, err
		}
		stagedName = filepath.Base(f.Name())
		defer os.Remove(f.Name())
		_, werr := f.Write(plan.png)
		f.Close()
		if werr != nil {
			return nil, werr
		}
	} else if face != (config.BotAvatar{}) {
		next = &face
	}
	plan.next = next
	out, err := set(src, name, next)
	if err != nil {
		return nil, err
	}
	check := out
	if stagedName != "" {
		staged := *next
		staged.Image = stagedName
		if check, err = set(src, name, &staged); err != nil {
			return nil, err
		}
	}
	_, verr := validateCopy(cfgPath, check, planFull)
	plan.Valid = verr == nil
	if verr != nil {
		plan.Error = verr.Error()
	}
	plan.Diff = bot.UnifiedDiff(string(src), string(out))
	plan.Config = string(out)
	return plan, nil
}

// writeAvatar writes a valid plan: the picture, then the config. It refuses
// a config that changed since the plan.
func writeAvatar(cfgPath string, p *avatarPlan) error {
	if !p.Valid {
		return errors.New("the change does not validate — nothing written")
	}
	src, err := os.ReadFile(cfgPath)
	if err != nil {
		return err
	}
	if sha(src) != p.cfgHash {
		return errors.New("the config changed since this plan — plan it again")
	}
	if p.png != nil {
		if err := writeAvatarFile(cfgPath, p); err != nil {
			return err
		}
	}
	return writeConfig(cfgPath, p.Config)
}

// stageAvatar turns a valid plan into a config edit for the dashboard's
// config pipeline, which reviews, confirms (per config_stepup), backs up,
// audits and applies it under its own lock like any other edit. The picture
// is written now, under the plan's content name, which nothing references
// until that apply. Nothing is removed here: the pipeline collects
// unreferenced pictures after an apply (collectAvatars).
func stageAvatar(cfgPath string, p *avatarPlan) (path []string, value any, del bool, err error) {
	if !p.Valid {
		return nil, nil, false, errors.New("the change does not validate — nothing staged")
	}
	kind, name := "bots", p.Bot
	if p.Team != "" {
		kind, name = "teams", p.Team
	}
	path = []string{kind, name, "avatar"}
	if p.next == nil {
		return path, nil, true, nil
	}
	a := *p.next
	if p.png != nil {
		if err := writeAvatarFile(cfgPath, p); err != nil {
			return nil, nil, false, err
		}
	}
	v := map[string]any{}
	for k, x := range map[string]string{"image": a.Image, "shape": a.Shape, "eyes": a.Eyes, "preset": a.Preset, "icon": a.Icon, "color": a.Color} {
		if x != "" {
			v[k] = x
		}
	}
	return path, v, false, nil
}

// writeAvatarFile writes a plan's picture at its content name.
func writeAvatarFile(cfgPath string, p *avatarPlan) error {
	dst := filepath.Join(filepath.Dir(cfgPath), p.Image)
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	tmp := dst + ".conductor-new"
	if err := os.WriteFile(tmp, p.png, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, dst)
}

// avatarContentDir holds uploaded pictures, each named for its content
// (avatarContentRel). No bot's or team's own name is ever a path there, so
// collecting them can't reach a picture an operator named by hand.
const avatarContentDir = "avatars/content"

// avatarContentRel is a picture's content name, relative to the config.
func avatarContentRel(png []byte) string { return avatarContentDir + "/" + sha(png)[:32] + ".png" }

// isContentFile reports a file name in avatarContentDir: 32 lowercase hex
// digits and ".png".
func isContentFile(name string) bool {
	h, ok := strings.CutSuffix(name, ".png")
	if !ok || len(h) != 32 {
		return false
	}
	for _, c := range h {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

// collectAvatars removes the uploaded pictures (avatarContentDir) that
// nothing references: not the live config (as loaded, imports included),
// not any backup, not any session's staged edit (refs, from the config
// pipeline, which calls this after an apply or a restore under its lock).
// A picture is kept when its content name appears in any of them. If the
// live config doesn't load, nothing is collected.
func collectAvatars(cfgPath string, refs [][]byte) {
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return
	}
	var names []string
	for _, b := range cfg.Bots {
		if b.Avatar != nil {
			names = append(names, b.Avatar.Image)
		}
	}
	for _, t := range cfg.Teams {
		if t.Avatar != nil {
			names = append(names, t.Avatar.Image)
		}
	}
	refs = append(refs, []byte(strings.Join(names, "\n")))
	dir := filepath.Join(filepath.Dir(cfgPath), avatarContentDir)
	ents, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, ent := range ents {
		name := ent.Name()
		if ent.IsDir() || !isContentFile(name) {
			continue
		}
		h := []byte(strings.TrimSuffix(name, ".png"))
		used := false
		for _, r := range refs {
			if bytes.Contains(r, h) {
				used = true
				break
			}
		}
		if !used {
			_ = os.Remove(filepath.Join(dir, name))
		}
	}
}

func printAvatarPlan(p *avatarPlan) {
	who := "Bot " + p.Bot
	if p.Team != "" {
		who = "Team " + p.Team
	}
	if p.Image != "" {
		instead := ""
		if p.Replaces {
			instead = ", in place of its current picture"
		}
		fmt.Printf("%s: a %d-byte PNG (re-encoded, at most %dpx square, no metadata) — writes %s beside the config%s\n", who, p.Bytes, avatar.Size, p.Image, instead)
	} else {
		fmt.Printf("%s: its picture is removed from the config (the file itself is left in place)\n", who)
	}
	fmt.Println("  widening: none — a picture changes nothing anyone may do")
	if p.Valid {
		fmt.Println("  validated on a copy of your config: ok")
	} else {
		fmt.Printf("  validated on a copy of your config: FAILED — %s\n", p.Error)
	}
	fmt.Println("\n--- config diff")
	fmt.Print(p.Diff)
}

// parseCrop reads x,y,side.
func parseCrop(s string) (*avatar.Crop, error) {
	if s == "" {
		return nil, nil
	}
	parts := strings.Split(s, ",")
	if len(parts) != 3 {
		return nil, fmt.Errorf("--crop: want x,y,side (pixels of the source image)")
	}
	var n [3]int
	for i, p := range parts {
		v, err := strconv.Atoi(strings.TrimSpace(p))
		if err != nil {
			return nil, fmt.Errorf("--crop: %q is not a number", p)
		}
		n[i] = v
	}
	return &avatar.Crop{X: n[0], Y: n[1], Side: n[2]}, nil
}

func botAvatarCmd(args []string) error { return avatarCmd("bot", args) }

// avatarCmd is `conductor bot|team avatar set|clear …` (a team's picture is
// an operator's change too: refused in a session).
func avatarCmd(kind string, args []string) error {
	cfgPath, rest := configPath(args)
	pos, fl, _, _ := botFlags(rest)
	usage := fmt.Errorf("usage: conductor %[1]s avatar set <%[1]s> <file> [--crop x,y,side] [--write]\n       conductor %[1]s avatar clear <%[1]s> [--write]", kind)
	if os.Getenv("CONDUCTOR_SKILL_TOKEN") != "" {
		return fmt.Errorf("refusing: this shell is a conductor agent session — only an operator changes a %s's picture", kind)
	}
	if len(pos) < 2 {
		return usage
	}
	var plan *avatarPlan
	var err error
	switch pos[0] {
	case "set":
		if len(pos) != 3 {
			return usage
		}
		st, serr := os.Stat(pos[2])
		if serr != nil {
			return serr
		}
		if st.Size() > avatar.MaxUpload {
			return fmt.Errorf("%s: %d bytes, over the cap of %d", pos[2], st.Size(), avatar.MaxUpload)
		}
		img, rerr := os.ReadFile(pos[2])
		if rerr != nil {
			return rerr
		}
		crop, cerr := parseCrop(fl["crop"])
		if cerr != nil {
			return cerr
		}
		plan, err = planAvatarFor(cfgPath, kind, pos[1], img, crop)
	case "clear":
		if len(pos) != 2 {
			return usage
		}
		plan, err = planAvatarFor(cfgPath, kind, pos[1], nil, nil)
	default:
		return usage
	}
	if err != nil {
		return err
	}
	if fl["json"] != "" {
		if err := printJSON(plan); err != nil {
			return err
		}
	} else {
		printAvatarPlan(plan)
	}
	if !plan.Valid {
		return errors.New("the change does not validate — nothing written")
	}
	if fl["write"] == "" {
		fmt.Println("\nNot written: review the diff, then run the same command with --write.")
		return nil
	}
	if err := writeAvatar(cfgPath, plan); err != nil {
		return err
	}
	fmt.Printf("\nWritten to %s. `conductor reload` (or a restart) loads it.\n", cfgPath)
	return nil
}

// ---- the operator API ----

// avatarPlans are the dashboard's pending plans, by token, for the apply
// that follows its review (short-lived, in memory).
type avatarPlans struct {
	mu    sync.Mutex
	plans map[string]pendingAvatar
}

type pendingAvatar struct {
	plan    *avatarPlan
	expires time.Time
}

const avatarPlanTTL = 15 * time.Minute

func (ps *avatarPlans) put(p *avatarPlan) string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	tok := hex.EncodeToString(b[:])
	ps.mu.Lock()
	defer ps.mu.Unlock()
	now := time.Now()
	for k, v := range ps.plans {
		if now.After(v.expires) {
			delete(ps.plans, k)
		}
	}
	if ps.plans == nil {
		ps.plans = map[string]pendingAvatar{}
	}
	ps.plans[tok] = pendingAvatar{p, now.Add(avatarPlanTTL)}
	return tok
}

func (ps *avatarPlans) take(tok, key string) (*avatarPlan, bool) {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	v, ok := ps.plans[tok]
	delete(ps.plans, tok) // single use: a wrong-bot attempt burns it too
	if !ok || time.Now().After(v.expires) || v.plan.key() != key {
		return nil, false
	}
	return v.plan, true
}

func apiErr(w http.ResponseWriter, code int, err error) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
}

// avatarRoutes adds the avatar endpoints to the operator API:
//
//	GET  /v1/bots/{name}/avatar          its picture, re-encoded (404: none — show the face or initials)
//	POST /v1/bots/{name}/avatar/plan     multipart: file [, crop_x, crop_y, crop_side] | clear=1  → a plan with a token
//	POST /v1/bots/{name}/avatar/apply    token=…  → writes the planned change
//
// and the same under /v1/teams/{name}/avatar for a team's picture. On the
// dashboard, apply stages the change into the session's config edits instead
// (dashboardAvatarStage): the config pipeline writes it.
func avatarRoutes(mux routeAdder, ps *avatarPlans, cfg func() *config.Config, cfgPath func() string) {
	for _, kind := range []string{"bot", "team"} {
		avatarRoutesFor(mux, ps, kind, cfg, cfgPath)
	}
}

// avatarOf is a bot's or a team's avatar (ok false: no such bot or team).
func avatarOf(cfg *config.Config, kind, name string) (*config.BotAvatar, bool) {
	if kind == "team" {
		t, ok := cfg.Teams[name]
		return t.Avatar, ok
	}
	m, ok := cfg.Bots[name]
	return m.Avatar, ok
}

func avatarRoutesFor(mux routeAdder, ps *avatarPlans, kind string, cfg func() *config.Config, cfgPath func() string) {
	type cached struct {
		key string
		png []byte
	}
	var cmu sync.Mutex
	cache := map[string]cached{}
	base := "/v1/" + kind + "s/{name}/avatar"
	mux.HandleFunc("GET "+base, func(w http.ResponseWriter, r *http.Request) {
		name := r.PathValue("name")
		a, ok := avatarOf(cfg(), kind, name)
		if !ok {
			apiErr(w, http.StatusNotFound, fmt.Errorf("unknown %s %q", kind, name))
			return
		}
		if a == nil || a.ImagePath == "" {
			apiErr(w, http.StatusNotFound, fmt.Errorf("%s %s has no picture: show its face or its initials (%s show → identity)", kind, name, kind))
			return
		}
		st, err := os.Stat(a.ImagePath)
		if err != nil {
			apiErr(w, http.StatusNotFound, err)
			return
		}
		key := fmt.Sprintf("%s|%d|%d", a.ImagePath, st.Size(), st.ModTime().UnixNano())
		cmu.Lock()
		c, hit := cache[name]
		cmu.Unlock()
		if !hit || c.key != key {
			b, err := os.ReadFile(a.ImagePath)
			if err == nil {
				c.png, err = avatar.Serve(b)
			}
			if err != nil {
				apiErr(w, http.StatusUnprocessableEntity, err)
				return
			}
			c.key = key
			cmu.Lock()
			cache[name] = c
			cmu.Unlock()
		}
		w.Header().Set("Content-Type", "image/png")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; sandbox")
		w.Header().Set("Cache-Control", "private, no-cache")
		_, _ = w.Write(c.png)
	})
	mux.HandleFunc("POST "+base+"/plan", func(w http.ResponseWriter, r *http.Request) {
		name := r.PathValue("name")
		path := cfgPath()
		if path == "" {
			apiErr(w, http.StatusServiceUnavailable, errors.New("this daemon does not know its config file"))
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, avatar.MaxUpload+1<<20)
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			code := http.StatusBadRequest
			var mb *http.MaxBytesError
			if errors.As(err, &mb) {
				code = http.StatusRequestEntityTooLarge
				err = fmt.Errorf("the upload is over the cap of %d bytes", avatar.MaxUpload)
			}
			apiErr(w, code, err)
			return
		}
		defer r.MultipartForm.RemoveAll()
		var img []byte
		var crop *avatar.Crop
		if r.FormValue("clear") != "1" {
			f, _, err := r.FormFile("file")
			if err != nil {
				apiErr(w, http.StatusBadRequest, errors.New("want a file part (or clear=1)"))
				return
			}
			img, err = io.ReadAll(io.LimitReader(f, avatar.MaxUpload+1))
			f.Close()
			if err != nil {
				apiErr(w, http.StatusBadRequest, err)
				return
			}
			if len(img) > avatar.MaxUpload {
				apiErr(w, http.StatusRequestEntityTooLarge, fmt.Errorf("the image is over the cap of %d bytes", avatar.MaxUpload))
				return
			}
			if r.FormValue("crop_side") != "" {
				c, err := parseCrop(r.FormValue("crop_x") + "," + r.FormValue("crop_y") + "," + r.FormValue("crop_side"))
				if err != nil {
					apiErr(w, http.StatusBadRequest, err)
					return
				}
				crop = c
			}
		}
		planFull = false
		plan, err := planAvatarFor(path, kind, name, img, crop)
		if err != nil {
			apiErr(w, http.StatusUnprocessableEntity, err)
			return
		}
		out := toJSONMap(plan)
		if plan.Valid {
			out["token"] = ps.put(plan)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(out)
	})
	mux.HandleFunc("POST "+base+"/apply", func(w http.ResponseWriter, r *http.Request) {
		name := r.PathValue("name")
		r.Body = http.MaxBytesReader(w, r.Body, 4096)
		_ = r.ParseForm()
		plan, ok := ps.take(r.FormValue("token"), kind+":"+name)
		if !ok {
			apiErr(w, http.StatusNotFound, errors.New("no such plan (or it expired): plan it again"))
			return
		}
		if err := writeAvatar(cfgPath(), plan); err != nil {
			apiErr(w, http.StatusConflict, err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"written": true, "image": plan.Image, "next": "reload the config to see it"})
	})
}
