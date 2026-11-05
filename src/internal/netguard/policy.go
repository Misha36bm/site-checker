package netguard

import (
	"net/netip"
)

// blockedIPv4 lists every non-public IPv4 range.
var blockedIPv4 = mustPrefixes(
	"0.0.0.0/8",       // "this network"
	"10.0.0.0/8",      // private
	"100.64.0.0/10",   // carrier-grade NAT
	"127.0.0.0/8",     // loopback
	"169.254.0.0/16",  // link-local
	"172.16.0.0/12",   // private
	"192.0.0.0/24",    // IETF protocol assignments
	"192.0.2.0/24",    // documentation
	"192.88.99.0/24",  // deprecated 6to4 relay
	"192.168.0.0/16",  // private
	"198.18.0.0/15",   // benchmarking
	"198.51.100.0/24", // documentation
	"203.0.113.0/24",  // documentation
	"224.0.0.0/4",     // multicast
	"240.0.0.0/4",     // reserved, including broadcast
)

// globalIPv6 is the only IPv6 space in use on the public internet. Everything
// outside it is blocked, which covers loopback, link-local, unique local,
// multicast, and NAT64.
var globalIPv6 = netip.MustParsePrefix("2000::/3")

// blockedIPv6 lists the non-public ranges inside globalIPv6.
var blockedIPv6 = mustPrefixes(
	"2001::/23",     // IETF special purpose, including Teredo
	"2001:db8::/32", // documentation
	"2002::/16",     // 6to4, which embeds arbitrary IPv4 addresses
	"3fff::/20",     // documentation
)

// metadataAddrs are cloud metadata endpoints. SSRF_ALLOWED_CIDRS cannot exempt
// them: they hand out credentials, and no monitoring target needs them.
var metadataAddrs = map[netip.Addr]bool{
	netip.MustParseAddr("169.254.169.254"): true, // AWS, GCP, Azure, and others
	netip.MustParseAddr("168.63.129.16"):   true, // Azure wire server
	netip.MustParseAddr("100.100.100.200"): true, // Alibaba Cloud
	netip.MustParseAddr("fd00:ec2::254"):   true, // AWS over IPv6
}

// Policy decides which IP addresses the application may connect to.
type Policy struct {
	allowed []netip.Prefix
}

// NewPolicy returns a Policy blocking all non-public addresses, except those in
// allowed.
func NewPolicy(allowed []netip.Prefix) *Policy {
	return &Policy{allowed: allowed}
}

// Blocked reports whether connecting to addr is forbidden.
func (p *Policy) Blocked(addr netip.Addr) bool {
	if !addr.IsValid() {
		return true
	}
	// Without Unmap, ::ffff:127.0.0.1 would be judged as IPv6.
	addr = addr.Unmap().WithZone("")
	if metadataAddrs[addr] {
		return true
	}
	for _, prefix := range p.allowed {
		if prefix.Contains(addr) {
			return false
		}
	}
	if addr.Is4() {
		return containsAny(blockedIPv4, addr)
	}
	return !globalIPv6.Contains(addr) || containsAny(blockedIPv6, addr)
}

func containsAny(prefixes []netip.Prefix, addr netip.Addr) bool {
	for _, prefix := range prefixes {
		if prefix.Contains(addr) {
			return true
		}
	}
	return false
}

func mustPrefixes(values ...string) []netip.Prefix {
	prefixes := make([]netip.Prefix, len(values))
	for i, value := range values {
		prefixes[i] = netip.MustParsePrefix(value)
	}
	return prefixes
}
