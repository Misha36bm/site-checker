// Package config reads application settings from the environment.
package config

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"time"
)

// Getter returns the value of a setting, or "" if it is unset. os.Getenv
// satisfies it; tests pass a map lookup instead.
type Getter func(key string) string

// App holds the settings of the application process itself. Database settings
// are loaded by the database package, because they depend on the driver.
type App struct {
	Env              string
	Host             string
	Port             int
	ShutdownTimeout  time.Duration
	MaxBodyBytes     int64
	MaxURLLength     int
	MaxPendingChecks int
	Worker           Worker
	HTTP             HTTP
	AllowedCIDRs     []netip.Prefix
	Browser          Browser
}

// Browser holds the settings of the browser and its egress proxy.
type Browser struct {
	CDPURL         string
	ProxyListen    string
	Timeout        time.Duration
	SettleDelay    time.Duration
	ViewportWidth  int
	ViewportHeight int
	Concurrency    int
	StoragePath    string
	MaxBytes       int64
	MaxAge         time.Duration
}

// Largest allowed viewport. Screenshots capture exactly this area, so it bounds
// their dimensions.
const (
	maxViewportWidth  = 3840
	maxViewportHeight = 2160
)

// HTTP holds the settings of the HTTP checker.
type HTTP struct {
	Timeout      time.Duration
	MaxRedirects int
	MaxBodyBytes int64
	UserAgent    string
}

// Worker holds the settings of the background worker pool.
type Worker struct {
	Concurrency     int
	PollInterval    time.Duration
	LockTTL         time.Duration
	MaxAttempts     int
	ShutdownTimeout time.Duration
}

// Addr returns the address the HTTP server listens on.
func (a App) Addr() string {
	return net.JoinHostPort(a.Host, strconv.Itoa(a.Port))
}

// Load reads the application settings.
func Load(get Getter) (App, error) {
	port, err := Int(get, "APP_PORT", 8080, 1)
	if err != nil {
		return App{}, err
	}
	if port > 65535 {
		return App{}, fmt.Errorf("APP_PORT: %d is not a valid port", port)
	}
	shutdown, err := Duration(get, "APP_SHUTDOWN_TIMEOUT", 10*time.Second)
	if err != nil {
		return App{}, err
	}
	maxBody, err := Int(get, "API_MAX_BODY_BYTES", 4096, 1)
	if err != nil {
		return App{}, err
	}
	maxURL, err := Int(get, "API_MAX_URL_LENGTH", 2048, 1)
	if err != nil {
		return App{}, err
	}
	maxPending, err := Int(get, "MAX_PENDING_CHECKS", 1000, 1)
	if err != nil {
		return App{}, err
	}
	worker, err := loadWorker(get)
	if err != nil {
		return App{}, err
	}
	httpConfig, err := loadHTTP(get)
	if err != nil {
		return App{}, err
	}
	allowed, err := prefixes(get, "SSRF_ALLOWED_CIDRS")
	if err != nil {
		return App{}, err
	}
	browser, err := loadBrowser(get)
	if err != nil {
		return App{}, err
	}
	return App{
		Env:              String(get, "APP_ENV", "development"),
		Host:             String(get, "APP_HOST", "0.0.0.0"),
		Port:             port,
		ShutdownTimeout:  shutdown,
		MaxBodyBytes:     int64(maxBody),
		MaxURLLength:     maxURL,
		MaxPendingChecks: maxPending,
		Worker:           worker,
		HTTP:             httpConfig,
		AllowedCIDRs:     allowed,
		Browser:          browser,
	}, nil
}

func loadBrowser(get Getter) (Browser, error) {
	timeout, err := Duration(get, "BROWSER_TIMEOUT", 30*time.Second)
	if err != nil {
		return Browser{}, err
	}
	if timeout == 0 {
		return Browser{}, errors.New("BROWSER_TIMEOUT must be above zero")
	}
	settle, err := Duration(get, "BROWSER_SETTLE_DELAY", 500*time.Millisecond)
	if err != nil {
		return Browser{}, err
	}
	width, err := Int(get, "BROWSER_VIEWPORT_WIDTH", 1366, 1)
	if err != nil {
		return Browser{}, err
	}
	height, err := Int(get, "BROWSER_VIEWPORT_HEIGHT", 768, 1)
	if err != nil {
		return Browser{}, err
	}
	if width > maxViewportWidth || height > maxViewportHeight {
		return Browser{}, fmt.Errorf("viewport %dx%d exceeds the maximum of %dx%d", width, height, maxViewportWidth, maxViewportHeight)
	}
	concurrency, err := Int(get, "BROWSER_MAX_CONCURRENCY", 2, 1)
	if err != nil {
		return Browser{}, err
	}
	maxBytes, err := Int(get, "SCREENSHOT_MAX_BYTES", 10<<20, 1)
	if err != nil {
		return Browser{}, err
	}
	maxAge, err := Duration(get, "SCREENSHOT_MAX_AGE", 24*time.Hour)
	if err != nil {
		return Browser{}, err
	}
	return Browser{
		CDPURL:         String(get, "BROWSER_CDP_URL", "http://browser:9222"),
		ProxyListen:    String(get, "BROWSER_PROXY_LISTEN", "0.0.0.0:3128"),
		Timeout:        timeout,
		SettleDelay:    settle,
		ViewportWidth:  width,
		ViewportHeight: height,
		Concurrency:    concurrency,
		StoragePath:    String(get, "SCREENSHOT_STORAGE_PATH", "/app/storage/screenshots"),
		MaxBytes:       int64(maxBytes),
		MaxAge:         maxAge,
	}, nil
}

func loadHTTP(get Getter) (HTTP, error) {
	timeout, err := Duration(get, "HTTP_TIMEOUT", 15*time.Second)
	if err != nil {
		return HTTP{}, err
	}
	if timeout == 0 {
		return HTTP{}, errors.New("HTTP_TIMEOUT must be above zero")
	}
	maxRedirects, err := Int(get, "HTTP_MAX_REDIRECTS", 10, 0)
	if err != nil {
		return HTTP{}, err
	}
	maxBody, err := Int(get, "HTTP_MAX_BODY_BYTES", 5<<20, 1)
	if err != nil {
		return HTTP{}, err
	}
	return HTTP{
		Timeout:      timeout,
		MaxRedirects: maxRedirects,
		MaxBodyBytes: int64(maxBody),
		UserAgent:    String(get, "HTTP_USER_AGENT", "site-checker/1.0"),
	}, nil
}

// prefixes reads a comma-separated list of CIDR ranges.
func prefixes(get Getter, key string) ([]netip.Prefix, error) {
	value := get(key)
	if value == "" {
		return nil, nil
	}
	var result []netip.Prefix
	for part := range strings.SplitSeq(value, ",") {
		prefix, err := netip.ParsePrefix(strings.TrimSpace(part))
		if err != nil {
			return nil, fmt.Errorf("%s: %q is not a CIDR range", key, part)
		}
		result = append(result, prefix.Masked())
	}
	return result, nil
}

func loadWorker(get Getter) (Worker, error) {
	concurrency, err := Int(get, "MAX_CONCURRENT_CHECKS", 5, 1)
	if err != nil {
		return Worker{}, err
	}
	poll, err := Duration(get, "WORKER_POLL_INTERVAL", time.Second)
	if err != nil {
		return Worker{}, err
	}
	if poll == 0 {
		return Worker{}, errors.New("WORKER_POLL_INTERVAL must be above zero")
	}
	lockTTL, err := Duration(get, "CHECK_LOCK_TTL", 2*time.Minute)
	if err != nil {
		return Worker{}, err
	}
	// Leases renew every third of the TTL, so a shorter TTL would renew too often
	// and tolerate no database hiccup at all.
	if lockTTL < 3*time.Second {
		return Worker{}, errors.New("CHECK_LOCK_TTL must be at least 3s")
	}
	maxAttempts, err := Int(get, "CHECK_MAX_ATTEMPTS", 3, 1)
	if err != nil {
		return Worker{}, err
	}
	shutdown, err := Duration(get, "WORKER_SHUTDOWN_TIMEOUT", 30*time.Second)
	if err != nil {
		return Worker{}, err
	}
	return Worker{
		Concurrency:     concurrency,
		PollInterval:    poll,
		LockTTL:         lockTTL,
		MaxAttempts:     maxAttempts,
		ShutdownTimeout: shutdown,
	}, nil
}

// String returns the setting, or def if it is unset.
func String(get Getter, key, def string) string {
	if v := get(key); v != "" {
		return v
	}
	return def
}

// Int returns the setting as an integer no smaller than minimum, or def if it
// is unset.
func Int(get Getter, key string, def, minimum int) (int, error) {
	v := get(key)
	if v == "" {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("%s: %q is not an integer", key, v)
	}
	if n < minimum {
		return 0, fmt.Errorf("%s: %d is below the minimum of %d", key, n, minimum)
	}
	return n, nil
}

// Duration returns the setting as a non-negative duration, or def if it is
// unset.
func Duration(get Getter, key string, def time.Duration) (time.Duration, error) {
	v := get(key)
	if v == "" {
		return def, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return 0, fmt.Errorf("%s: %q is not a duration", key, v)
	}
	if d < 0 {
		return 0, fmt.Errorf("%s: %s is negative", key, v)
	}
	return d, nil
}
