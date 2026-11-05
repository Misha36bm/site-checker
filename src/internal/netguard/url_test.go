package netguard

import (
	"strings"
	"testing"
)

func TestValidateURL(t *testing.T) {
	cases := []struct {
		name    string
		url     string
		ok      bool
		blocked bool
	}{
		{name: "https", url: "https://example.com", ok: true},
		{name: "http with path and query", url: "http://example.com/a?b=c", ok: true},
		{name: "uppercase scheme", url: "HTTPS://example.com", ok: true},
		{name: "explicit port", url: "https://example.com:8443/", ok: true},
		{name: "public ipv4", url: "https://1.1.1.1/", ok: true},
		{name: "public ipv6", url: "https://[2606:4700:4700::1111]/", ok: true},
		{name: "trailing dot", url: "https://example.com./", ok: true},
		{name: "digits in a name", url: "https://web2.example.com/", ok: true},
		{name: "name starting with digits", url: "https://123.example.com/", ok: true},
		{name: "hex-looking tld is not hex", url: "https://example.0xyz/", ok: true},

		{name: "empty", url: ""},
		{name: "no scheme", url: "example.com"},
		{name: "ftp", url: "ftp://example.com"},
		{name: "file", url: "file:///etc/passwd"},
		{name: "javascript", url: "javascript:alert(1)"},
		{name: "opaque", url: "https:example.com"},
		{name: "no host", url: "https://"},
		{name: "port without host", url: "https://:443"},
		{name: "username", url: "https://user@example.com"},
		{name: "username and password", url: "https://user:pass@example.com"},
		{name: "port zero", url: "https://example.com:0"},
		{name: "port too large", url: "https://example.com:70000"},
		{name: "space in host", url: "https://exa mple.com"},
		{name: "control character", url: "https://example.com/\x00"},
		{name: "invalid utf-8", url: "https://example.com/\xff"},
		{name: "too long", url: "https://example.com/" + strings.Repeat("a", 3000)},
		{name: "decimal ip", url: "http://2130706433/"},
		{name: "hex ip", url: "http://0x7f000001/"},
		{name: "short hex ip", url: "http://0x7f.1/"},
		{name: "octal ip", url: "http://0177.0.0.1/"},
		{name: "short ip", url: "http://127.1/"},

		{name: "loopback", url: "http://127.0.0.1/", blocked: true},
		{name: "loopback with port", url: "http://127.0.0.1:8080/", blocked: true},
		{name: "loopback trailing dot", url: "http://127.0.0.1./", blocked: true},
		{name: "ipv6 loopback", url: "http://[::1]/", blocked: true},
		{name: "ipv4-mapped loopback", url: "http://[::ffff:127.0.0.1]/", blocked: true},
		{name: "ipv6 zone", url: "http://[fe80::1%25en0]/", blocked: true},
		{name: "private", url: "http://192.168.1.1/", blocked: true},
		{name: "metadata ip", url: "http://169.254.169.254/latest/", blocked: true},
		{name: "metadata name", url: "http://metadata.google.internal/", blocked: true},
		{name: "metadata name uppercase", url: "http://METADATA.GOOGLE.INTERNAL/", blocked: true},
		{name: "localhost", url: "http://localhost/", blocked: true},
		{name: "localhost uppercase", url: "http://LOCALHOST:8080/", blocked: true},
		{name: "localhost trailing dot", url: "http://localhost./", blocked: true},
		{name: "localhost subdomain", url: "http://api.localhost/", blocked: true},
	}
	policy := NewPolicy(nil)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := policy.ValidateURL(tc.url, 2048)
			if tc.ok {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if !IsInvalidURL(err) {
				t.Fatalf("err = %v, want an InvalidURLError", err)
			}
			if IsBlocked(err) != tc.blocked {
				t.Errorf("IsBlocked = %v, want %v (err: %v)", !tc.blocked, tc.blocked, err)
			}
		})
	}
}

func TestValidateURLDoesNotEchoInput(t *testing.T) {
	err := NewPolicy(nil).ValidateURL("https://secret-token@exa mple.com", 2048)
	if err == nil || strings.Contains(err.Error(), "secret-token") {
		t.Fatalf("err = %v", err)
	}
}
