// Package browser drives headless Chromium over CDP and stores its screenshots.
package browser

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"sync/atomic"
	"time"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"
	"github.com/chromedp/chromedp/remote"
)

// descriptionScript reads the meta description from the rendered page.
const descriptionScript = `document.querySelector('meta[name="description" i]')?.content ?? ""`

var (
	// ErrRedirectLimit means navigation passed the configured redirect limit.
	ErrRedirectLimit = errors.New("browser passed the redirect limit")
	// ErrTimeout means the capture did not finish within the browser timeout.
	ErrTimeout = errors.New("browser timed out")
	// ErrTooLarge means the screenshot exceeded the size limit.
	ErrTooLarge = errors.New("screenshot is too large")
)

// Config configures the browser.
type Config struct {
	CDPURL       string
	Timeout      time.Duration
	SettleDelay  time.Duration
	Width        int64
	Height       int64
	MaxRedirects int
	MaxBytes     int64
	Concurrency  int
}

// Resolver looks up host names. *net.Resolver implements it.
type Resolver interface {
	LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error)
}

// Capture is what the browser saw.
type Capture struct {
	PNG         []byte
	FinalURL    string
	Title       string
	Description string
}

// Browser captures pages. Every capture gets its own connection and browser
// context, so nothing carries over between checks and a Chromium restart costs
// only the captures in progress.
type Browser struct {
	cfg      Config
	resolver Resolver
	slots    chan struct{}
}

// New returns a Browser. It does not connect until the first capture.
func New(cfg Config, resolver Resolver) *Browser {
	return &Browser{cfg: cfg, resolver: resolver, slots: make(chan struct{}, cfg.Concurrency)}
}

// Capture navigates to pageURL and screenshots the viewport. Waiting for a free
// slot does not count against the timeout. ctx must have no deadline of its own:
// chromedp ties the connection's lifetime to the context of its first call.
func (b *Browser) Capture(ctx context.Context, pageURL string) (*Capture, error) {
	select {
	case b.slots <- struct{}{}:
		defer func() { <-b.slots }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	timer := time.AfterFunc(b.cfg.Timeout, func() { cancel(ErrTimeout) })
	defer timer.Stop()

	capture, err := b.capture(ctx, cancel, pageURL)
	if err == nil {
		return capture, nil
	}
	// A limit reached mid-action surfaces as a generic cancellation.
	if cause := context.Cause(ctx); cause != nil {
		return nil, cause
	}
	return nil, err
}

func (b *Browser) capture(ctx context.Context, cancel context.CancelCauseFunc, pageURL string) (*Capture, error) {
	endpoint, err := b.endpoint(ctx)
	if err != nil {
		return nil, fmt.Errorf("resolve browser address: %w", err)
	}
	allocator, cancelAllocator := remote.NewAllocator(ctx, endpoint)
	defer cancelAllocator()
	tab, cancelTab := chromedp.NewContext(allocator, chromedp.WithNewBrowserContext())
	defer cancelTab()

	// Opening the tab first means the listener sees the very first request.
	if err := chromedp.Do(tab); err != nil {
		return nil, fmt.Errorf("open tab: %w", err)
	}
	if err := b.watch(tab, cancel); err != nil {
		return nil, fmt.Errorf("watch tab: %w", err)
	}

	err = chromedp.Do(tab,
		chromedp.EmulateViewport(b.cfg.Width, b.cfg.Height),
		chromedp.Navigate(pageURL),
		chromedp.Sleep(b.cfg.SettleDelay),
	)
	if err != nil {
		return nil, fmt.Errorf("navigate: %w", err)
	}

	var result Capture
	if result.FinalURL, err = chromedp.Run(tab, chromedp.Location()); err != nil {
		return nil, fmt.Errorf("read location: %w", err)
	}
	if result.Title, err = chromedp.Run(tab, chromedp.Title()); err != nil {
		return nil, fmt.Errorf("read title: %w", err)
	}
	if result.Description, err = chromedp.Run(tab, chromedp.Evaluate[string](descriptionScript)); err != nil {
		return nil, fmt.Errorf("read description: %w", err)
	}
	if result.PNG, err = chromedp.Run(tab, chromedp.CaptureScreenshot()); err != nil {
		return nil, fmt.Errorf("capture screenshot: %w", err)
	}
	if int64(len(result.PNG)) > b.cfg.MaxBytes {
		return nil, ErrTooLarge
	}
	return &result, nil
}

// watch enforces the redirect limit and dismisses dialogs, which would otherwise
// block the page until the timeout. Every document request in the main frame after
// the first is a redirect, whether it came from HTTP, meta refresh, or JavaScript.
func (b *Browser) watch(tab context.Context, cancel context.CancelCauseFunc) error {
	mainFrame := cdp.FrameID(chromedp.FromContext(tab).Target.TargetID)
	var documents atomic.Int64

	return chromedp.Listen(tab, func(event any) {
		switch e := event.(type) {
		case *network.EventRequestWillBeSent:
			if e.Type != network.ResourceTypeDocument || e.FrameID != mainFrame {
				return
			}
			if documents.Add(1)-1 > int64(b.cfg.MaxRedirects) {
				cancel(ErrRedirectLimit)
			}
		case *page.EventJavascriptDialogOpening:
			// A listener must not block, and this waits for a reply. The result does
			// not matter: if it fails, the page times out.
			go chromedp.Do(tab, chromedp.Func(func(ctx context.Context, target *chromedp.Target) error {
				_, err := cdp.Call(ctx, target, page.HandleJavaScriptDialog, page.HandleJavaScriptDialogParams{})
				return err
			}))
		}
	})
}

// endpoint replaces the host of CDPURL with its IP: Chromium refuses CDP requests
// whose Host header is a name, as a defense against DNS rebinding.
func (b *Browser) endpoint(ctx context.Context) (string, error) {
	u, err := url.Parse(b.cfg.CDPURL)
	if err != nil {
		return "", fmt.Errorf("parse BROWSER_CDP_URL: %w", err)
	}
	host := u.Hostname()
	if _, err := netip.ParseAddr(host); err == nil {
		return u.String(), nil
	}
	addrs, err := b.resolver.LookupNetIP(ctx, "ip", host)
	if err != nil {
		return "", err
	}
	if len(addrs) == 0 {
		return "", fmt.Errorf("no addresses for %s", host)
	}
	u.Host = net.JoinHostPort(addrs[0].Unmap().String(), u.Port())
	return u.String(), nil
}
