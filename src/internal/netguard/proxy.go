package netguard

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"sync"
	"time"
)

// maxProxyConnections bounds concurrent requests and tunnels from the browser.
const maxProxyConnections = 256

// maxProxyHostLength bounds the destination of a proxied request.
const maxProxyHostLength = 2048

// Proxy is the browser's only way out of its isolated network. It is a forward
// proxy applying the SSRF policy to every destination, so subresources, WebSockets,
// and client-side redirects are all held to the same rules as the HTTP checker.
type Proxy struct {
	policy  *Policy
	dial    func(ctx context.Context, network, address string) (net.Conn, error)
	timeout time.Duration
	slots   chan struct{}
	reverse *httputil.ReverseProxy
}

// NewProxy returns a Proxy. timeout limits the lifetime of each request or tunnel.
func NewProxy(policy *Policy, dial func(ctx context.Context, network, address string) (net.Conn, error), timeout time.Duration) *Proxy {
	p := &Proxy{
		policy:  policy,
		dial:    dial,
		timeout: timeout,
		slots:   make(chan struct{}, maxProxyConnections),
	}
	p.reverse = &httputil.ReverseProxy{
		// Rewrite, unlike Director, adds no X-Forwarded-For. The URL is already
		// absolute, and only the Host needs carrying over.
		Rewrite: func(r *httputil.ProxyRequest) { r.Out.Host = r.In.Host },
		// Proxy stays nil, so environment variables cannot route around the dialer.
		Transport: &http.Transport{
			DialContext:       dial,
			DisableKeepAlives: true,
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			p.refuse(w, r.URL.Host, err)
		},
	}
	return p
}

// ServeHTTP handles CONNECT tunnels and absolute-URL requests.
func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	select {
	case p.slots <- struct{}{}:
		defer func() { <-p.slots }()
	default:
		http.Error(w, "too many connections", http.StatusServiceUnavailable)
		return
	}

	if r.Method == http.MethodConnect {
		p.tunnel(w, r)
		return
	}
	// A request naming no destination was aimed at the proxy itself. Plain HTTP
	// only here: browsers send HTTPS through CONNECT.
	if r.URL.Scheme != "http" || r.URL.Host == "" {
		http.Error(w, "not a proxy request", http.StatusBadRequest)
		return
	}
	if err := p.policy.ValidateURL("http://"+r.URL.Host+"/", maxProxyHostLength); err != nil {
		p.refuse(w, r.URL.Host, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), p.timeout)
	defer cancel()
	p.reverse.ServeHTTP(w, r.WithContext(ctx))
}

func (p *Proxy) tunnel(w http.ResponseWriter, r *http.Request) {
	target := r.Host
	if err := p.policy.ValidateURL("https://"+target+"/", maxProxyHostLength); err != nil {
		p.refuse(w, target, err)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), p.timeout)
	defer cancel()
	upstream, err := p.dial(ctx, "tcp", target)
	if err != nil {
		p.refuse(w, target, err)
		return
	}
	defer upstream.Close()

	hijacker, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "tunnels are not supported", http.StatusInternalServerError)
		return
	}
	client, buffered, err := hijacker.Hijack()
	if err != nil {
		slog.Warn("proxy: hijack connection", "error", err)
		return
	}
	defer client.Close()

	if _, err := io.WriteString(client, "HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
		return
	}

	deadline := time.Now().Add(p.timeout)
	if err := client.SetDeadline(deadline); err != nil {
		return
	}
	if err := upstream.SetDeadline(deadline); err != nil {
		return
	}

	// When either direction ends, closing both unblocks the other.
	var once sync.Once
	closeBoth := func() {
		once.Do(func() {
			client.Close()
			upstream.Close()
		})
	}
	var copies sync.WaitGroup
	copies.Go(func() {
		defer closeBoth()
		io.Copy(upstream, buffered) // the reader holds bytes sent ahead of the 200
	})
	copies.Go(func() {
		defer closeBoth()
		io.Copy(client, upstream)
	})
	copies.Wait()
}

// refuse logs the destination's host and port only: paths and queries can hold
// secrets, and CONNECT never reveals them anyway.
func (p *Proxy) refuse(w http.ResponseWriter, target string, err error) {
	if IsBlocked(err) {
		slog.Info("proxy: destination blocked", "target", target)
		http.Error(w, "destination is forbidden", http.StatusForbidden)
		return
	}
	if IsInvalidURL(err) {
		http.Error(w, "invalid destination", http.StatusBadRequest)
		return
	}
	slog.Info("proxy: connection failed", "target", target, "error", err)
	http.Error(w, "connection failed", http.StatusBadGateway)
}
