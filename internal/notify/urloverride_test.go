package notify

import "testing"

func TestNormalizeNotifiarrURL(t *testing.T) {
	cases := map[string]string{
		"http://sink:8080/notifiarr":        "http://sink:8080/notifiarr/api/v1/notification/passthrough/%s",
		"http://sink:8080/notifiarr/":       "http://sink:8080/notifiarr/api/v1/notification/passthrough/%s",
		"http://sink:8080/custom/%s/path":   "http://sink:8080/custom/%s/path",
		"https://notifiarr.com/api/v1/x/%s": "https://notifiarr.com/api/v1/x/%s",
	}
	for in, want := range cases {
		if got := normalizeNotifiarrURL(in); got != want {
			t.Errorf("normalizeNotifiarrURL(%q) = %q, want %q", in, got, want)
		}
	}
}
