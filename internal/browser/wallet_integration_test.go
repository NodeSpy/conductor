//go:build linux || darwin

package browser

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/NodeSpy/conductor/internal/browser/browsertest"
	"github.com/NodeSpy/conductor/internal/config"
)

// The profile's and card's values: none may reach a reply, the audit or
// the workspace (values under 4 characters — a region or country code, the
// CVC — are hidden by their field, not scrubbed from text).
var walletSecrets = []string{"Jane Q Tester", "jane.private@example.test", "+1 555 0100 777", "1 Hidden Lane", "Springfield",
	"62704", "Acme Hidden Corp", testCard, "4242 4242 4242 4242", "12/34"}

type walletRig struct {
	*rig
	shop, other *browsertest.Shop
	a, b        string // the card's merchant; another shop
	shared      []string
}

func newWalletRig(t *testing.T) *walletRig {
	r := newRig(t)
	shop, other := browsertest.NewShop(), browsertest.NewShop()
	sa, sb := httptest.NewServer(shop), httptest.NewServer(other)
	t.Cleanup(sa.Close)
	t.Cleanup(sb.Close)
	_, pa, _ := strings.Cut(strings.TrimPrefix(sa.URL, "http://"), ":")
	hb := strings.TrimPrefix(sb.URL, "http://")
	w := &walletRig{rig: r, shop: shop, other: other, a: "http://localhost:" + pa, b: "http://" + hb}
	r.st.Allow = append(r.st.Allow, "localhost:"+pa, hb)
	v := &fakeVault{m: map[string]string{
		"p/name": "Jane Q Tester", "p/email": "jane.private@example.test", "p/phone": "+1 555 0100 777", "p/line1": "1 Hidden Lane",
		"p/city": "Springfield", "p/region": "IL", "p/zip": "62704", "p/country": "US", "p/company": "Acme Hidden Corp",
		"c/number": testCard, "c/exp": "12/34", "c/cvc": "987", "c/name": "Jane Q Tester",
	}, fetches: map[string]int{}}
	ref := func(k string) *SecretRef { return &SecretRef{Vault: "testvault", Key: k} }
	r.m.Secrets = v.read
	r.m.Purchases = &Purchases{Dir: filepath.Join(t.TempDir(), "purchases")}
	r.st.Profiles = map[string]Profile{"personal": {Name: "personal",
		Fields: map[string]*SecretRef{"name": ref("p/name"), "email": ref("p/email"), "phone": ref("p/phone"), "company": ref("p/company"),
			"address.line1": ref("p/line1"), "address.city": ref("p/city"), "address.region": ref("p/region"),
			"address.postal_code": ref("p/zip"), "address.country": ref("p/country")},
		Identities: []string{"agent:test@acme/app"},
		Sites:      []ProfileSite{{Origin: w.a, Fields: []string{"name", "email", "phone", "address"}}},
		Visible:    map[string]string{"email": "fact", "address.country": "fact"}}}
	r.st.Cards = map[string]Card{"visa": {Name: "visa", Number: ref("c/number"), Expiry: ref("c/exp"), CVC: ref("c/cvc"), HolderName: ref("c/name"),
		Merchants: []string{w.a}, Identities: []string{"agent:test@acme/app"}, Limits: config.CardLimits{PerPurchase: 30, PerDay: 50, PerMonth: 100}}}
	r.st.PurchaseTTL = time.Minute
	return w
}

func (w *walletRig) do(c Principal, action string, args map[string]any) map[string]any {
	w.t.Helper()
	out := w.rig.do(c, action, args)
	b, _ := json.Marshal(out)
	w.shared = append(w.shared, string(b))
	return out
}

func (w *walletRig) refused(c Principal, action string, args map[string]any, want string) {
	w.t.Helper()
	err := w.fail(c, action, args)
	if !strings.Contains(err.Error(), want) {
		w.t.Fatalf("%s %v: want %q, got %v", action, args, want, err)
	}
	w.shared = append(w.shared, err.Error())
}

func (w *walletRig) noLeak() {
	w.t.Helper()
	hay := append([]string(nil), w.shared...)
	w.audit.mu.Lock()
	for _, e := range w.audit.es {
		b, _ := json.Marshal(e)
		hay = append(hay, string(b))
	}
	w.audit.mu.Unlock()
	filepath.Walk(w.ws, func(p string, fi os.FileInfo, err error) error {
		if err == nil && !fi.IsDir() {
			b, _ := os.ReadFile(p)
			hay = append(hay, string(b))
		}
		return nil
	})
	ps, _ := w.m.Purchases.List("")
	b, _ := json.Marshal(ps)
	hay = append(hay, string(b))
	for _, s := range walletSecrets {
		for _, h := range hay {
			if strings.Contains(h, s) {
				w.t.Fatalf("a wallet value (%q) reached: %.300s", s, h)
			}
		}
	}
}

func (w *walletRig) propose(c Principal, total string, merchant string) map[string]any {
	w.t.Helper()
	return w.do(c, "purchase", map[string]any{"card": "visa", "merchant": merchant, "total": total, "currency": "USD", "why": "test",
		"items": `[{"name":"Widget","qty":1,"amount":12}]`})
}

func (w *walletRig) approve(id string) {
	w.t.Helper()
	p, _, _ := w.m.Purchases.Get(id)
	if _, err := w.m.ApprovePurchase(id, p.Digest, "daniel"); err != nil {
		w.t.Fatal(err)
	}
}

// A sign-up from a profile: conductor fills what the site may have, the
// agent never sees a value (not in the form, not in the page's echo), and
// a field the site was not opted in for stays empty.
func TestWalletProfileSignup(t *testing.T) {
	w := newWalletRig(t)
	op := w.caller("k-op", RequesterOperator)
	facts := w.do(op, "profiles", nil)
	b, _ := json.Marshal(facts)
	if !strings.Contains(string(b), "domain example.test") || !strings.Contains(string(b), "country US") {
		t.Fatalf("visible facts: %s", b)
	}
	w.do(op, "open", map[string]any{"url": w.a + "/signup"})
	out := w.do(op, "fill-form", map[string]any{"profile": "personal"})
	filled, _ := json.Marshal(out["filled"])
	for _, f := range []string{"name", "email", "phone", "address.line1", "address.city", "address.region", "address.postal_code", "address.country"} {
		if !strings.Contains(string(filled), `"`+f+`"`) {
			t.Fatalf("%s not filled: %v", f, out)
		}
	}
	if left := fmt.Sprint(out["left"]); !strings.Contains(left, "company:not opted in for this site") {
		t.Fatalf("company: %v", out["left"])
	}
	snap := w.do(op, "snapshot", nil)["snapshot"].(string)
	if !strings.Contains(snap, FilledMark) {
		t.Fatalf("filled fields show their mark: %s", snap)
	}
	w.refused(op, "fill-profile", map[string]any{"profile": "personal", "field": "company", "ref": refOf(t, snap, "Company")}, "not opted in")
	w.do(op, "click", map[string]any{"ref": refOf(t, snap, "Sign up")})
	welcome := w.do(op, "snapshot", nil)["snapshot"].(string)
	if !strings.Contains(welcome, "Welcome") || !strings.Contains(welcome, Redacted) {
		t.Fatalf("the welcome page's echo is scrubbed: %s", welcome)
	}
	_, signups := w.shop.Snapshot()
	if len(signups) != 1 || signups[0]["email"] != "jane.private@example.test" || signups[0]["country"] != "US" || signups[0]["company"] != "" || signups[0]["password"] != "" {
		t.Fatalf("the shop got: %+v", signups)
	}
	// Not the operator's request: no profile, not even its visible facts.
	ev := w.caller("k-ev", "event")
	if f := w.do(ev, "profiles", nil); strings.Contains(fmt.Sprint(f), "domain") {
		t.Fatalf("an event's session learned a fact: %v", f)
	}
	w.do(ev, "open", map[string]any{"url": w.a + "/signup"})
	w.refused(ev, "fill-form", map[string]any{"profile": "personal"}, "not the operator")
	// Another site: nothing is opted in there.
	w.do(op, "open", map[string]any{"url": w.b + "/signup"})
	w.refused(op, "fill-form", map[string]any{"profile": "personal"}, "no field of this form")
	w.noLeak()
}

// A purchase: proposed, approved, the card filled only then and only on
// the merchant, the total re-read, the order placed, the receipt kept; a
// second use, a price bump, a price raised after the fill, an over-cap
// total, another merchant, a frozen card and a pause are each refused.
func TestWalletPurchase(t *testing.T) {
	w := newWalletRig(t)
	op := w.caller("k-op", RequesterOperator)
	if cards := w.do(op, "cards", nil); !strings.Contains(func() string { b, _ := json.Marshal(cards); return string(b) }(), `"visa"`) {
		t.Fatalf("cards: %v", cards)
	}
	w.do(op, "open", map[string]any{"url": w.a + "/cart/add?item=widget"})
	p := w.propose(op, "12.00", w.a)
	id := p["purchase"].(string)
	if p["status"] != PurchasePending {
		t.Fatalf("propose: %v", p)
	}
	w.do(op, "open", map[string]any{"url": w.a + "/checkout"})
	place := refOf(t, w.do(op, "snapshot", nil)["snapshot"].(string), "Place order")
	w.refused(op, "pay", map[string]any{"purchase": id, "submit": place}, "is pending")
	w.approve(id)
	done := w.do(op, "pay", map[string]any{"purchase": id, "submit": place})
	if done["status"] != PurchaseDone || done["order_number"] != "FS-1001" || done["charged"] != 12.0 {
		t.Fatalf("pay: %v", done)
	}
	orders, _ := w.shop.Snapshot()
	if len(orders) != 1 || orders[0].Last4 != "4242" || orders[0].Total != 12 || orders[0].Expiry != "12/34" || !orders[0].CVCSeen {
		t.Fatalf("the shop's order: %+v", orders)
	}
	conf := w.do(op, "snapshot", nil)["snapshot"].(string)
	if !strings.Contains(conf, "FS-1001") || strings.Contains(conf, "4242 4242") {
		t.Fatalf("the confirmation (its card echo scrubbed): %s", conf)
	}
	w.do(op, "screenshot", map[string]any{"path": "conf.png"})
	got, _, _ := w.m.Purchases.Get(id)
	if got.Receipt == nil || got.Receipt.OrderNumber != "FS-1001" || got.Receipt.Note != "" {
		t.Fatalf("receipt: %+v", got.Receipt)
	}
	// The approval is spent.
	w.do(op, "open", map[string]any{"url": w.a + "/cart/add?item=widget"})
	w.do(op, "open", map[string]any{"url": w.a + "/checkout"})
	place = refOf(t, w.do(op, "snapshot", nil)["snapshot"].(string), "Place order")
	w.refused(op, "pay", map[string]any{"purchase": id, "submit": place}, "completed")

	// A price bump at checkout: refused, and a new approval proposed.
	p2 := w.propose(op, "12.00", w.a)["purchase"].(string)
	w.approve(p2)
	w.shop.Bump = 3
	w.do(op, "open", map[string]any{"url": w.a + "/checkout"})
	place = refOf(t, w.do(op, "snapshot", nil)["snapshot"].(string), "Place order")
	w.refused(op, "pay", map[string]any{"purchase": p2, "submit": place}, "higher than the approved 12.00")
	if pend, _ := w.m.Purchases.List(PurchasePending); len(pend) != 1 || pend[0].Total != 15 || pend[0].Supersedes != p2 {
		t.Fatalf("the new approval: %+v", pend)
	}
	// Approved purchase p2 was not spent by the refusal: still usable once
	// the price is back.
	w.shop.Bump = 0
	// The page raises the price once the card is typed: cleared, nothing placed.
	w.shop.BumpAfterFill = 5
	w.do(op, "open", map[string]any{"url": w.a + "/checkout"})
	place = refOf(t, w.do(op, "snapshot", nil)["snapshot"].(string), "Place order")
	w.refused(op, "pay", map[string]any{"purchase": p2, "submit": place}, "the total changed after the card was filled")
	w.shop.BumpAfterFill = 0
	if orders, _ := w.shop.Snapshot(); len(orders) != 1 {
		t.Fatalf("an order was placed: %+v", orders)
	}
	// Nothing was charged: the spend left the ledger, the approval stayed spent.
	if sum, _ := w.m.Purchases.Summary(); len(sum) == 0 || sum[0].Day != 12 {
		t.Fatalf("spend: %+v", sum)
	}
	if got, _, _ := w.m.Purchases.Get(p2); got.Status != PurchaseRefused {
		t.Fatalf("p2: %s", got.Status)
	}
	// Over the per-purchase cap; another merchant; a frozen card; a pause.
	w.refused(op, "purchase", map[string]any{"card": "visa", "merchant": w.a, "total": "30.01"}, "per-action cap 30.00")
	w.refused(op, "purchase", map[string]any{"card": "visa", "merchant": w.b, "total": "5"}, "not one of card visa's merchants")
	p3 := w.propose(op, "12.00", w.a)["purchase"].(string)
	w.approve(p3)
	w.do(op, "open", map[string]any{"url": w.b + "/cart/add?item=widget"})
	w.do(op, "open", map[string]any{"url": w.b + "/checkout"})
	place = refOf(t, w.do(op, "snapshot", nil)["snapshot"].(string), "Place order")
	w.refused(op, "pay", map[string]any{"purchase": p3, "submit": place}, "not the approved merchant")
	if orders, _ := w.other.Snapshot(); len(orders) != 0 {
		t.Fatal("the other shop got an order")
	}
	cd := w.st.Cards["visa"]
	cd.Frozen = true
	w.st.Cards["visa"] = cd
	w.refused(op, "purchase", map[string]any{"card": "visa", "merchant": w.a, "total": "5"}, "frozen")
	w.do(op, "open", map[string]any{"url": w.a + "/checkout"})
	place = refOf(t, w.do(op, "snapshot", nil)["snapshot"].(string), "Place order")
	w.refused(op, "pay", map[string]any{"purchase": p3, "submit": place}, "frozen")
	cd.Frozen = false
	w.st.Cards["visa"] = cd
	w.m.Paused = func() bool { return true }
	w.refused(op, "pay", map[string]any{"purchase": p3, "submit": place}, "paused")
	w.refused(op, "purchase", map[string]any{"card": "visa", "merchant": w.a, "total": "5"}, "paused")
	w.m.Paused = nil
	// A merchant the operator removed after the approval: refused at pay.
	cd = w.st.Cards["visa"]
	cd.Merchants = []string{"https://elsewhere.example"}
	w.st.Cards["visa"] = cd
	w.do(op, "open", map[string]any{"url": w.a + "/checkout"})
	place = refOf(t, w.do(op, "snapshot", nil)["snapshot"].(string), "Place order")
	w.refused(op, "pay", map[string]any{"purchase": p3, "submit": place}, "no longer one of card visa's merchants")
	cd.Merchants = []string{w.a}
	w.st.Cards["visa"] = cd
	// Another identity cannot pay with this session's approval.
	other := w.caller("k-other", RequesterOperator)
	other.Identity = "agent:else@acme/app"
	w.do(other, "open", map[string]any{"url": w.a + "/checkout"})
	place2 := refOf(t, w.do(other, "snapshot", nil)["snapshot"].(string), "Place order")
	w.refused(other, "pay", map[string]any{"purchase": p3, "submit": place2}, "no purchase")
	// evaluate is off where a card was filled.
	w.refused(op, "evaluate", map[string]any{"expr": "1"}, "evaluate is off")
	w.noLeak()
}
