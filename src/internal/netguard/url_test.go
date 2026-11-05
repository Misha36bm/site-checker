package netguard

import (
	"strings"
	"testing"
)

func TestValidateURL(t *testing.T) {
	cases := []struct {
		name string
		url  string
		ok   bool
	}{
		{"https", "https://example.com", true},
		{"http with path and query", "http://example.com/a?b=c", true},
		{"uppercase scheme", "HTTPS://example.com", true},
		{"explicit port", "https://example.com:8443/", true},
		{"ipv6 host", "https://[2606:4700:4700::1111]/", true},
		{"empty", "", false},
		{"no scheme", "example.com", false},
		{"ftp", "ftp://example.com", false},
		{"file", "file:///etc/passwd", false},
		{"javascript", "javascript:alert(1)", false},
		{"opaque", "https:example.com", false},
		{"no host", "https://", false},
		{"port without host", "https://:443", false},
		{"username", "https://user@example.com", false},
		{"username and password", "https://user:pass@example.com", false},
		{"port zero", "https://example.com:0", false},
		{"port too large", "https://example.com:70000", false},
		{"space in host", "https://exa mple.com", false},
		{"control character", "https://example.com/\x00", false},
		{"too long", "https://example.com/" + strings.Repeat("a", 100), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateURL(tc.url, 64)
			if tc.ok && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !tc.ok && !IsInvalidURL(err) {
				t.Fatalf("err = %v, want an InvalidURLError", err)
			}
		})
	}
}

func TestValidateURLDoesNotEchoInput(t *testing.T) {
	err := ValidateURL("https://secret-token@exa mple.com", 2048)
	if err == nil || strings.Contains(err.Error(), "secret-token") {
		t.Fatalf("err = %v", err)
	}
}
