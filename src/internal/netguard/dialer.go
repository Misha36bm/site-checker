package netguard

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"syscall"
	"time"
)

const dialAttemptTimeout = 5 * time.Second

// BlockedError means a connection to a forbidden address was refused. It names no
// address, so it cannot reveal internal network details if it reaches a client.
type BlockedError struct{}

func (*BlockedError) Error() string {
	return "destination address is forbidden"
}

// Resolver looks up host names. *net.Resolver implements it.
type Resolver interface {
	LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error)
}

// Dialer connects only to addresses its Policy permits. Resolving once and then
// dialing the resulting IP leaves no second lookup to rebind, and the check in
// Control runs on the address actually being connected to.
type Dialer struct {
	Policy   *Policy
	Resolver Resolver
}

// DialContext implements the signature of http.Transport.DialContext. If a name
// resolves to several addresses and any is forbidden, the connection is refused:
// a mix of public and internal addresses is what a rebinding attack looks like.
func (d *Dialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}
	addrs, err := d.resolve(ctx, host)
	if err != nil {
		return nil, err
	}
	for _, addr := range addrs {
		if d.Policy.Blocked(addr) {
			return nil, &BlockedError{}
		}
	}

	dialer := net.Dialer{Timeout: dialAttemptTimeout, Control: d.control}
	var lastErr error
	for _, addr := range addrs {
		conn, err := dialer.DialContext(ctx, network, net.JoinHostPort(addr.String(), port))
		if err == nil {
			return conn, nil
		}
		lastErr = err
	}
	return nil, lastErr
}

func (d *Dialer) resolve(ctx context.Context, host string) ([]netip.Addr, error) {
	if addr, err := netip.ParseAddr(host); err == nil {
		return []netip.Addr{addr}, nil
	}
	addrs, err := d.Resolver.LookupNetIP(ctx, "ip", host)
	if err != nil {
		return nil, err
	}
	if len(addrs) == 0 {
		return nil, &net.DNSError{Err: "no addresses found", Name: host, IsNotFound: true}
	}
	return addrs, nil
}

func (d *Dialer) control(network, address string, _ syscall.RawConn) error {
	switch network {
	case "tcp4", "tcp6":
	default:
		return fmt.Errorf("network %q is not supported", network)
	}
	addrPort, err := netip.ParseAddrPort(address)
	if err != nil {
		return err
	}
	if d.Policy.Blocked(addrPort.Addr()) {
		return &BlockedError{}
	}
	return nil
}
