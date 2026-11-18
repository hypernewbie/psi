package webaccess

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"time"
)

// Error classes are fixed, safe labels. Messages never repeat resolved
// addresses, provider bodies, headers, or configuration values.
const (
	classValidation = "validation"
	classBlocked    = "blocked target"
	classDNS        = "dns"
	classTransport  = "tls/transport"
	classTimeout    = "timeout"
	classCancelled  = "cancellation"
	classStatus     = "http status"
	classInvalid    = "invalid response"
	classRateLimit  = "rate limit"
)

// Denied IPv4 ranges. The explicit table complements netip predicates so a
// future predicate change cannot silently open a reserved range.
var deniedIPv4 = mustPrefixes(
	"0.0.0.0/8", "10.0.0.0/8", "100.64.0.0/10", "127.0.0.0/8",
	"169.254.0.0/16", "172.16.0.0/12", "192.0.0.0/24", "192.0.2.0/24",
	"192.88.99.0/24", "192.168.0.0/16", "198.18.0.0/15",
	"198.51.100.0/24", "203.0.113.0/24", "224.0.0.0/4", "240.0.0.0/4",
	"168.63.129.16/32",
)

var deniedIPv6 = mustPrefixes(
	"2001::/23", "2001:db8::/32", "2002::/16", "3fff::/20",
)

var publicIPv6 = mustPrefixes("2000::/3")

func mustPrefixes(raw ...string) []netip.Prefix {
	out := make([]netip.Prefix, 0, len(raw))
	for _, item := range raw {
		prefix, err := netip.ParsePrefix(item)
		if err != nil {
			panic(err)
		}
		out = append(out, prefix)
	}
	return out
}

func inPrefixes(addr netip.Addr, prefixes []netip.Prefix) bool {
	for _, prefix := range prefixes {
		if prefix.Contains(addr) {
			return true
		}
	}
	return false
}

// addressPermitted reports whether a destination is public or explicitly
// exempted. allowRanges bypass the denial, not URL or TLS checks.
func addressPermitted(addr netip.Addr, allow []netip.Prefix) bool {
	addr = addr.Unmap()
	if !addr.IsValid() {
		return false
	}
	if inPrefixes(addr, allow) {
		return true
	}
	if addr.Is4() {
		return !inPrefixes(addr, deniedIPv4)
	}
	if !inPrefixes(addr, publicIPv6) {
		return false
	}
	return !inPrefixes(addr, deniedIPv6)
}

type resolverFunc func(ctx context.Context, host string) ([]netip.Addr, error)

type dialFunc func(ctx context.Context, network, address string) (net.Conn, error)

// guardedDialer resolves once, checks every answer, and dials a checked
// literal address. The original host stays in the request for Host and SNI.
type guardedDialer struct {
	allow          []netip.Prefix
	loopbackOrigin string // host:port of the configured provider origin
	resolve        resolverFunc
	dial           dialFunc
}

func defaultResolver(ctx context.Context, host string) ([]netip.Addr, error) {
	return net.DefaultResolver.LookupNetIP(ctx, "ip", host)
}

func defaultDialer() *net.Dialer {
	return &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
}

// DialContext implements the transport dial hook.
func (d *guardedDialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	dialCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, fmt.Errorf("%s: invalid address", classValidation)
	}
	if addr, err := netip.ParseAddr(host); err == nil {
		if !d.permits(addr, address) {
			return nil, fmt.Errorf("%s: destination is not public", classBlocked)
		}
		return d.dialLiteral(dialCtx, network, addr, port)
	}

	addrs, err := d.resolve(dialCtx, host)
	if err != nil {
		return nil, wrapClass(dialCtx, classDNS, "lookup failed")
	}
	if len(addrs) == 0 {
		return nil, fmt.Errorf("%s: lookup returned no addresses", classDNS)
	}
	// A mixed answer set is suspicious: one denied address blocks the host.
	for _, addr := range addrs {
		if !d.permits(addr, address) {
			return nil, fmt.Errorf("%s: destination is not public", classBlocked)
		}
	}
	var lastErr error
	for _, addr := range addrs {
		conn, err := d.dialLiteral(dialCtx, network, addr, port)
		if err == nil {
			return conn, nil
		}
		lastErr = err
	}
	if lastErr == nil {
		lastErr = errors.New("no usable address")
	}
	return nil, lastErr
}

func (d *guardedDialer) dialLiteral(ctx context.Context, network string, addr netip.Addr, port string) (net.Conn, error) {
	target := net.JoinHostPort(addr.String(), port)
	conn, err := d.dial(ctx, network, target)
	if err != nil {
		if ctx.Err() != nil {
			return nil, wrapClass(ctx, classTimeout, "dial interrupted")
		}
		var netErr net.Error
		if errors.As(err, &netErr) && netErr.Timeout() {
			return nil, fmt.Errorf("%s: dial timed out", classTimeout)
		}
		return nil, fmt.Errorf("%s: dial failed", classTransport)
	}
	return conn, nil
}

func (d *guardedDialer) permits(addr netip.Addr, address string) bool {
	// A configured loopback provider origin must resolve only to loopback.
	// Otherwise DNS could mix a public answer into the exception and receive
	// provider headers intended for the local instance.
	if d.loopbackOrigin != "" && strings.EqualFold(address, d.loopbackOrigin) {
		return addr.Unmap().IsLoopback()
	}
	return addressPermitted(addr, d.allow)
}

// hasClass recognizes our fixed class prefixes without matching a hostname
// such as dns.example inside a wrapped transport error.
func hasClass(message, class string) bool {
	return strings.HasPrefix(message, class+":") || strings.Contains(message, ": "+class+":")
}

func wrapClass(ctx context.Context, class, message string) error {
	if errors.Is(ctx.Err(), context.Canceled) {
		return fmt.Errorf("%s: cancelled", classCancelled)
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return fmt.Errorf("%s: %s", classTimeout, message)
	}
	return fmt.Errorf("%s: %s", class, message)
}

// newHTTPTransport builds a private pool. Proxy settings and environment
// proxies are deliberately ignored. Idle connections are bounded across all
// origins, because the pool can outlive any one session.
func newHTTPTransport(dial func(ctx context.Context, network, address string) (net.Conn, error)) *http.Transport {
	return &http.Transport{
		Proxy:                  nil,
		DialContext:            dial,
		TLSHandshakeTimeout:    10 * time.Second,
		ResponseHeaderTimeout:  30 * time.Second,
		MaxResponseHeaderBytes: 64 << 10,
		MaxIdleConns:           32,
		IdleConnTimeout:        90 * time.Second,
		ForceAttemptHTTP2:      true,
	}
}
