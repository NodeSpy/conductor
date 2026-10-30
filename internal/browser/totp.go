package browser

import (
	"crypto/hmac"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base32"
	"encoding/binary"
	"fmt"
	"hash"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// A one-time code is computed by conductor, outside every sandbox, from what
// the credential's otp source holds: a code already (1Password's
// `?attribute=otp` reference returns the current one — the seed never leaves
// 1Password), an otpauth:// URI, or a base32 seed (RFC 6238). Neither the
// seed nor the code reaches the agent.

var codeOnly = regexp.MustCompile(`^[0-9]{6,8}$`)

// OTPCode turns an otp source's value into the current code.
func OTPCode(v string, now time.Time) (string, error) {
	v = strings.TrimSpace(v)
	if codeOnly.MatchString(v) {
		return v, nil
	}
	secret, algo, digits, period := v, "SHA1", 6, int64(30)
	if strings.HasPrefix(strings.ToLower(v), "otpauth://") {
		u, err := url.Parse(v)
		if err != nil || u.Host != "totp" {
			return "", fmt.Errorf("browser: the otp source is not a TOTP otpauth:// URI")
		}
		q := u.Query()
		secret = q.Get("secret")
		if a := q.Get("algorithm"); a != "" {
			algo = strings.ToUpper(a)
		}
		if d := q.Get("digits"); d != "" {
			n, err := strconv.Atoi(d)
			if err != nil || n < 6 || n > 8 {
				return "", fmt.Errorf("browser: the otp source has bad digits")
			}
			digits = n
		}
		if p := q.Get("period"); p != "" {
			n, err := strconv.ParseInt(p, 10, 64)
			if err != nil || n <= 0 || n > 600 {
				return "", fmt.Errorf("browser: the otp source has a bad period")
			}
			period = n
		}
	}
	key, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(strings.ToUpper(strings.NewReplacer(" ", "", "=", "", "-", "").Replace(secret)))
	if err != nil || len(key) == 0 {
		return "", fmt.Errorf("browser: the otp source is neither a code, an otpauth:// URI, nor a base32 seed")
	}
	var h func() hash.Hash
	switch algo {
	case "SHA1":
		h = sha1.New
	case "SHA256":
		h = sha256.New
	case "SHA512":
		h = sha512.New
	default:
		return "", fmt.Errorf("browser: the otp source's algorithm %s is not supported", algo)
	}
	return totp(key, h, digits, now.Unix()/period), nil
}

func totp(key []byte, h func() hash.Hash, digits int, counter int64) string {
	var msg [8]byte
	binary.BigEndian.PutUint64(msg[:], uint64(counter))
	mac := hmac.New(h, key)
	mac.Write(msg[:])
	sum := mac.Sum(nil)
	off := sum[len(sum)-1] & 0x0f
	bin := binary.BigEndian.Uint32(sum[off:off+4]) & 0x7fffffff
	mod := uint32(1)
	for i := 0; i < digits; i++ {
		mod *= 10
	}
	return fmt.Sprintf("%0*d", digits, bin%mod)
}
