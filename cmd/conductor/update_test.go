package main

import "testing"

// A 200 response with headers + JSON body: status, ETag, and tag_name all parse.
func TestParseHTTPResponse200(t *testing.T) {
	out := "HTTP/2.0 200 OK\r\n" +
		"Content-Type: application/json\r\n" +
		"Etag: W/\"abc123\"\r\n" +
		"X-Ratelimit-Remaining: 4999\r\n" +
		"\r\n" +
		"{\"tag_name\":\"v0.5.24\",\"name\":\"v0.5.24\"}\n"
	status, etag, body := parseHTTPResponse(out)
	if status != 200 {
		t.Fatalf("status = %d, want 200", status)
	}
	if etag != `W/"abc123"` {
		t.Fatalf("etag = %q, want W/\"abc123\"", etag)
	}
	if want := `{"tag_name":"v0.5.24","name":"v0.5.24"}`; body != want+"\n" && body != want {
		t.Fatalf("body = %q, want the JSON payload", body)
	}
}

// A 304 (gh appends a "gh: HTTP 304" line to stderr in the combined output) parses
// to status 304 — the cheap "nothing new" path.
func TestParseHTTPResponse304(t *testing.T) {
	out := "HTTP/2.0 304 Not Modified\r\n" +
		"Etag: \"abc123\"\r\n" +
		"\r\n" +
		"gh: HTTP 304\n"
	status, _, _ := parseHTTPResponse(out)
	if status != 304 {
		t.Fatalf("status = %d, want 304", status)
	}
}

// Non-HTTP output (gh failed to run at all) parses to status 0 so the checker can
// report an error rather than misread it as a release.
func TestParseHTTPResponseNoHTTP(t *testing.T) {
	if status, _, _ := parseHTTPResponse("could not connect\n"); status != 0 {
		t.Fatalf("status = %d, want 0 for non-HTTP output", status)
	}
}

func TestNewerRelease(t *testing.T) {
	cases := []struct {
		name    string
		tag     string
		changed bool
		running string
		want    bool
	}{
		{"304 nothing new", "", false, "v0.5.23", false},
		{"changed but same tag", "v0.5.23", true, "v0.5.23", false},
		{"changed and newer", "v0.5.24", true, "v0.5.23", true},
		{"changed but empty tag", "", true, "v0.5.23", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := newerRelease(c.tag, c.changed, c.running); got != c.want {
				t.Fatalf("newerRelease(%q,%v,%q) = %v, want %v", c.tag, c.changed, c.running, got, c.want)
			}
		})
	}
}
