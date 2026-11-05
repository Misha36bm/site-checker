package checker

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/Misha36bm/site-checker/src/internal/models"
	"github.com/Misha36bm/site-checker/src/internal/netguard"
)

// testResolver points every .test name at the local test server, and a few names
// at addresses the policy forbids.
type testResolver struct{}

func (testResolver) LookupNetIP(_ context.Context, _, host string) ([]netip.Addr, error) {
	switch host {
	case "internal.test":
		return []netip.Addr{netip.MustParseAddr("10.0.0.1")}, nil
	case "missing.test":
		return nil, &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
	}
	return []netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil
}

// site is a test server reachable under several names.
type site struct {
	*httptest.Server
	port string
}

func newSite(t *testing.T, handler http.HandlerFunc) *site {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return &site{server, portOf(t, server)}
}

func portOf(t *testing.T, server *httptest.Server) string {
	t.Helper()
	u, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	return u.Port()
}

func (s *site) at(host, path string) string {
	return "http://" + host + ":" + s.port + path
}

func newChecker(maxRedirects int) *HTTPChecker {
	// Only 127.0.0.1 is exempt, so 127.0.0.2 stands in for a forbidden address.
	policy := netguard.NewPolicy([]netip.Prefix{netip.MustParsePrefix("127.0.0.1/32")})
	dialer := &netguard.Dialer{Policy: policy, Resolver: testResolver{}}
	return NewHTTPChecker(HTTPConfig{
		Timeout:      5 * time.Second,
		MaxRedirects: maxRedirects,
		MaxBodyBytes: 1024,
		MaxURLLength: 2048,
		UserAgent:    "site-checker-test",
	}, policy, dialer.DialContext)
}

func wantFailure(t *testing.T, out HTTPOutcome, code string) {
	t.Helper()
	if out.Final || out.ErrorCode != code {
		t.Errorf("final = %v, code = %q, want code %q", out.Final, out.ErrorCode, code)
	}
	if out.ErrorMessage == "" {
		t.Error("error message is empty")
	}
	if out.Result.IsAvailable {
		t.Error("a failed check must not be available")
	}
}

func TestFinalStatusCodes(t *testing.T) {
	cases := []struct {
		status    int
		available bool
	}{
		{200, true},
		{201, false},
		{204, false},
		{304, false},
		{404, false},
		{500, false},
		{503, false},
	}
	for _, tc := range cases {
		t.Run(fmt.Sprint(tc.status), func(t *testing.T) {
			s := newSite(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(tc.status) })
			out := newChecker(10).Check(context.Background(), s.at("a.test", "/"))

			if !out.Final || out.ErrorCode != "" {
				t.Fatalf("final = %v, code = %q", out.Final, out.ErrorCode)
			}
			if *out.Result.StatusCode != tc.status || out.Result.IsAvailable != tc.available {
				t.Errorf("status = %d, available = %v", *out.Result.StatusCode, out.Result.IsAvailable)
			}
			if out.Result.FinalURL != s.at("a.test", "/") || len(out.Result.RedirectChain) != 1 {
				t.Errorf("result = %+v", out.Result)
			}
			if out.Result.ResponseTimeMS == nil {
				t.Error("response time is missing")
			}
		})
	}
}

func TestEveryRedirectStatusIsFollowed(t *testing.T) {
	for _, status := range []int{301, 302, 303, 307, 308} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			s := newSite(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/start" {
					http.Redirect(w, r, "/end", status)
				}
			})
			out := newChecker(10).Check(context.Background(), s.at("a.test", "/start"))

			if !out.Final || !out.Result.IsAvailable {
				t.Fatalf("out = %+v", out)
			}
			if chain := out.Result.RedirectChain; len(chain) != 2 || chain[0].StatusCode != status || chain[1].StatusCode != 200 {
				t.Errorf("chain = %+v", chain)
			}
		})
	}
}

func TestRedirectChainAcrossDomains(t *testing.T) {
	var s *site
	s = newSite(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.Host {
		case "a.test:" + s.port:
			http.Redirect(w, r, s.at("www.a.test", "/"), http.StatusMovedPermanently)
		case "www.a.test:" + s.port:
			http.Redirect(w, r, s.at("b.test", "/new"), http.StatusFound)
		}
	})
	out := newChecker(10).Check(context.Background(), s.at("a.test", "/"))

	want := []struct {
		url    string
		status int
	}{
		{s.at("a.test", "/"), 301},
		{s.at("www.a.test", "/"), 302},
		{s.at("b.test", "/new"), 200},
	}
	chain := out.Result.RedirectChain
	if len(chain) != len(want) {
		t.Fatalf("chain = %+v", chain)
	}
	for i, hop := range want {
		if chain[i].URL != hop.url || chain[i].StatusCode != hop.status {
			t.Errorf("hop %d = %+v, want %+v", i, chain[i], hop)
		}
	}
	if out.Result.FinalURL != s.at("b.test", "/new") || !out.Result.IsAvailable {
		t.Errorf("result = %+v", out.Result)
	}
}

func TestRelativeRedirect(t *testing.T) {
	s := newSite(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/a/b" {
			w.Header().Set("Location", "../c?x=1")
			w.WriteHeader(http.StatusFound)
		}
	})
	out := newChecker(10).Check(context.Background(), s.at("a.test", "/a/b"))
	if want := s.at("a.test", "/c?x=1"); out.Result.FinalURL != want {
		t.Errorf("final URL = %q, want %q", out.Result.FinalURL, want)
	}
}

func TestRedirectDepth(t *testing.T) {
	// /n redirects to /n-1, and /0 responds 200: starting at /n takes n redirects.
	s := newSite(t, func(w http.ResponseWriter, r *http.Request) {
		var n int
		fmt.Sscanf(r.URL.Path, "/%d", &n)
		if n > 0 {
			http.Redirect(w, r, fmt.Sprintf("/%d", n-1), http.StatusFound)
		}
	})
	cases := []struct {
		name         string
		maxRedirects int
		redirects    int
		ok           bool
	}{
		{"no redirect needed, limit zero", 0, 0, true},
		{"one redirect, limit zero", 0, 1, false},
		{"at the limit", 3, 3, true},
		{"one past the limit", 3, 4, false},
		{"far past the limit", 3, 50, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := newChecker(tc.maxRedirects).Check(context.Background(), s.at("a.test", fmt.Sprintf("/%d", tc.redirects)))
			if !tc.ok {
				wantFailure(t, out, models.ErrorRedirectLimit)
				if got := len(out.Result.RedirectChain); got != tc.maxRedirects+1 {
					t.Errorf("made %d requests, want %d", got, tc.maxRedirects+1)
				}
				if out.Result.FinalURL == "" || out.Result.StatusCode == nil || *out.Result.StatusCode != 302 {
					t.Errorf("last reached URL and status were not kept: %+v", out.Result)
				}
				return
			}
			if !out.Final || !out.Result.IsAvailable || len(out.Result.RedirectChain) != tc.redirects+1 {
				t.Errorf("out = %+v", out)
			}
		})
	}
}

func TestRedirectLoops(t *testing.T) {
	var s *site
	s = newSite(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/self":
			http.Redirect(w, r, "/self", http.StatusFound)
		case "/fragment":
			http.Redirect(w, r, "/fragment#top", http.StatusFound)
		case "/a":
			http.Redirect(w, r, "/b", http.StatusFound)
		case "/b":
			http.Redirect(w, r, s.at("b.test", "/c"), http.StatusFound)
		case "/c":
			http.Redirect(w, r, s.at("a.test", "/a"), http.StatusFound)
		}
	})
	for _, path := range []string{"/self", "/fragment", "/a"} {
		t.Run(path, func(t *testing.T) {
			wantFailure(t, newChecker(10).Check(context.Background(), s.at("a.test", path)), models.ErrorRedirectLoop)
		})
	}
}

func TestRedirectBackAfterSettingCookieIsNotALoop(t *testing.T) {
	s := newSite(t, func(w http.ResponseWriter, r *http.Request) {
		if _, err := r.Cookie("seen"); err != nil {
			http.SetCookie(w, &http.Cookie{Name: "seen", Value: "1", Path: "/"})
			http.Redirect(w, r, "/", http.StatusFound)
		}
	})
	out := newChecker(10).Check(context.Background(), s.at("a.test", "/"))
	if !out.Final || !out.Result.IsAvailable {
		t.Errorf("out = %+v", out)
	}
}

func TestCookieChurnStillHitsTheLimit(t *testing.T) {
	n := 0
	s := newSite(t, func(w http.ResponseWriter, r *http.Request) {
		n++
		http.SetCookie(w, &http.Cookie{Name: "n", Value: fmt.Sprint(n), Path: "/"})
		http.Redirect(w, r, "/", http.StatusFound)
	})
	wantFailure(t, newChecker(5).Check(context.Background(), s.at("a.test", "/")), models.ErrorRedirectLimit)
}

func TestCookiesDoNotCarryBetweenChecks(t *testing.T) {
	var withCookie int
	s := newSite(t, func(w http.ResponseWriter, r *http.Request) {
		if _, err := r.Cookie("seen"); err == nil {
			withCookie++
		}
		http.SetCookie(w, &http.Cookie{Name: "seen", Value: "1", Path: "/"})
	})
	checker := newChecker(10)
	checker.Check(context.Background(), s.at("a.test", "/"))
	checker.Check(context.Background(), s.at("a.test", "/"))
	if withCookie != 0 {
		t.Error("a cookie from one check reached another")
	}
}

func TestInvalidRedirects(t *testing.T) {
	cases := map[string]string{
		"missing location": "",
		"ftp":              "ftp://a.test/",
		"javascript":       "javascript:alert(1)",
		"file":             "file:///etc/passwd",
		"credentials":      "http://user:pass@a.test/",
		"malformed":        "http://a b/",
		"numeric host":     "http://2130706433/",
	}
	for name, location := range cases {
		t.Run(name, func(t *testing.T) {
			s := newSite(t, func(w http.ResponseWriter, _ *http.Request) {
				if location != "" {
					w.Header()["Location"] = []string{location}
				}
				w.WriteHeader(http.StatusFound)
			})
			out := newChecker(10).Check(context.Background(), s.at("a.test", "/"))
			wantFailure(t, out, models.ErrorInvalidRedirect)
			if len(out.Result.RedirectChain) != 1 {
				t.Errorf("chain = %+v", out.Result.RedirectChain)
			}
		})
	}
}

func TestRedirectsCannotBypassSSRFProtection(t *testing.T) {
	var internalHits int
	internal := newSite(t, func(http.ResponseWriter, *http.Request) { internalHits++ })

	cases := map[string]string{
		"literal loopback":           "http://127.0.0.2:" + internal.port + "/",
		"ipv6 loopback":              "http://[::1]:" + internal.port + "/",
		"ipv4-mapped loopback":       "http://[::ffff:127.0.0.2]:" + internal.port + "/",
		"cloud metadata":             "http://169.254.169.254/latest/meta-data/",
		"metadata name":              "http://metadata.google.internal/",
		"name resolving to internal": "http://internal.test:" + internal.port + "/",
		"private range":              "http://192.168.0.1/",
	}
	for name, location := range cases {
		t.Run(name, func(t *testing.T) {
			s := newSite(t, func(w http.ResponseWriter, r *http.Request) {
				http.Redirect(w, r, location, http.StatusFound)
			})
			out := newChecker(10).Check(context.Background(), s.at("a.test", "/"))
			wantFailure(t, out, models.ErrorSSRFBlocked)
			if out.Result.FinalURL != s.at("a.test", "/") {
				t.Errorf("final URL = %q, want the last URL reached", out.Result.FinalURL)
			}
		})
	}
	if internalHits != 0 {
		t.Errorf("forbidden server received %d requests", internalHits)
	}
}

func TestStartingURLResolvingToInternalAddressIsBlocked(t *testing.T) {
	out := newChecker(10).Check(context.Background(), "http://internal.test/")
	wantFailure(t, out, models.ErrorSSRFBlocked)
	if out.Result.FinalURL != "" || out.Result.StatusCode != nil {
		t.Errorf("result = %+v", out.Result)
	}
}

func TestDNSFailure(t *testing.T) {
	wantFailure(t, newChecker(10).Check(context.Background(), "http://missing.test/"), models.ErrorDNS)
}

func TestConnectionRefused(t *testing.T) {
	s := newSite(t, func(http.ResponseWriter, *http.Request) {})
	target := s.at("a.test", "/")
	s.Close()
	wantFailure(t, newChecker(10).Check(context.Background(), target), models.ErrorConnection)
}

func TestTimeoutCoversWholeChain(t *testing.T) {
	s := newSite(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/slow" {
			select {
			case <-r.Context().Done():
			case <-time.After(5 * time.Second):
			}
			return
		}
		http.Redirect(w, r, "/slow", http.StatusFound)
	})
	checker := newChecker(10)
	checker.cfg.Timeout = 100 * time.Millisecond

	start := time.Now()
	out := checker.Check(context.Background(), s.at("a.test", "/start"))
	wantFailure(t, out, models.ErrorTimeout)
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("took %s", elapsed)
	}
	if len(out.Result.RedirectChain) != 1 {
		t.Errorf("the redirect before the timeout was not recorded: %+v", out.Result.RedirectChain)
	}
}

func TestUntrustedCertificate(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	t.Cleanup(server.Close)
	out := newChecker(10).Check(context.Background(), "https://a.test:"+portOf(t, server)+"/")
	wantFailure(t, out, models.ErrorTLS)
}

func TestBodyHandling(t *testing.T) {
	large := "<title>T</title>" + strings.Repeat("x", 5000)
	cases := []struct {
		name        string
		contentType string
		body        string
		wantBody    bool
		wantLen     int
	}{
		{"html is read", "text/html", "<title>T</title>", true, 16},
		{"large html is cut off", "text/html", large, true, 1024},
		{"json is not read", "application/json", `{"a":1}`, false, 0},
		{"missing type is sniffed as html", "", "<!doctype html><title>T</title>", true, 31},
		{"missing type sniffed as other", "", "plain text", false, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newSite(t, func(w http.ResponseWriter, _ *http.Request) {
				w.Header()["Content-Type"] = nil
				if tc.contentType != "" {
					w.Header().Set("Content-Type", tc.contentType)
				}
				fmt.Fprint(w, tc.body)
			})
			out := newChecker(10).Check(context.Background(), s.at("a.test", "/"))
			if !out.Final {
				t.Fatalf("out = %+v", out)
			}
			if (out.Body != nil) != tc.wantBody || len(out.Body) != tc.wantLen {
				t.Errorf("body is %d bytes, nil = %v", len(out.Body), out.Body == nil)
			}
		})
	}
}

func TestErrorMessagesExposeNoInternals(t *testing.T) {
	out := newChecker(10).Check(context.Background(), "http://internal.test/")
	if strings.Contains(out.ErrorMessage, "10.0.0.1") || strings.Contains(out.ErrorMessage, "internal.test") {
		t.Errorf("message = %q", out.ErrorMessage)
	}
}

func TestRequestHeaders(t *testing.T) {
	var userAgent string
	s := newSite(t, func(_ http.ResponseWriter, r *http.Request) { userAgent = r.UserAgent() })
	newChecker(10).Check(context.Background(), s.at("a.test", "/"))
	if userAgent != "site-checker-test" {
		t.Errorf("User-Agent = %q", userAgent)
	}
}

func TestLogsDoNotContainQueryStrings(t *testing.T) {
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })

	const query = "?token=secret-value"
	refused := newSite(t, func(http.ResponseWriter, *http.Request) {})
	refusedURL := refused.at("a.test", "/"+query)
	refused.Close()

	slow := newSite(t, func(_ http.ResponseWriter, r *http.Request) { <-r.Context().Done() })

	tlsServer := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	t.Cleanup(tlsServer.Close)

	checker := newChecker(10)
	checker.cfg.Timeout = 200 * time.Millisecond
	for _, target := range []string{
		refusedURL,
		"http://missing.test/" + query,
		"http://internal.test/" + query,
		slow.at("a.test", "/"+query),
		"https://a.test:" + portOf(t, tlsServer) + "/" + query,
	} {
		if out := checker.Check(context.Background(), target); out.ErrorCode == "" {
			t.Errorf("%s: expected a failure", target)
		}
	}

	if logs.Len() == 0 {
		t.Fatal("nothing was logged, so this test proves nothing")
	}
	if strings.Contains(logs.String(), "secret-value") {
		t.Errorf("logs contain the query string:\n%s", logs.String())
	}
}
