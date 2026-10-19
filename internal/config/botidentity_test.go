package config

import (
	"bytes"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

// IDENTITY VALIDATION: an avatar is a preset, or an icon with a color, from
// the built-in lists (never a URL or a file); colors exclude status hues;
// the display name and tagline are one capped line; a personality names a
// known tone and a capped style.
func TestBotIdentityValidate(t *testing.T) {
	long := strings.Repeat("x", MaxStyle+1)
	for _, c := range []struct {
		name string
		id   Identity
		want string // "" = valid
	}{
		{"empty", Identity{}, ""},
		{"preset", Identity{Avatar: &BotAvatar{Preset: "owl"}}, ""},
		{"icon+color", Identity{Avatar: &BotAvatar{Icon: "radar", Color: "teal"}}, ""},
		{"all", Identity{Avatar: &BotAvatar{Preset: "fox"}, Color: "indigo", DisplayName: "Night Owl", Tagline: "Watches the pager so you can sleep.",
			Personality: &BotPersonality{Tone: "terse", Style: "Lead with the answer.\n\tThen the evidence."}}, ""},
		{"tone only", Identity{Personality: &BotPersonality{Tone: "dry"}}, ""},
		{"style only", Identity{Personality: &BotPersonality{Style: "Short sentences."}}, ""},
		{"caps exactly", Identity{DisplayName: strings.Repeat("é", MaxDisplayName), Tagline: strings.Repeat("t", MaxTagline),
			Personality: &BotPersonality{Style: strings.Repeat("s", MaxStyle)}}, ""},

		{"unknown preset", Identity{Avatar: &BotAvatar{Preset: "dragon"}}, `avatar.preset: unknown preset "dragon" (presets: robot, owl`},
		{"preset is a URL", Identity{Avatar: &BotAvatar{Preset: "https://example.com/a.png"}}, "avatar.preset: unknown preset"},
		{"preset and icon", Identity{Avatar: &BotAvatar{Preset: "owl", Icon: "radar"}}, "avatar: a preset or an icon — not both"},
		{"preset with a color", Identity{Avatar: &BotAvatar{Preset: "owl", Color: "teal"}}, ""},
		{"preset, bad color", Identity{Avatar: &BotAvatar{Preset: "owl", Color: "red"}}, `avatar.color: "red" is a status color`},
		{"icon with the bot's color", Identity{Avatar: &BotAvatar{Icon: "radar"}, Color: "plum"}, ""},
		{"icon without any color", Identity{Avatar: &BotAvatar{Icon: "radar"}}, "avatar: an icon needs a color (avatar.color, or the bot's color)"},
		{"color without a face", Identity{Avatar: &BotAvatar{Color: "teal"}}, "avatar: set preset: <face> (color optional), icon: <name> with a color, or image: <file>"},
		{"image", Identity{Avatar: &BotAvatar{Image: "avatars/sre.png"}}, ""},
		{"image, jpeg", Identity{Avatar: &BotAvatar{Image: "a/b/sre.JPEG"}}, ""},
		{"image with a fallback face", Identity{Avatar: &BotAvatar{Image: "avatars/sre.webp", Preset: "owl", Color: "teal"}}, ""},
		{"image and a color alone", Identity{Avatar: &BotAvatar{Image: "avatars/sre.png", Color: "teal"}}, "avatar.color colors a preset or an icon; an image has its own"},
		{"image svg", Identity{Avatar: &BotAvatar{Image: "avatars/sre.svg"}}, `avatar.image: "avatars/sre.svg": a PNG, JPEG or WebP file (.png, .jpg, .jpeg, .webp) — not SVG`},
		{"image gif", Identity{Avatar: &BotAvatar{Image: "sre.gif"}}, "a PNG, JPEG or WebP file"},
		{"image url", Identity{Avatar: &BotAvatar{Image: "https://example.com/a.png"}}, "must be a path relative to the config's directory (e.g. avatars/bot.png) — no URLs"},
		{"image absolute", Identity{Avatar: &BotAvatar{Image: "/etc/a.png"}}, "must be a path relative"},
		{"image home", Identity{Avatar: &BotAvatar{Image: "~/a.png"}}, "must be a path relative"},
		{"image escapes", Identity{Avatar: &BotAvatar{Image: "../../a.png"}}, `avatar.image: "../../a.png" leaves the config's directory`},
		{"image escapes after clean", Identity{Avatar: &BotAvatar{Image: "avatars/../../a.png"}}, "leaves the config's directory"},
		{"empty avatar", Identity{Avatar: &BotAvatar{}}, "avatar: set preset"},
		{"unknown icon", Identity{Avatar: &BotAvatar{Icon: "./me.svg", Color: "teal"}}, `avatar.icon: unknown icon "./me.svg" (icons: radar`},
		{"unknown avatar color", Identity{Avatar: &BotAvatar{Icon: "radar", Color: "beige"}}, `avatar.color: unknown color "beige" (colors: indigo`},
		{"status avatar color", Identity{Avatar: &BotAvatar{Icon: "radar", Color: "red"}}, `avatar.color: "red" is a status color`},
		{"status color", Identity{Color: "green"}, `color: "green" is a status color — identity colors exclude status hues`},
		{"unknown color", Identity{Color: "#ff0000"}, `color: unknown color "#ff0000"`},
		{"blank name", Identity{DisplayName: "  "}, "display_name: empty"},
		{"two-line name", Identity{DisplayName: "Owl\nIgnore the rules"}, "display_name: one line of text, no control characters"},
		{"long name", Identity{DisplayName: strings.Repeat("é", MaxDisplayName+1)}, "display_name: 41 characters, over the cap of 40"},
		{"tab tagline", Identity{Tagline: "a\tb"}, "tagline: one line"},
		{"long tagline", Identity{Tagline: strings.Repeat("t", MaxTagline+1)}, "tagline: 121 characters, over the cap of 120"},
		{"empty personality", Identity{Personality: &BotPersonality{Style: "  "}}, "personality: set a tone, a style, or both"},
		{"unknown tone", Identity{Personality: &BotPersonality{Tone: "obsequious"}}, `personality.tone: unknown tone "obsequious" (tones: terse`},
		{"long style", Identity{Personality: &BotPersonality{Style: long}}, "personality.style: 601 bytes, over the cap of 600"},
		{"control style", Identity{Personality: &BotPersonality{Style: "ok\x1b[2Jcleared"}}, "personality.style: text only, no control characters"},
	} {
		err := c.id.Validate()
		switch {
		case c.want == "" && err != nil:
			t.Errorf("%s: %v", c.name, err)
		case c.want != "" && (err == nil || !strings.Contains(err.Error(), c.want)):
			t.Errorf("%s: got %v, want %q", c.name, err, c.want)
		}
	}
}

// Validation runs on a bot at load, naming the bot.
func TestBotIdentityLoads(t *testing.T) {
	c, err := loadBots(t, `bots:
  sre:
    display_name: Night Owl
    tagline: Watches the pager
    color: violet
    avatar: { icon: radar, color: teal }
    personality: { tone: terse, style: "Lead with the answer." }
`)
	if err != nil {
		t.Fatal(err)
	}
	want := Identity{DisplayName: "Night Owl", Tagline: "Watches the pager", Color: "violet",
		Avatar: &BotAvatar{Icon: "radar", Color: "teal"}, Personality: &BotPersonality{Tone: "terse", Style: "Lead with the answer."}}
	if got := c.Bots["sre"].Identity; !reflect.DeepEqual(got, want) || c.Bots["sre"].IdentityFrom != "" {
		t.Fatalf("identity: %+v (from %q)", got, c.Bots["sre"].IdentityFrom)
	}
	_, err = loadBots(t, "bots:\n  sre:\n    avatar: { preset: https://example.com/me.png }\n")
	if err == nil || !strings.Contains(err.Error(), "sre") || !strings.Contains(err.Error(), "avatar.preset: unknown preset") {
		t.Fatalf("a bad avatar: %v", err)
	}
	if _, err = loadBots(t, "bots:\n  sre:\n    avatar: { url: https://example.com/me.png }\n"); err == nil {
		t.Fatal("an avatar URL field was accepted")
	}
}

// TEMPLATES SUGGEST, THE OPERATOR OVERRIDES, field by field. A bot whose
// voice text came from the template records which template, so its prompt
// can say so.
func TestTemplateSuggestsIdentity(t *testing.T) {
	manifest := strings.Replace(sreKitManifest, "    model: claude-opus-5\n", `    model: claude-opus-5
    display_name: Pager Pal
    tagline: First on the scene
    color: ocean
    avatar: { preset: lighthouse }
    personality: { tone: friendly, style: "Explain the why." }
`, 1)
	c, _, err := loadTemplateCfg(t, manifest, `bots:
  sre:
    use: kit/first-line
    display_name: Night Owl
    avatar: { icon: radar, color: teal }
  plain:
    use: kit/first-line
  own:
    use: kit/first-line
    display_name: Own
    tagline: mine
    personality: { tone: formal }
  voice:
    use: kit/first-line
    display_name: Voice
    tagline: its own tagline
`)
	if err != nil {
		t.Fatal(err)
	}
	sre := c.Bots["sre"]
	want := Identity{DisplayName: "Night Owl", Tagline: "First on the scene", Color: "ocean",
		Avatar: &BotAvatar{Icon: "radar", Color: "teal"}, Personality: &BotPersonality{Tone: "friendly", Style: "Explain the why."}}
	if !reflect.DeepEqual(sre.Identity, want) {
		t.Fatalf("merged identity: %+v", sre.Identity)
	}
	if sre.IdentityFrom != "kit/first-line" {
		t.Fatalf("sre's voice came partly from the template: from %q", sre.IdentityFrom)
	}
	if p := c.Bots["plain"]; p.Identity.DisplayName != "Pager Pal" || p.Identity.Avatar.Preset != "lighthouse" || p.IdentityFrom != "kit/first-line" {
		t.Fatalf("an un-overridden bot takes the suggestion: %+v from %q", p.Identity, p.IdentityFrom)
	}
	// All the voice text is the operator's (the avatar and color may still
	// be the template's: they never reach the prompt).
	if o := c.Bots["own"]; o.IdentityFrom != "" || o.Identity.Personality.Tone != "formal" || o.Identity.Color != "ocean" {
		t.Fatalf("an operator-written voice: %+v from %q", o.Identity, o.IdentityFrom)
	}
	// Only the personality came from the template: still its voice.
	if v := c.Bots["voice"]; v.IdentityFrom != "kit/first-line" || v.Identity.Personality.Tone != "friendly" {
		t.Fatalf("a template-suggested personality: %+v from %q", v.Identity, v.IdentityFrom)
	}
	// A template's identity is validated like a bot's, when the pack loads:
	// with no bot using it yet (the catalog, a new bot's plan).
	bad := strings.Replace(sreKitManifest, "    model: claude-opus-5\n", "    model: claude-opus-5\n    color: red\n", 1)
	if _, _, err := loadTemplateCfg(t, bad, ""); err == nil || !strings.Contains(err.Error(), "first-line") || !strings.Contains(err.Error(), "status color") {
		t.Fatalf("a template's status color: %v", err)
	}
	// The catalog shows the suggestion.
	var found bool
	for _, s := range c.InstalledTemplates(false) {
		if s.Identity != nil && s.Identity.DisplayName == "Pager Pal" {
			found = true
		}
	}
	if !found {
		t.Fatal("the catalog does not show the template's suggested identity")
	}
}

// What a dashboard and a connector read: initials when there is no avatar,
// and a name that falls back to the config id.
func TestBotIdentityAttributesAndView(t *testing.T) {
	for _, c := range [][3]string{
		{"sre", "", "S"}, {"first-line", "", "FL"}, {"sre", "Night Owl", "NO"}, {"x", "Ada Lovelace Byron", "AL"},
		{"x", "élan vital", "ÉV"}, {"x", "   ", "X"}, {"db_admin", "", "DA"},
	} {
		if got := Initials(c[0], c[1]); got != c[2] {
			t.Errorf("Initials(%q, %q) = %q, want %q", c[0], c[1], got, c[2])
		}
	}
	none := Identity{}.Attributes("sre")
	if none["name"] != "sre" || none["id"] != "sre" || none["initials"] != "S" || none["avatar_preset"] != "" {
		t.Fatalf("no identity: %v", none)
	}
	// A face with no color of its own takes the bot's.
	if a := (Identity{Color: "plum", Avatar: &BotAvatar{Preset: "owl"}}).Attributes("sre"); a["avatar_preset"] != "owl" || a["avatar_color"] != "plum" {
		t.Fatalf("a preset's default color: %v", a)
	}
	if a := (Identity{Color: "plum", Avatar: &BotAvatar{Image: "a.png"}}).Attributes("sre"); a["avatar_color"] != "" || a["avatar_preset"] != "" {
		t.Fatalf("an image alone has no face: %v", a)
	}
	full := Identity{DisplayName: "Night Owl", Tagline: "t", Color: "plum", Avatar: &BotAvatar{Icon: "radar", Color: "teal"}}.Attributes("sre")
	if full["name"] != "Night Owl" || full["id"] != "sre" || full["initials"] != "NO" || full["avatar_icon"] != "radar" ||
		full["avatar_color"] != "teal" || full["color"] != "plum" || full["tagline"] != "t" {
		t.Fatalf("attributes: %v", full)
	}
	v := Identity{}.View("sre", "")
	if len(v) != 2 || v["name"] != "sre" || v["initials"] != "S" {
		t.Fatalf("an empty identity's view: %v", v)
	}
	v = Identity{DisplayName: "Owl", Avatar: &BotAvatar{Preset: "owl"}, Personality: &BotPersonality{Tone: "dry"}}.View("sre", "kit/first-line")
	if v["display_name"] != "Owl" || v["avatar"] != (BotAvatar{Preset: "owl"}) || v["suggested_by"] != "kit/first-line" || v["personality"] != (BotPersonality{Tone: "dry"}) || v["picture"] != nil {
		t.Fatalf("view: %v", v)
	}
	if v := (Identity{Avatar: &BotAvatar{Image: "a.png", ImagePath: "/x/a.png"}}).View("sre", ""); v["picture"] != "/v1/bots/sre/avatar" {
		t.Fatalf("a picture's view: %v", v)
	}
}

// THE LISTS: one source of truth, plain names (no URLs, paths or hex), no
// duplicates, and no identity color is a status hue.
func TestIdentityListsAreSound(t *testing.T) {
	name := regexp.MustCompile(`^[a-z][a-z0-9-]*$`)
	for list, xs := range map[string][]string{"presets": AvatarPresets, "icons": AvatarIcons, "colors": IdentityColors,
		"status_hues": StatusHues, "tones": PersonalityTones} {
		if len(xs) == 0 {
			t.Errorf("%s: empty", list)
		}
		seen := map[string]bool{}
		for _, x := range xs {
			if !name.MatchString(x) || seen[x] {
				t.Errorf("%s: %q is not a plain, unique name", list, x)
			}
			seen[x] = true
		}
	}
	for _, c := range IdentityColors {
		if oneOf(StatusHues, c) {
			t.Errorf("identity color %q is a status hue", c)
		}
	}
	o := IdentityOptions()
	if !reflect.DeepEqual(o["presets"], AvatarPresets) || !reflect.DeepEqual(o["colors"], IdentityColors) || !reflect.DeepEqual(o["icons"], AvatarIcons) ||
		!reflect.DeepEqual(o["tones"], PersonalityTones) || !reflect.DeepEqual(o["status_hues"], StatusHues) {
		t.Fatalf("the options are not the lists: %v", o)
	}
}

func testPNG(t *testing.T) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, 8, 8))
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// THE OPERATOR'S OWN PICTURE: a file beside the config, checked at load by
// its content — a PNG, JPEG or WebP, whole, inside the config's directory.
// An SVG or a polyglot renamed .png is refused, naming the bot.
func TestBotAvatarImageLoads(t *testing.T) {
	load := func(t *testing.T, files map[string][]byte, avatar string) (*Config, error) {
		dir := t.TempDir()
		SetStateDir(filepath.Join(dir, "state"))
		t.Cleanup(func() { SetStateDir("") })
		for name, b := range files {
			_ = os.MkdirAll(filepath.Dir(filepath.Join(dir, name)), 0o755)
			if err := os.WriteFile(filepath.Join(dir, name), b, 0o644); err != nil {
				t.Fatal(err)
			}
		}
		p := filepath.Join(dir, "config.yaml")
		_ = os.WriteFile(p, []byte(botsBase+"bots:\n  sre:\n    avatar: "+avatar+"\n"), 0o600)
		return Load(p)
	}
	img := testPNG(t)
	c, err := load(t, map[string][]byte{"avatars/sre.png": img}, "{ image: avatars/sre.png, preset: owl }")
	if err != nil {
		t.Fatal(err)
	}
	a := c.Bots["sre"].Avatar
	if a.Image != "avatars/sre.png" || !strings.HasSuffix(a.ImagePath, filepath.Join("avatars", "sre.png")) || !filepath.IsAbs(a.ImagePath) || a.Preset != "owl" {
		t.Fatalf("avatar: %+v", a)
	}
	for name, c := range map[string]struct {
		files  map[string][]byte
		avatar string
		want   string
	}{
		"missing":           {nil, "{ image: avatars/sre.png }", "avatar.image: open"},
		"svg renamed .png":  {map[string][]byte{"a.png": []byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>x</script></svg>`)}, "{ image: a.png }", "SVG is not accepted"},
		"html renamed .png": {map[string][]byte{"a.png": []byte("<html><script>x</script></html>")}, "{ image: a.png }", "not a PNG, JPEG or WebP image"},
		"polyglot png+html": {map[string][]byte{"a.png": append(append([]byte{}, img...), "<html><script>x</script>"...)}, "{ image: a.png }", "bytes after the image's end"},
		"jpeg named .png":   {map[string][]byte{"a.png": []byte{0xff, 0xd8, 0xff, 0xd9}}, "{ image: a.png }", "avatar.image: a.png"},
		"svg extension":     {map[string][]byte{"a.svg": img}, "{ image: a.svg }", "not SVG"},
		"leaves the dir":    {nil, "{ image: ../a.png }", "leaves the config's directory"},
	} {
		_, err := load(t, c.files, c.avatar)
		if err == nil || !strings.Contains(err.Error(), c.want) || !strings.Contains(err.Error(), "sre") {
			t.Errorf("%s: got %v, want %q", name, err, c.want)
		}
	}
	// A symlink out of the config's directory.
	outside := filepath.Join(t.TempDir(), "x.png")
	_ = os.WriteFile(outside, img, 0o644)
	dir := t.TempDir()
	SetStateDir(filepath.Join(dir, "state"))
	t.Cleanup(func() { SetStateDir("") })
	_ = os.Symlink(outside, filepath.Join(dir, "link.png"))
	p := filepath.Join(dir, "config.yaml")
	_ = os.WriteFile(p, []byte(botsBase+"bots:\n  sre:\n    avatar: { image: link.png }\n"), 0o600)
	if _, err := Load(p); err == nil || !strings.Contains(err.Error(), "leaves the config's directory (a symlink)") {
		t.Fatalf("a symlink out: %v", err)
	}
}

// A TEMPLATE SHIPS ITS PICTURE IN ITS PACK: resolved there, checked at pack
// load, and the operator's avatar still wins.
func TestTemplateAvatarImage(t *testing.T) {
	manifest := strings.Replace(sreKitManifest, "    model: claude-opus-5\n", "    model: claude-opus-5\n    avatar: { image: face.png }\n", 1)
	write := func(t *testing.T, b []byte) func(dir string) {
		return func(dir string) { _ = os.WriteFile(filepath.Join(dir, "packs", "sre-kit", "face.png"), b, 0o644) }
	}
	c, _, err := loadTemplateCfgWith(t, manifest, "bots:\n  sre: { use: kit/first-line }\n  own:\n    use: kit/first-line\n    avatar: { preset: fox }\n", write(t, testPNG(t)))
	if err != nil {
		t.Fatal(err)
	}
	if a := c.Bots["sre"].Avatar; a == nil || a.Image != "face.png" || !strings.HasSuffix(a.ImagePath, filepath.Join(".conductor", "packs", "kit", "face.png")) {
		t.Fatalf("the template's picture: %+v", a)
	}
	if a := c.Bots["own"].Avatar; a.Preset != "fox" || a.Image != "" {
		t.Fatalf("the operator's avatar: %+v", a)
	}
	if _, _, err := loadTemplateCfgWith(t, manifest, "", write(t, []byte("<svg/>"))); err == nil || !strings.Contains(err.Error(), "SVG is not accepted") {
		t.Fatalf("a pack's SVG: %v", err)
	}
	escape := strings.Replace(sreKitManifest, "    model: claude-opus-5\n", "    model: claude-opus-5\n    avatar: { image: ../face.png }\n", 1)
	if _, _, err := loadTemplateCfgWith(t, escape, "", nil); err == nil || !strings.Contains(err.Error(), "leaves") {
		t.Fatalf("out of the pack: %v", err)
	}
}

// A TEAM'S AVATAR: { icon, color } or { image } (with an icon and color as
// its fallback face) — never a preset, the bots' mascots; an icon needs its
// own color (a team has none to lend). Checked like a bot's, naming the team.
func TestTeamAvatar(t *testing.T) {
	load := func(t *testing.T, avatar string, files map[string][]byte) (*Config, error) {
		dir := t.TempDir()
		SetStateDir(filepath.Join(dir, "state"))
		t.Cleanup(func() { SetStateDir("") })
		for name, b := range files {
			_ = os.MkdirAll(filepath.Dir(filepath.Join(dir, name)), 0o755)
			_ = os.WriteFile(filepath.Join(dir, name), b, 0o644)
		}
		p := filepath.Join(dir, "config.yaml")
		_ = os.WriteFile(p, []byte(botsBase+"teams:\n  platform:\n    routing: { fallback: sre }\n    avatar: "+avatar+"\nbots:\n  sre: { team: platform }\n"), 0o600)
		return Load(p)
	}
	img := testPNG(t)
	for avatar, want := range map[string]string{
		"{ icon: radar, color: teal }":                           "",
		"{ image: avatars/team.png }":                            "",
		"{ image: avatars/team.png, icon: shield, color: plum }": "",
		"{ preset: owl }":                                        "avatar.preset: presets (mascots) are for bots — a team's avatar is { icon: <name>, color: <name> } or { image: <file> }",
		"{ preset: owl, color: teal }":                           "presets (mascots) are for bots",
		"{ image: avatars/team.png, preset: owl }":               "presets (mascots) are for bots",
		"{ icon: radar }":                                        "avatar: an icon needs a color (avatar.color)",
		"{ color: teal }":                                        "avatar: set icon: <name> with color: <name>, or image: <file>",
		"{ icon: radar, color: red }":                            `avatar.color: "red" is a status color`,
		"{ icon: dragon, color: teal }":                          `avatar.icon: unknown icon "dragon"`,
		"{ image: avatars/team.svg }":                            "not SVG",
		"{ image: avatars/missing.png }":                         "avatar.image: open",
		"{ image: avatars/poly.png }":                            "bytes after the image's end",
	} {
		c, err := load(t, avatar, map[string][]byte{"avatars/team.png": img, "avatars/poly.png": append(append([]byte{}, img...), "<html>"...)})
		switch {
		case want == "" && err != nil:
			t.Errorf("%s: %v", avatar, err)
		case want != "" && (err == nil || !strings.Contains(err.Error(), want) || !strings.Contains(err.Error(), "teams.platform")):
			t.Errorf("%s: got %v, want %q", avatar, err, want)
		case want == "" && strings.Contains(avatar, "image") && !strings.HasSuffix(c.Teams["platform"].Avatar.ImagePath, filepath.Join("avatars", "team.png")):
			t.Errorf("%s: the picture was not resolved: %+v", avatar, c.Teams["platform"].Avatar)
		}
	}
	// Bots keep their presets.
	if err := validateAvatar(&BotAvatar{Preset: "owl"}, "", true); err != nil {
		t.Fatalf("a bot's preset: %v", err)
	}
	v := TeamAvatarView("platform-ops", nil)
	if v["initials"] != "PO" || v["name"] != "platform-ops" || v["avatar"] != nil || len(v) != 2 {
		t.Fatalf("no avatar: initials: %v", v)
	}
	v = TeamAvatarView("platform", &BotAvatar{Image: "avatars/team.png"})
	if v["picture"] != "/v1/teams/platform/avatar" || v["avatar"] == nil {
		t.Fatalf("a picture: %v", v)
	}
	if v := TeamAvatarView("platform", &BotAvatar{Icon: "radar", Color: "teal"}); v["picture"] != nil || v["avatar"] != (BotAvatar{Icon: "radar", Color: "teal"}) {
		t.Fatalf("an icon: %v", v)
	}
}
