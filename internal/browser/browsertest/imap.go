package browsertest

import (
	"bufio"
	"crypto/tls"
	"fmt"
	"net"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Mailbox is a fake IMAP server holding a TEST mailbox: enough IMAP4rev1
// (LOGIN, EXAMINE, UID SEARCH, UID FETCH, LOGOUT) for conductor's email
// codes, recording every command so a test can see exactly what was read.
type Mailbox struct {
	User, Pass string
	// Lax: SEARCH ignores its criteria and returns every message (a server
	// conductor must not rely on).
	Lax bool

	mu       sync.Mutex
	msgs     []mailMsg
	Commands []string
	ln       net.Listener
}

type mailMsg struct {
	uid           int
	from, subject string
	body          string
	at            time.Time
}

// Deliver puts a message in the mailbox.
func (m *Mailbox) Deliver(from, subject, body string, at time.Time) {
	m.mu.Lock()
	m.msgs = append(m.msgs, mailMsg{uid: len(m.msgs) + 1, from: from, subject: subject, body: body, at: at})
	m.mu.Unlock()
}

// Log returns the commands received (passwords elided).
func (m *Mailbox) Log() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.Commands...)
}

// Addr is the server's address.
func (m *Mailbox) Addr() string { return m.ln.Addr().String() }

// Close stops the server.
func (m *Mailbox) Close() { m.ln.Close() }

// Serve listens on addr (TLS when cfg is set) and serves until Close.
func (m *Mailbox) Serve(addr string, cfg *tls.Config) error {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	if cfg != nil {
		ln = tls.NewListener(ln, cfg)
	}
	m.ln = ln
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go m.conn(c)
		}
	}()
	return nil
}

var quotedRE = regexp.MustCompile(`"((?:[^"\\]|\\.)*)"`)

func unq(s string) string { return strings.NewReplacer(`\"`, `"`, `\\`, `\`).Replace(s) }

func (m *Mailbox) conn(c net.Conn) {
	defer c.Close()
	r := bufio.NewReader(c)
	fmt.Fprint(c, "* OK test IMAP ready\r\n")
	authed := false
	for {
		l, err := r.ReadString('\n')
		if err != nil {
			return
		}
		l = strings.TrimRight(l, "\r\n")
		f := strings.SplitN(l, " ", 3)
		if len(f) < 2 {
			return
		}
		tag, cmd, rest := f[0], strings.ToUpper(f[1]), ""
		if len(f) == 3 {
			rest = f[2]
		}
		logged := l
		if cmd == "LOGIN" {
			logged = tag + " LOGIN <elided>"
		}
		m.mu.Lock()
		m.Commands = append(m.Commands, logged)
		m.mu.Unlock()
		switch {
		case cmd == "LOGIN":
			q := quotedRE.FindAllStringSubmatch(rest, -1)
			if len(q) == 2 && unq(q[0][1]) == m.User && unq(q[1][1]) == m.Pass {
				authed = true
				fmt.Fprintf(c, "%s OK logged in\r\n", tag)
			} else {
				fmt.Fprintf(c, "%s NO bad login\r\n", tag)
			}
		case cmd == "LOGOUT":
			fmt.Fprintf(c, "* BYE\r\n%s OK bye\r\n", tag)
			return
		case !authed:
			fmt.Fprintf(c, "%s NO log in first\r\n", tag)
		case cmd == "EXAMINE":
			m.mu.Lock()
			n := len(m.msgs)
			m.mu.Unlock()
			fmt.Fprintf(c, "* %d EXISTS\r\n%s OK [READ-ONLY] examined\r\n", n, tag)
		case cmd == "UID" && strings.HasPrefix(strings.ToUpper(rest), "SEARCH"):
			q := quotedRE.FindAllStringSubmatch(rest, -1)
			var from, subj string
			if len(q) == 2 {
				from, subj = strings.ToLower(unq(q[0][1])), strings.ToLower(unq(q[1][1]))
			}
			var hits []string
			m.mu.Lock()
			for _, msg := range m.msgs {
				if m.Lax || strings.Contains(strings.ToLower(msg.from), from) && strings.Contains(strings.ToLower(msg.subject), subj) {
					hits = append(hits, strconv.Itoa(msg.uid))
				}
			}
			m.mu.Unlock()
			fmt.Fprintf(c, "* SEARCH %s\r\n%s OK search done\r\n", strings.Join(hits, " "), tag)
		case cmd == "UID" && strings.HasPrefix(strings.ToUpper(rest), "FETCH"):
			ff := strings.Fields(rest)
			uid, _ := strconv.Atoi(ff[1])
			m.mu.Lock()
			var msg *mailMsg
			for i := range m.msgs {
				if m.msgs[i].uid == uid {
					msg = &m.msgs[i]
				}
			}
			m.mu.Unlock()
			if msg == nil {
				fmt.Fprintf(c, "%s OK nothing\r\n", tag)
				continue
			}
			hdr := fmt.Sprintf("From: %s\r\nSubject: %s\r\n\r\n", msg.from, msg.subject)
			if strings.Contains(rest, "HEADER.FIELDS") {
				fmt.Fprintf(c, "* %d FETCH (UID %d INTERNALDATE \"%s\" BODY[HEADER.FIELDS (FROM SUBJECT)] {%d}\r\n%s)\r\n%s OK fetched\r\n",
					uid, uid, msg.at.Format("02-Jan-2006 15:04:05 -0700"), len(hdr), hdr, tag)
			} else {
				full := fmt.Sprintf("From: %s\r\nSubject: %s\r\nContent-Type: text/plain\r\nContent-Transfer-Encoding: quoted-printable\r\n\r\n%s\r\n", msg.from, msg.subject, msg.body)
				fmt.Fprintf(c, "* %d FETCH (UID %d BODY[] {%d}\r\n%s)\r\n%s OK fetched\r\n", uid, uid, len(full), full, tag)
			}
		default:
			fmt.Fprintf(c, "%s BAD unsupported\r\n", tag)
		}
	}
}
