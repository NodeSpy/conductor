package main

import (
	"strings"
	"testing"
)

func TestFolderTree(t *testing.T) {
	es := []folderEntry{
		{"", "workflow", "loose", ""},
		{"work/platform", "team", "platform", "platform services"},
		{"work/platform", "bot", "platform-sre", "team platform"},
		{"work/platform", "trigger", "bot:platform-sre:worker", "on: manual — bot platform-sre"},
		{"work", "workflow", "deploy", "ship it"},
		{"packs/kit", "trigger", "kit/nightly", "on: manual — pack kit"},
		{"personal/home", "bot", "home-helper", ""},
	}
	got := folderTreeText(es, "", false)
	want := strings.Join([]string{
		"workflow loose",
		"packs/",
		"  kit/",
		"    trigger  kit/nightly  — on: manual — pack kit",
		"personal/",
		"  home/",
		"    bot      home-helper",
		"work/",
		"  workflow deploy  — ship it",
		"  platform/",
		"    team     platform  — platform services",
		"    bot      platform-sre  — team platform",
		"    trigger  bot:platform-sre:worker  — on: manual — bot platform-sre",
	}, "\n") + "\n"
	if got != want {
		t.Fatalf("tree:\n%s\nwant:\n%s", got, want)
	}
	// Descendants included; a name prefix is not a folder.
	sub := folderTreeText(es, "work", false)
	if !strings.Contains(sub, "deploy") || !strings.Contains(sub, "platform-sre") || strings.Contains(sub, "home-helper") || strings.Contains(sub, "loose") {
		t.Fatalf("--folder work:\n%s", sub)
	}
	if s := folderTreeText(es, "wor", false); !strings.Contains(s, "nothing in folder wor") {
		t.Fatalf("a name prefix matched: %s", s)
	}
	flat := folderTreeText(es, "work/platform", true)
	if n := strings.Count(flat, "\n"); n != 3 || strings.HasPrefix(flat, " ") {
		t.Fatalf("--flat:\n%s", flat)
	}
}

func TestTakeFolderFlags(t *testing.T) {
	rest, f, flat, err := takeFolderFlags([]string{"ls", "--folder", " work / ops ", "--flat", "--config", "c.yaml"})
	if err != nil || f != "work/ops" || !flat || strings.Join(rest, " ") != "ls --config c.yaml" {
		t.Fatalf("%v %q %v %v", rest, f, flat, err)
	}
	if _, f, _, err := takeFolderFlags([]string{"--folder=a/b"}); err != nil || f != "a/b" {
		t.Fatalf("= form: %q %v", f, err)
	}
	if _, _, _, err := takeFolderFlags([]string{"--folder", "../x"}); err == nil {
		t.Fatal("a bad --folder")
	}
}
