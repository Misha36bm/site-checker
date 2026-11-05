package browser

import (
	"context"
	"errors"
	"net/netip"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

type fakeResolver struct {
	addrs []netip.Addr
	err   error
}

func (f fakeResolver) LookupNetIP(context.Context, string, string) ([]netip.Addr, error) {
	return f.addrs, f.err
}

func TestEndpoint(t *testing.T) {
	boom := errors.New("boom")
	cases := []struct {
		name     string
		cdpURL   string
		resolver fakeResolver
		want     string
		wantErr  bool
	}{
		{
			name:     "name is resolved",
			cdpURL:   "http://browser:9222",
			resolver: fakeResolver{addrs: []netip.Addr{netip.MustParseAddr("172.20.0.3")}},
			want:     "http://172.20.0.3:9222",
		},
		{
			name:     "mapped address is unwrapped",
			cdpURL:   "http://browser:9222",
			resolver: fakeResolver{addrs: []netip.Addr{netip.MustParseAddr("::ffff:172.20.0.3")}},
			want:     "http://172.20.0.3:9222",
		},
		{
			name:     "ipv6 is bracketed",
			cdpURL:   "http://browser:9222",
			resolver: fakeResolver{addrs: []netip.Addr{netip.MustParseAddr("fd00::3")}},
			want:     "http://[fd00::3]:9222",
		},
		{
			name:     "literal address needs no lookup",
			cdpURL:   "http://10.0.0.5:9222",
			resolver: fakeResolver{err: boom},
			want:     "http://10.0.0.5:9222",
		},
		{name: "lookup failure", cdpURL: "http://browser:9222", resolver: fakeResolver{err: boom}, wantErr: true},
		{name: "no addresses", cdpURL: "http://browser:9222", wantErr: true},
		{name: "malformed url", cdpURL: "http://bro wser", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := New(Config{CDPURL: tc.cdpURL, Concurrency: 1}, tc.resolver)
			got, err := b.endpoint(context.Background())
			if tc.wantErr {
				if err == nil {
					t.Fatalf("got %q, want an error", got)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestCaptureWaitsForAFreeSlotUntilCancelled(t *testing.T) {
	b := New(Config{CDPURL: "http://browser:9222", Concurrency: 1, Timeout: time.Second}, fakeResolver{})
	b.slots <- struct{}{} // all slots taken

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := b.Capture(ctx, "https://example.com"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v", err)
	}
}

func TestCaptureReportsUnreachableBrowser(t *testing.T) {
	b := New(Config{CDPURL: "http://browser:9222", Concurrency: 1, Timeout: time.Second}, fakeResolver{err: errors.New("no such host")})
	if _, err := b.Capture(context.Background(), "https://example.com"); err == nil {
		t.Fatal("expected an error")
	}
	if len(b.slots) != 0 {
		t.Error("the slot was not returned")
	}
}

func TestHash(t *testing.T) {
	cases := map[string]string{
		// Same output as PHP's rtrim(strtr(base64_encode(hash('sha256', $s, true)), '+/', '-_'), '=').
		"abc":                  "ungWv48Bz-pBQUDeXa4iI7ADYaOWF3qctBD_YfIAFa0",
		"":                     "47DEQpj8HBSa-_TImW-5JCeuQeRkm5NMpJWZG3hSuFU",
		"https://example.com/": "DxFdsGK3wN0DCxaHjJnepcNUtJ3DezjriEYXnHeD6dc",
	}
	for input, want := range cases {
		if got := Hash(input); got != want {
			t.Errorf("Hash(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestFileNameIsSafe(t *testing.T) {
	urls := []string{
		"https://example.com",
		"https://example.com/../../etc/passwd",
		"https://example.com/a?token=secret",
		"https://example.com/" + strings.Repeat("a", 2000),
		"https://кіт.укр/",
		"https://[2606:4700::1111]/",
	}
	valid := regexp.MustCompile(`^[A-Za-z0-9_-]{43}\.png$`)
	for _, raw := range urls {
		name, err := FileName(raw)
		if err != nil {
			t.Fatalf("%s: %v", raw, err)
		}
		if !valid.MatchString(name) {
			t.Errorf("%s: name = %q", raw, name)
		}
	}
}

func TestEquivalentURLsShareAFile(t *testing.T) {
	groups := [][]string{
		{"https://example.com", "https://example.com/", "HTTPS://EXAMPLE.COM/", "https://example.com:443/", "https://example.com./", "https://example.com/#top"},
		{"http://example.com/a?x=1", "http://example.com:80/a?x=1", "http://EXAMPLE.com/a?x=1#y"},
	}
	for _, group := range groups {
		want, err := FileName(group[0])
		if err != nil {
			t.Fatal(err)
		}
		for _, raw := range group[1:] {
			if got, err := FileName(raw); err != nil || got != want {
				t.Errorf("%s: got %q (%v), want %q", raw, got, err, want)
			}
		}
	}
}

func TestDifferentPagesGetDifferentFiles(t *testing.T) {
	urls := []string{
		"https://example.com/",
		"http://example.com/",
		"https://www.example.com/",
		"https://example.com:8443/",
		"https://example.com/a",
		"https://example.com/A",
		"https://example.com/a/",
		"https://example.com/a?x=1",
		"https://example.com/a?x=2",
		"https://example.com/a?x=1&y=2",
		"https://example.com/a?y=2&x=1",
		"https://example.com/a-b",
		"https://example.com/a/b",
		"https://example.com/" + strings.Repeat("a", 500) + "1",
		"https://example.com/" + strings.Repeat("a", 500) + "2",
	}
	seen := map[string]string{}
	for _, raw := range urls {
		name, err := FileName(raw)
		if err != nil {
			t.Fatal(err)
		}
		if other, taken := seen[name]; taken {
			t.Errorf("%s and %s share the name %s", raw, other, name)
		}
		seen[name] = raw
	}
}

func TestFileNameRejectsUnusableURLs(t *testing.T) {
	for _, raw := range []string{"http://bad host/", "not a url", ""} {
		if name, err := FileName(raw); err == nil {
			t.Errorf("%q: got %q, want an error", raw, name)
		}
	}
}

func TestStorageSaveAndFind(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested", "screenshots")
	storage, err := NewStorage(dir)
	if err != nil {
		t.Fatal(err)
	}

	const page = "https://example.com/"
	if key, found, err := storage.Find(page); err != nil || found || key == "" {
		t.Fatalf("before saving: key = %q, found = %v, err = %v", key, found, err)
	}

	key, err := storage.Save(page, []byte("first"))
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(key) != key {
		t.Errorf("key = %q: it must be relative, so no filesystem path reaches the database", key)
	}

	// An equivalent URL finds the same file.
	found, ok, err := storage.Find("HTTPS://EXAMPLE.COM")
	if err != nil || !ok || found != key {
		t.Fatalf("after saving: key = %q, found = %v, err = %v", found, ok, err)
	}

	// Saving again replaces the file and leaves nothing else behind.
	if _, err := storage.Save(page, []byte("second")); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(filepath.Join(dir, key))
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "second" {
		t.Errorf("content = %q", content)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Errorf("entries = %v", entries)
	}
}

func TestStorageRejectsUnusableURL(t *testing.T) {
	storage, err := NewStorage(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := storage.Find("http://bad host/"); err == nil {
		t.Error("Find: expected an error")
	}
	if _, err := storage.Save("http://bad host/", []byte("x")); err == nil {
		t.Error("Save: expected an error")
	}
}

func TestNewStorageRemovesLeftoverTemporaryFiles(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{".tmp-123", "7.png"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o640); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := NewStorage(dir); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "7.png" {
		t.Errorf("entries = %v", entries)
	}
}
