package pkgproxy

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
)

// Hex serves a Hex repository (mix, rebar3). Its registry resources
// (names, versions, packages/<name>) are signed by the repository and
// verified by the client, so they are relayed unchanged; a tarball is
// verified against the outer checksum in the package's signed registry
// entry before it is cached.
type Hex struct{ *registry }

// DefaultHexUpstream is the hex.pm repository.
const DefaultHexUpstream = "https://repo.hex.pm"

// NewHex builds the Hex ecosystem.
func NewHex(cfg RegistryConfig) (*Hex, error) {
	r, err := newRegistry("hex", DefaultHexUpstream, cfg)
	if err != nil {
		return nil, err
	}
	return &Hex{r}, nil
}

// Name is the URL segment.
func (h *Hex) Name() string { return "hex" }

// AgentEnv points mix (HEX_MIRROR) and rebar3 (HEX_CDN) at base.
func (h *Hex) AgentEnv(base string) []string {
	return []string{"HEX_MIRROR=" + base, "HEX_CDN=" + base}
}

var (
	hexName = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)
	hexVer  = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+([-+][0-9A-Za-z.+-]{1,100})?$`)
)

const hexMetaMax = 64 << 20

// Serve routes names, versions, packages/<name> and tarballs/<name>-<ver>.tar.
func (h *Hex) Serve(w http.ResponseWriter, r *http.Request, c *Call) {
	bad := func(msg string) {
		st, m := h.fail(w, http.StatusBadRequest, msg)
		c.Emit(Event{Artifact: "request", Status: st, Err: m})
	}
	relay := func(p, what string) {
		st, msg := h.passthrough(w, r.Context(), h.upstream+"/"+p, "", what, hexMetaMax, "application/octet-stream")
		if msg != "" {
			c.Emit(Event{Module: what, Artifact: "metadata", Upstream: true, Status: st, Err: msg})
		}
	}
	rest := c.Rest
	switch {
	case rest == "names" || rest == "versions" || rest == "public_key":
		relay(rest, rest)
	case strings.HasPrefix(rest, "packages/"):
		name := strings.TrimPrefix(rest, "packages/")
		if !hexName.MatchString(name) {
			bad("bad package name")
			return
		}
		relay(rest, name)
	case strings.HasPrefix(rest, "tarballs/") && strings.HasSuffix(rest, ".tar"):
		name, ver, _ := strings.Cut(strings.TrimSuffix(strings.TrimPrefix(rest, "tarballs/"), ".tar"), "-")
		if !hexName.MatchString(name) || !hexVer.MatchString(ver) {
			bad("bad tarball name")
			return
		}
		d, err := h.outerChecksum(r.Context(), name, ver)
		if err != nil {
			var st int
			var msg string
			if nf, ok := err.(errNotFound); ok {
				st, msg = h.notFound(w, name+" "+ver, nf.msg)
			} else {
				st, msg = h.fail(w, http.StatusBadGateway, err.Error())
			}
			c.Emit(Event{Module: name, Version: ver, Artifact: "artifact", Status: st, Err: msg})
			return
		}
		h.serveArtifact(w, r, c, artifact{Name: name, Version: ver, URL: h.upstream + "/" + rest, Digest: d}, h.isPrivate(name))
	default:
		bad("not a registry resource or tarball path")
	}
}

// outerChecksum reads name@ver's outer checksum from its registry entry:
// gzip(Signed{payload=1}) → Package{releases=1} → Release{version=1,
// outer_checksum=5}. The proxy does not check the signature (the client
// does, on the same bytes); the checksum guards the tarball the same way.
func (h *Hex) outerChecksum(ctx context.Context, name, ver string) (Digest, error) {
	b, err := h.meta.get(ctx, h.fetch, h.upstream+"/packages/"+name, "", hexMetaMax)
	if err != nil {
		return Digest{}, err
	}
	zr, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		return Digest{}, fmt.Errorf("%s registry entry: %w", name, err)
	}
	raw, err := io.ReadAll(io.LimitReader(zr, hexMetaMax))
	if err != nil {
		return Digest{}, fmt.Errorf("%s registry entry: %w", name, err)
	}
	payload, err := pbField(raw, 1)
	if err != nil {
		return Digest{}, fmt.Errorf("%s registry entry: %w", name, err)
	}
	var found Digest
	err = pbEach(payload, 1, func(rel []byte) error {
		v, err := pbField(rel, 1)
		if err != nil || string(v) != ver {
			return err
		}
		sum, err := pbField(rel, 5)
		if err != nil {
			return err
		}
		if len(sum) != 32 {
			return fmt.Errorf("%s %s: no outer checksum", name, ver)
		}
		found = Digest{Algo: "sha256", Sum: sum}
		return nil
	})
	if err != nil {
		return Digest{}, fmt.Errorf("%s registry entry: %w", name, err)
	}
	if found.IsZero() {
		return Digest{}, errNotFound{msg: "no " + name + " " + ver + " in the registry"}
	}
	return found, nil
}

var errPB = errors.New("malformed protobuf")

// pbEach calls fn with every length-delimited field num of msg.
func pbEach(msg []byte, num uint64, fn func([]byte) error) error {
	for len(msg) > 0 {
		key, n := binary.Uvarint(msg)
		if n <= 0 {
			return errPB
		}
		msg = msg[n:]
		switch key & 7 {
		case 0:
			_, n := binary.Uvarint(msg)
			if n <= 0 {
				return errPB
			}
			msg = msg[n:]
		case 1:
			if len(msg) < 8 {
				return errPB
			}
			msg = msg[8:]
		case 5:
			if len(msg) < 4 {
				return errPB
			}
			msg = msg[4:]
		case 2:
			l, n := binary.Uvarint(msg)
			if n <= 0 || l > uint64(len(msg)-n) {
				return errPB
			}
			v := msg[n : n+int(l)]
			msg = msg[n+int(l):]
			if key>>3 == num {
				if err := fn(v); err != nil {
					return err
				}
			}
		default:
			return errPB
		}
	}
	return nil
}

// pbField is the last length-delimited field num of msg (nil if absent).
func pbField(msg []byte, num uint64) ([]byte, error) {
	var out []byte
	err := pbEach(msg, num, func(v []byte) error { out = v; return nil })
	return out, err
}
