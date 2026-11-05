//go:build integration

package tests

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
	"github.com/chromedp/chromedp/remote"
)

// cdpEndpoint returns BROWSER_CDP_URL with its host resolved, because Chromium
// refuses CDP requests whose Host header is a name.
func cdpEndpoint(t *testing.T) string {
	t.Helper()
	raw := os.Getenv("BROWSER_CDP_URL")
	if raw == "" {
		t.Fatal("BROWSER_CDP_URL is required")
	}
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	addrs, err := net.LookupHost(u.Hostname())
	if err != nil {
		t.Fatal(err)
	}
	u.Host = net.JoinHostPort(addrs[0], u.Port())
	return u.String()
}

// TestBrowserIsolation drives real Chromium the way a hostile page would. These run
// from about:blank with the same reach as any script on a checked page.
func TestBrowserIsolation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	alloc, cancelAlloc := remote.NewAllocator(ctx, cdpEndpoint(t))
	defer cancelAlloc()
	tab, cancelTab := chromedp.NewContext(alloc, chromedp.WithNewBrowserContext())
	defer cancelTab()
	if err := chromedp.Do(tab, chromedp.Navigate("about:blank")); err != nil {
		t.Fatal(err)
	}

	// A navigation shows what really came back. fetch cannot: to a no-cors request,
	// the proxy's own 403 looks like a response from the destination.
	probe := func(t *testing.T, target string) string {
		t.Helper()
		ctx, cancel := context.WithTimeout(tab, 20*time.Second)
		defer cancel()
		if err := chromedp.Do(ctx, chromedp.Navigate(target)); err != nil {
			return "blocked"
		}
		text, err := chromedp.Run(ctx, chromedp.Evaluate[string](`document.body ? document.body.innerText : ""`))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(text, "destination is forbidden") || strings.Contains(text, "invalid destination") {
			return "blocked"
		}
		return "reached: " + text
	}

	t.Run("public sites are reachable through the proxy", func(t *testing.T) {
		for _, target := range []string{"https://example.com/", "http://example.com/"} {
			if got := probe(t, target); !strings.HasPrefix(got, "reached") {
				t.Errorf("%s: %s, so the blocked results below would prove nothing", target, got)
			}
		}
	})

	t.Run("internal destinations are blocked", func(t *testing.T) {
		for _, target := range []string{
			"http://postgres:5432/",
			"http://api:8080/health",
			"http://api:3128/",
			"http://browser:9222/json/version",
			"http://127.0.0.1:9222/json/version",
			"http://127.0.0.1:9223/json/version",
			"http://localhost:9222/json/version",
			"http://[::1]:9222/json/version",
			"http://169.254.169.254/latest/meta-data/",
			"http://metadata.google.internal/",
			"http://10.0.0.1/",
			"http://192.168.0.1/",
			"http://172.17.0.1/",
			"http://host.docker.internal/",
			"http://localtest.me/",
			"http://169.254.169.254.nip.io/",
			"http://2130706433:9222/",
			"http://0x7f.1:9222/",
			"https://postgres:5432/",
			"https://127.0.0.1:9222/",
		} {
			if got := probe(t, target); got != "blocked" {
				t.Errorf("%s: %s", target, got)
			}
		}
	})

	t.Run("websockets to internal destinations are blocked", func(t *testing.T) {
		// From an HTTPS page Chromium itself would refuse ws://, proving nothing.
		if err := chromedp.Do(tab, chromedp.Navigate("about:blank")); err != nil {
			t.Fatal(err)
		}
		for _, target := range []string{"ws://postgres:5432/", "ws://127.0.0.1:9222/", "ws://api:8080/"} {
			script := fmt.Sprintf(`new Promise(resolve => {
				const socket = new WebSocket(%q);
				socket.onopen = () => resolve("reached");
				socket.onerror = () => resolve("blocked");
				setTimeout(() => resolve("blocked"), 15000);
			})`, target)
			result, err := chromedp.Run(tab, chromedp.Evaluate[string](script, chromedp.EvalAwaitPromise))
			if err != nil {
				t.Fatal(err)
			}
			if result != "blocked" {
				t.Errorf("%s: %s", target, result)
			}
		}
	})

	t.Run("webrtc reveals no local candidates", func(t *testing.T) {
		const script = `new Promise(resolve => {
			const found = [];
			const peer = new RTCPeerConnection({iceServers: [{urls: "stun:1.1.1.1:3478"}]});
			peer.createDataChannel("x");
			peer.onicecandidate = e => { if (e.candidate && e.candidate.candidate) found.push(e.candidate.candidate); };
			peer.createOffer().then(o => peer.setLocalDescription(o));
			setTimeout(() => { peer.close(); resolve(found); }, 5000);
		})`
		found, err := chromedp.Run(tab, chromedp.Evaluate[[]string](script, chromedp.EvalAwaitPromise))
		if err != nil {
			t.Fatal(err)
		}
		if len(found) != 0 {
			t.Errorf("ICE candidates: %v", found)
		}
	})
}
