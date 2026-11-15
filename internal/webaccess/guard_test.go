package webaccess

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"strings"
	"testing"
)

func TestAddressPolicy(t *testing.T) {
	allow := []netip.Prefix{netip.MustParsePrefix("198.18.0.0/15")}
	denied := []string{
		"0.0.0.0", "10.1.2.3", "100.64.0.1", "127.0.0.1", "169.254.169.254",
		"172.16.0.1", "192.0.0.8", "192.0.2.1", "192.88.99.1", "192.168.1.1",
		"198.51.100.4", "203.0.113.9", "224.0.0.1", "255.255.255.255",
		"168.63.129.16", "::1", "fe80::1", "fc00::1", "4000::1",
		"2001:db8::1", "2002::1", "3fff::1",
	}
	for _, raw := range denied {
		addr := netip.MustParseAddr(raw)
		if addressPermitted(addr, allow) {
			t.Errorf("%s should be denied", raw)
		}
	}
	for _, raw := range []string{"93.184.216.34", "8.8.8.8", "2606:4700::1111", "::ffff:93.184.216.34", "198.18.0.7"} {
		addr := netip.MustParseAddr(raw)
		if !addressPermitted(addr, allow) {
			t.Errorf("%s should be permitted", raw)
		}
	}
}

type recordingDialer struct {
	addresses []string
	resolve   resolverFunc
	calls     int
	dialFn    func(ctx context.Context, network, address string) (net.Conn, error)
}

func (r *recordingDialer) dial(ctx context.Context, network, address string) (net.Conn, error) {
	r.addresses = append(r.addresses, address)
	if r.dialFn != nil {
		return r.dialFn(ctx, network, address)
	}
	client, server := net.Pipe()
	_ = server.Close()
	return client, nil
}

func TestCheckedLiteralIsDialed(t *testing.T) {
	rec := &recordingDialer{}
	rec.resolve = func(ctx context.Context, host string) ([]netip.Addr, error) {
		rec.calls++
		return []netip.Addr{netip.MustParseAddr("93.184.216.34")}, nil
	}
	d := &guardedDialer{resolve: rec.resolve, dial: rec.dial}
	conn, err := d.DialContext(context.Background(), "tcp", "example.com:443")
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	_ = conn.Close()
	if rec.calls != 1 {
		t.Fatalf("resolver calls = %d, want 1", rec.calls)
	}
	if len(rec.addresses) != 1 || rec.addresses[0] != "93.184.216.34:443" {
		t.Fatalf("dialed %v", rec.addresses)
	}
}

func TestMixedDNSAnswerRejected(t *testing.T) {
	rec := &recordingDialer{}
	rec.resolve = func(ctx context.Context, host string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("93.184.216.34"), netip.MustParseAddr("10.0.0.5")}, nil
	}
	d := &guardedDialer{resolve: rec.resolve, dial: rec.dial}
	_, err := d.DialContext(context.Background(), "tcp", "mixed.example:443")
	if err == nil || !strings.Contains(err.Error(), classBlocked) {
		t.Fatalf("err = %v, want blocked", err)
	}
	if len(rec.addresses) != 0 {
		t.Fatalf("must not dial mixed answers: %v", rec.addresses)
	}
}

func TestLoopbackExemptionIsOriginScoped(t *testing.T) {
	rec := &recordingDialer{}
	rec.resolve = func(ctx context.Context, host string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil
	}
	provider := &guardedDialer{loopbackOrigin: "127.0.0.1:8080", resolve: rec.resolve, dial: rec.dial}
	if conn, err := provider.DialContext(context.Background(), "tcp", "127.0.0.1:8080"); err != nil {
		t.Fatalf("provider loopback: %v", err)
	} else {
		_ = conn.Close()
	}
	if _, err := provider.DialContext(context.Background(), "tcp", "127.0.0.1:9999"); err == nil {
		t.Fatal("provider exemption must not cover another port")
	}

	strict := &guardedDialer{resolve: rec.resolve, dial: rec.dial}
	if _, err := strict.DialContext(context.Background(), "tcp", "127.0.0.1:8080"); err == nil {
		t.Fatal("page pool must not inherit the provider loopback exemption")
	}
}

func TestLiteralIPCheckedBeforeDial(t *testing.T) {
	rec := &recordingDialer{}
	d := &guardedDialer{dial: rec.dial, resolve: rec.resolve}
	_, err := d.DialContext(context.Background(), "tcp", "169.254.169.254:80")
	if err == nil || !strings.Contains(err.Error(), classBlocked) {
		t.Fatalf("err = %v", err)
	}
	if len(rec.addresses) != 0 {
		t.Fatalf("denied literal was dialed: %v", rec.addresses)
	}
}

func TestResolverCalledOncePerAttempt(t *testing.T) {
	rec := &recordingDialer{}
	rec.resolve = func(ctx context.Context, host string) ([]netip.Addr, error) {
		rec.calls++
		if rec.calls > 1 {
			return nil, errors.New("second lookup")
		}
		return []netip.Addr{netip.MustParseAddr("93.184.216.34")}, nil
	}
	rec.dialFn = func(ctx context.Context, network, address string) (net.Conn, error) {
		return nil, errors.New("refused")
	}
	d := &guardedDialer{resolve: rec.resolve, dial: rec.dial}
	_, _ = d.DialContext(context.Background(), "tcp", "example.com:443")
	if rec.calls != 1 {
		t.Fatalf("resolver calls = %d, want 1", rec.calls)
	}
}

func TestLoopbackExemptionRejectsPublicResolution(t *testing.T) {
	rec := &recordingDialer{}
	rec.resolve = func(ctx context.Context, host string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("93.184.216.34")}, nil
	}
	d := &guardedDialer{loopbackOrigin: "localhost:8080", resolve: rec.resolve, dial: rec.dial}
	_, err := d.DialContext(context.Background(), "tcp", "localhost:8080")
	if err == nil || !strings.Contains(err.Error(), classBlocked) {
		t.Fatalf("err = %v, want blocked", err)
	}
	if len(rec.addresses) != 0 {
		t.Fatalf("public answer was dialed under a loopback exemption: %v", rec.addresses)
	}
}
