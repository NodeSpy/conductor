package browsertest

import (
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// Shop is a local fake shop for the payment tests: no real merchant, no
// real card. It sells a few items and a monthly plan, has a sign-up form
// (personal details) and a checkout (a card form and a visible total), and
// takes only the test card 4242 4242 4242 4242. Its confirmation page echoes
// the card number in full — a careless merchant, which is the point: the
// agent must not see it.
type Shop struct {
	mu sync.Mutex
	// Bump is added to every total at checkout and when an order is placed
	// (a price change between the proposal and the checkout).
	Bump float64
	// BumpAfterFill raises the total by this much once the page's card
	// fields are filled (a page that changes its price under the card).
	BumpAfterFill float64
	// Orders and Signups are what the shop received.
	Orders  []ShopOrder
	Signups []map[string]string
	next    int
}

// ShopOrder is an order the shop took.
type ShopOrder struct {
	Number  string  `json:"number"`
	Total   float64 `json:"total"`
	Last4   string  `json:"last4"`
	Items   string  `json:"items"`
	Expiry  string  `json:"expiry"`
	CVCSeen bool    `json:"cvc_seen"`
}

// ShopTestCard is the one card the shop accepts.
const ShopTestCard = "4242424242424242"

var shopItems = map[string]struct {
	Name  string
	Price float64
	Plan  bool
}{
	"widget": {"Widget", 12.00, false},
	"drive":  {"Hard drive", 85.00, false},
	"gadget": {"Gadget", 8.50, false},
	"plan":   {"Pro plan (monthly)", 5.00, true},
}

// NewShop makes a shop.
func NewShop() *Shop { return &Shop{next: 1000} }

func (s *Shop) cart(r *http.Request) []string {
	c, err := r.Cookie("cart")
	if err != nil || c.Value == "" {
		return nil
	}
	return strings.Split(c.Value, ".")
}

func (s *Shop) subtotal(items []string) float64 {
	var t float64
	for _, it := range items {
		t += shopItems[it].Price
	}
	return t
}

// Snapshot is the shop's state (orders, sign-ups).
func (s *Shop) Snapshot() ([]ShopOrder, []map[string]string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]ShopOrder(nil), s.Orders...), append([]map[string]string(nil), s.Signups...)
}

func shopPage(w http.ResponseWriter, title, body string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, "<!doctype html><html><head><title>%s</title></head><body>%s</body></html>", html.EscapeString(title), body)
}

// ServeHTTP serves the shop.
func (s *Shop) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/":
		var b strings.Builder
		b.WriteString(`<h1>Fake Shop</h1><ul>`)
		keys := make([]string, 0, len(shopItems))
		for k := range shopItems {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			it := shopItems[k]
			fmt.Fprintf(&b, `<li>%s — $%.2f <a href="/cart/add?item=%s">Add %s to cart</a></li>`, it.Name, it.Price, k, it.Name)
		}
		b.WriteString(`</ul><a href="/signup">Create an account</a> <a href="/cart">Cart</a>`)
		shopPage(w, "Fake Shop", b.String())
	case "/signup":
		if r.Method == http.MethodPost {
			_ = r.ParseForm()
			got := map[string]string{}
			for k := range r.PostForm {
				got[k] = r.PostForm.Get(k)
			}
			s.mu.Lock()
			s.Signups = append(s.Signups, got)
			s.mu.Unlock()
			// A careless echo of what was submitted: the agent must not see it.
			shopPage(w, "Welcome", fmt.Sprintf(`<h1>Welcome, %s!</h1><p>We sent a confirmation to %s.</p><p>Shipping to %s, %s %s.</p>`,
				html.EscapeString(got["name"]), html.EscapeString(got["email"]), html.EscapeString(got["street"]), html.EscapeString(got["city"]), html.EscapeString(got["zip"])))
			return
		}
		shopPage(w, "Create an account", `<h1>Create an account</h1><form method="post" action="/signup">
<label>Full name <input name="name" autocomplete="name"></label>
<label>Email <input name="email" type="email" autocomplete="email"></label>
<label>Phone <input name="phone" type="tel" autocomplete="tel"></label>
<label>Company <input name="company" autocomplete="organization"></label>
<label>Street <input name="street" autocomplete="address-line1"></label>
<label>City <input name="city" autocomplete="address-level2"></label>
<label>State <input name="state" autocomplete="address-level1"></label>
<label>ZIP <input name="zip" autocomplete="postal-code"></label>
<label>Country <select name="country" autocomplete="country"><option value="">Choose</option><option value="US">United States</option><option value="CA">Canada</option><option value="GB">United Kingdom</option></select></label>
<label>Choose a password <input name="password" type="password" autocomplete="new-password"></label>
<button type="submit">Sign up</button></form>`)
	case "/cart/add":
		it := r.URL.Query().Get("item")
		if _, ok := shopItems[it]; !ok {
			http.Error(w, "no such item", 404)
			return
		}
		items := append(s.cart(r), it)
		http.SetCookie(w, &http.Cookie{Name: "cart", Value: strings.Join(items, "."), Path: "/"})
		http.Redirect(w, r, "/cart", http.StatusSeeOther)
	case "/cart":
		items := s.cart(r)
		var b strings.Builder
		b.WriteString(`<h1>Your cart</h1><ul>`)
		for _, it := range items {
			fmt.Fprintf(&b, `<li>%s $%.2f</li>`, shopItems[it].Name, shopItems[it].Price)
		}
		fmt.Fprintf(&b, `</ul><p>Subtotal: $%.2f</p><a href="/checkout">Checkout</a>`, s.subtotal(items))
		shopPage(w, "Cart", b.String())
	case "/checkout":
		items := s.cart(r)
		s.mu.Lock()
		bump, after := s.Bump, s.BumpAfterFill
		s.mu.Unlock()
		total := s.subtotal(items) + bump
		late := ""
		if after > 0 {
			// The page raises its own total once the card number is typed.
			late = fmt.Sprintf(`<script>document.getElementById("cc").addEventListener("input", () => { document.getElementById("total").textContent = "Order total: $%.2f"; });</script>`, total+after)
		}
		shopPage(w, "Checkout", fmt.Sprintf(`<h1>Checkout</h1><p>%d item(s)</p><p id="total">Order total: $%.2f</p>
<form method="post" action="/order">
<label>Name on card <input name="ccname" autocomplete="cc-name"></label>
<label>Card number <input id="cc" name="ccnumber" inputmode="numeric" autocomplete="cc-number"></label>
<label>Expiry <input name="ccexp" autocomplete="cc-exp" placeholder="MM/YY"></label>
<label>CVC <input name="cvc" inputmode="numeric" autocomplete="cc-csc"></label>
<button type="submit">Place order</button></form>%s`, len(items), total, late))
	case "/order":
		if r.Method != http.MethodPost {
			http.Redirect(w, r, "/checkout", http.StatusSeeOther)
			return
		}
		_ = r.ParseForm()
		num := strings.ReplaceAll(strings.ReplaceAll(r.PostForm.Get("ccnumber"), " ", ""), "-", "")
		if num != ShopTestCard || len(r.PostForm.Get("cvc")) < 3 || r.PostForm.Get("ccexp") == "" {
			shopPage(w, "Payment declined", `<h1>Payment declined</h1><p role="alert">Card declined</p>`)
			return
		}
		items := s.cart(r)
		s.mu.Lock()
		total := s.subtotal(items) + s.Bump
		s.next++
		o := ShopOrder{Number: "FS-" + strconv.Itoa(s.next), Total: total, Last4: num[len(num)-4:], Items: strings.Join(items, ","),
			Expiry: r.PostForm.Get("ccexp"), CVCSeen: r.PostForm.Get("cvc") != ""}
		s.Orders = append(s.Orders, o)
		s.mu.Unlock()
		http.SetCookie(w, &http.Cookie{Name: "cart", Value: "", Path: "/", MaxAge: -1})
		shopPage(w, "Order confirmed", fmt.Sprintf(`<h1>Thank you!</h1><p>Order number: %s</p><p>Total charged: $%.2f</p><p>Paid with card %s, expiring %s.</p>`,
			o.Number, o.Total, html.EscapeString(r.PostForm.Get("ccnumber")), html.EscapeString(o.Expiry)))
	case "/_harness/state":
		orders, signups := s.Snapshot()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"orders": orders, "signups": signups})
	case "/_harness/bump":
		f, _ := strconv.ParseFloat(r.URL.Query().Get("amount"), 64)
		s.mu.Lock()
		s.Bump = f
		s.mu.Unlock()
	default:
		http.NotFound(w, r)
	}
}
