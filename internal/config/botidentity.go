package config

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/NodeSpy/conductor/internal/avatar"
)

// BOT IDENTITY (docs/wiki/Bots.md#identity): what makes a bot recognizable —
// an avatar, a color, a display name, a tagline and a personality. All of it
// is presentation: none of it changes what a bot may do.
//
// THE NAME LISTS BELOW ARE THE ONE SOURCE OF TRUTH: validation, the
// catalog, `conductor bot show` and the dashboard API all read them. They are
// placeholders until the designed set lands — changing a list is
// a one-file change. No URLs: an avatar is a name from a list, or the
// operator's own picture — a PNG, JPEG or WebP file beside the config (never
// SVG), checked by content and re-encoded whenever conductor serves it.

// A bot's face is mix-and-match: `avatar: { shape: …, eyes: …, color: … }`,
// each a name from its own list.

// AvatarShapes are the shapes a face may take.
var AvatarShapes = []string{"circle", "squircle", "hexagon", "shield", "blob", "diamond", "drop", "star"}

// AvatarEyes are the eyes a face may wear.
var AvatarEyes = []string{"round", "wide", "sleepy", "wink", "visor", "dots", "happy", "focused"}

// AvatarFace is a shape with its eyes.
type AvatarFace struct {
	Shape string `json:"shape"`
	Eyes  string `json:"eyes"`
}

// AvatarPresetAliases keep the old precanned faces loading: DEPRECATED
// `avatar: { preset: owl }` reads as the shape and eyes named here.
var AvatarPresetAliases = map[string]AvatarFace{
	"robot":      {"squircle", "visor"},
	"owl":        {"circle", "wide"},
	"fox":        {"diamond", "sleepy"},
	"octopus":    {"blob", "dots"},
	"lighthouse": {"drop", "focused"},
	"satellite":  {"hexagon", "round"},
	"compass":    {"star", "happy"},
	"anvil":      {"shield", "focused"},
}

// AvatarPresetNames are the deprecated preset names, sorted.
func AvatarPresetNames() []string {
	out := mapKeys(AvatarPresetAliases)
	sort.Strings(out)
	return out
}

// AvatarIcons are the icons an avatar may pair with a color
// (`avatar: { icon: …, color: … }`).
var AvatarIcons = []string{"radar", "shield", "wrench", "book", "bolt", "eye", "gear", "pulse", "cloud", "database", "terminal", "chart", "bell", "key", "globe"}

// IdentityColors is the identity palette: an avatar's color and a bot's
// `color:`. It never includes a status hue (StatusHues), so a bot's color
// can never read as "failing" or "healthy".
var IdentityColors = []string{"indigo", "violet", "plum", "magenta", "teal", "ocean", "slate", "sand", "graphite"}

// StatusHues are reserved for status (failing, warning, healthy, …): never an
// identity color.
var StatusHues = []string{"red", "green", "amber", "yellow", "orange"}

// PersonalityTones are the tones a personality may name.
var PersonalityTones = []string{"terse", "friendly", "formal", "warm", "dry", "playful"}

// Identity size caps.
const (
	MaxDisplayName = 40  // runes
	MaxTagline     = 120 // runes
	MaxStyle       = 600 // bytes
)

// BotAvatar is a face (a shape, with eyes), or an icon with a color — at
// most one — and optionally the operator's own picture.
type BotAvatar struct {
	// Shape (AvatarShapes) and Eyes (AvatarEyes) make a face; Icon is one
	// from AvatarIcons instead. Color colors either (default: the bot's
	// identity color).
	Shape string `yaml:"shape,omitempty" json:"shape,omitempty"`
	Eyes  string `yaml:"eyes,omitempty" json:"eyes,omitempty"`
	Icon  string `yaml:"icon,omitempty" json:"icon,omitempty"`
	Color string `yaml:"color,omitempty" json:"color,omitempty"`
	// Preset is DEPRECATED: a name from AvatarPresetAliases, read at load
	// as its shape and eyes (resolveAvatarPreset), which leaves it empty.
	Preset string `yaml:"preset,omitempty" json:"preset,omitempty"`
	// PresetAlias is the preset a face was read from (computed).
	PresetAlias string `yaml:"-" json:"-"`
	// Image is the operator's own picture: a PNG, JPEG or WebP file,
	// relative to the config's directory (a template's: relative to its
	// pack), so it travels with the config. A face or an icon beside it is
	// what shows where an image can't go.
	Image string `yaml:"image,omitempty" json:"image,omitempty"`
	// ImagePath is Image resolved at load (computed).
	ImagePath string `yaml:"-" json:"-"`
}

// resolveAvatarPreset reads a deprecated preset as its shape and eyes, on a
// copy (a template's avatar may be shared).
func resolveAvatarPreset(a *BotAvatar) *BotAvatar {
	if a == nil || a.Preset == "" {
		return a
	}
	f, ok := AvatarPresetAliases[a.Preset]
	if !ok {
		return a // validation names it
	}
	cp := *a
	cp.Shape, cp.Eyes, cp.PresetAlias, cp.Preset = f.Shape, f.Eyes, a.Preset, ""
	return &cp
}

// presetWarning is what validate says about a deprecated preset.
func presetWarning(where string, a *BotAvatar) string {
	f := AvatarPresetAliases[a.PresetAlias]
	return fmt.Sprintf("%s: avatar.preset is deprecated — preset %q is { shape: %s, eyes: %s }; write those instead (a color still goes beside them)", where, a.PresetAlias, f.Shape, f.Eyes)
}

// resolveAvatarImage resolves an avatar image inside dir (the config's
// directory, or a template's pack) and checks the file: inside dir (no
// symlink out), a PNG, JPEG or WebP by its content, whole, within the caps.
// It sets ImagePath on a copy and returns it.
func resolveAvatarImage(a *BotAvatar, dir, where string) (*BotAvatar, error) {
	if a == nil || a.Image == "" || a.ImagePath != "" {
		return a, nil
	}
	p, err := packFile(dir, a.Image, "avatar.image")
	if err != nil {
		return nil, fmt.Errorf("%s", strings.Replace(err.Error(), "the pack", where, 1))
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return nil, fmt.Errorf("avatar.image: %w", err)
	}
	if err := avatar.CheckFile(b); err != nil {
		return nil, fmt.Errorf("avatar.image: %s: %w", a.Image, err)
	}
	cp := *a
	cp.ImagePath = p
	return &cp, nil
}

// AvatarImageExts are the file extensions an avatar image may have (its
// content is checked too: the extension never decides).
var AvatarImageExts = []string{".png", ".jpg", ".jpeg", ".webp"}

// checkAvatarImageName refuses an image path that is not a plain relative
// path to a PNG, JPEG or WebP file.
func checkAvatarImageName(p string) error {
	clean := filepath.ToSlash(filepath.Clean(p))
	switch {
	case strings.TrimSpace(p) == "" || filepath.IsAbs(p) || strings.HasPrefix(p, "~") || strings.Contains(p, "://"):
		return fmt.Errorf("avatar.image: %q must be a path relative to the config's directory (e.g. avatars/bot.png) — no URLs", p)
	case clean == ".." || strings.HasPrefix(clean, "../"):
		return fmt.Errorf("avatar.image: %q leaves the config's directory", p)
	case !oneOf(AvatarImageExts, strings.ToLower(filepath.Ext(p))):
		return fmt.Errorf("avatar.image: %q: a PNG, JPEG or WebP file (%s) — not SVG", p, strings.Join(AvatarImageExts, ", "))
	}
	return nil
}

// BotPersonality is a bot's voice: a tone from the list and a short free-text
// style. It shapes HOW a bot writes — never what it may do (the prompt says
// so, below the charter and the rules, inside a fence).
type BotPersonality struct {
	Tone  string `yaml:"tone,omitempty" json:"tone,omitempty"`
	Style string `yaml:"style,omitempty" json:"style,omitempty"`
}

// Identity is a bot's identity fields, as config sets them.
type Identity struct {
	Avatar      *BotAvatar      `yaml:"avatar,omitempty" json:"avatar,omitempty"`
	Color       string          `yaml:"color,omitempty" json:"color,omitempty"`
	DisplayName string          `yaml:"display_name,omitempty" json:"display_name,omitempty"`
	Tagline     string          `yaml:"tagline,omitempty" json:"tagline,omitempty"`
	Personality *BotPersonality `yaml:"personality,omitempty" json:"personality,omitempty"`
}

func oneOf(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func checkName(field, kind, s string, list []string) error {
	if oneOf(list, s) {
		return nil
	}
	plural := kind
	if !strings.HasSuffix(kind, "s") {
		plural += "s"
	}
	return fmt.Errorf("%s: unknown %s %q (%s: %s)", field, kind, s, plural, strings.Join(list, ", "))
}

func checkColor(field, s string) error {
	if oneOf(StatusHues, s) {
		return fmt.Errorf("%s: %q is a status color — identity colors exclude status hues (%s); use one of %s", field, s, strings.Join(StatusHues, ", "), strings.Join(IdentityColors, ", "))
	}
	return checkName(field, "color", s, IdentityColors)
}

// checkLine refuses an empty, multi-line, control-character or overlong text.
func checkLine(field, s string, max int) error {
	if strings.TrimSpace(s) == "" {
		return fmt.Errorf("%s: empty — leave it out instead", field)
	}
	for _, r := range s {
		if unicode.IsControl(r) {
			return fmt.Errorf("%s: one line of text, no control characters", field)
		}
	}
	if n := utf8.RuneCountInString(s); n > max {
		return fmt.Errorf("%s: %d characters, over the cap of %d", field, n, max)
	}
	return nil
}

// Validate checks the identity fields against the lists and caps.
func (id Identity) Validate() error {
	if err := validateAvatar(id.Avatar, id.Color, true); err != nil {
		return err
	}
	if id.Color != "" {
		if err := checkColor("color", id.Color); err != nil {
			return err
		}
	}
	if id.DisplayName != "" {
		if err := checkLine("display_name", id.DisplayName, MaxDisplayName); err != nil {
			return err
		}
	}
	if id.Tagline != "" {
		if err := checkLine("tagline", id.Tagline, MaxTagline); err != nil {
			return err
		}
	}
	if p := id.Personality; p != nil {
		if p.Tone == "" && strings.TrimSpace(p.Style) == "" {
			return fmt.Errorf("personality: set a tone, a style, or both")
		}
		if p.Tone != "" {
			if err := checkName("personality.tone", "tone", p.Tone, PersonalityTones); err != nil {
				return err
			}
		}
		if len(p.Style) > MaxStyle {
			return fmt.Errorf("personality.style: %d bytes, over the cap of %d", len(p.Style), MaxStyle)
		}
		for _, r := range p.Style {
			if unicode.IsControl(r) && r != '\n' && r != '\t' {
				return fmt.Errorf("personality.style: text only, no control characters")
			}
		}
	}
	return nil
}

// validateAvatar checks an avatar: a bot's (allowFace; defaultColor is the
// bot's identity color an icon may take) or a team's — never a face: the
// mascots are for bots, and a team has no color of its own to lend.
func validateAvatar(a *BotAvatar, defaultColor string, allowFace bool) error {
	if a == nil {
		return nil
	}
	if a.Image != "" {
		if err := checkAvatarImageName(a.Image); err != nil {
			return err
		}
	}
	face := a.Shape != "" || a.Eyes != "" || a.Preset != ""
	if face && !allowFace {
		field := "avatar.shape"
		switch {
		case a.Preset != "":
			field = "avatar.preset"
		case a.Shape == "":
			field = "avatar.eyes"
		}
		return fmt.Errorf("%s: faces (mascots) are for bots — a team's avatar is { icon: <name>, color: <name> } or { image: <file> }", field)
	}
	switch {
	case a.Preset != "" && (a.Shape != "" || a.Eyes != ""):
		return fmt.Errorf("avatar.preset is the deprecated spelling of a shape and eyes — set one or the other")
	case face && a.Icon != "":
		return fmt.Errorf("avatar: a face (shape, eyes) or an icon — not both")
	case a.Preset != "":
		if err := checkName("avatar.preset", "preset", a.Preset, AvatarPresetNames()); err != nil {
			return err
		}
	case face:
		if a.Shape == "" {
			return fmt.Errorf("avatar.eyes: a face needs a shape (avatar.shape)")
		}
		if err := checkName("avatar.shape", "shape", a.Shape, AvatarShapes); err != nil {
			return err
		}
		if a.Eyes != "" {
			if err := checkName("avatar.eyes", "eyes", a.Eyes, AvatarEyes); err != nil {
				return err
			}
		}
	case a.Icon != "":
		if err := checkName("avatar.icon", "icon", a.Icon, AvatarIcons); err != nil {
			return err
		}
		if a.Color == "" && defaultColor == "" {
			if !allowFace {
				return fmt.Errorf("avatar: an icon needs a color (avatar.color)")
			}
			return fmt.Errorf("avatar: an icon needs a color (avatar.color, or the bot's color)")
		}
	case a.Image == "":
		if !allowFace {
			return fmt.Errorf("avatar: set icon: <name> with color: <name>, or image: <file>")
		}
		return fmt.Errorf("avatar: set shape: <name> (eyes and color optional), icon: <name> with a color, or image: <file>")
	case a.Color != "":
		return fmt.Errorf("avatar.color colors a face or an icon; an image has its own")
	}
	if a.Color != "" {
		if err := checkColor("avatar.color", a.Color); err != nil {
			return err
		}
	}
	return nil
}

// TeamAvatarView is a team's avatar as `team show` and the dashboard read
// it: always a name and initials (what a client shows with no avatar),
// plus the avatar and its picture's path when set.
func TeamAvatarView(team string, a *BotAvatar) map[string]any {
	out := map[string]any{"name": team, "initials": Initials(team, "")}
	if a != nil {
		out["avatar"] = *a
		if a.Image != "" {
			out["picture"] = "/v1/teams/" + team + "/avatar"
		}
	}
	return out
}

// Merge fills what id leaves unset from fallback (a template's suggestion):
// the operator's identity wins, field by field.
func (id Identity) Merge(fallback Identity) Identity {
	if id.Avatar == nil {
		id.Avatar = fallback.Avatar
	}
	if id.Color == "" {
		id.Color = fallback.Color
	}
	if id.DisplayName == "" {
		id.DisplayName = fallback.DisplayName
	}
	if id.Tagline == "" {
		id.Tagline = fallback.Tagline
	}
	if id.Personality == nil {
		id.Personality = fallback.Personality
	}
	return id
}

// Initials are what a dashboard shows for a bot with no avatar: the first
// letters of its display name's (else its id's) first two words.
func Initials(name, displayName string) string {
	s := displayName
	if strings.TrimSpace(s) == "" {
		s = name
	}
	words := strings.FieldsFunc(s, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
	var out []rune
	for _, w := range words {
		r, _ := utf8.DecodeRuneInString(w)
		out = append(out, unicode.ToUpper(r))
		if len(out) == 2 {
			break
		}
	}
	return string(out)
}

// Attributes are the bot's identity attributes a connector verb can carry
// (pkg/plugin IdentityAttributes): name falls back to the config id, and an
// unset attribute is "".
func (id Identity) Attributes(botID string) map[string]string {
	name := id.DisplayName
	if name == "" {
		name = botID
	}
	out := map[string]string{"name": name, "id": botID, "initials": Initials(botID, id.DisplayName),
		"tagline": id.Tagline, "color": id.Color}
	if a := id.Avatar; a != nil {
		out["avatar_shape"], out["avatar_eyes"], out["avatar_icon"], out["avatar_color"] = a.Shape, a.Eyes, a.Icon, a.Color
		// avatar_preset is deprecated: set only when the face was written
		// as a preset.
		out["avatar_preset"] = a.PresetAlias
		if a.Preset != "" {
			out["avatar_preset"] = a.Preset
		}
		if a.Color == "" && (a.Shape != "" || a.Preset != "" || a.Icon != "") {
			out["avatar_color"] = id.Color // a face takes the bot's color by default
		}
	}
	return out
}

// View is the identity as `bot show` and the dashboard read it. It always
// carries a name and initials, which are what the dashboard shows when
// there is no avatar. from names the template that suggested the identity
// text ("" when the operator wrote it).
func (id Identity) View(botID, from string) map[string]any {
	a := id.Attributes(botID)
	out := map[string]any{"name": a["name"], "initials": a["initials"]}
	if id.DisplayName != "" {
		out["display_name"] = id.DisplayName
	}
	if id.Tagline != "" {
		out["tagline"] = id.Tagline
	}
	if id.Color != "" {
		out["color"] = id.Color
	}
	if id.Avatar != nil {
		out["avatar"] = *id.Avatar
		if id.Avatar.Image != "" {
			// The operator API serves it, re-encoded; elsewhere the face (a
			// face or an icon) or the initials stand in.
			out["picture"] = "/v1/bots/" + botID + "/avatar"
		}
	}
	if id.Personality != nil {
		out["personality"] = *id.Personality
	}
	if from != "" {
		out["suggested_by"] = from
	}
	return out
}

// IdentityOptions are the lists, for the dashboard's pickers.
func IdentityOptions() map[string]any {
	return map[string]any{"shapes": AvatarShapes, "eyes": AvatarEyes, "icons": AvatarIcons, "colors": IdentityColors,
		"tones": PersonalityTones, "status_hues": StatusHues,
		"caps": map[string]int{"display_name": MaxDisplayName, "tagline": MaxTagline, "style": MaxStyle},
		// Deprecated: the shape and eyes each old preset reads as.
		"preset_aliases": AvatarPresetAliases}
}
