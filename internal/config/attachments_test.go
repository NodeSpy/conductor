package config

import (
	"strings"
	"testing"
	"time"
)

// Every attachment cap is on by default; a knob loosens or tightens one.
func TestAttachmentLimitsDefaults(t *testing.T) {
	var l AttachmentLimits
	if l.MaxFileOr() != 10<<20 || l.MaxMessageOr() != 25<<20 || l.MaxFilesOr() != 10 || l.TTLOr() != 7*24*time.Hour ||
		strings.Join(l.TypesOr(), ",") != strings.Join(AttachmentTypes, ",") {
		t.Fatalf("defaults: %d %d %d %s %v", l.MaxFileOr(), l.MaxMessageOr(), l.MaxFilesOr(), l.TTLOr(), l.TypesOr())
	}
	for _, typ := range []string{"image/png", "text/plain", "application/pdf"} {
		if !l.AllowsType(typ) {
			t.Errorf("default refuses %s", typ)
		}
	}
	for _, typ := range []string{"text/html", "image/svg+xml", "application/zip", "application/octet-stream", "application/x-sh", ""} {
		if l.AllowsType(typ) {
			t.Errorf("default allows %s", typ)
		}
	}
	w := AttachmentLimits{Types: []string{"image/*", "text/csv"}}
	if !w.AllowsType("image/webp") || !w.AllowsType("text/csv") || w.AllowsType("text/plain") || w.AllowsType("imagex/png") {
		t.Fatal("wildcard types")
	}
}

func TestAttachmentLimitsValidate(t *testing.T) {
	for body, want := range map[string]string{
		"{ max_file: 5MB, max_message: 50MB, max_files: 3, ttl: 24h, types: [image/*, text/plain] }": "",
		"{ off: true }":                              "",
		"{ max_file: 200MB }":                        "attachments.max_file: want 1 to 104857600 bytes",
		"{ max_files: 51 }":                          "attachments.max_files: want 1 to 50",
		"{ max_file: 10MB, max_message: 5MB }":       "attachments.max_message (5242880) is below max_file (10485760)",
		"{ types: [png] }":                           `attachments.types: "png" is not a media type`,
		"{ types: [\"*/*\"] }":                       `"*/*" is not a media type`,
		"{ types: [\"image/p*\"] }":                  `"image/p*" is not a media type`,
		"{ types: [\"text/plain; charset=utf-8\"] }": "is not a media type",
	} {
		_, err := loadBots(t, "bots:\n  sre:\n    attachments: "+body+"\n")
		switch {
		case want == "" && err != nil:
			t.Errorf("%s: %v", body, err)
		case want != "" && (err == nil || !strings.Contains(err.Error(), want) || !strings.Contains(err.Error(), "sre")):
			t.Errorf("%s: got %v, want %q", body, err, want)
		}
	}
	if _, err := loadBots(t, "bots:\n  sre:\n    attachments: { max_size: 1MB }\n"); err == nil || !strings.Contains(err.Error(), "max_size") {
		t.Fatalf("an unknown knob: %v", err)
	}
	c, err := loadBots(t, "bots:\n  sre:\n    attachments: { max_file: 2MB, ttl: 1h }\n")
	if err != nil {
		t.Fatal(err)
	}
	if l := c.Bots["sre"].Attachments; l.MaxFileOr() != 2<<20 || l.TTLOr() != time.Hour || l.MaxFilesOr() != 10 {
		t.Fatalf("knobs: %+v", l)
	}
	if !strings.HasSuffix(c.BotAttachmentsDir("sre"), "/bots/sre/attachments") || strings.HasPrefix(c.BotAttachmentsDir("sre"), c.BotRepoDir("sre")) {
		t.Fatalf("attachments dir %s", c.BotAttachmentsDir("sre"))
	}
}
