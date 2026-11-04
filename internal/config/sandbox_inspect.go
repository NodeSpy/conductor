package config

import (
	"crypto/x509"
	"fmt"
	"net"
	"net/url"
	"os"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// InspectEntry is one host whose TLS the agent sandbox's egress proxy
// terminates (sandbox.inspect, isolation.inspect): `npm.lan` or
// `{host: npm.lan, ca: /etc/ssl/lan-ca.pem}`. It makes https:// path
// entries for that host enforceable.
type InspectEntry struct {
	Host string `yaml:"host"`
	// CA is a PEM file of extra roots the upstream may chain to (a private
	// mirror's CA), on top of the system roots. Verification is never off.
	CA string `yaml:"ca,omitempty" path:"local"`
}

// UnmarshalYAML accepts a hostname or the map form.
func (e *InspectEntry) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind == yaml.ScalarNode {
		e.Host = n.Value
		return nil
	}
	type plain InspectEntry
	return strictNodeDecode(n, (*plain)(e))
}

var inspectHostRE = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)*$`)

// validateInspect checks one inspect: list.
func validateInspect(where string, list []InspectEntry) error {
	for _, e := range list {
		h := strings.ToLower(strings.TrimSpace(e.Host))
		switch {
		case strings.ContainsAny(h, "*?["):
			return fmt.Errorf("config: %s: %q: inspect takes exact hostnames only — a wildcard would mint certificates for hosts you did not name", where, e.Host)
		case net.ParseIP(strings.Trim(h, "[]")) != nil:
			return fmt.Errorf("config: %s: %q: inspect takes hostnames, not addresses (TLS clients name a host)", where, e.Host)
		case !inspectHostRE.MatchString(h):
			return fmt.Errorf("config: %s: %q: want an exact hostname (no port, scheme or path)", where, e.Host)
		}
		if e.CA != "" {
			b, err := os.ReadFile(e.CA)
			if err != nil {
				return fmt.Errorf("config: %s: %s: ca: %w", where, h, err)
			}
			if !x509.NewCertPool().AppendCertsFromPEM(b) {
				return fmt.Errorf("config: %s: %s: ca: %s holds no PEM certificate", where, h, e.CA)
			}
		}
	}
	return nil
}

// validateInspectedEgress checks every https:// path entry names a host
// inspected where the entry is written: in sandbox.inspect (every launch),
// or in the same isolation: block's inspect. Without it the path could not
// be enforced. (A launch that does not inspect the host still refuses a
// tunnel to it: fail closed.)
func (c *Config) validateInspectedEgress() error {
	global := map[string]bool{}
	var errs []error
	type entries struct {
		where     string
		list      []string
		inspected map[string]bool
	}
	hostsOf := func(list []InspectEntry) map[string]bool {
		m := map[string]bool{}
		for _, e := range list {
			m[strings.ToLower(strings.TrimSpace(e.Host))] = true
		}
		return m
	}
	var all []entries
	if c.Sandbox != nil {
		if err := validateInspect("sandbox.inspect", c.Sandbox.Inspect); err != nil {
			errs = append(errs, err)
		}
		global = hostsOf(c.Sandbox.Inspect)
		all = append(all, entries{"sandbox.egress", c.Sandbox.Egress, nil})
	}
	c.scopeSandboxSites(func(where string, iso *IsolationConfig) {
		if err := validateInspect(where+": isolation.inspect", iso.Inspect); err != nil {
			errs = append(errs, err)
		}
		own := hostsOf(iso.Inspect)
		if iso.EgressOnly != nil {
			all = append(all, entries{where + ": isolation.egress_only", *iso.EgressOnly, own})
		}
		if iso.Network != nil {
			all = append(all, entries{where + ": isolation network.egress", iso.Network.Egress, own})
		}
	})
	if len(errs) > 0 {
		return errs[0]
	}
	for _, es := range all {
		for _, e := range es.list {
			scheme, _, ok := strings.Cut(strings.TrimSpace(e), "://")
			if !ok || !strings.EqualFold(scheme, "https") {
				continue
			}
			u, err := url.Parse(strings.TrimSpace(e))
			if err != nil {
				continue // validateEgressEntry reported it
			}
			if h := strings.ToLower(u.Hostname()); !global[h] && !es.inspected[h] {
				port := u.Port()
				if port == "" {
					port = "443"
				}
				return fmt.Errorf("config: %s: %q: a path cannot be enforced on HTTPS unless conductor inspects that host's TLS — add %s to inspect: in this block (or sandbox.inspect), or list the host alone (%s:%s)", es.where, e, h, h, port)
			}
		}
	}
	return nil
}
