package github

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/NodeSpy/paseo-conductor/internal/core"
)

// Start builds the App auth + REST client and streams webhook deliveries from
// the smee channel, translating each into Triggers.
func (g *Integration) Start(ctx context.Context, emit core.EmitFunc) error {
	if err := g.ensureClients(); err != nil {
		return err
	}

	if g.cfg.App.Verify() {
		log.Printf("github[%s]: signature verification ON — note the smee re-serialization caveat; "+
			"set verify_signature:false if valid deliveries are being dropped", g.name)
	}

	if g.cfg.Sweep.Enabled {
		go g.sweepLoop(ctx, emit)
	}

	seen := newDeliveryDedup(2048)
	backoff := time.Second
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		err := g.streamSmee(ctx, emit, seen)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		log.Printf("github[%s]: smee stream ended (%v); reconnecting in %s", g.name, err, backoff)
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

// smeePayload is one forwarded webhook as delivered over the smee SSE channel.
type smeePayload struct {
	Event    string          `json:"x-github-event"`
	Delivery string          `json:"x-github-delivery"`
	Sig      string          `json:"x-hub-signature-256"`
	Body     json.RawMessage `json:"body"`
}

func (g *Integration) streamSmee(ctx context.Context, emit core.EmitFunc, seen *deliveryDedup) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, g.cfg.Webhook.SmeeURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "text/event-stream")
	resp, err := http.DefaultClient.Do(req)
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
				g.handleSmeeData(ctx, emit, seen, data.String())
				data.Reset()
			}
		case strings.HasPrefix(line, "data:"):
			data.WriteString(strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
		default:
			// ignore event:/id:/retry: and comments
		}
	}
	if err := sc.Err(); err != nil {
		return err
	}
	return fmt.Errorf("stream closed")
}

func (g *Integration) handleSmeeData(ctx context.Context, emit core.EmitFunc, seen *deliveryDedup, data string) {
	var p smeePayload
	if err := json.Unmarshal([]byte(data), &p); err != nil {
		return // control frame ("ready", ping, etc.)
	}
	if p.Event == "" || len(p.Body) == 0 {
		return
	}
	if p.Delivery != "" && !seen.add(p.Delivery) {
		return // duplicate redelivery on reconnect
	}
	if g.cfg.App.Verify() {
		if !verifySignature(g.cfg.App.WebhookSecret, p.Body, p.Sig) {
			log.Printf("github[%s]: signature mismatch for delivery %s (dropped) — "+
				"if this repeats over smee, set verify_signature:false", g.name, p.Delivery)
			return
		}
	}
	for _, t := range g.triggersFor(ctx, p.Event, p.Body) {
		emit(ctx, t)
	}
}

// deliveryDedup is a bounded set of recently-seen delivery ids.
type deliveryDedup struct {
	mu   sync.Mutex
	max  int
	set  map[string]struct{}
	ring []string
}

func newDeliveryDedup(max int) *deliveryDedup {
	return &deliveryDedup{max: max, set: map[string]struct{}{}}
}

// add records id and reports true if it was new.
func (d *deliveryDedup) add(id string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	if _, ok := d.set[id]; ok {
		return false
	}
	d.set[id] = struct{}{}
	d.ring = append(d.ring, id)
	if len(d.ring) > d.max {
		old := d.ring[0]
		d.ring = d.ring[1:]
		delete(d.set, old)
	}
	return true
}
