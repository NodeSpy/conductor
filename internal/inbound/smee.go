package inbound

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"
)

// smeeClient streams a smee channel with tuned TCP keep-alive, so a half-open
// connection (a network blip with no clean close) is surfaced in ~80s instead of
// waiting ~15 min for the kernel's default retransmit timeout. Keep-alive probes
// only error when they actually fail, so a healthy-but-quiet channel is never
// reconnected. No Client.Timeout — this is a long-lived stream. Mirrors the github
// integration's streaming client.
var smeeClient = func() *http.Client {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.DialContext = (&net.Dialer{
		Timeout:   30 * time.Second,
		KeepAlive: 20 * time.Second,
		KeepAliveConfig: net.KeepAliveConfig{
			Enable: true, Idle: 20 * time.Second, Interval: 15 * time.Second, Count: 4,
		},
	}).DialContext
	return &http.Client{Transport: tr}
}()

// Frame is one delivery pulled off a smee channel or received over HTTP: the raw
// body bytes plus the request headers (lower-cased keys). smee.io re-serializes
// JSON bodies, so an HMAC computed over Body may not match a signature the origin
// computed over its exact bytes — the same caveat the github integration documents
// for its smee transport. Use a direct listener when byte-exact verification matters.
type Frame struct {
	Headers map[string]string
	Body    []byte
}

// Header returns a header value case-insensitively.
func (f Frame) Header(name string) string { return f.Headers[strings.ToLower(name)] }

// Smee streams a smee.io channel, delivering each forwarded request to onFrame and
// reconnecting with capped exponential backoff until ctx is cancelled. It mirrors
// the github integration's runSmee/streamSmee loop but yields generic Frames rather
// than github-specific payloads.
func Smee(ctx context.Context, url string, logf func(string, ...any), onFrame func(Frame)) error {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	backoff := time.Second
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		err := streamSmee(ctx, url, onFrame)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		logf("inbound: smee stream ended (%v); reconnecting in %s", err, backoff)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(backoff):
		}
		if backoff < 30*time.Second {
			backoff *= 2
		}
	}
}

func streamSmee(ctx context.Context, url string, onFrame func(Frame)) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "text/event-stream")
	resp, err := smeeClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("smee connect: HTTP %d", resp.StatusCode)
	}

	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	var data strings.Builder
	for sc.Scan() {
		line := sc.Text()
		switch {
		case line == "":
			if data.Len() > 0 {
				if f, ok := parseSmeeFrame(data.String()); ok {
					onFrame(f)
				}
				data.Reset()
			}
		case strings.HasPrefix(line, "data:"):
			data.WriteString(strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
		default:
			// ignore event:/id:/retry: lines and comments
		}
	}
	if err := sc.Err(); err != nil {
		return err
	}
	return fmt.Errorf("stream closed")
}

// parseSmeeFrame turns one SSE data payload into a Frame. smee forwards the
// original request as a JSON object whose scalar top-level keys are the headers
// (host, content-type, x-*) plus a `body` field carrying the (re-serialized)
// payload. Control frames ("ready"/ping) that don't carry a body are skipped.
func parseSmeeFrame(data string) (Frame, bool) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(data), &raw); err != nil {
		return Frame{}, false
	}
	rawBody, ok := raw["body"]
	if !ok {
		return Frame{}, false
	}
	// body may be a JSON object/array (re-serialized as-is) or a JSON string.
	body := []byte(rawBody)
	var asString string
	if json.Unmarshal(rawBody, &asString) == nil {
		body = []byte(asString)
	}
	headers := map[string]string{}
	for k, v := range raw {
		if k == "body" || k == "query" {
			continue
		}
		var s string
		if json.Unmarshal(v, &s) == nil {
			headers[strings.ToLower(k)] = s
		}
	}
	return Frame{Headers: headers, Body: body}, true
}
