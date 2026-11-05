package browser

import (
	"context"
	"errors"
	"net/netip"
	"os"
	"path/filepath"
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

func TestStorageSave(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested", "screenshots")
	storage, err := NewStorage(dir)
	if err != nil {
		t.Fatal(err)
	}

	key, err := storage.Save(42, []byte("first"))
	if err != nil {
		t.Fatal(err)
	}
	if key != "42.png" {
		t.Errorf("key = %q: it must be relative, so no filesystem path reaches the database", key)
	}

	// A retry of the same check replaces the file.
	if _, err := storage.Save(42, []byte("second")); err != nil {
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
		t.Errorf("temporary files were left behind: %v", entries)
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
