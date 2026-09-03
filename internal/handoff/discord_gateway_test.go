package handoff

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func noopLog(string, ...any) {}

func TestHandleDiscordFrameHello(t *testing.T) {
	gs := &discordGatewayState{}
	action, hbMS := handleDiscordFrame(gs, []byte(`{"op":10,"d":{"heartbeat_interval":41250}}`), NewInbox(), noopLog)
	if action != discordActionIdentify {
		t.Fatalf("HELLO should trigger discordActionIdentify, got %v", action)
	}
	if hbMS != 41250 {
		t.Fatalf("expected heartbeat_interval 41250, got %d", hbMS)
	}
}

func TestHandleDiscordFrameReady(t *testing.T) {
	gs := &discordGatewayState{}
	action, _ := handleDiscordFrame(gs, []byte(`{"op":0,"t":"READY","s":1,"d":{"user":{"id":"SELF1"}}}`), NewInbox(), noopLog)
	if action != discordActionNone {
		t.Fatalf("READY should not request an action, got %v", action)
	}
	if gs.selfID != "SELF1" {
		t.Fatalf("READY should capture the bot's own user id, got %q", gs.selfID)
	}
	if gs.seq == nil || *gs.seq != 1 {
		t.Fatalf("expected sequence to be tracked as 1, got %v", gs.seq)
	}
}

func TestHandleDiscordFrameMessageCreateFromAnotherUserDelivers(t *testing.T) {
	gs := &discordGatewayState{selfID: "SELF1"}
	inbox := NewInbox()
	pend := inbox.register("C123", "")

	raw := []byte(`{"op":0,"t":"MESSAGE_CREATE","s":2,"d":{"channel_id":"C123","content":"approve","author":{"id":"U999","bot":false}}}`)
	action, _ := handleDiscordFrame(gs, raw, inbox, noopLog)
	if action != discordActionNone {
		t.Fatalf("MESSAGE_CREATE should not request a connection action, got %v", action)
	}
	select {
	case d := <-pend.done:
		if d.Action != ActionApprove {
			t.Fatalf("expected an approve decision, got %+v", d)
		}
	default:
		t.Fatal("a MESSAGE_CREATE from another user should deliver to the pending hand-off")
	}
}

func TestHandleDiscordFrameIgnoresOwnMessageByID(t *testing.T) {
	gs := &discordGatewayState{selfID: "SELF1"}
	inbox := NewInbox()
	pend := inbox.register("C123", "")

	raw := []byte(`{"op":0,"t":"MESSAGE_CREATE","d":{"channel_id":"C123","content":"the posted draft","author":{"id":"SELF1","bot":false}}}`)
	handleDiscordFrame(gs, raw, inbox, noopLog)
	select {
	case d := <-pend.done:
		t.Fatalf("the gateway's own message (matching selfID) must not resolve its own hand-off, got %+v", d)
	default:
		// expected: nothing delivered
	}
}

func TestHandleDiscordFrameIgnoresBotMessages(t *testing.T) {
	gs := &discordGatewayState{selfID: "SELF1"}
	inbox := NewInbox()
	pend := inbox.register("C123", "")

	// A different bot account (author.bot true, different id) must also be
	// ignored — not just the gateway's own id.
	raw := []byte(`{"op":0,"t":"MESSAGE_CREATE","d":{"channel_id":"C123","content":"beep boop","author":{"id":"OTHERBOT","bot":true}}}`)
	handleDiscordFrame(gs, raw, inbox, noopLog)
	select {
	case d := <-pend.done:
		t.Fatalf("a bot message must not resolve a pending hand-off, got %+v", d)
	default:
		// expected: nothing delivered
	}
}

func TestHandleDiscordFrameReconnectOps(t *testing.T) {
	for _, op := range []int{7, 9} {
		gs := &discordGatewayState{}
		action, _ := handleDiscordFrame(gs, []byte(fmt.Sprintf(`{"op":%d}`, op)), NewInbox(), noopLog)
		if action != discordActionReconnect {
			t.Fatalf("op %d should trigger discordActionReconnect, got %v", op, action)
		}
	}
}

func TestHandleDiscordFrameMalformedIsIgnored(t *testing.T) {
	gs := &discordGatewayState{}
	action, _ := handleDiscordFrame(gs, []byte(`not json`), NewInbox(), noopLog)
	if action != discordActionNone {
		t.Fatalf("malformed frame should be a no-op, got %v", action)
	}
}

func TestHandleDiscordFrameTracksSequence(t *testing.T) {
	gs := &discordGatewayState{}
	handleDiscordFrame(gs, []byte(`{"op":11,"s":5}`), NewInbox(), noopLog) // heartbeat ACK
	if gs.seq == nil || *gs.seq != 5 {
		t.Fatalf("expected sequence 5, got %v", gs.seq)
	}
	handleDiscordFrame(gs, []byte(`{"op":11,"s":6}`), NewInbox(), noopLog)
	if gs.seq == nil || *gs.seq != 6 {
		t.Fatalf("expected sequence to advance to 6, got %v", gs.seq)
	}
}

func TestFetchDiscordGatewayURL(t *testing.T) {
	var gotAuth, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotPath = r.URL.Path
		fmt.Fprint(w, `{"url":"wss://gateway.example.test"}`)
	}))
	defer srv.Close()
	defer setDiscordAPIURL(srv.URL)()

	url, err := fetchDiscordGatewayURL(context.Background(), "bot-secret")
	if err != nil {
		t.Fatal(err)
	}
	if url != "wss://gateway.example.test?v=10&encoding=json" {
		t.Fatalf("unexpected gateway url %q", url)
	}
	if gotPath != "/gateway/bot" {
		t.Fatalf("unexpected path %q", gotPath)
	}
	if gotAuth != "Bot bot-secret" {
		t.Fatalf("unexpected Authorization header %q", gotAuth)
	}
}

func TestFetchDiscordGatewayURLError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()
	defer setDiscordAPIURL(srv.URL)()

	if _, err := fetchDiscordGatewayURL(context.Background(), "bad-token"); err == nil {
		t.Fatal("expected an error on a non-2xx gateway/bot response")
	}
}
