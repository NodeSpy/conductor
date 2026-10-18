package config

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

// BOT IDENTITY (docs/wiki/Bots.md#identity): what makes a bot recognizable —
// an avatar, a color, a display name, a tagline and a personality. All of it
// is presentation: none of it changes what a bot may do.
//
// THE NAME LISTS BELOW ARE THE ONE SOURCE OF TRUTH: validation, the
// catalog, `conductor bot show` and the dashboard API all read them. They are
// placeholders until the dashboard's options are settled — changing a list is
// a one-file change. No URLs or files: an avatar is a name from a list, which
// keeps a config hermetic and safe to publish.

// AvatarPresets are the precanned avatars (`avatar: { preset: … }`).
var AvatarPresets = []string{"robot", "owl", "fox", "octopus", "lighthouse", "satellite", "compass", "anvil"}

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

// BotAvatar is a precanned avatar, or an icon with a color — exactly one.
type BotAvatar struct {
	Preset string `yaml:"preset,omitempty" json:"preset,omitempty"`
	Icon   string `yaml:"icon,omitempty" json:"icon,omitempty"`
	Color  string `yaml:"color,omitempty" json:"color,omitempty"`
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
	return fmt.Errorf("%s: unknown %s %q (%ss: %s)", field, kind, s, kind, strings.Join(list, ", "))
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
	if a := id.Avatar; a != nil {
		switch {
		case a.Preset != "" && (a.Icon != "" || a.Color != ""):
			return fmt.Errorf("avatar: a preset, or an icon with a color — not both")
		case a.Preset != "":
			if err := checkName("avatar.preset", "preset", a.Preset, AvatarPresets); err != nil {
				return err
			}
		case a.Icon == "" || a.Color == "":
			return fmt.Errorf("avatar: set preset: <name>, or icon: <name> with color: <name>")
		default:
			if err := checkName("avatar.icon", "icon", a.Icon, AvatarIcons); err != nil {
				return err
			}
			if err := checkColor("avatar.color", a.Color); err != nil {
				return err
			}
		}
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
		out["avatar_preset"], out["avatar_icon"], out["avatar_color"] = a.Preset, a.Icon, a.Color
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
	return map[string]any{"presets": AvatarPresets, "icons": AvatarIcons, "colors": IdentityColors,
		"tones": PersonalityTones, "status_hues": StatusHues,
		"caps": map[string]int{"display_name": MaxDisplayName, "tagline": MaxTagline, "style": MaxStyle}}
}
