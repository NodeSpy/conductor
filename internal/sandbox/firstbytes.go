package sandbox

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"net"
	"net/textproto"
	"strings"

	"golang.org/x/net/http2/hpack"
)

// The egress proxy's protocol check: a CONNECT tunnel's first client bytes
// are read before the upstream is dialed and recognized as one of
//
//   - TLS: a ClientHello whose server name is the CONNECT host;
//   - HTTP/1.x: a request line and headers whose Host is the CONNECT host;
//   - HTTP/2 (prior knowledge): the connection preface and a first HEADERS
//     frame whose :authority is the CONNECT host;
//
// which pass, or as SSH (the client's "SSH-" identification), refused and
// named. Anything else — unknown bytes, or a client that sends nothing first
// (a raw shell waiting on the far end, a server-speaks-first protocol) — is
// refused as an unrecognized protocol. The remote host never sees a refused
// tunnel. A target in the launch's raw allowance (allow.raw) skips the check.
// It is a check at the door, not inspection: once a tunnel passes it is
// blind.

// Tunnel protocols as the check names them.
const (
	ProtoTLS   = "tls"
	ProtoHTTP  = "http"
	ProtoHTTP2 = "h2c"
	ProtoSSH   = "ssh"
)

// maxFirstBytes bounds what the check reads of a plaintext opening.
const maxFirstBytes = 64 << 10

const h2Preface = "PRI * HTTP/2.0\r\n\r\nSM\r\n\r\n"

// tunnelOpening is what a tunnel's client sent first: the bytes to replay
// upstream, the protocol, and the TLS server name.
type tunnelOpening struct {
	raw   []byte
	proto string
	sni   string
}

// classifyTunnel reads a CONNECT tunnel's opening from r for target
// (host:port) and returns it, or the reason it is refused.
func classifyTunnel(r *bufio.Reader, target string) (tunnelOpening, string) {
	host, port := splitHostPort(target)
	first, err := r.Peek(1)
	if len(first) == 0 {
		why := "the client sent nothing first"
		if err != nil && err != io.EOF {
			why += " (a raw shell waiting on the far end, or a server-speaks-first protocol)"
		}
		return tunnelOpening{}, fmt.Sprintf("unrecognized protocol to %s: %s — allow it with allow.raw for that host", target, why)
	}
	switch {
	case first[0] == 0x16:
		raw, info, err := readClientHello(r)
		if err != nil {
			return tunnelOpening{}, err.Error()
		}
		switch {
		case info.ECH:
			return tunnelOpening{}, "encrypted ClientHello: the server name cannot be checked"
		case !sniMatches(host, info.SNI, net.ParseIP(host) != nil):
			if info.SNI == "" {
				return tunnelOpening{}, "no TLS server name (SNI)"
			}
			return tunnelOpening{}, fmt.Sprintf("TLS server name %q is not the CONNECT host %q", info.SNI, host)
		}
		return tunnelOpening{raw: raw, proto: ProtoTLS, sni: info.SNI}, ""
	case peekIs(r, "SSH-"):
		return tunnelOpening{}, fmt.Sprintf("ssh to %s refused — allow it with allow.raw for that host", target)
	case peekIs(r, h2Preface):
		raw, authority, err := readH2Opening(r)
		if err != nil {
			return tunnelOpening{}, "HTTP/2: " + err.Error()
		}
		if !authorityMatches(authority, host, port) {
			return tunnelOpening{}, fmt.Sprintf("HTTP/2 :authority %q is not the CONNECT host %q", authority, host)
		}
		return tunnelOpening{raw: raw, proto: ProtoHTTP2}, ""
	case looksLikeHTTP1(r):
		raw, hostHdr, err := readHTTP1Opening(r)
		if err != nil {
			return tunnelOpening{}, "HTTP/1: " + err.Error()
		}
		if !authorityMatches(hostHdr, host, port) {
			return tunnelOpening{}, fmt.Sprintf("HTTP Host %q is not the CONNECT host %q", hostHdr, host)
		}
		return tunnelOpening{raw: raw, proto: ProtoHTTP}, ""
	}
	return tunnelOpening{}, fmt.Sprintf("unrecognized protocol to %s (%v) — allow it with allow.raw for that host", target, strings.TrimPrefix(describeFirstBytes(r, nil).Error(), errNotTLS.Error()+": "))
}

// peekIs reports whether r's next bytes are prefix (waiting for as many as
// the prefix has while they keep matching).
func peekIs(r *bufio.Reader, prefix string) bool {
	for n := 1; n <= len(prefix); n++ {
		b, err := r.Peek(n)
		if len(b) < n {
			return false
		}
		if b[n-1] != prefix[n-1] {
			return false
		}
		if err != nil {
			return false
		}
	}
	return true
}

// looksLikeHTTP1 reports a request line's start: a method token of
// uppercase letters and a space.
func looksLikeHTTP1(r *bufio.Reader) bool {
	for n := 1; n <= 16; n++ {
		b, _ := r.Peek(n)
		if len(b) < n {
			return false
		}
		c := b[n-1]
		switch {
		case c == ' ':
			return n > 1
		case c < 'A' || c > 'Z':
			return false
		}
	}
	return false
}

// readHTTP1Opening reads a request's head (request line and headers) and
// returns its bytes and its one Host.
func readHTTP1Opening(r *bufio.Reader) ([]byte, string, error) {
	var raw bytes.Buffer
	tp := textproto.NewReader(bufio.NewReader(io.TeeReader(io.LimitReader(r, maxFirstBytes), &raw)))
	line, err := tp.ReadLine()
	if err != nil {
		return nil, "", err
	}
	parts := strings.Split(line, " ")
	if len(parts) != 3 || (parts[2] != "HTTP/1.1" && parts[2] != "HTTP/1.0") {
		return nil, "", fmt.Errorf("not a request line: %q", trimShow(line))
	}
	hdr, err := tp.ReadMIMEHeader()
	if err != nil {
		return nil, "", err
	}
	hosts := hdr.Values("Host")
	if len(hosts) != 1 {
		return nil, "", fmt.Errorf("%d Host headers", len(hosts))
	}
	// raw is everything taken from r, a body's first bytes the reader
	// buffered past the head included: all of it is replayed upstream.
	return raw.Bytes(), hosts[0], nil
}

// readH2Opening reads the HTTP/2 preface and frames up to the end of the
// first HEADERS block, and returns their bytes and its :authority.
func readH2Opening(r *bufio.Reader) ([]byte, string, error) {
	raw := make([]byte, len(h2Preface))
	if _, err := io.ReadFull(r, raw); err != nil {
		return nil, "", err
	}
	var block []byte
	inHeaders := false
	for len(raw) < maxFirstBytes {
		var fh [9]byte
		if _, err := io.ReadFull(r, fh[:]); err != nil {
			return nil, "", err
		}
		n := int(fh[0])<<16 | int(fh[1])<<8 | int(fh[2])
		typ, flags := fh[3], fh[4]
		if len(raw)+9+n > maxFirstBytes {
			return nil, "", fmt.Errorf("opening longer than %d bytes", maxFirstBytes)
		}
		payload := make([]byte, n)
		if _, err := io.ReadFull(r, payload); err != nil {
			return nil, "", err
		}
		raw = append(append(raw, fh[:]...), payload...)
		switch {
		case typ == 0x1 && !inHeaders: // HEADERS
			p := payload
			pad := 0
			if flags&0x8 != 0 { // PADDED
				if len(p) < 1 {
					return nil, "", fmt.Errorf("bad HEADERS frame")
				}
				pad, p = int(p[0]), p[1:]
			}
			if flags&0x20 != 0 { // PRIORITY
				if len(p) < 5 {
					return nil, "", fmt.Errorf("bad HEADERS frame")
				}
				p = p[5:]
			}
			if pad > len(p) {
				return nil, "", fmt.Errorf("bad HEADERS padding")
			}
			block, inHeaders = append(block, p[:len(p)-pad]...), true
		case typ == 0x9 && inHeaders: // CONTINUATION
			block = append(block, payload...)
		case inHeaders:
			return nil, "", fmt.Errorf("frame type %d inside a header block", typ)
		default:
			continue // SETTINGS, WINDOW_UPDATE, PRIORITY before the request
		}
		if flags&0x4 == 0 { // not END_HEADERS
			continue
		}
		var authority, host string
		dec := hpack.NewDecoder(4096, func(f hpack.HeaderField) {
			switch strings.ToLower(f.Name) {
			case ":authority":
				authority = f.Value
			case "host":
				host = f.Value
			}
		})
		if _, err := dec.Write(block); err != nil {
			return nil, "", err
		}
		if authority == "" {
			authority = host
		}
		return raw, authority, nil
	}
	return nil, "", fmt.Errorf("opening longer than %d bytes", maxFirstBytes)
}

// authorityMatches reports a Host / :authority naming the CONNECT host
// (case-insensitive, a trailing dot ignored), with no port or its port.
func authorityMatches(authority, host, port string) bool {
	h, p := splitHostPort(authority)
	if strings.Contains(authority, ":") && !strings.HasPrefix(authority, "[") && strings.Count(authority, ":") > 1 {
		h, p = authority, "" // a bare IPv6 literal
	}
	norm := func(s string) string { return strings.TrimSuffix(strings.ToLower(s), ".") }
	if h == "" || norm(h) != norm(host) {
		return false
	}
	return p == "" || p == port
}

func trimShow(s string) string {
	if len(s) > 40 {
		return s[:40] + "…"
	}
	return s
}
