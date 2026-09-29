package dispatch

import (
	"strings"
	"testing"
)

// The unjailed identity guidance is today's text, byte for byte: jailing
// swaps it for JailedIdentityGuidance, it never edits it.
func TestWriteWrapperGuidanceUnchanged(t *testing.T) {
	const want = "\n\n---\n" +
		"IDENTITY: you act as ME. GH_TOKEN/GITHUB_TOKEN are MY token, so every comment, " +
		"review, reply, and `gh`/API write is attributed to me — and commits and `git push` " +
		"go over SSH as me. NEVER post, submit, approve, or otherwise write anything with the " +
		"App/bot token. If a large read would burn my rate limit you MAY read (only) with the " +
		"App token via `GH_TOKEN=$PC_GH_APP_TOKEN gh ...`, but never write with it.\n" +
		"SCOPE: your writes are bound to THIS target — its PR or issue, and its branch. " +
		"If the PR is merged or closed, stop and report that; do not push a new branch, " +
		"open a new PR or issue, or write to any other PR. conductor refuses such writes."
	if WriteWrapperGuidance != want {
		t.Fatalf("unjailed identity guidance changed:\n%s", WriteWrapperGuidance)
	}
}

// ForJail swaps exactly the identity block; the jailed block names no token
// variable and no SSH, and a prompt without the block is untouched.
func TestForJailSwapsOnlyTheIdentityBlock(t *testing.T) {
	p := "task text" + WriteWrapperGuidance + "\n\nmore guidance"
	got := ForJail(p)
	if got != "task text"+JailedIdentityGuidance+"\n\nmore guidance" {
		t.Fatalf("ForJail:\n%s", got)
	}
	for _, bad := range []string{"TOKEN", "SSH", "ssh", "$"} {
		if strings.Contains(JailedIdentityGuidance, bad) {
			t.Errorf("jailed identity guidance mentions %q", bad)
		}
	}
	if ForJail("plain prompt") != "plain prompt" {
		t.Fatal("a prompt without the identity block is unchanged")
	}
}
