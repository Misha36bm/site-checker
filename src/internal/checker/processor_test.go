package checker

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/Misha36bm/site-checker/src/internal/browser"
	"github.com/Misha36bm/site-checker/src/internal/models"
)

type fakeResults struct {
	http          *models.HTTPResult
	metadata      []models.MetadataResult
	screenshot    *models.ScreenshotResult
	httpErr       error
	metadataErr   error
	screenshotErr error
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

type fakeFiles struct {
	saved map[int64][]byte
	err   error
}

func (f *fakeFiles) Save(id int64, png []byte) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	if f.saved == nil {
		f.saved = map[int64][]byte{}
	}
	f.saved[id] = png
	return fmt.Sprintf("%d.png", id), nil
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
	if string(files.saved[1]) != "png" {
		t.Errorf("saved = %v", files.saved)
	}
	shot := store.screenshot
	if shot == nil || shot.Status != models.StageSuccess || shot.Path != "1.png" || shot.BrowserFinalURL != "https://final.example/" {
		t.Errorf("screenshot = %+v", shot)
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
