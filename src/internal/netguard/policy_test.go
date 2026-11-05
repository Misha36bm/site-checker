package netguard

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"testing"
)

func TestPolicyBlocked(t *testing.T) {
	cases := []struct {
		addr    string
		blocked bool
	}{
		{"1.1.1.1", false},
		{"8.8.8.8", false},
		{"93.184.216.34", false},
		{"172.15.255.255", false},
		{"172.32.0.0", false},
		{"100.63.255.255", false},
		{"100.128.0.0", false},
		{"2606:4700:4700::1111", false},
		{"2001:4860:4860::8888", false},
		{"::ffff:8.8.8.8", false},

		{"0.0.0.0", true},
		{"0.1.2.3", true},
		{"10.0.0.1", true},
		{"100.64.0.1", true},
		{"100.127.255.255", true},
		{"127.0.0.1", true},
		{"127.255.255.255", true},
		{"169.254.0.1", true},
		{"169.254.169.254", true},
		{"172.16.0.1", true},
		{"172.31.255.255", true},
		{"192.0.0.1", true},
		{"192.0.2.1", true},
		{"192.88.99.1", true},
		{"192.168.0.1", true},
		{"198.18.0.1", true},
		{"198.19.255.255", true},
		{"198.51.100.1", true},
		{"203.0.113.1", true},
		{"224.0.0.1", true},
		{"239.255.255.255", true},
		{"240.0.0.1", true},
		{"255.255.255.255", true},

		{"::", true},
		{"::1", true},
		{"::ffff:127.0.0.1", true},
		{"::ffff:10.0.0.1", true},
		{"::ffff:169.254.169.254", true},
		{"::127.0.0.1", true},
		{"64:ff9b::7f00:1", true},
		{"64:ff9b:1::1", true},
		{"100::1", true},
		{"2001::1", true},
		{"2001:db8::1", true},
		{"2002:7f00:1::", true},
		{"3fff::1", true},
		{"fc00::1", true},
		{"fd00:ec2::254", true},
		{"fe80::1", true},
		{"fe80::1%en0", true},
		{"fec0::1", true},
		{"ff02::1", true},
	}
	policy := NewPolicy(nil)
	for _, tc := range cases {
		t.Run(tc.addr, func(t *testing.T) {
			if got := policy.Blocked(netip.MustParseAddr(tc.addr)); got != tc.blocked {
				t.Errorf("Blocked = %v, want %v", got, tc.blocked)
			}
		})
	}
}

func TestPolicyBlocksInvalidAddress(t *testing.T) {
	if !NewPolicy(nil).Blocked(netip.Addr{}) {
		t.Error("the zero address must be blocked")
	}
}

func TestPolicyAllowedCIDRs(t *testing.T) {
	policy := NewPolicy([]netip.Prefix{
		netip.MustParsePrefix("10.1.0.0/16"),
		netip.MustParsePrefix("169.254.0.0/16"),
		netip.MustParsePrefix("0.0.0.0/0"),
	})
	if policy.Blocked(netip.MustParseAddr("10.1.2.3")) {
		t.Error("an allowed address is blocked")
	}
	if policy.Blocked(netip.MustParseAddr("::ffff:10.1.2.3")) {
		t.Error("an allowed IPv4-mapped address is blocked")
	}

	for _, metadata := range []string{"169.254.169.254", "::ffff:169.254.169.254", "168.63.129.16", "100.100.100.200"} {
		if !policy.Blocked(netip.MustParseAddr(metadata)) {
			t.Errorf("metadata address %s must stay blocked", metadata)
		}
	}
}

func TestPolicyAllowedCIDRsAreNarrow(t *testing.T) {
	policy := NewPolicy([]netip.Prefix{netip.MustParsePrefix("10.1.0.0/16")})
	if !policy.Blocked(netip.MustParseAddr("10.2.0.1")) {
		t.Error("an address outside the allowed range is not blocked")
	}
}

type fakeResolver map[string][]string

func (f fakeResolver) LookupNetIP(_ context.Context, _, host string) ([]netip.Addr, error) {
	values, ok := f[host]
	if !ok {
		return nil, &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
	}
	addrs := make([]netip.Addr, len(values))
	for i, value := range values {
		addrs[i] = netip.MustParseAddr(value)
	}
	return addrs, nil
}

// listen returns a local listener and a counter of accepted connections.
func listen(t *testing.T) (string, <-chan struct{}) {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	accepted := make(chan struct{}, 10)
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			conn.Close()
			accepted <- struct{}{}
		}
	}()
	_, port, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	return port, accepted
}

func TestDialerRefusesBlockedAddresses(t *testing.T) {
	port, accepted := listen(t)
	cases := map[string]string{
		"literal loopback":            "127.0.0.1",
		"name resolving to loopback":  "rebind.test",
		"mix of public and internal":  "mixed.test",
		"name resolving to metadata":  "cloud.test",
		"name resolving to mapped ip": "mapped.test",
	}
	dialer := &Dialer{
		Policy: NewPolicy(nil),
		Resolver: fakeResolver{
			"rebind.test": {"127.0.0.1"},
			"mixed.test":  {"1.1.1.1", "127.0.0.1"},
			"cloud.test":  {"169.254.169.254"},
			"mapped.test": {"::ffff:127.0.0.1"},
		},
	}
	for name, host := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := dialer.DialContext(context.Background(), "tcp", net.JoinHostPort(host, port))
			var blocked *BlockedError
			if !errors.As(err, &blocked) {
				t.Fatalf("err = %v, want a BlockedError", err)
			}
		})
	}
	select {
	case <-accepted:
		t.Error("a forbidden address received a connection")
	default:
	}
}

func TestDialerConnectsToPermittedAddress(t *testing.T) {
	port, accepted := listen(t)
	dialer := &Dialer{
		Policy:   NewPolicy([]netip.Prefix{netip.MustParsePrefix("127.0.0.0/8")}),
		Resolver: fakeResolver{"site.test": {"127.0.0.1"}},
	}
	conn, err := dialer.DialContext(context.Background(), "tcp", net.JoinHostPort("site.test", port))
	if err != nil {
		t.Fatal(err)
	}
	conn.Close()
	<-accepted
}

func TestDialerControlRechecksTheAddress(t *testing.T) {
	dialer := &Dialer{Policy: NewPolicy(nil)}
	var blocked *BlockedError
	if err := dialer.control("tcp4", "127.0.0.1:80", nil); !errors.As(err, &blocked) {
		t.Errorf("err = %v, want a BlockedError", err)
	}
	if err := dialer.control("tcp4", "1.1.1.1:80", nil); err != nil {
		t.Errorf("err = %v", err)
	}
	if err := dialer.control("udp4", "1.1.1.1:53", nil); err == nil {
		t.Error("UDP must be refused")
	}
}

func TestDialerReportsDNSErrors(t *testing.T) {
	dialer := &Dialer{Policy: NewPolicy(nil), Resolver: fakeResolver{"empty.test": {}}}
	for _, host := range []string{"missing.test", "empty.test"} {
		_, err := dialer.DialContext(context.Background(), "tcp", net.JoinHostPort(host, "80"))
		var dnsErr *net.DNSError
		if !errors.As(err, &dnsErr) {
			t.Errorf("%s: err = %v, want a DNSError", host, err)
		}
	}
}
