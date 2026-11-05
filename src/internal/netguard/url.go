// Package netguard decides which destinations the application may contact.
package netguard

import (
	"errors"
	"fmt"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"unicode/utf8"
)

// metadataHosts are names of cloud metadata endpoints.
var metadataHosts = map[string]bool{
	"metadata.google.internal": true,
	"metadata.goog":            true,
}

// InvalidURLError describes why a URL was rejected. Its message is safe to return
// to API clients.
type InvalidURLError struct {
	Reason string
	// Blocked means the URL is well formed but its destination is forbidden.
	Blocked bool
}

func (e *InvalidURLError) Error() string {
	return e.Reason
}

// ValidateURL performs the checks that need no network access: the URL's form,
// scheme, and port, the absence of credentials, and whether its host is visibly a
// forbidden destination. The resolved address is checked when connecting.
func (p *Policy) ValidateURL(raw string, maxLength int) error {
	if raw == "" {
		return &InvalidURLError{Reason: "URL is required"}
	}
	if len(raw) > maxLength {
		return &InvalidURLError{Reason: fmt.Sprintf("URL is longer than %d bytes", maxLength)}
	}
	if !utf8.ValidString(raw) {
		return &InvalidURLError{Reason: "URL is malformed"}
	}
	u, err := url.Parse(raw)
	if err != nil {
		// The parser's message quotes the input, so it is not passed on.
		return &InvalidURLError{Reason: "URL is malformed"}
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return &InvalidURLError{Reason: "URL scheme must be http or https"}
	}
	if u.Hostname() == "" {
		return &InvalidURLError{Reason: "URL must include a host"}
	}
	if u.User != nil {
		return &InvalidURLError{Reason: "URL must not include credentials"}
	}
	if port := u.Port(); port != "" {
		if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 {
			return &InvalidURLError{Reason: "URL port is out of range"}
		}
	}
	return p.validateHost(u.Hostname())
}

func (p *Policy) validateHost(hostname string) error {
	blocked := &InvalidURLError{Reason: "URL points to a forbidden address", Blocked: true}

	host := strings.TrimSuffix(strings.ToLower(hostname), ".")
	if addr, err := netip.ParseAddr(host); err == nil {
		if p.Blocked(addr) {
			return blocked
		}
		return nil
	}
	if metadataHosts[host] {
		return blocked
	}
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		if p.Blocked(netip.AddrFrom4([4]byte{127, 0, 0, 1})) {
			return blocked
		}
		return nil
	}
	if endsInNumber(host) {
		return &InvalidURLError{Reason: "URL host is not a valid address"}
	}
	return nil
}

// endsInNumber reports whether browsers would read host as an IPv4 address in
// decimal, octal, or hex, such as 2130706433 or 0x7f.1, for 127.0.0.1. No public
// name ends in a number, so these are rejected rather than decoded.
func endsInNumber(host string) bool {
	label := host[strings.LastIndexByte(host, '.')+1:]
	if label == "" {
		return false
	}
	if hex, ok := strings.CutPrefix(label, "0x"); ok {
		_, err := strconv.ParseUint("0"+hex, 16, 64)
		return err == nil
	}
	return strings.Trim(label, "0123456789") == ""
}

// IsInvalidURL reports whether err is a rejected URL.
func IsInvalidURL(err error) bool {
	var invalid *InvalidURLError
	return errors.As(err, &invalid)
}

// IsBlocked reports whether err means a destination is forbidden, whether that
// was seen in the URL or when connecting.
func IsBlocked(err error) bool {
	var invalid *InvalidURLError
	if errors.As(err, &invalid) {
		return invalid.Blocked
	}
	var blocked *BlockedError
	return errors.As(err, &blocked)
}
