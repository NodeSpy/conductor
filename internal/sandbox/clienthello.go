package sandbox

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"strings"
)

// The egress proxy's first-bytes check: a CONNECT tunnel must open with a
// TLS ClientHello whose server name is the CONNECT host, read before the
// upstream is dialed — so a non-TLS protocol (an SSH banner, a raw shell)
// never reaches the remote host at all. It is a hygiene check at the door,
// not inspection: once the handshake passes the tunnel is blind.

// helloInfo is what the check reads from a ClientHello.
type helloInfo struct {
	SNI string // the server_name host ("" when absent)
	ECH bool   // an encrypted_client_hello extension (the real name is hidden)
}

var errNotTLS = errors.New("not a TLS ClientHello")

const (
	maxHello       = 64 << 10 // a ClientHello reassembled across records
	extServerName  = 0x0000
	extEncryptedCH = 0xfe0d
)

// readClientHello reads a ClientHello from r — one or more TLS handshake
// records — and returns its bytes (to replay upstream) and what it names.
func readClientHello(r *bufio.Reader) ([]byte, helloInfo, error) {
	var raw, hs []byte
	need := -1
	for need < 0 || len(hs) < need {
		hdr, err := r.Peek(5)
		if err != nil {
			if len(raw) == 0 {
				return nil, helloInfo{}, describeFirstBytes(r, err)
			}
			return nil, helloInfo{}, fmt.Errorf("%w: truncated", errNotTLS)
		}
		if hdr[0] != 0x16 || hdr[1] != 0x03 {
			if len(raw) == 0 {
				return nil, helloInfo{}, describeFirstBytes(r, nil)
			}
			return nil, helloInfo{}, fmt.Errorf("%w: a non-handshake record inside the ClientHello", errNotTLS)
		}
		n := int(binary.BigEndian.Uint16(hdr[3:5]))
		if n == 0 || n > 1<<14+256 || len(raw)+5+n > maxHello {
			return nil, helloInfo{}, fmt.Errorf("%w: bad record length %d", errNotTLS, n)
		}
		rec := make([]byte, 5+n)
		if _, err := io.ReadFull(r, rec); err != nil {
			return nil, helloInfo{}, fmt.Errorf("%w: %v", errNotTLS, err)
		}
		raw = append(raw, rec...)
		hs = append(hs, rec[5:]...)
		if need < 0 && len(hs) >= 4 {
			if hs[0] != 0x01 {
				return nil, helloInfo{}, fmt.Errorf("%w: handshake type %d", errNotTLS, hs[0])
			}
			need = 4 + (int(hs[1])<<16 | int(hs[2])<<8 | int(hs[3]))
			if need > maxHello {
				return nil, helloInfo{}, fmt.Errorf("%w: ClientHello of %d bytes", errNotTLS, need)
			}
		}
	}
	info, err := parseClientHello(hs[4:need])
	if err != nil {
		return nil, helloInfo{}, err
	}
	return raw, info, nil
}

// parseClientHello reads the extensions of a ClientHello body.
func parseClientHello(b []byte) (helloInfo, error) {
	var info helloInfo
	bad := fmt.Errorf("%w: malformed", errNotTLS)
	if len(b) < 2+32+1 {
		return info, bad
	}
	b = b[2+32:] // legacy_version, random
	skip := func(lenBytes int) bool {
		if len(b) < lenBytes {
			return false
		}
		n := 0
		for i := 0; i < lenBytes; i++ {
			n = n<<8 | int(b[i])
		}
		if len(b) < lenBytes+n {
			return false
		}
		b = b[lenBytes+n:]
		return true
	}
	if !skip(1) || !skip(2) || !skip(1) { // session_id, cipher_suites, compression
		return info, bad
	}
	if len(b) == 0 {
		return info, nil // no extensions: no server name
	}
	if len(b) < 2 {
		return info, bad
	}
	ext := b[2:]
	if int(binary.BigEndian.Uint16(b)) != len(ext) {
		return info, bad
	}
	for len(ext) >= 4 {
		typ := binary.BigEndian.Uint16(ext)
		n := int(binary.BigEndian.Uint16(ext[2:]))
		if len(ext) < 4+n {
			return info, bad
		}
		body := ext[4 : 4+n]
		ext = ext[4+n:]
		switch typ {
		case extEncryptedCH:
			info.ECH = true
		case extServerName:
			if len(body) < 2 {
				return info, bad
			}
			list := body[2:]
			for len(list) >= 3 {
				nt, nl := list[0], int(binary.BigEndian.Uint16(list[1:]))
				if len(list) < 3+nl {
					return info, bad
				}
				if nt == 0 && info.SNI == "" {
					info.SNI = string(list[3 : 3+nl])
				}
				list = list[3+nl:]
			}
		}
	}
	return info, nil
}

// describeFirstBytes names what a client sent instead of a ClientHello —
// for the audit row ("SSH-2.0-…"), printable and short.
func describeFirstBytes(r *bufio.Reader, err error) error {
	b, _ := r.Peek(r.Buffered())
	if len(b) == 0 && err != nil {
		return fmt.Errorf("%w: no bytes (%v)", errNotTLS, err)
	}
	if len(b) > 16 {
		b = b[:16]
	}
	var s strings.Builder
	for _, c := range b {
		if c >= 0x20 && c < 0x7f {
			s.WriteByte(c)
		} else {
			fmt.Fprintf(&s, "\\x%02x", c)
		}
	}
	return fmt.Errorf("%w: first bytes %q", errNotTLS, s.String())
}

// sniMatches reports whether a ClientHello's server name is acceptable for
// a CONNECT to host: the same name (case-insensitive, a trailing dot
// ignored); for an IP-literal host, no name or the same literal.
func sniMatches(host, sni string, isIP bool) bool {
	norm := func(s string) string { return strings.TrimSuffix(strings.ToLower(s), ".") }
	if isIP {
		return sni == "" || norm(sni) == norm(host)
	}
	return sni != "" && norm(sni) == norm(host)
}
