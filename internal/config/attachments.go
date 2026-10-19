package config

import (
	"fmt"
	"strings"
	"time"
)

// ATTACHMENTS (docs/wiki/Bots.md#attachments): files an operator (or a chat
// user, through a connector) sends a bot with a message. They live in
// conductor's state, never in the bot's memory repo: a bot keeps what it
// needs with `conductor bot remember`. Every cap is ON by default; a bigger
// value loosens one:
//
//	bots:
//	  platform-sre:
//	    attachments:
//	      max_file: 10MB          # one file
//	      max_message: 25MB       # all of one message's files
//	      max_files: 10           # files in one message
//	      types: [image/*, text/plain, application/pdf]   # default: AttachmentTypes
//	      ttl: 168h               # removed this long after arriving, if their case is still open
type AttachmentLimits struct {
	MaxFile    ByteSize `yaml:"max_file,omitempty"`
	MaxMessage ByteSize `yaml:"max_message,omitempty"`
	MaxFiles   int      `yaml:"max_files,omitempty"`
	// Types are the media types accepted, by exact type or `<type>/*`. A
	// file's type is what conductor detects from its content and name, never
	// what the sender claims.
	Types []string `yaml:"types,omitempty"`
	// TTL removes an attachment this long after it arrived, whatever its
	// case. Closing the case removes it sooner.
	TTL Duration `yaml:"ttl,omitempty"`
	// Off refuses attachments for this bot.
	Off bool `yaml:"off,omitempty"`
}

// Attachment defaults and ceilings.
const (
	DefaultAttachMaxFile    = 10 << 20
	DefaultAttachMaxMessage = 25 << 20
	DefaultAttachMaxFiles   = 10
	DefaultAttachTTL        = 7 * 24 * time.Hour
	// MaxAttachFile and MaxAttachFiles bound the knobs: an attachment is a
	// file someone sends with a message, not a bulk upload.
	MaxAttachFile  = 100 << 20
	MaxAttachFiles = 50
)

// AttachmentTypes is the default allowlist: images a model can look at, and
// text-like files and PDFs it can read.
var AttachmentTypes = []string{
	"image/png", "image/jpeg", "image/gif", "image/webp",
	"text/plain", "text/markdown", "text/csv", "application/json", "application/yaml", "application/pdf",
}

// MaxFileOr, MaxMessageOr, MaxFilesOr, TTLOr and TypesOr are the effective
// limits.
func (l AttachmentLimits) MaxFileOr() int64 {
	if l.MaxFile > 0 {
		return int64(l.MaxFile)
	}
	return DefaultAttachMaxFile
}

func (l AttachmentLimits) MaxMessageOr() int64 {
	if l.MaxMessage > 0 {
		return int64(l.MaxMessage)
	}
	return DefaultAttachMaxMessage
}

func (l AttachmentLimits) MaxFilesOr() int {
	if l.MaxFiles > 0 {
		return l.MaxFiles
	}
	return DefaultAttachMaxFiles
}

func (l AttachmentLimits) TTLOr() time.Duration {
	if l.TTL > 0 {
		return time.Duration(l.TTL)
	}
	return DefaultAttachTTL
}

func (l AttachmentLimits) TypesOr() []string {
	if len(l.Types) > 0 {
		return l.Types
	}
	return AttachmentTypes
}

// AllowsType reports whether a detected media type is on the allowlist.
func (l AttachmentLimits) AllowsType(mediaType string) bool {
	for _, t := range l.TypesOr() {
		if t == mediaType {
			return true
		}
		if strings.HasSuffix(t, "/*") && strings.HasPrefix(mediaType, strings.TrimSuffix(t, "*")) {
			return true
		}
	}
	return false
}

func (l AttachmentLimits) validate() error {
	if l.MaxFile < 0 || l.MaxFile > MaxAttachFile {
		return fmt.Errorf("max_file: want 1 to %d bytes, got %d", MaxAttachFile, l.MaxFile)
	}
	if l.MaxMessage < 0 || l.MaxMessage > MaxAttachFile*MaxAttachFiles {
		return fmt.Errorf("max_message: want 1 to %d bytes, got %d", int64(MaxAttachFile)*MaxAttachFiles, l.MaxMessage)
	}
	if l.MaxFiles < 0 || l.MaxFiles > MaxAttachFiles {
		return fmt.Errorf("max_files: want 1 to %d, got %d", MaxAttachFiles, l.MaxFiles)
	}
	if l.TTL < 0 {
		return fmt.Errorf("ttl: must be positive")
	}
	if l.MaxFile > 0 && l.MaxMessage > 0 && l.MaxMessage < l.MaxFile {
		return fmt.Errorf("max_message (%d) is below max_file (%d)", l.MaxMessage, l.MaxFile)
	}
	for _, t := range l.Types {
		typ, sub, ok := strings.Cut(t, "/")
		if !ok || typ == "" || sub == "" || typ == "*" || strings.ContainsAny(t, " ;,") || (strings.Contains(sub, "*") && sub != "*") {
			return fmt.Errorf("types: %q is not a media type (type/subtype, or type/*)", t)
		}
	}
	return nil
}
