package checker

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Misha36bm/site-checker/src/internal/browser"
	"github.com/Misha36bm/site-checker/src/internal/database"
	"github.com/Misha36bm/site-checker/src/internal/models"
)

type fakeResults struct {
	captured      *models.Check
	capturedErr   error
	http          *models.HTTPResult
	metadata      []models.MetadataResult
	screenshot    *models.ScreenshotResult
	httpErr       error
	metadataErr   error
	screenshotErr error
}

func (f *fakeResults) FindCapturedCheck(context.Context, string) (*models.Check, error) {
	if f.capturedErr != nil {
		return nil, f.capturedErr
	}
	if f.captured == nil {
		return nil, database.ErrNotFound
	}
	return f.captured, nil
}

func (f *fakeResults) SaveHTTPResult(_ context.Context, _ models.Lease, r models.HTTPResult) error {
	f.http = &r
	return f.httpErr
}

func (f *fakeResults) SaveMetadataResult(_ context.Context, _ models.Lease, r models.MetadataResult) error {
	f.metadata = append(f.metadata, r)
	return f.metadataErr
}

func (f *fakeResults) SaveScreenshotResult(_ context.Context, _ models.Lease, r models.ScreenshotResult) error {
	f.screenshot = &r
	return f.screenshotErr
}

type fakeBrowser struct {
	capture *browser.Capture
	err     error
	calls   int
}

func (f *fakeBrowser) Capture(context.Context, string) (*browser.Capture, error) {
	f.calls++
	return f.capture, f.err
}

var capturedLongAgo = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

type fakeFiles struct {
	existing bool
	findErr  error
	saved    map[string][]byte
	err      error
}

func (f *fakeFiles) FindReusable(string) (browser.Stored, bool, error) {
	return browser.Stored{Key: "site.png", CapturedAt: capturedLongAgo}, f.existing, f.findErr
}

func (f *fakeFiles) Save(url string, _ int, png []byte) (browser.Stored, error) {
	if f.err != nil {
		return browser.Stored{}, f.err
	}
	if f.saved == nil {
		f.saved = map[string][]byte{}
	}
	f.saved[url] = png
	return browser.Stored{Key: "site.png", CapturedAt: time.Now().UTC()}, nil
}

func working() *fakeBrowser {
	return &fakeBrowser{capture: &browser.Capture{PNG: []byte("png"), FinalURL: "https://final.example/"}}
}

func process(store ResultStore, b Screenshotter, files ScreenshotStorage, url string) models.CheckOutcome {
	return NewProcessor(store, newChecker(10), b, files).Process(context.Background(), &models.Check{ID: 1, URL: url, Attempts: 1})
}

func htmlSite(t *testing.T, status int, body string) *site {
	t.Helper()
	return newSite(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(status)
		fmt.Fprint(w, body)
	})
}

func TestProcessHTMLPage(t *testing.T) {
	s := htmlSite(t, 200, `<title>Hello</title><meta name="description" content="World">`)
	store, files := &fakeResults{}, &fakeFiles{}
	outcome := process(store, working(), files, s.at("a.test", "/"))

	if outcome.Status != models.StatusCompleted || outcome.ErrorCode != "" {
		t.Errorf("outcome = %+v", outcome)
	}
	if outcome.MetadataStatus != models.StageSuccess || outcome.ScreenshotStatus != models.StageSuccess {
		t.Errorf("stage statuses = %+v", outcome)
	}
	if store.http == nil || !store.http.IsAvailable {
		t.Errorf("HTTP result = %+v", store.http)
	}
	if len(store.metadata) != 1 || store.metadata[0].Title != "Hello" || store.metadata[0].Description != "World" {
		t.Errorf("metadata = %+v", store.metadata)
	}
	if string(files.saved[s.at("a.test", "/")]) != "png" {
		t.Errorf("saved = %v", files.saved)
	}
	shot := store.screenshot
	if shot == nil || shot.Status != models.StageSuccess || shot.Path != "site.png" || shot.BrowserFinalURL != "https://final.example/" {
		t.Errorf("screenshot = %+v", shot)
	}
	if time.Since(shot.CapturedAt) > time.Minute {
		t.Errorf("captured at = %v", shot.CapturedAt)
	}
}

func TestProcessReusesExistingScreenshot(t *testing.T) {
	s := htmlSite(t, 200, `<title>Hello</title>`)
	store, b, files := &fakeResults{}, working(), &fakeFiles{existing: true}
	outcome := process(store, b, files, s.at("a.test", "/"))

	if b.calls != 0 {
		t.Errorf("the browser ran %d times for a page already captured", b.calls)
	}
	if len(files.saved) != 0 {
		t.Errorf("saved = %v", files.saved)
	}
	if outcome.Status != models.StatusCompleted || outcome.ScreenshotStatus != models.StageSuccess || outcome.MetadataStatus != models.StageSuccess {
		t.Errorf("outcome = %+v", outcome)
	}
	shot := store.screenshot
	if shot == nil || shot.Status != models.StageSuccess || shot.Path != "site.png" || shot.BrowserFinalURL != "" {
		t.Errorf("screenshot = %+v", shot)
	}
	if !shot.CapturedAt.Equal(capturedLongAgo) {
		t.Errorf("captured at = %v, want the original capture time", shot.CapturedAt)
	}
	// The HTTP stage and metadata still run: only the capture is skipped.
	if store.http == nil || !store.http.IsAvailable || store.metadata[0].Title != "Hello" {
		t.Errorf("HTTP = %+v, metadata = %+v", store.http, store.metadata)
	}
}

// A site that recovers must not stay frozen on its error page, and one that breaks
// must not hide behind its working page.
func TestErrorPagesAreNeverReused(t *testing.T) {
	status := http.StatusInternalServerError
	s := newSite(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(status)
	})
	dir := t.TempDir()
	files, err := browser.NewStorage(dir, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	target := s.at("a.test", "/")
	check := func() (*fakeBrowser, *fakeResults) {
		t.Helper()
		store, b := &fakeResults{}, working()
		if outcome := process(store, b, files, target); outcome.ScreenshotStatus != models.StageSuccess {
			t.Fatalf("outcome = %+v", outcome)
		}
		return b, store
	}

	b, first := check() // site is down
	if b.calls != 1 {
		t.Errorf("error page: browser ran %d times", b.calls)
	}
	b, again := check() // still down: captured afresh, same file
	if b.calls != 1 {
		t.Errorf("repeated error page: browser ran %d times", b.calls)
	}
	if first.screenshot.Path != again.screenshot.Path || !strings.HasSuffix(again.screenshot.Path, "-500.png") {
		t.Errorf("paths = %q, %q", first.screenshot.Path, again.screenshot.Path)
	}

	status = http.StatusOK // site recovers
	b, recovered := check()
	if b.calls != 1 {
		t.Errorf("recovered site: browser ran %d times, so the error page was reused", b.calls)
	}
	if strings.HasSuffix(recovered.screenshot.Path, "-500.png") {
		t.Errorf("path = %q", recovered.screenshot.Path)
	}

	b, reused := check() // still fine: reused
	if b.calls != 0 {
		t.Errorf("working page: browser ran %d times, want reuse", b.calls)
	}
	if reused.screenshot.Path != recovered.screenshot.Path {
		t.Errorf("paths = %q, %q", reused.screenshot.Path, recovered.screenshot.Path)
	}

	status = http.StatusInternalServerError // site breaks again
	b, broken := check()
	if b.calls != 1 {
		t.Errorf("broken site: browser ran %d times, so the working page hid the error", b.calls)
	}
	if !strings.HasSuffix(broken.screenshot.Path, "-500.png") {
		t.Errorf("path = %q", broken.screenshot.Path)
	}
}

func text(value string) *string { return &value }

func TestReusedScreenshotInheritsWhatTheBrowserSaw(t *testing.T) {
	previous := &models.Check{
		BrowserFinalURL: text("https://final.example/"),
		Title:           text("Rendered title"),
		Description:     text("Rendered description"),
	}
	cases := []struct {
		name      string
		html      string
		wantTitle string
		wantDesc  string
		wantSaves int
	}{
		{"client-rendered page", `<div id=app></div>`, "Rendered title", "Rendered description", 2},
		{"html takes priority", `<title>From HTML</title>`, "From HTML", "Rendered description", 2},
		{"nothing to add", `<title>A</title><meta name=description content=B>`, "A", "B", 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := htmlSite(t, 200, tc.html)
			store, b := &fakeResults{captured: previous}, working()
			outcome := process(store, b, &fakeFiles{existing: true}, s.at("a.test", "/"))

			if b.calls != 0 || outcome.ScreenshotStatus != models.StageSuccess || outcome.MetadataStatus != models.StageSuccess {
				t.Errorf("browser calls = %d, outcome = %+v", b.calls, outcome)
			}
			if store.screenshot.BrowserFinalURL != "https://final.example/" {
				t.Errorf("screenshot = %+v", store.screenshot)
			}
			if len(store.metadata) != tc.wantSaves {
				t.Fatalf("saved %d times, want %d", len(store.metadata), tc.wantSaves)
			}
			last := store.metadata[len(store.metadata)-1]
			if last.Title != tc.wantTitle || last.Description != tc.wantDesc {
				t.Errorf("metadata = %+v", last)
			}
		})
	}
}

func TestReusedScreenshotWorksWithoutTheCapturingCheck(t *testing.T) {
	for name, store := range map[string]*fakeResults{
		"check is gone":     {},
		"database failure":  {capturedErr: errors.New("connection lost")},
		"nothing was found": {captured: &models.Check{}},
	} {
		t.Run(name, func(t *testing.T) {
			s := htmlSite(t, 200, `<title>T</title>`)
			outcome := process(store, working(), &fakeFiles{existing: true}, s.at("a.test", "/"))
			if outcome.Status != models.StatusCompleted || outcome.ScreenshotStatus != models.StageSuccess {
				t.Errorf("outcome = %+v", outcome)
			}
			if store.screenshot.Path != "site.png" || store.screenshot.BrowserFinalURL != "" {
				t.Errorf("screenshot = %+v", store.screenshot)
			}
		})
	}
}

// slowBrowser counts captures and holds each long enough for others to pile up.
type slowBrowser struct {
	calls atomic.Int32
}

func (b *slowBrowser) Capture(context.Context, string) (*browser.Capture, error) {
	b.calls.Add(1)
	time.Sleep(50 * time.Millisecond)
	return &browser.Capture{PNG: []byte("png"), FinalURL: "https://final.example/"}, nil
}

// lockedResults lets several checks share a store.
type lockedResults struct {
	mu sync.Mutex
	fakeResults
}

func (l *lockedResults) SaveHTTPResult(ctx context.Context, lease models.Lease, r models.HTTPResult) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.fakeResults.SaveHTTPResult(ctx, lease, r)
}

func (l *lockedResults) SaveMetadataResult(ctx context.Context, lease models.Lease, r models.MetadataResult) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.fakeResults.SaveMetadataResult(ctx, lease, r)
}

func (l *lockedResults) SaveScreenshotResult(ctx context.Context, lease models.Lease, r models.ScreenshotResult) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.fakeResults.SaveScreenshotResult(ctx, lease, r)
}

func (l *lockedResults) FindCapturedCheck(ctx context.Context, path string) (*models.Check, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.fakeResults.FindCapturedCheck(ctx, path)
}

func runConcurrently(t *testing.T, urls []string) (*slowBrowser, []models.CheckOutcome) {
	t.Helper()
	files, err := browser.NewStorage(t.TempDir(), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	b := &slowBrowser{}
	processor := NewProcessor(&lockedResults{}, newChecker(10), b, files)

	outcomes := make([]models.CheckOutcome, len(urls))
	var wg sync.WaitGroup
	for i, url := range urls {
		wg.Go(func() {
			outcomes[i] = processor.Process(context.Background(), &models.Check{ID: int64(i + 1), URL: url, Attempts: 1})
		})
	}
	wg.Wait()
	return b, outcomes
}

func TestConcurrentChecksOfOnePageCaptureOnce(t *testing.T) {
	s := htmlSite(t, 200, `<title>T</title>`)
	urls := []string{
		s.at("a.test", "/"),
		s.at("a.test", "/"),
		s.at("A.TEST", "/"), // an equivalent URL shares the lock too
		s.at("a.test", "/#top"),
		s.at("a.test", "/"),
	}
	b, outcomes := runConcurrently(t, urls)

	if got := b.calls.Load(); got != 1 {
		t.Errorf("Chromium ran %d times for one page", got)
	}
	for i, outcome := range outcomes {
		if outcome.Status != models.StatusCompleted || outcome.ScreenshotStatus != models.StageSuccess {
			t.Errorf("check %d: outcome = %+v", i, outcome)
		}
	}
}

func TestConcurrentChecksOfDifferentPagesEachCapture(t *testing.T) {
	s := htmlSite(t, 200, `<title>T</title>`)
	b, _ := runConcurrently(t, []string{s.at("a.test", "/1"), s.at("a.test", "/2"), s.at("b.test", "/1")})
	if got := b.calls.Load(); got != 3 {
		t.Errorf("Chromium ran %d times for 3 pages", got)
	}
}

func TestFailedCaptureLetsTheNextCheckTry(t *testing.T) {
	s := htmlSite(t, 200, `<title>T</title>`)
	files, err := browser.NewStorage(t.TempDir(), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	broken := &fakeBrowser{err: errors.New("crashed")}
	if outcome := process(&fakeResults{}, broken, files, s.at("a.test", "/")); outcome.ScreenshotStatus != models.StageFailed {
		t.Fatalf("outcome = %+v", outcome)
	}
	fixed := working()
	if outcome := process(&fakeResults{}, fixed, files, s.at("a.test", "/")); outcome.ScreenshotStatus != models.StageSuccess || fixed.calls != 1 {
		t.Errorf("outcome = %+v, browser calls = %d", outcome, fixed.calls)
	}
}

func TestCancelledWaitDoesNotCapture(t *testing.T) {
	s := htmlSite(t, 200, `<title>T</title>`)
	key, err := browser.FileName(s.at("a.test", "/"), 200)
	if err != nil {
		t.Fatal(err)
	}
	store, b := &fakeResults{}, working()
	processor := NewProcessor(store, newChecker(10), b, &fakeFiles{})
	unlock, err := processor.captures.Lock(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	processor.Process(ctx, &models.Check{ID: 1, URL: s.at("a.test", "/"), Attempts: 1})
	if b.calls != 0 {
		t.Errorf("browser ran %d times after the wait was cancelled", b.calls)
	}
}

func TestProcessLookupFailureKeepsCheckCompleted(t *testing.T) {
	s := htmlSite(t, 200, `<title>T</title>`)
	store, b := &fakeResults{}, working()
	outcome := process(store, b, &fakeFiles{findErr: errors.New("permission denied at /app/storage")}, s.at("a.test", "/"))

	if outcome.Status != models.StatusCompleted || outcome.ScreenshotStatus != models.StageFailed {
		t.Errorf("outcome = %+v", outcome)
	}
	if strings.Contains(store.screenshot.Error, "/app") {
		t.Errorf("a filesystem path reached the result: %q", store.screenshot.Error)
	}
}

func TestProcessErrorPageIsCaptured(t *testing.T) {
	s := htmlSite(t, http.StatusNotFound, `<title>Not found</title>`)
	store, b := &fakeResults{}, working()
	outcome := process(store, b, &fakeFiles{}, s.at("a.test", "/"))

	if outcome.Status != models.StatusCompleted || outcome.ScreenshotStatus != models.StageSuccess {
		t.Errorf("outcome = %+v", outcome)
	}
	if store.http.IsAvailable || *store.http.StatusCode != http.StatusNotFound {
		t.Errorf("HTTP result = %+v", store.http)
	}
	if b.calls != 1 {
		t.Errorf("browser ran %d times", b.calls)
	}
}

func TestProcessNonHTMLSkipsLaterStages(t *testing.T) {
	s := newSite(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/pdf")
	})
	store, b := &fakeResults{}, working()
	outcome := process(store, b, &fakeFiles{}, s.at("a.test", "/"))

	if outcome.Status != models.StatusCompleted || outcome.MetadataStatus != models.StageSkipped || outcome.ScreenshotStatus != models.StageSkipped {
		t.Errorf("outcome = %+v", outcome)
	}
	if store.metadata != nil || store.screenshot != nil || b.calls != 0 {
		t.Errorf("metadata = %+v, screenshot = %+v, browser calls = %d", store.metadata, store.screenshot, b.calls)
	}
}

func TestProcessFailedRequestFailsCheckButKeepsPartialResult(t *testing.T) {
	s := newSite(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/", http.StatusFound)
	})
	store, b := &fakeResults{}, working()
	outcome := process(store, b, &fakeFiles{}, s.at("a.test", "/"))

	if outcome.Status != models.StatusFailed || outcome.ErrorCode != models.ErrorRedirectLoop || outcome.ErrorMessage == "" {
		t.Errorf("outcome = %+v", outcome)
	}
	if outcome.MetadataStatus != models.StageSkipped || outcome.ScreenshotStatus != models.StageSkipped {
		t.Errorf("stage statuses = %+v", outcome)
	}
	if store.http == nil || len(store.http.RedirectChain) != 1 {
		t.Errorf("the partial chain was not saved: %+v", store.http)
	}
	if b.calls != 0 {
		t.Error("a failed check must not reach the browser")
	}
}

func TestProcessReportsFailureToSaveHTTPResult(t *testing.T) {
	s := htmlSite(t, 200, "")
	outcome := process(&fakeResults{httpErr: errors.New("secret database detail")}, working(), &fakeFiles{}, s.at("a.test", "/"))
	if outcome.Status != models.StatusFailed || outcome.ErrorCode != models.ErrorInternal {
		t.Errorf("outcome = %+v", outcome)
	}
	if strings.Contains(outcome.ErrorMessage, "secret") {
		t.Error("the database error reached the check result")
	}
}

func TestProcessMetadataFailureKeepsCheckCompleted(t *testing.T) {
	s := htmlSite(t, 200, `<title>T</title>`)
	outcome := process(&fakeResults{metadataErr: errors.New("boom")}, working(), &fakeFiles{}, s.at("a.test", "/"))
	if outcome.Status != models.StatusCompleted || outcome.MetadataStatus != models.StageFailed {
		t.Errorf("outcome = %+v", outcome)
	}
}

// A screenshot failure must not invalidate a successful HTTP check.
func TestProcessBrowserFailureKeepsHTTPResult(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"timeout", browser.ErrTimeout, "in time"},
		{"redirect limit", browser.ErrRedirectLimit, "redirected"},
		{"too large", browser.ErrTooLarge, "size limit"},
		{"unknown", errors.New("dial tcp 172.20.0.3:9222: secret"), "could not be rendered"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := htmlSite(t, 200, `<title>T</title>`)
			store := &fakeResults{}
			outcome := process(store, &fakeBrowser{err: tc.err}, &fakeFiles{}, s.at("a.test", "/"))

			if outcome.Status != models.StatusCompleted || outcome.ErrorCode != "" {
				t.Errorf("outcome = %+v", outcome)
			}
			if outcome.MetadataStatus != models.StageSuccess || outcome.ScreenshotStatus != models.StageFailed {
				t.Errorf("stage statuses = %+v", outcome)
			}
			if !store.http.IsAvailable {
				t.Error("the HTTP result was lost")
			}
			shot := store.screenshot
			if shot == nil || shot.Status != models.StageFailed || !strings.Contains(shot.Error, tc.want) {
				t.Errorf("screenshot = %+v", shot)
			}
			if strings.Contains(shot.Error, "172.20") {
				t.Errorf("an internal address reached the result: %q", shot.Error)
			}
		})
	}
}

func TestProcessStorageFailureKeepsCheckCompleted(t *testing.T) {
	s := htmlSite(t, 200, `<title>T</title>`)
	store := &fakeResults{}
	outcome := process(store, working(), &fakeFiles{err: errors.New("disk full at /app/storage")}, s.at("a.test", "/"))

	if outcome.Status != models.StatusCompleted || outcome.ScreenshotStatus != models.StageFailed {
		t.Errorf("outcome = %+v", outcome)
	}
	if strings.Contains(store.screenshot.Error, "/app") {
		t.Errorf("a filesystem path reached the result: %q", store.screenshot.Error)
	}
}

func TestMetadataFallsBackToRenderedDOM(t *testing.T) {
	cases := []struct {
		name        string
		html        string
		domTitle    string
		domDesc     string
		wantTitle   string
		wantDesc    string
		wantUpdates int
	}{
		{"client-rendered page", `<div id=app></div>`, "  Rendered\n title ", "Rendered description", "Rendered title", "Rendered description", 2},
		{"only the description is missing", `<title>From HTML</title>`, "From DOM", "From DOM", "From HTML", "From DOM", 2},
		{"html takes priority", `<title>A</title><meta name=description content=B>`, "C", "D", "A", "B", 1},
		{"dom has nothing to add", `<title>A</title>`, "", "", "A", "", 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := htmlSite(t, 200, tc.html)
			store := &fakeResults{}
			b := &fakeBrowser{capture: &browser.Capture{PNG: []byte("png"), Title: tc.domTitle, Description: tc.domDesc}}
			outcome := process(store, b, &fakeFiles{}, s.at("a.test", "/"))

			if outcome.MetadataStatus != models.StageSuccess {
				t.Errorf("metadata status = %q", outcome.MetadataStatus)
			}
			if len(store.metadata) != tc.wantUpdates {
				t.Fatalf("saved %d times, want %d", len(store.metadata), tc.wantUpdates)
			}
			last := store.metadata[len(store.metadata)-1]
			if last.Title != tc.wantTitle || last.Description != tc.wantDesc {
				t.Errorf("metadata = %+v", last)
			}
		})
	}
}
