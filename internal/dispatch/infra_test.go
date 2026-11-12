package dispatch

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestClassifyReplyRecognisesRuntimeErrors(t *testing.T) {
	for _, reply := range []string{
		"",
		"   \n",
		"API Error: Unable to connect to API: SSL certificate verification failed",
		"API Error: 529 {\"type\":\"error\"}"[:14] + " overloaded",
		"Error: connect ECONNREFUSED 127.0.0.1:443",
		"Failed to authenticate. API Error: 401 invalid x-api-key",
		"API Error: Request rejected (429) · rate limit reached",
		"Error: getaddrinfo EAI_AGAIN api.example.com",
		"Request failed: tls: handshake failure",
	} {
		if ClassifyReply(reply) == nil {
			t.Errorf("not classified as an infrastructure failure: %q", reply)
		}
	}
}

// A false positive re-runs a step, so an answer must never read as one.
func TestClassifyReplyLeavesAnswersAlone(t *testing.T) {
	for _, reply := range []string{
		`{"verdict":"error: the API rate limit handling is wrong"}`,
		"The change looks fine. One nit: the retry on a 429 rate limit should back off.",
		"I fixed the TLS certificate loading and pushed.",
		"Error handling in the parser is missing a case; see line 40.",
		"API Error: 400 prompt is too long",
		strings.Repeat("Error: connection refused while running the tests. ", 40),
		"Failed\n1\n2\n3\n4\n5\n6\n7\n8\n9 connection refused",
	} {
		if ie := ClassifyReply(reply); ie != nil {
			t.Errorf("an answer was classified as an infrastructure failure (%s): %q", ie.Reason, reply)
		}
	}
}

func TestExcerptMasksCredentialsAndClips(t *testing.T) {
	got := Excerpt("Error: 401 Authorization: Bearer sk-abc123def456 api_key=supersecret token: hunter2 https://user:pw@host/x")
	for _, leak := range []string{"sk-abc123def456", "supersecret", "hunter2", ":pw@"} {
		if strings.Contains(got, leak) {
			t.Fatalf("excerpt leaks %q: %s", leak, got)
		}
	}
	if long := Excerpt(strings.Repeat("é", 1000)); len(long) > maxExcerpt+len("…") || !strings.HasSuffix(long, "…") {
		t.Fatalf("not clipped: %d bytes", len(long))
	}
}

var verdictSchema = map[string]any{"type": "object", "required": []any{"verdict"},
	"properties": map[string]any{"verdict": map[string]any{"type": "string"}}}

// The incident: the runtime printed its error instead of an answer. That is
// an infrastructure failure carrying the reply — and the corrective turn is
// not spent on it.
func TestEnforceSchemaInfraReplySkipsCorrectiveTurn(t *testing.T) {
	retried := false
	retry := func(context.Context, string) (string, error) { retried = true; return "", nil }
	_, err := EnforceSchema(context.Background(), verdictSchema, "p",
		"API Error: Unable to connect to API: SSL certificate verification failed", retry)
	if !IsInfra(err) || retried {
		t.Fatalf("err = %v (infra %v), corrective turn spent %v", err, IsInfra(err), retried)
	}
	if !strings.Contains(err.Error(), "SSL certificate verification failed") {
		t.Fatalf("the runtime's reply is not on the error: %v", err)
	}
	// The corrective turn's own reply can be the runtime's error too.
	_, err = EnforceSchema(context.Background(), verdictSchema, "p", "Sure! Here you go.",
		func(context.Context, string) (string, error) { return "", nil })
	if !IsInfra(err) {
		t.Fatalf("an empty corrective reply = %v, want an infrastructure failure", err)
	}
}

// A real miss still gets its corrective turn, and its error keeps what the
// agent actually said.
func TestEnforceSchemaMissKeepsTheReply(t *testing.T) {
	_, err := EnforceSchema(context.Background(), verdictSchema, "p", "I would rather not answer in JSON.",
		func(context.Context, string) (string, error) { return "Still prose, sorry.", nil })
	if err == nil || IsInfra(err) || !strings.Contains(err.Error(), "the agent replied: Still prose, sorry.") {
		t.Fatalf("err = %v", err)
	}
}

func TestInfraErrorWrapsItsCause(t *testing.T) {
	cause := Unrecoverable(errors.New("launcher missing"))
	err := Infra("could not start", "", cause)
	if !IsInfra(err) || !IsUnrecoverable(err) {
		t.Fatalf("infra %v unrecoverable %v", IsInfra(err), IsUnrecoverable(err))
	}
}
