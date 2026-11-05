//go:build integration

package tests

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"image/png"
	"net"
	"net/http"
	"net/url"
	"os"
	"testing"
	"time"

	cdpbrowser "github.com/chromedp/cdproto/browser"
	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/chromedp"
	"github.com/chromedp/chromedp/remote"

	"github.com/Misha36bm/site-checker/src/internal/browser"
)

func newBrowser(t *testing.T, change func(*browser.Config)) *browser.Browser {
	t.Helper()
	cfg := browser.Config{
		CDPURL:       os.Getenv("BROWSER_CDP_URL"), // Browser resolves the name itself
		Timeout:      30 * time.Second,
		SettleDelay:  100 * time.Millisecond,
		Width:        800,
		Height:       600,
		MaxRedirects: 10,
		MaxBytes:     10 << 20,
		Concurrency:  2,
	}
	if change != nil {
		change(&cfg)
	}
	return browser.New(cfg, net.DefaultResolver)
}

func dataURL(html string) string {
	return "data:text/html," + url.PathEscape(html)
}

// openTabs returns the number of pages Chromium has open.
func openTabs(t *testing.T) int {
	t.Helper()
	resp, err := http.Get(cdpEndpoint(t) + "/json/list")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var targets []struct {
		Type string `json:"type"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&targets); err != nil {
		t.Fatal(err)
	}
	pages := 0
	for _, target := range targets {
		if target.Type == "page" {
			pages++
		}
	}
	return pages
}

func TestBrowserCapture(t *testing.T) {
	ctx := context.Background()
	tabsBefore := openTabs(t)

	t.Run("real page through the proxy", func(t *testing.T) {
		got, err := newBrowser(t, nil).Capture(ctx, "https://example.com/")
		if err != nil {
			t.Fatal(err)
		}
		if got.Title != "Example Domain" || got.FinalURL != "https://example.com/" {
			t.Errorf("title = %q, final URL = %q", got.Title, got.FinalURL)
		}
		image, err := png.Decode(bytes.NewReader(got.PNG))
		if err != nil {
			t.Fatalf("not a PNG: %v", err)
		}
		if size := image.Bounds().Size(); size.X != 800 || size.Y != 600 {
			t.Errorf("screenshot is %dx%d, want the 800x600 viewport", size.X, size.Y)
		}
	})

	t.Run("rendered title and description", func(t *testing.T) {
		got, err := newBrowser(t, nil).Capture(ctx, dataURL(`<script>
			document.title = "Rendered";
			const meta = document.createElement("meta");
			meta.name = "Description";
			meta.content = "From script";
			document.head.append(meta);
		</script>`))
		if err != nil {
			t.Fatal(err)
		}
		if got.Title != "Rendered" || got.Description != "From script" {
			t.Errorf("title = %q, description = %q", got.Title, got.Description)
		}
	})

	t.Run("dialogs do not block the page", func(t *testing.T) {
		b := newBrowser(t, func(c *browser.Config) { c.Timeout = 10 * time.Second })
		got, err := b.Capture(ctx, dataURL(`<title>After</title><script>alert("x"); confirm("y");</script>`))
		if err != nil {
			t.Fatal(err)
		}
		if got.Title != "After" {
			t.Errorf("title = %q", got.Title)
		}
	})

	t.Run("http redirects count against the limit", func(t *testing.T) {
		b := newBrowser(t, func(c *browser.Config) { c.MaxRedirects = 1 })
		if _, err := b.Capture(ctx, "https://httpbin.org/redirect/3"); !errors.Is(err, browser.ErrRedirectLimit) {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("redirects within the limit pass", func(t *testing.T) {
		b := newBrowser(t, func(c *browser.Config) { c.MaxRedirects = 1 })
		got, err := b.Capture(ctx, "http://github.com/")
		if err != nil {
			t.Fatal(err)
		}
		if got.FinalURL != "https://github.com/" {
			t.Errorf("final URL = %q", got.FinalURL)
		}
	})

	t.Run("client-side redirects count against the limit", func(t *testing.T) {
		for name, page := range map[string]string{
			"javascript":   `<script>location.href = "https://example.com/";</script>`,
			"meta refresh": `<meta http-equiv="refresh" content="0;url=https://example.com/">`,
		} {
			t.Run(name, func(t *testing.T) {
				b := newBrowser(t, func(c *browser.Config) {
					c.MaxRedirects = 0
					c.SettleDelay = 2 * time.Second
				})
				if _, err := b.Capture(ctx, dataURL(page)); !errors.Is(err, browser.ErrRedirectLimit) {
					t.Fatalf("err = %v", err)
				}
			})
		}
	})

	t.Run("timeout", func(t *testing.T) {
		b := newBrowser(t, func(c *browser.Config) { c.Timeout = 2 * time.Second })
		start := time.Now()
		if _, err := b.Capture(ctx, "https://httpbin.org/delay/10"); !errors.Is(err, browser.ErrTimeout) {
			t.Fatalf("err = %v", err)
		}
		if elapsed := time.Since(start); elapsed > 8*time.Second {
			t.Errorf("took %s", elapsed)
		}
	})

	t.Run("size limit", func(t *testing.T) {
		b := newBrowser(t, func(c *browser.Config) { c.MaxBytes = 10 })
		if _, err := b.Capture(ctx, "https://example.com/"); !errors.Is(err, browser.ErrTooLarge) {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("failed navigation", func(t *testing.T) {
		if _, err := newBrowser(t, nil).Capture(ctx, "https://nonexistent.invalid/"); err == nil {
			t.Fatal("expected an error")
		}
	})

	t.Run("tabs are closed afterwards", func(t *testing.T) {
		// Closing happens just after each capture returns, timeouts included.
		deadline := time.Now().Add(5 * time.Second)
		for openTabs(t) > tabsBefore && time.Now().Before(deadline) {
			time.Sleep(100 * time.Millisecond)
		}
		if got := openTabs(t); got > tabsBefore {
			t.Errorf("%d tabs open, started with %d", got, tabsBefore)
		}
	})
}

// TestBrowserCrash crashes the real browser process in the middle of a capture.
// It restores the browser before returning, so later tests are unaffected.
func TestBrowserCrash(t *testing.T) {
	ctx := context.Background()
	b := newBrowser(t, nil)

	result := make(chan error, 1)
	start := time.Now()
	go func() {
		_, err := b.Capture(ctx, "https://httpbin.org/delay/10")
		result <- err
	}()
	time.Sleep(2 * time.Second)

	allocator, cancelAllocator := remote.NewAllocator(ctx, cdpEndpoint(t))
	defer cancelAllocator()
	tab, cancelTab := chromedp.NewContext(allocator)
	defer cancelTab()
	// The connection dies with the browser, so this call's own error says nothing.
	chromedp.Do(tab, chromedp.Func(func(ctx context.Context, target *chromedp.Target) error {
		_, err := cdp.Call(ctx, target, cdpbrowser.Crash, cdp.Empty{})
		return err
	}))

	select {
	case err := <-result:
		if err == nil {
			t.Fatal("a capture survived the browser crash")
		}
		t.Logf("capture failed after %s: %v", time.Since(start).Round(time.Millisecond), err)
	case <-time.After(15 * time.Second):
		t.Fatal("the capture did not notice the crash")
	}

	// The restart policy brings Chromium back, and the next capture just works.
	deadline := time.Now().Add(60 * time.Second)
	for {
		capture, err := newBrowser(t, nil).Capture(ctx, "https://example.com/")
		if err == nil {
			t.Logf("recovered after %s, title %q", time.Since(start).Round(time.Second), capture.Title)
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("no recovery: %v", err)
		}
		time.Sleep(2 * time.Second)
	}
}
