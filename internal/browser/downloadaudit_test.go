package browser

import (
	"strings"
	"testing"
)

// The download audit path builds and logs its own entry (it does not go through
// doWith, where M3's scrubEntry runs). A page that reflects a filled credential
// into the download link's PATH — which redactURL does not strip — would land
// that secret verbatim in the audit log. Route the download audit through the
// scrub too. Regression guard for #5.
func TestDownloadAuditScrubsReflectedSecret(t *testing.T) {
	var got map[string]any
	m := &Manager{
		Settings: func() Settings { return Settings{} },
		Audit:    func(e map[string]any) { got = e },
	}
	in := &Instance{proc: &Process{done: make(chan struct{})}, dir: t.TempDir()}
	const secret = "s3cr3tP@ssw0rd"
	in.remember(1, secret) // a filled credential

	// The page reflected the filled secret into the link's path.
	d := &Download{GUID: "g1", URL: "https://site.example/export/" + secret + "/report.pdf", Name: "report.pdf"}

	m.landDownload(in, Principal{Identity: "agent:a@home"}, true, d)

	if got == nil {
		t.Fatal("no audit entry was recorded")
	}
	url, _ := got["url"].(string)
	if strings.Contains(url, secret) {
		t.Fatalf("the download audit url leaked a page-reflected secret: %q", url)
	}
	if !strings.Contains(url, Redacted) {
		t.Fatalf("the reflected secret in the url was not redacted: %q", url)
	}
}
