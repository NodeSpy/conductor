package sandbox

import (
	"strings"
	"testing"
)

func TestFilterEnv(t *testing.T) {
	in := []string{
		"PATH=/usr/bin",
		"HOME=/home/x",
		"LC_ALL=C",
		"JIRA_TOKEN=secret",          // dropped: a credential
		"GH_WEBHOOK_SECRET=hush",     // dropped: a credential
		"AWS_SECRET_ACCESS_KEY=nope", // dropped
		"malformed-no-equals",        // dropped: not KEY=VALUE
	}
	got := strings.Join(FilterEnv(in), "\n")
	for _, keep := range []string{"PATH=/usr/bin", "HOME=/home/x", "LC_ALL=C"} {
		if !strings.Contains(got, keep) {
			t.Errorf("expected to keep %q\n%s", keep, got)
		}
	}
	for _, drop := range []string{"JIRA_TOKEN", "GH_WEBHOOK_SECRET", "AWS_SECRET_ACCESS_KEY", "malformed"} {
		if strings.Contains(got, drop) {
			t.Errorf("expected to drop %q\n%s", drop, got)
		}
	}
}
