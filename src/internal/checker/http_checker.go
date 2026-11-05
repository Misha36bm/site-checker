package checker

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"time"

	"github.com/Misha36bm/site-checker/src/internal/metadata"
	"github.com/Misha36bm/site-checker/src/internal/models"
	"github.com/Misha36bm/site-checker/src/internal/netguard"
)

const (
	tlsHandshakeTimeout = 10 * time.Second
	maxResponseHeaders  = 1 << 20
)

// errorMessages are shown to API clients. They are fixed texts, because the
// underlying errors can name internal addresses.
var errorMessages = map[string]string{
	models.ErrorSSRFBlocked:     "The destination address is not allowed.",
	models.ErrorInvalidRedirect: "The site redirected to an invalid destination.",
	models.ErrorDNS:             "The host name could not be resolved.",
	models.ErrorConnection:      "The connection to the site failed.",
	models.ErrorTLS:             "The site's TLS certificate could not be verified.",
	models.ErrorTimeout:         "The site did not respond in time.",
	models.ErrorRedirectLimit:   "The site redirected too many times.",
	models.ErrorRedirectLoop:    "The site redirects in a loop.",
}

// HTTPConfig configures the HTTP checker.
type HTTPConfig struct {
	Timeout      time.Duration
	MaxRedirects int
	MaxBodyBytes int64
	MaxURLLength int
	UserAgent    string
}

// Dialer opens network connections. Production uses netguard.Dialer, so every
// connection is checked against the SSRF policy.
type Dialer func(ctx context.Context, network, address string) (net.Conn, error)

// HTTPChecker requests a URL and follows its redirects.
type HTTPChecker struct {
	cfg    HTTPConfig
	policy *netguard.Policy
	dial   Dialer
}

// NewHTTPChecker returns an HTTPChecker.
func NewHTTPChecker(cfg HTTPConfig, policy *netguard.Policy, dial Dialer) *HTTPChecker {
	return &HTTPChecker{cfg: cfg, policy: policy, dial: dial}
}

// HTTPOutcome is the result of the HTTP stage.
type HTTPOutcome struct {
	Result models.HTTPResult
	// Final is true if a final, non-redirect response was received.
	Final bool
	// Body and ContentType are set for HTML responses, for the metadata stage. The
	// body is cut off at MaxBodyBytes.
	Body        []byte
	ContentType string
	// ErrorCode and ErrorMessage are set if Final is false.
	ErrorCode    string
	ErrorMessage string
}

func (o HTTPOutcome) failed(code string) HTTPOutcome {
	o.ErrorCode = code
	o.ErrorMessage = errorMessages[code]
	return o
}

// Check requests rawURL, which the caller has validated. It uses the transport
// directly rather than http.Client, so each redirect is recorded and validated
// here, and a malformed Location is still a recorded response rather than an error
// from the client. HTTP_TIMEOUT covers the whole chain.
func (c *HTTPChecker) Check(ctx context.Context, rawURL string) HTTPOutcome {
	var out HTTPOutcome

	ctx, cancel := context.WithTimeout(ctx, c.cfg.Timeout)
	defer cancel()

	current, err := url.Parse(rawURL)
	if err != nil {
		return out.failed(models.ErrorInvalidRedirect)
	}
	jar, err := cookiejar.New(nil)
	if err != nil {
		return out.failed(models.ErrorConnection)
	}
	// One transport per check: nothing carries over between checks. Proxy stays
	// nil, so environment variables cannot route traffic around the dialer.
	transport := &http.Transport{
		DialContext:            c.dial,
		ForceAttemptHTTP2:      true,
		TLSHandshakeTimeout:    tlsHandshakeTimeout,
		MaxResponseHeaderBytes: maxResponseHeaders,
	}
	defer transport.CloseIdleConnections()

	start := time.Now()
	visited := map[string]string{}
	for {
		visited[visitKey(current)] = cookieState(jar, current)

		hopStart := time.Now()
		resp, err := c.get(ctx, transport, jar, current)
		if err != nil {
			slog.Info("http check failed", "error", err)
			return out.failed(classify(ctx, err))
		}
		now := time.Now()

		out.Result.RedirectChain = append(out.Result.RedirectChain, models.RedirectHop{
			URL:        current.String(),
			StatusCode: resp.StatusCode,
			DurationMS: now.Sub(hopStart).Milliseconds(),
		})
		out.Result.FinalURL = current.String()
		status := resp.StatusCode
		out.Result.StatusCode = &status

		if !isRedirect(resp.StatusCode) {
			elapsed := now.Sub(start).Milliseconds()
			out.Result.ResponseTimeMS = &elapsed
			out.Result.IsAvailable = resp.StatusCode == http.StatusOK
			out.Final = true
			out.Body, out.ContentType = c.readHTML(resp)
			resp.Body.Close()
			return out
		}

		next, err := resp.Location()
		resp.Body.Close()
		if err != nil {
			return out.failed(models.ErrorInvalidRedirect)
		}
		if err := c.policy.ValidateURL(next.String(), c.cfg.MaxURLLength); err != nil {
			if netguard.IsBlocked(err) {
				return out.failed(models.ErrorSSRFBlocked)
			}
			return out.failed(models.ErrorInvalidRedirect)
		}
		if previous, seen := visited[visitKey(next)]; seen && previous == cookieState(jar, next) {
			return out.failed(models.ErrorRedirectLoop)
		}
		if len(out.Result.RedirectChain) > c.cfg.MaxRedirects {
			return out.failed(models.ErrorRedirectLimit)
		}
		current = next
	}
}

func (c *HTTPChecker) get(ctx context.Context, transport http.RoundTripper, jar http.CookieJar, target *url.URL) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", c.cfg.UserAgent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,*/*;q=0.8")
	for _, cookie := range jar.Cookies(target) {
		req.AddCookie(cookie)
	}
	resp, err := transport.RoundTrip(req)
	if err != nil {
		return nil, err
	}
	jar.SetCookies(target, resp.Cookies())
	return resp, nil
}

// readHTML reads the body only if it can be HTML, so large downloads are not
// fetched for nothing. A body that fails partway still returns what arrived: the
// status is already known and the title is usually near the start.
func (c *HTTPChecker) readHTML(resp *http.Response) ([]byte, string) {
	contentType := resp.Header.Get("Content-Type")
	if contentType != "" && !metadata.IsHTML(contentType, nil) {
		return nil, contentType
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, c.cfg.MaxBodyBytes))
	if err != nil {
		slog.Info("read response body", "error", err)
	}
	if !metadata.IsHTML(contentType, body) {
		return nil, contentType
	}
	return body, contentType
}

func isRedirect(status int) bool {
	switch status {
	case http.StatusMovedPermanently, http.StatusFound, http.StatusSeeOther,
		http.StatusTemporaryRedirect, http.StatusPermanentRedirect:
		return true
	}
	return false
}

// visitKey identifies a URL for loop detection. Fragments never reach the server.
func visitKey(u *url.URL) string {
	withoutFragment := *u
	withoutFragment.Fragment = ""
	withoutFragment.RawFragment = ""
	return withoutFragment.String()
}

// cookieState returns the cookies a request to u would send. If it changed since
// the previous visit, a repeated URL is a site setting a cookie and redirecting
// back, not a loop.
func cookieState(jar http.CookieJar, u *url.URL) string {
	var state string
	for _, cookie := range jar.Cookies(u) {
		state += cookie.String() + ";"
	}
	return state
}

func classify(ctx context.Context, err error) string {
	if netguard.IsBlocked(err) {
		return models.ErrorSSRFBlocked
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) || errors.Is(err, os.ErrDeadlineExceeded) || isTimeout(err) {
		return models.ErrorTimeout
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return models.ErrorDNS
	}
	if isTLSError(err) {
		return models.ErrorTLS
	}
	return models.ErrorConnection
}

func isTimeout(err error) bool {
	var timeout interface{ Timeout() bool }
	return errors.As(err, &timeout) && timeout.Timeout()
}

func isTLSError(err error) bool {
	var (
		verification *tls.CertificateVerificationError
		header       tls.RecordHeaderError
		unknown      x509.UnknownAuthorityError
		hostname     x509.HostnameError
		invalid      x509.CertificateInvalidError
		alert        tls.AlertError
	)
	return errors.As(err, &verification) || errors.As(err, &header) || errors.As(err, &unknown) ||
		errors.As(err, &hostname) || errors.As(err, &invalid) || errors.As(err, &alert)
}
