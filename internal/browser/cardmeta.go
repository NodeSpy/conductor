package browser

import (
	"fmt"
	"strings"
	"time"
)

// A card's brand and last four digits are the only parts of the number the
// dashboard ever learns (design §4.5.1, "NEW for the daemon"). They are derived
// once — when the number is stored (typed) or first resolved (a reference) — and
// cached on the CardRecord. The full number is never retained beyond the fill.

// CardBrand names a card network from its number's leading digits (the IIN), by
// the common prefix ranges. It returns "" when nothing matches.
func CardBrand(number string) string {
	d := digitsOnly(number)
	if len(d) < 2 {
		return ""
	}
	two := d[:2]
	switch {
	case d[0] == '4':
		return "Visa"
	case two == "34" || two == "37":
		return "Amex"
	case two >= "51" && two <= "55":
		return "Mastercard"
	case len(d) >= 4 && d[:4] >= "2221" && d[:4] <= "2720":
		return "Mastercard"
	case two == "65" || strings.HasPrefix(d, "6011") || (len(d) >= 3 && d[:3] >= "644" && d[:3] <= "649"):
		return "Discover"
	case two == "35":
		return "JCB"
	case two == "36" || two == "38" || (len(d) >= 3 && d[:3] >= "300" && d[:3] <= "305"):
		return "Diners"
	}
	return "Card"
}

// CardLast4 is the last four digits of a card number.
func CardLast4(number string) string {
	d := digitsOnly(number)
	if len(d) < 4 {
		return ""
	}
	return d[len(d)-4:]
}

func digitsOnly(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// SetCardMeta records a card's brand and last four. It is a narrowing-free,
// value-free write: the dashboard calls it right after the number is set, having
// derived the two parts from the typed value or a one-time resolve of the
// reference, so the record carries no more of the number than this.
func (s *LoginStore) SetCardMeta(name, brand, last4 string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	w, err := s.loadWalletLocked()
	if err != nil {
		return err
	}
	c, ok := w.Cards[name]
	if !ok {
		return fmt.Errorf("browser: no stored card %q", name)
	}
	c.Brand, c.Last4, c.Updated = brand, last4, time.Now().UTC()
	return s.saveWalletLocked(w)
}
