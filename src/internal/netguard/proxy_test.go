package netguard

import (
	"bufio"
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"testing"
	"time"
)

// startProxy returns a proxy that treats 127.0.0.1 as a public address, so 127.0.0.2
// and other private addresses stand in for forbidden ones.
func startProxy(t *testing.T, timeout time.Duration) *httptest.Server {
	t.Helper()
	policy := NewPolicy([]netip.Prefix{netip.MustParsePrefix("127.0.0.1/32")})
	dialer := &Dialer{
		Policy: policy,
		Resolver: fakeResolver{
			"site.test":     {"127.0.0.1"},
			"internal.test": {"10.0.0.1"},
			"mixed.test":    {"127.0.0.1", "10.0.0.1"},
		},
	}
	server := httptest.NewServer(NewProxy(policy, dialer.DialContext, timeout))
	t.Cleanup(server.Close)
	return server
}

func proxyClient(proxy *httptest.Server) *http.Client {
	proxyURL, _ := url.Parse(proxy.URL)
	return &http.Client{
		Transport: &http.Transport{
			Proxy:             http.ProxyURL(proxyURL),
			TLSClientConfig:   &tls.Config{InsecureSkipVerify: true},
			DisableKeepAlives: true,
		},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

func portOfURL(t *testing.T, raw string) string {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return u.Port()
}

func TestProxyForwardsPlainHTTP(t *testing.T) {
	var gotHost, gotForwarded string
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHost, gotForwarded = r.Host, r.Header.Get("X-Forwarded-For")
		w.Header().Set("X-Target", "yes")
		fmt.Fprint(w, "hello")
	}))
	defer target.Close()

	host := "site.test:" + portOfURL(t, target.URL)
	resp, err := proxyClient(startProxy(t, 5*time.Second)).Get("http://" + host + "/page?a=1")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != 200 || string(body) != "hello" || resp.Header.Get("X-Target") != "yes" {
		t.Errorf("status = %d, body = %q", resp.StatusCode, body)
	}
	if gotHost != host {
		t.Errorf("Host = %q, want %q", gotHost, host)
	}
	if gotForwarded != "" {
		t.Errorf("the proxy revealed the client address: %q", gotForwarded)
	}
}

func TestProxyTunnelsHTTPS(t *testing.T) {
	target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, "secure")
	}))
	defer target.Close()

	resp, err := proxyClient(startProxy(t, 5*time.Second)).Get("https://site.test:" + portOfURL(t, target.URL) + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "secure" {
		t.Errorf("body = %q", body)
	}
}

func TestProxyPassesRedirectsToTheBrowser(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://10.0.0.1/", http.StatusFound)
	}))
	defer target.Close()

	resp, err := proxyClient(startProxy(t, 5*time.Second)).Get("http://site.test:" + portOfURL(t, target.URL) + "/")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusFound {
		t.Errorf("status = %d", resp.StatusCode)
	}
}

// connect sends a raw CONNECT and returns the proxy's status code.
func connect(t *testing.T, proxy *httptest.Server, target string) int {
	t.Helper()
	conn, err := net.Dial("tcp", strings.TrimPrefix(proxy.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	fmt.Fprintf(conn, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", target, target)
	resp, err := http.ReadResponse(bufio.NewReader(conn), &http.Request{Method: http.MethodConnect})
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

func TestProxyBlocksForbiddenDestinations(t *testing.T) {
	var hits int
	internal := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits++ }))
	defer internal.Close()
	port := portOfURL(t, internal.URL)

	proxy := startProxy(t, 5*time.Second)
	targets := []string{
		"127.0.0.2:" + port,
		"[::1]:" + port,
		"[::ffff:127.0.0.2]:" + port,
		"internal.test:" + port,
		"mixed.test:" + port,
		"10.0.0.1:" + port,
		"169.254.169.254:80",
		"metadata.google.internal:80",
	}
	for _, target := range targets {
		t.Run("connect "+target, func(t *testing.T) {
			if got := connect(t, proxy, target); got != http.StatusForbidden {
				t.Errorf("status = %d, want 403", got)
			}
		})
		t.Run("http "+target, func(t *testing.T) {
			resp, err := proxyClient(proxy).Get("http://" + target + "/")
			if err != nil {
				t.Fatal(err)
			}
			resp.Body.Close()
			if resp.StatusCode != http.StatusForbidden {
				t.Errorf("status = %d, want 403", resp.StatusCode)
			}
		})
	}
	if hits != 0 {
		t.Errorf("a forbidden server received %d requests", hits)
	}
}

func TestProxyRejectsMalformedTargets(t *testing.T) {
	proxy := startProxy(t, 5*time.Second)
	for _, target := range []string{"2130706433:80", "0x7f.1:80"} {
		if got := connect(t, proxy, target); got != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400", target, got)
		}
	}
}

func TestProxyRefusesRequestsAimedAtItself(t *testing.T) {
	resp, err := http.Get(startProxy(t, 5*time.Second).URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d", resp.StatusCode)
	}
}

func TestProxyReportsUnreachableDestination(t *testing.T) {
	proxy := startProxy(t, 5*time.Second)
	closed := httptest.NewServer(http.NotFoundHandler())
	port := portOfURL(t, closed.URL)
	closed.Close()
	if got := connect(t, proxy, "site.test:"+port); got != http.StatusBadGateway {
		t.Errorf("status = %d, want 502", got)
	}
}

func TestProxyTunnelEndsAtTimeout(t *testing.T) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		io.Copy(io.Discard, conn) // holds the connection open
	}()
	_, port, _ := net.SplitHostPort(listener.Addr().String())

	proxy := startProxy(t, 200*time.Millisecond)
	conn, err := net.Dial("tcp", strings.TrimPrefix(proxy.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	fmt.Fprintf(conn, "CONNECT site.test:%s HTTP/1.1\r\nHost: site.test:%s\r\n\r\n", port, port)

	reader := bufio.NewReader(conn)
	resp, err := http.ReadResponse(reader, &http.Request{Method: http.MethodConnect})
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}

	conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	if _, err := reader.ReadByte(); err != io.EOF {
		t.Errorf("read = %v, want the proxy to close the tunnel", err)
	}
}

func TestProxyUsesTheGivenDialer(t *testing.T) {
	var dialed string
	policy := NewPolicy(nil)
	proxy := httptest.NewServer(NewProxy(policy, func(_ context.Context, _, address string) (net.Conn, error) {
		dialed = address
		return nil, &BlockedError{}
	}, time.Second))
	defer proxy.Close()

	if got := connect(t, proxy, "public.test:443"); got != http.StatusForbidden {
		t.Errorf("status = %d", got)
	}
	if dialed != "public.test:443" {
		t.Errorf("dialed = %q", dialed)
	}
}
