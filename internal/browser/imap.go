package browser

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net"
	"net/mail"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Email codes: a sign-in that mails a one-time code. Conductor reads the
// operator's named mailbox over IMAP — read-only (EXAMINE), with an app
// password from a vault — and only what it must: the server searches by the
// credential's sender and subject, conductor reads those messages' headers
// and arrival times, and the body of the single newest match that arrived
// after the sign-in step. The code is extracted with the credential's
// declared pattern and filled like a TOTP code. The agent never sees the
// mailbox, the message, or the code; no error carries message content.

// Mailbox is one IMAP mailbox a credential's email codes come from.
type Mailbox struct {
	Name string
	// Addr is host:port (993: implicit TLS).
	Addr string
	// StartTLS upgrades a plain connection (port 143) before login.
	StartTLS bool
	// PlainLoopback allows a plaintext connection, to a loopback address
	// only (a local mail bridge).
	PlainLoopback bool
	// CAFile is a PEM bundle to trust instead of the system's.
	CAFile string
	// Username / Password are vault references (an app password).
	Username, Password *SecretRef
	// Folder is the mailbox folder (default INBOX).
	Folder string
}

// EmailCode is how a credential finds its emailed code.
type EmailCode struct {
	// Mailbox names the Mailbox.
	Mailbox string
	// From is the sender: an exact address, or "@domain" for any sender at
	// exactly that domain. Subject must appear in the subject
	// (case-insensitive). The server's search narrows; conductor decides.
	From, Subject string
	// Code extracts the code: its first group, else the whole match.
	Code *regexp.Regexp
	// Wait bounds how long conductor waits for the message (default 60s).
	Wait time.Duration
}

// imapDial is the connection seam (tests replace it).
var imapDial = func(ctx context.Context, mb Mailbox) (net.Conn, error) {
	host, _, err := net.SplitHostPort(mb.Addr)
	if err != nil {
		return nil, fmt.Errorf("mailbox %s: address %q is not host:port", mb.Name, mb.Addr)
	}
	if mb.PlainLoopback {
		// Refused before any connection: plaintext never leaves the box.
		if ip := net.ParseIP(host); (ip == nil || !ip.IsLoopback()) && host != "localhost" {
			return nil, fmt.Errorf("mailbox %s: plaintext is allowed to a loopback address only", mb.Name)
		}
	}
	var d net.Dialer
	c, err := d.DialContext(ctx, "tcp", mb.Addr)
	if err != nil {
		return nil, fmt.Errorf("mailbox %s: %v", mb.Name, err)
	}
	if mb.PlainLoopback || mb.StartTLS {
		return c, nil
	}
	cfg, err := imapTLS(mb, host)
	if err != nil {
		c.Close()
		return nil, err
	}
	tc := tls.Client(c, cfg)
	if err := tc.HandshakeContext(ctx); err != nil {
		c.Close()
		return nil, fmt.Errorf("mailbox %s: TLS: %v", mb.Name, err)
	}
	return tc, nil
}

func imapTLS(mb Mailbox, host string) (*tls.Config, error) {
	cfg := &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12}
	if mb.CAFile != "" {
		pem, err := os.ReadFile(mb.CAFile)
		if err != nil {
			return nil, fmt.Errorf("mailbox %s: ca_file: %v", mb.Name, err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("mailbox %s: ca_file has no certificate", mb.Name)
		}
		cfg.RootCAs = pool
	}
	return cfg, nil
}

// imapConn is a minimal IMAP4rev1 client: what reading one message takes.
type imapConn struct {
	c   net.Conn
	r   *bufio.Reader
	tag int
}

// line reads one response line, literals ({n}) inlined.
func (ic *imapConn) line() (string, error) {
	var b strings.Builder
	for {
		l, err := ic.r.ReadString('\n')
		if err != nil {
			return "", err
		}
		l = strings.TrimRight(l, "\r\n")
		b.WriteString(l)
		if i := strings.LastIndexByte(l, '{'); i >= 0 && strings.HasSuffix(l, "}") {
			n, err := strconv.Atoi(l[i+1 : len(l)-1])
			if err == nil && n >= 0 && n <= 4<<20 {
				lit := make([]byte, n)
				if _, err := io.ReadFull(ic.r, lit); err != nil {
					return "", err
				}
				b.WriteString("\x00LIT\x00")
				b.Write(lit)
				b.WriteString("\x00END\x00")
				continue
			}
		}
		return b.String(), nil
	}
}

// cmd sends one command and collects its untagged responses.
func (ic *imapConn) cmd(format string, a ...any) ([]string, error) {
	ic.tag++
	tag := fmt.Sprintf("c%d", ic.tag)
	if _, err := fmt.Fprintf(ic.c, "%s %s\r\n", tag, fmt.Sprintf(format, a...)); err != nil {
		return nil, err
	}
	var out []string
	for {
		l, err := ic.line()
		if err != nil {
			return nil, err
		}
		if strings.HasPrefix(l, tag+" ") {
			rest := strings.TrimPrefix(l, tag+" ")
			if !strings.HasPrefix(rest, "OK") {
				verb := strings.Fields(format)[0]
				return nil, fmt.Errorf("IMAP %s refused", verb)
			}
			return out, nil
		}
		out = append(out, l)
	}
}

// quote renders an IMAP quoted string; anything that cannot be quoted is
// refused (never sent raw).
func quote(s string) (string, error) {
	if strings.ContainsAny(s, "\r\n\x00") {
		return "", fmt.Errorf("a value with a line break")
	}
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`, nil
}

// fetchEmailCode waits (at most ec.Wait) for the newest message from
// ec.From with ec.Subject that arrived after `after` and is not in `used`,
// and returns its code and an id for it. user / pass are the mailbox login.
func fetchEmailCode(ctx context.Context, mb Mailbox, ec EmailCode, user, pass string, after time.Time, used map[string]bool) (code, id string, err error) {
	wait := ec.Wait
	if wait <= 0 {
		wait = 60 * time.Second
	}
	deadline := time.Now().Add(wait)
	for {
		code, id, err = readEmailCode(ctx, mb, ec, user, pass, after, used)
		if err != errNoMessage || time.Now().After(deadline) {
			if err == errNoMessage {
				err = fmt.Errorf("no message from %q with subject %q arrived in %s", ec.From, ec.Subject, wait)
			}
			return code, id, err
		}
		select {
		case <-ctx.Done():
			return "", "", ctx.Err()
		case <-time.After(3 * time.Second):
		}
	}
}

var errNoMessage = fmt.Errorf("no matching message")

func readEmailCode(ctx context.Context, mb Mailbox, ec EmailCode, user, pass string, after time.Time, used map[string]bool) (string, string, error) {
	c, err := imapDial(ctx, mb)
	if err != nil {
		return "", "", err
	}
	defer c.Close()
	if dl, ok := ctx.Deadline(); ok {
		_ = c.SetDeadline(dl)
	} else {
		_ = c.SetDeadline(time.Now().Add(60 * time.Second))
	}
	ic := &imapConn{c: c, r: bufio.NewReader(c)}
	if g, err := ic.line(); err != nil || !strings.HasPrefix(g, "* OK") {
		return "", "", fmt.Errorf("mailbox %s: no IMAP greeting", mb.Name)
	}
	if mb.StartTLS {
		if _, err := ic.cmd("STARTTLS"); err != nil {
			return "", "", fmt.Errorf("mailbox %s: STARTTLS: %v", mb.Name, err)
		}
		host, _, _ := net.SplitHostPort(mb.Addr)
		cfg, err := imapTLS(mb, host)
		if err != nil {
			return "", "", err
		}
		tc := tls.Client(c, cfg)
		if err := tc.HandshakeContext(ctx); err != nil {
			return "", "", fmt.Errorf("mailbox %s: TLS: %v", mb.Name, err)
		}
		ic = &imapConn{c: tc, r: bufio.NewReader(tc), tag: ic.tag}
	}
	qu, err1 := quote(user)
	qp, err2 := quote(pass)
	if err1 != nil || err2 != nil {
		return "", "", fmt.Errorf("mailbox %s: the login cannot be sent", mb.Name)
	}
	if _, err := ic.cmd("LOGIN %s %s", qu, qp); err != nil {
		return "", "", fmt.Errorf("mailbox %s: login refused", mb.Name)
	}
	defer ic.cmd("LOGOUT") //nolint:errcheck
	folder := mb.Folder
	if folder == "" {
		folder = "INBOX"
	}
	qf, err := quote(folder)
	if err != nil {
		return "", "", err
	}
	// EXAMINE: read-only — conductor changes no flag, marks nothing seen.
	if _, err := ic.cmd("EXAMINE %s", qf); err != nil {
		return "", "", fmt.Errorf("mailbox %s: folder %s: %v", mb.Name, folder, err)
	}
	qfrom, err1 := quote(ec.From)
	qsubj, err2 := quote(ec.Subject)
	if err1 != nil || err2 != nil {
		return "", "", fmt.Errorf("mailbox %s: a matcher cannot be sent", mb.Name)
	}
	since := after.Add(-24 * time.Hour).UTC().Format("2-Jan-2006")
	resp, err := ic.cmd("UID SEARCH SINCE %s FROM %s SUBJECT %s", since, qfrom, qsubj)
	if err != nil {
		return "", "", fmt.Errorf("mailbox %s: search: %v", mb.Name, err)
	}
	var uids []int
	for _, l := range resp {
		if f := strings.Fields(l); len(f) >= 2 && f[0] == "*" && strings.EqualFold(f[1], "SEARCH") {
			for _, u := range f[2:] {
				if n, err := strconv.Atoi(u); err == nil {
					uids = append(uids, n)
				}
			}
		}
	}
	sort.Sort(sort.Reverse(sort.IntSlice(uids)))
	if len(uids) > 20 {
		uids = uids[:20]
	}
	for _, uid := range uids {
		id := fmt.Sprintf("%s/%d", mb.Name, uid)
		if used[id] {
			continue
		}
		// Headers and arrival time only.
		r, err := ic.cmd("UID FETCH %d (INTERNALDATE BODY.PEEK[HEADER.FIELDS (FROM SUBJECT)])", uid)
		f := fetchLine(r)
		if err != nil || f == "" {
			continue
		}
		at, hdr := parseFetch(f)
		if at.IsZero() || at.Before(after) {
			continue
		}
		m, err := mail.ReadMessage(strings.NewReader(hdr + "\r\n"))
		if err != nil {
			continue
		}
		subj := decodeHeader(m.Header.Get("Subject"))
		if !senderMatches(m.Header.Get("From"), ec.From) || !strings.Contains(strings.ToLower(subj), strings.ToLower(ec.Subject)) {
			continue
		}
		// The one message: its body.
		r, err = ic.cmd("UID FETCH %d (BODY.PEEK[])", uid)
		f = fetchLine(r)
		if err != nil || f == "" {
			return "", "", fmt.Errorf("mailbox %s: the matching message could not be read", mb.Name)
		}
		_, raw := parseFetch(f)
		text := subj + "\n" + messageText(raw)
		mm := ec.Code.FindStringSubmatch(text)
		if mm == nil {
			return "", "", fmt.Errorf("mailbox %s: the matching message holds no code (the credential's code pattern)", mb.Name)
		}
		code := mm[0]
		if len(mm) > 1 {
			code = mm[1]
		}
		return strings.TrimSpace(code), id, nil
	}
	return "", "", errNoMessage
}

// senderMatches holds a message's From address to the credential's sender
// exactly — an address, or "@domain" for any sender at exactly that domain.
// (The server's FROM search is a substring: it only narrows.)
func senderMatches(header, want string) bool {
	a, err := mail.ParseAddress(decodeHeader(header))
	if err != nil {
		return false
	}
	got, want := strings.ToLower(a.Address), strings.ToLower(strings.TrimSpace(want))
	if dom, ok := strings.CutPrefix(want, "@"); ok {
		at := strings.LastIndexByte(got, '@')
		return dom != "" && at >= 0 && got[at+1:] == dom
	}
	return want != "" && got == want
}

// fetchLine is the FETCH response among a command's untagged ones.
func fetchLine(r []string) string {
	for _, l := range r {
		if f := strings.Fields(l); len(f) >= 3 && f[0] == "*" && strings.EqualFold(f[2], "FETCH") {
			return l
		}
	}
	return ""
}

// parseFetch pulls INTERNALDATE and the (one) literal out of a FETCH line.
func parseFetch(l string) (time.Time, string) {
	var at time.Time
	if i := strings.Index(l, `INTERNALDATE "`); i >= 0 {
		rest := l[i+len(`INTERNALDATE "`):]
		if j := strings.IndexByte(rest, '"'); j >= 0 {
			at, _ = time.Parse("02-Jan-2006 15:04:05 -0700", strings.TrimSpace(rest[:j]))
			if at.IsZero() {
				at, _ = time.Parse("2-Jan-2006 15:04:05 -0700", strings.TrimSpace(rest[:j]))
			}
		}
	}
	var lit string
	if i := strings.Index(l, "\x00LIT\x00"); i >= 0 {
		rest := l[i+5:]
		if j := strings.Index(rest, "\x00END\x00"); j >= 0 {
			lit = rest[:j]
		}
	}
	return at, lit
}

func decodeHeader(s string) string {
	d, err := new(mime.WordDecoder).DecodeHeader(s)
	if err != nil {
		return s
	}
	return d
}

var tagRE = regexp.MustCompile(`(?s)<[^>]*>`)

// messageText is a message's readable text: its text/plain parts (else
// text/html, tags stripped), transfer encodings decoded.
func messageText(raw string) string {
	m, err := mail.ReadMessage(strings.NewReader(raw))
	if err != nil {
		return raw
	}
	var plain, html []string
	var walk func(h mailHeader, body io.Reader, depth int)
	walk = func(h mailHeader, body io.Reader, depth int) {
		ct, params, err := mime.ParseMediaType(h.Get("Content-Type"))
		if err != nil {
			ct = "text/plain"
		}
		if strings.HasPrefix(ct, "multipart/") && depth < 5 {
			mr := multipart.NewReader(body, params["boundary"])
			for {
				p, err := mr.NextPart()
				if err != nil {
					return
				}
				walk(p.Header, p, depth+1)
			}
		}
		b, _ := io.ReadAll(io.LimitReader(decodeTransfer(h.Get("Content-Transfer-Encoding"), body), 1<<20))
		switch ct {
		case "text/plain":
			plain = append(plain, string(b))
		case "text/html":
			html = append(html, tagRE.ReplaceAllString(string(b), " "))
		}
	}
	walk(m.Header, m.Body, 0)
	if len(plain) > 0 {
		return strings.Join(plain, "\n")
	}
	return strings.Join(html, "\n")
}

type mailHeader interface{ Get(string) string }

func decodeTransfer(enc string, r io.Reader) io.Reader {
	switch strings.ToLower(strings.TrimSpace(enc)) {
	case "quoted-printable":
		return quotedprintable.NewReader(r)
	case "base64":
		b, _ := io.ReadAll(io.LimitReader(r, 2<<20))
		d, err := base64.StdEncoding.DecodeString(string(bytes.Join(bytes.Fields(b), nil)))
		if err != nil {
			return bytes.NewReader(b)
		}
		return bytes.NewReader(d)
	}
	return r
}
